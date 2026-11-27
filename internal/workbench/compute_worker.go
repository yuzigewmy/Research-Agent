package workbench

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const computeJobsPath = "/api/v1/jobs/"

const computeWorkerSchema = `CREATE TABLE jobs (
	job_id TEXT PRIMARY KEY NOT NULL,
	input_sha256 TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('RUNNING', 'CANCELLING', 'CANCELLED', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
	started_at TEXT NOT NULL,
	finished_at TEXT NOT NULL DEFAULT '',
	result_sha256 TEXT NOT NULL DEFAULT '',
	result_json TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT ''
);`

type computeJob struct {
	JobID        string `json:"jobId"`
	InputSHA256  string `json:"inputSha256"`
	Status       string `json:"status"`
	StartedAt    string `json:"startedAt,omitempty"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	ResultSHA256 string `json:"resultSha256,omitempty"`
	Message      string `json:"message,omitempty"`
}

type computeJobResult struct {
	Method      string            `json:"method"`
	InputSHA256 string            `json:"inputSha256"`
	Metrics     experimentMetrics `json:"metrics"`
}

// ComputeWorker runs a single bounded paired comparison at a time.
type ComputeWorker struct {
	db         *sql.DB
	name       string
	credential [32]byte
	slot       chan struct{}
	mu         sync.Mutex
	closing    bool
	active     sync.WaitGroup
	activeID   string
	cancel     context.CancelFunc
}

// OpenComputeWorker opens the node's separate job receipt database.
func OpenComputeWorker(directory, name, token string) (*ComputeWorker, error) {
	if directory == "" || !validComputeNodeName(name) || !validComputeToken(token) || strings.Contains(name, token) {
		return nil, errors.New("请设置有效的计算节点目录、名称和访问凭据")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	filename, err := filepath.Abs(filepath.Join(directory, "compute.sqlite3"))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", workspaceSQLiteURI(filename, "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initializeComputeWorker(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &ComputeWorker{db: db, name: name, credential: sha256.Sum256([]byte("Bearer " + token)), slot: make(chan struct{}, 1)}, nil
}

func initializeComputeWorker(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		var hasJobs bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'jobs')`).Scan(&hasJobs); err != nil {
			return err
		}
		if hasJobs {
			if _, err := tx.Exec(`ALTER TABLE jobs RENAME TO legacy_jobs`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(computeWorkerSchema); err != nil {
			return err
		}
		if hasJobs {
			if _, err := tx.Exec(`INSERT INTO jobs (job_id, input_sha256, status, started_at, finished_at, result_sha256, result_json, message)
				SELECT job_id, input_sha256, status, started_at, finished_at, result_sha256, result_json, message FROM legacy_jobs;
				DROP TABLE legacy_jobs`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
	case 1, 2:
	default:
		return errors.New("计算节点回执库版本不受支持")
	}
	if version < 2 {
		if _, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN scatter_svg TEXT NOT NULL DEFAULT '';
			ALTER TABLE jobs ADD COLUMN scatter_sha256 TEXT NOT NULL DEFAULT '';
			PRAGMA user_version = 2`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE jobs SET
		status = CASE status WHEN 'CANCELLING' THEN 'CANCELLED' ELSE 'INTERRUPTED' END,
		message = CASE status WHEN 'CANCELLING' THEN '' ELSE '计算节点重启中断了此作业。' END,
		finished_at = ? WHERE status IN ('RUNNING', 'CANCELLING')`, time.Now().UTC().Format(createdAtLayout)); err != nil {
		return err
	}
	return tx.Commit()
}

func (worker *ComputeWorker) Close() error {
	worker.mu.Lock()
	worker.closing = true
	worker.mu.Unlock()
	worker.active.Wait()
	return worker.db.Close()
}

func (worker *ComputeWorker) acquire() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing {
		return false
	}
	select {
	case worker.slot <- struct{}{}:
		worker.active.Add(1)
		return true
	default:
		return false
	}
}

func (worker *ComputeWorker) release() {
	<-worker.slot
	worker.active.Done()
}

func (worker *ComputeWorker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/capabilities", worker.capabilities)
	mux.HandleFunc("PUT "+computeJobsPath+"{jobId}", worker.submit)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}", worker.readJob)
	mux.HandleFunc("POST "+computeJobsPath+"{jobId}/cancel", worker.cancelJob)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}/result", worker.readResult)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}/artifacts", worker.listArtifacts)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}/artifacts/scatter.svg", worker.readScatter)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		credential := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(credential[:], worker.credential[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "请提供计算节点访问凭据。")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (worker *ComputeWorker) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Protocol string `json:"protocol"`
		computeNodeInfo
	}{computeProtocol, computeNodeInfo{worker.name, []computeCapability{{
		ID: experimentComparisonMethod, MaxInputBytes: maxWorkspaceText, MaxRows: maxExperimentRows,
		TimeoutSeconds: int(comparisonTimeout / time.Second),
	}}}})
}

func validComputeJobID(id string) bool {
	return len(id) == 32 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) == -1
}

func (worker *ComputeWorker) submit(w http.ResponseWriter, r *http.Request) {
	id, digest := r.PathValue("jobId"), r.Header.Get("X-Input-SHA256")
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !validComputeJobID(id) || !validObjectDigest(digest) || media != "text/csv" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的作业编号、CSV 和输入摘要。")
		return
	}
	if !worker.acquire() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "NODE_BUSY", "计算节点正在处理作业，请按原编号查询或稍后重试。")
		return
	}
	background := false
	defer func() {
		if !background {
			worker.release()
		}
	}()
	source, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWorkspaceText))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "INPUT_TOO_LARGE", "CSV 超过 512 KiB。")
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "无法读取 CSV 输入。")
		}
		return
	}
	if computeDigest(source) != digest {
		writeError(w, http.StatusUnprocessableEntity, "INPUT_DIGEST_MISMATCH", "CSV 输入摘要不一致。")
		return
	}
	runCtx, cancel := context.WithTimeout(context.Background(), comparisonTimeout)
	worker.mu.Lock()
	worker.activeID, worker.cancel = id, cancel
	worker.mu.Unlock()
	clearActive := func() {
		cancel()
		worker.mu.Lock()
		worker.activeID, worker.cancel = "", nil
		worker.mu.Unlock()
	}
	defer func() {
		if !background {
			clearActive()
		}
	}()
	job := computeJob{JobID: id, InputSHA256: digest, Status: taskRunning, StartedAt: time.Now().UTC().Format(createdAtLayout)}
	if err := worker.db.QueryRowContext(r.Context(), `INSERT INTO jobs (job_id, input_sha256, status, started_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(job_id) DO NOTHING RETURNING job_id`,
		job.JobID, job.InputSHA256, job.Status, job.StartedAt).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			worker.replyJob(w, r.Context(), job.JobID, digest)
		} else {
			writeStoreError(w, "claim compute job", err)
		}
		return
	}
	// A saved job can finish even if the submission connection drops.
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Prefer")), "respond-async") {
		background = true
		go func() {
			defer worker.release()
			defer clearActive()
			if _, err := worker.executeJob(runCtx, job, string(source)); err != nil {
				slog.Error("save background compute job", "job_id", job.JobID, "error", err)
			}
		}()
		w.Header().Set("Location", computeJobsPath+job.JobID)
		w.Header().Set("Preference-Applied", "respond-async")
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	finished, err := worker.executeJob(runCtx, job, string(source))
	if err != nil {
		writeStoreError(w, "save compute job", err)
		return
	}
	w.Header().Set("Location", computeJobsPath+job.JobID)
	writeJSON(w, http.StatusCreated, finished)
}

func (worker *ComputeWorker) executeJob(ctx context.Context, job computeJob, source string) (computeJob, error) {
	job, result, scatter := runComputeJob(ctx, job, source)
	saveCtx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	return worker.finishJob(saveCtx, job, result, scatter)
}

func (worker *ComputeWorker) replyJob(w http.ResponseWriter, ctx context.Context, id, digest string) {
	job, _, err := worker.lookupJob(ctx, id)
	if err != nil {
		writeStoreError(w, "read compute receipt", err)
		return
	}
	if job.InputSHA256 != digest {
		writeError(w, http.StatusConflict, "JOB_ID_CONFLICT", "该作业编号已绑定其他输入。")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (worker *ComputeWorker) finishJob(ctx context.Context, job computeJob, result, scatter string) (computeJob, error) {
	var scatterSHA256 string
	if scatter != "" {
		scatterSHA256 = computeDigest([]byte(scatter))
	}
	_, err := worker.db.ExecContext(ctx, `UPDATE jobs SET
		status = CASE status WHEN 'CANCELLING' THEN 'CANCELLED' ELSE ? END,
		finished_at = ?, result_sha256 = CASE status WHEN 'RUNNING' THEN ? ELSE '' END,
		result_json = CASE status WHEN 'RUNNING' THEN ? ELSE '' END,
		scatter_svg = CASE status WHEN 'RUNNING' THEN ? ELSE '' END,
		scatter_sha256 = CASE status WHEN 'RUNNING' THEN ? ELSE '' END,
		message = CASE status WHEN 'RUNNING' THEN ? ELSE '' END
		WHERE job_id = ? AND status IN ('RUNNING', 'CANCELLING')`,
		job.Status, job.FinishedAt, job.ResultSHA256, result, scatter, scatterSHA256, job.Message, job.JobID)
	if err != nil {
		return computeJob{}, err
	}
	saved, _, err := worker.lookupJob(ctx, job.JobID)
	return saved, err
}

func (worker *ComputeWorker) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, digest := r.PathValue("jobId"), r.Header.Get("X-Input-SHA256")
	if !validComputeJobID(id) || !validObjectDigest(digest) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的作业编号和输入摘要。")
		return
	}
	ctx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), taskDBLimit)
	defer stop()
	now := time.Now().UTC().Format(createdAtLayout)
	_, err := worker.db.ExecContext(ctx, `INSERT INTO jobs (job_id, input_sha256, status, started_at, finished_at)
		VALUES (?, ?, 'CANCELLED', '', ?) ON CONFLICT(job_id) DO UPDATE SET status = 'CANCELLING'
		WHERE jobs.status = 'RUNNING' AND jobs.input_sha256 = excluded.input_sha256`, id, digest, now)
	if err != nil {
		writeStoreError(w, "stop compute job", err)
		return
	}
	job, _, err := worker.lookupJob(ctx, id)
	if err != nil {
		writeStoreError(w, "read stop receipt", err)
		return
	}
	if job.InputSHA256 != digest {
		writeError(w, http.StatusConflict, "JOB_ID_CONFLICT", "该作业编号已绑定其他输入。")
		return
	}
	if job.Status == taskCancelling {
		worker.mu.Lock()
		if worker.activeID == id {
			worker.cancel()
		}
		worker.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, job)
}

func computeDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func runComputeJob(ctx context.Context, job computeJob, source string) (computeJob, string, string) {
	metrics, err := compareExperimentMetrics(ctx, source)
	job.FinishedAt = time.Now().UTC().Format(createdAtLayout)
	if err != nil {
		job.Status = taskFailed
		job.Message = "配对计算未完成。"
		var inputErr experimentDataError
		if errors.As(err, &inputErr) {
			job.Message = inputErr.Error()
		}
		return job, "", ""
	}
	result, err := json.Marshal(computeJobResult{experimentComparisonMethod, job.InputSHA256, metrics})
	if err != nil {
		job.Status, job.Message = taskFailed, "无法生成计算结果。"
		return job, "", ""
	}
	plotInput := experimentComparisonReport{Method: experimentComparisonMethod, Input: taskInput{SHA256: job.InputSHA256}, Metrics: metrics}
	scatter, err := renderExperimentPlot(ctx, plotInput, source)
	if err != nil || len(scatter) > maxComputeScatterBytes {
		scatter = ""
	}
	job.FinishedAt = time.Now().UTC().Format(createdAtLayout)
	job.Status, job.ResultSHA256 = taskSucceeded, computeDigest(result)
	return job, string(result), scatter
}

func (worker *ComputeWorker) lookupJob(ctx context.Context, id string) (computeJob, string, error) {
	job := computeJob{JobID: id}
	var result string
	err := worker.db.QueryRowContext(ctx, `SELECT input_sha256, status, started_at, finished_at, result_sha256, result_json, message
		FROM jobs WHERE job_id = ?`, id).Scan(&job.InputSHA256, &job.Status, &job.StartedAt, &job.FinishedAt, &job.ResultSHA256, &result, &job.Message)
	return job, result, err
}

func (worker *ComputeWorker) lookupRequest(w http.ResponseWriter, r *http.Request) (computeJob, string, bool) {
	id := r.PathValue("jobId")
	if !validComputeJobID(id) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "作业编号无效。")
		return computeJob{}, "", false
	}
	job, result, err := worker.lookupJob(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "JOB_NOT_FOUND", "没有找到此计算作业。")
	} else if err != nil {
		writeStoreError(w, "read compute job", err)
	}
	return job, result, err == nil
}

func (worker *ComputeWorker) readJob(w http.ResponseWriter, r *http.Request) {
	if job, _, ok := worker.lookupRequest(w, r); ok {
		writeJSON(w, http.StatusOK, job)
	}
}

func (worker *ComputeWorker) readResult(w http.ResponseWriter, r *http.Request) {
	job, result, ok := worker.lookupRequest(w, r)
	if !ok {
		return
	}
	if job.Status != taskSucceeded {
		writeError(w, http.StatusNotFound, "RESULT_NOT_FOUND", "此作业没有可取回的结果。")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Result-SHA256", job.ResultSHA256)
	_, _ = io.WriteString(w, result)
}

func validComputeNodeName(name string) bool {
	return len(name) <= 160 && strings.TrimSpace(name) != "" && !strings.ContainsFunc(name, unicode.IsControl)
}

func validComputeToken(token string) bool {
	return token != "" && strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) < 0
}
