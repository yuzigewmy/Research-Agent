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
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const computeJobsPath = "/api/v1/jobs/"

const computeWorkerSchema = `CREATE TABLE IF NOT EXISTS jobs (
	job_id TEXT PRIMARY KEY NOT NULL,
	input_sha256 TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('RUNNING', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
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
	if _, err := tx.Exec(computeWorkerSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE jobs SET status = 'INTERRUPTED', finished_at = ?, message = ? WHERE status = 'RUNNING'`,
		time.Now().UTC().Format(createdAtLayout), "计算节点重启中断了此作业。"); err != nil {
		return err
	}
	return tx.Commit()
}

func (worker *ComputeWorker) Close() error { return worker.db.Close() }

func (worker *ComputeWorker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/capabilities", worker.capabilities)
	mux.HandleFunc("PUT "+computeJobsPath+"{jobId}", worker.submit)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}", worker.readJob)
	mux.HandleFunc("GET "+computeJobsPath+"{jobId}/result", worker.readResult)
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
	select {
	case worker.slot <- struct{}{}:
		defer func() { <-worker.slot }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "NODE_BUSY", "计算节点正在处理作业，请按原编号查询或稍后重试。")
		return
	}
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
	job, _, err := worker.lookupJob(r.Context(), id)
	if err == nil {
		if job.InputSHA256 != digest {
			writeError(w, http.StatusConflict, "JOB_ID_CONFLICT", "该作业编号已绑定其他输入。")
			return
		}
		writeJSON(w, http.StatusOK, job)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeStoreError(w, "read compute job", err)
		return
	}
	job = computeJob{JobID: id, InputSHA256: digest, Status: taskRunning, StartedAt: time.Now().UTC().Format(createdAtLayout)}
	if _, err := worker.db.ExecContext(r.Context(), `INSERT INTO jobs (job_id, input_sha256, status, started_at)
		VALUES (?, ?, ?, ?)`, job.JobID, job.InputSHA256, job.Status, job.StartedAt); err != nil {
		writeStoreError(w, "claim compute job", err)
		return
	}
	// Once the receipt is saved, a lost connection leaves the calculation free to finish.
	job, result := runComputeJob(job, string(source))
	saveCtx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	if err := worker.db.QueryRowContext(saveCtx, `UPDATE jobs SET status = ?, finished_at = ?, result_sha256 = ?, result_json = ?, message = ?
		WHERE job_id = ? AND status = 'RUNNING' RETURNING status`,
		job.Status, job.FinishedAt, job.ResultSHA256, result, job.Message, job.JobID).Scan(&job.Status); err != nil {
		writeStoreError(w, "save compute job", err)
		return
	}
	w.Header().Set("Location", computeJobsPath+job.JobID)
	writeJSON(w, http.StatusCreated, job)
}

func computeDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func runComputeJob(job computeJob, source string) (computeJob, string) {
	ctx, cancel := context.WithTimeout(context.Background(), comparisonTimeout)
	defer cancel()
	metrics, err := compareExperimentMetrics(ctx, source)
	job.FinishedAt = time.Now().UTC().Format(createdAtLayout)
	if err != nil {
		job.Status = taskFailed
		job.Message = "配对计算未完成。"
		var inputErr experimentDataError
		if errors.As(err, &inputErr) {
			job.Message = inputErr.Error()
		}
		return job, ""
	}
	result, err := json.Marshal(computeJobResult{experimentComparisonMethod, job.InputSHA256, metrics})
	if err != nil {
		job.Status, job.Message = taskFailed, "无法生成计算结果。"
		return job, ""
	}
	job.Status, job.ResultSHA256 = taskSucceeded, computeDigest(result)
	return job, string(result)
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
