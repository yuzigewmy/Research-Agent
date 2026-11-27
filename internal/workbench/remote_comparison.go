package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const remoteComparisonSchema = `
	CREATE TABLE remote_comparisons (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		job_id TEXT NOT NULL UNIQUE,
		node_digest TEXT NOT NULL,
		input_sha256 TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
		started_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		result_sha256 TEXT NOT NULL DEFAULT '',
		result_json TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (project_id, task_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks (project_id, task_id) ON DELETE CASCADE
	);
	PRAGMA user_version = 61;
`

const remoteComparisonStopSchema = `
	ALTER TABLE remote_comparisons RENAME TO previous_remote_comparisons;
	CREATE TABLE remote_comparisons (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		job_id TEXT NOT NULL UNIQUE,
		node_digest TEXT NOT NULL,
		input_sha256 TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'RUNNING', 'CANCELLING', 'CANCELLED', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
		started_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		result_sha256 TEXT NOT NULL DEFAULT '',
		result_json TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (project_id, task_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks (project_id, task_id) ON DELETE CASCADE
	);
	INSERT INTO remote_comparisons SELECT * FROM previous_remote_comparisons;
	DROP TABLE previous_remote_comparisons;
	PRAGMA user_version = 62;
`

const remotePending = "PENDING"

func remoteJobActive(status string) bool {
	return status == remotePending || status == taskRunning || status == taskCancelling
}

var errRemoteResponse = errors.New("invalid remote comparison response")
var errRemoteMissing = errors.New("remote comparison not yet recorded")

type remoteComparison struct {
	computeJob
	nodeDigest string
	result     string
}

func readRemoteComparison(ctx context.Context, db workspaceQuery, projectID, taskID string) (remoteComparison, error) {
	var job remoteComparison
	err := db.QueryRowContext(ctx, `SELECT job_id, node_digest, input_sha256, status, started_at, finished_at, result_sha256, result_json
		FROM remote_comparisons WHERE project_id = ? AND task_id = ?`, projectID, taskID).Scan(&job.JobID, &job.nodeDigest,
		&job.InputSHA256, &job.Status, &job.StartedAt, &job.FinishedAt, &job.ResultSHA256, &job.result)
	return job, err
}

func (node *ComputeNode) identity() string {
	return computeDigest([]byte(node.base.JoinPath("api", "v1", "capabilities").String()))
}

func (node *ComputeNode) jobRequest(ctx context.Context, method, id, suffix, digest, source string) ([]byte, http.Header, error) {
	if !validComputeJobID(id) {
		return nil, nil, errRemoteResponse
	}
	endpoint := node.base.JoinPath("api", "v1", "jobs", id, suffix)
	var input io.Reader
	if method == http.MethodPut {
		input = strings.NewReader(source)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), input)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+node.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "text/csv")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Input-SHA256", digest)
	}
	res, err := node.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return nil, nil, errRemoteMissing
	}
	if res.StatusCode != http.StatusOK && (method != http.MethodPut || res.StatusCode != http.StatusCreated) {
		return nil, nil, errRemoteResponse
	}
	const maxResponse = 8 << 10
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return nil, nil, errRemoteResponse
	}
	return body, res.Header, nil
}

func (node *ComputeNode) reconcileJob(ctx context.Context, old remoteComparison, method, source string, expected experimentMetrics) (computeJob, string, error) {
	suffix := ""
	if method == http.MethodPost {
		suffix = "cancel"
	}
	body, _, err := node.jobRequest(ctx, method, old.JobID, suffix, old.InputSHA256, source)
	if err != nil {
		return computeJob{}, "", err
	}
	var job computeJob
	if json.Unmarshal(body, &job) != nil || job.JobID != old.JobID || job.InputSHA256 != old.InputSHA256 {
		return computeJob{}, "", errRemoteResponse
	}
	if job.Status != taskCancelled || job.StartedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, job.StartedAt); err != nil {
			return computeJob{}, "", errRemoteResponse
		}
	}
	job.Message = ""
	switch job.Status {
	case taskRunning, taskCancelling:
		if job.FinishedAt != "" || job.ResultSHA256 != "" {
			return computeJob{}, "", errRemoteResponse
		}
		return job, "", nil
	case taskSucceeded, taskFailed, taskInterrupted, taskCancelled:
		if _, err := time.Parse(time.RFC3339Nano, job.FinishedAt); err != nil {
			return computeJob{}, "", errRemoteResponse
		}
	default:
		return computeJob{}, "", errRemoteResponse
	}
	if job.Status != taskSucceeded {
		if job.ResultSHA256 != "" {
			return computeJob{}, "", errRemoteResponse
		}
		return job, "", nil
	}
	if !validObjectDigest(job.ResultSHA256) {
		return computeJob{}, "", errRemoteResponse
	}
	result, headers, err := node.jobRequest(ctx, http.MethodGet, job.JobID, "result", "", "")
	if err != nil {
		return computeJob{}, "", errRemoteResponse
	}
	var report computeJobResult
	if headers.Get("X-Result-SHA256") != job.ResultSHA256 || computeDigest(result) != job.ResultSHA256 ||
		json.Unmarshal(result, &report) != nil || report.Method != experimentComparisonMethod || report.InputSHA256 != job.InputSHA256 ||
		report.Metrics != expected {
		return computeJob{}, "", errRemoteResponse
	}
	return job, string(result), nil
}

func registerRemoteComparison(mux *http.ServeMux, store *Store, node *ComputeNode) {
	path := projectsPath + "/{id}/tasks/{taskId}/remote-comparison"
	mux.HandleFunc("GET "+path, store.remoteComparisonHandler(node, "snapshot"))
	mux.HandleFunc("GET "+path+"/result", store.remoteComparisonHandler(node, "result"))
	mux.HandleFunc("POST "+path+"/submit", store.remoteComparisonHandler(node, "submit"))
	mux.HandleFunc("POST "+path+"/refresh", store.remoteComparisonHandler(node, "refresh"))
	mux.HandleFunc("POST "+path+"/cancel", store.remoteComparisonHandler(node, "cancel"))
}

func (s *Store) remoteComparisonHandler(node *ComputeNode, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, projectID, taskID := r.Context(), r.PathValue("id"), r.PathValue("taskId")
		report, err := readExperimentReport(ctx, s.db, projectID, taskID)
		if err != nil {
			writeExperimentEvidenceError(w, err)
			return
		}
		job, err := readRemoteComparison(ctx, s.db, projectID, taskID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeStoreError(w, "read remote comparison", err)
			return
		}
		if action == "snapshot" {
			writeRemoteComparison(w, node, job)
			return
		}
		if action == "result" {
			if job.Status != taskSucceeded || job.InputSHA256 != report.Input.SHA256 || computeDigest([]byte(job.result)) != job.ResultSHA256 {
				writeError(w, http.StatusNotFound, "RESULT_NOT_FOUND", "没有通过校验的远程计算结果。")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Disposition", `attachment; filename="paired-comparison.json"`)
			w.Header().Set("X-Result-SHA256", job.ResultSHA256)
			_, _ = io.WriteString(w, job.result)
			return
		}
		var input *struct{}
		if !readJSON(w, r, &input, 1024) {
			return
		}
		if input == nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交空 JSON 对象。")
			return
		}
		if job.JobID != "" && !remoteJobActive(job.Status) {
			writeRemoteComparison(w, node, job)
			return
		}
		if node == nil {
			writeError(w, http.StatusServiceUnavailable, "COMPUTE_NODE_UNAVAILABLE", "请先在服务端配置计算节点。")
			return
		}
		if job.JobID == "" {
			if action != "submit" {
				writeRemoteComparison(w, node, job)
				return
			}
			var identifier [16]byte
			rand.Read(identifier[:])
			if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_comparisons (project_id, task_id, job_id, node_digest, input_sha256)
				VALUES (?, ?, ?, ?, ?) ON CONFLICT(project_id, task_id) DO NOTHING`, projectID, taskID,
				hex.EncodeToString(identifier[:]), node.identity(), report.Input.SHA256); err != nil {
				writeStoreError(w, "reserve remote comparison", err)
				return
			}
			job, err = readRemoteComparison(ctx, s.db, projectID, taskID)
			if err != nil {
				writeStoreError(w, "read reserved comparison", err)
				return
			}
		}
		if job.nodeDigest != node.identity() || job.InputSHA256 != report.Input.SHA256 {
			writeError(w, http.StatusConflict, "COMPUTE_NODE_CHANGED", "请恢复提交时的计算节点配置，再查询原作业。")
			return
		}
		method, source := http.MethodGet, ""
		if action == "cancel" {
			if _, err := s.db.ExecContext(ctx, `UPDATE remote_comparisons SET status = 'CANCELLING'
				WHERE project_id = ? AND task_id = ? AND status IN ('PENDING', 'RUNNING')`, projectID, taskID); err != nil {
				writeStoreError(w, "record remote stop", err)
				return
			}
			method = http.MethodPost
		} else if action == "submit" && job.Status == remotePending {
			method = http.MethodPut
			source, err = s.readExperimentSource(ctx, projectID, report.Input)
			if err != nil {
				writeExperimentEvidenceError(w, errExperimentEvidence)
				return
			}
		}
		nodeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		updated, result, err := node.reconcileJob(nodeCtx, job, method, source, report.Metrics)
		if (err != nil && action == "cancel") || (errors.Is(err, errRemoteMissing) && (job.Status == remotePending || job.Status == taskCancelling)) {
			current, err := readRemoteComparison(ctx, s.db, projectID, taskID)
			if err != nil {
				writeStoreError(w, "read unconfirmed remote comparison", err)
				return
			}
			writeRemoteComparison(w, node, current)
			return
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "COMPUTE_JOB_UNCONFIRMED", "暂时无法核对节点回执或结果，请刷新原作业状态。")
			return
		}
		saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), taskDBLimit)
		defer stop()
		if _, err := s.db.ExecContext(saveCtx, `UPDATE remote_comparisons SET status = ?, started_at = ?, finished_at = ?, result_sha256 = ?, result_json = ?
			WHERE project_id = ? AND task_id = ? AND job_id = ? AND status IN ('PENDING', 'RUNNING', 'CANCELLING')
			AND (status <> 'CANCELLING' OR ? <> 'RUNNING')`, updated.Status, updated.StartedAt,
			updated.FinishedAt, updated.ResultSHA256, result, projectID, taskID, job.JobID, updated.Status); err != nil {
			writeStoreError(w, "save remote comparison", err)
			return
		}
		job, err = readRemoteComparison(saveCtx, s.db, projectID, taskID)
		if err != nil {
			writeStoreError(w, "read updated comparison", err)
			return
		}
		writeRemoteComparison(w, node, job)
	}
}

func writeRemoteComparison(w http.ResponseWriter, node *ComputeNode, job remoteComparison) {
	var receipt *computeJob
	if job.JobID != "" {
		receipt = &job.computeJob
	}
	writeJSON(w, http.StatusOK, struct {
		Configured bool        `json:"configured"`
		Job        *computeJob `json:"job,omitempty"`
	}{node != nil, receipt})
}
