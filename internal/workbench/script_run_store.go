package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const scriptRunsSchema = `
	CREATE TABLE script_runs (
		ordinal INTEGER PRIMARY KEY,
		run_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		script_version_id TEXT NOT NULL,
		script_path TEXT NOT NULL,
		script_sha256 TEXT NOT NULL,
		input_version_id TEXT NOT NULL DEFAULT '',
		input_path TEXT NOT NULL DEFAULT '',
		input_sha256 TEXT NOT NULL DEFAULT '',
		timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 30),
		status TEXT NOT NULL CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'TIMED_OUT', 'CANCELLING', 'CANCELLED', 'INTERRUPTED')),
		phase TEXT NOT NULL DEFAULT '',
		exit_code INTEGER,
		stdout TEXT NOT NULL DEFAULT '',
		stderr TEXT NOT NULL DEFAULT '',
		stdout_truncated INTEGER NOT NULL DEFAULT 0,
		stderr_truncated INTEGER NOT NULL DEFAULT 0,
		artifacts_json TEXT NOT NULL DEFAULT '[]',
		error_message TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		started_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		UNIQUE (project_id, request_id)
	);
	CREATE INDEX script_runs_project_created ON script_runs (project_id, ordinal DESC);
	CREATE TABLE script_run_artifacts (
		run_id TEXT NOT NULL REFERENCES script_runs(run_id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size INTEGER NOT NULL,
		PRIMARY KEY (run_id, name)
	);
	PRAGMA user_version = 65;
`

const (
	scriptStatusQueued      = "QUEUED"
	scriptStatusRunning     = "RUNNING"
	scriptStatusSucceeded   = "SUCCEEDED"
	scriptStatusFailed      = "FAILED"
	scriptStatusTimedOut    = "TIMED_OUT"
	scriptStatusCancelling  = "CANCELLING"
	scriptStatusCancelled   = "CANCELLED"
	scriptStatusInterrupted = "INTERRUPTED"

	scriptPhaseQueued      = "已排队"
	scriptPhasePreparing   = "正在准备沙箱"
	scriptPhaseExecuting   = "正在执行脚本"
	scriptPhaseValidating  = "正在校验产物"
	scriptPhaseSucceeded   = "已完成"
	scriptPhaseTimedOut    = "已超时"
	scriptPhaseCancelling  = "正在停止"
	scriptPhaseCancelled   = "已取消"
	scriptPhaseFailed      = "执行失败"
	scriptPhaseInterrupted = "已中断"

	scriptRunnerKind            = "python-isolated"
	defaultScriptTimeoutSeconds = 10
	minScriptTimeoutSeconds     = 1
	maxScriptTimeoutSeconds     = 30
	maxScriptSourceBytes        = 64 << 10
	maxScriptInputBytes         = 512 << 10
	maxScriptOutputFiles        = 4
	maxScriptOutputFileBytes    = 512 << 10
	maxScriptOutputTotalBytes   = 1 << 20
	maxScriptLogBytes           = 16 << 10
	scriptRunsListLimit         = 20
)

const scriptRunColumns = `run_id, project_id, request_id,
	script_version_id, script_path, script_sha256,
	input_version_id, input_path, input_sha256,
	timeout_seconds, status, phase, exit_code,
	stdout, stderr, stdout_truncated, stderr_truncated,
	artifacts_json, error_message, created_at, started_at, finished_at`

var (
	errScriptProjectNotFound        = errors.New("项目不存在。")
	errScriptRunNotFound            = errors.New("脚本运行记录不存在。")
	errScriptRequestConflict        = errors.New("该请求编号已用于不同的脚本运行参数。")
	errScriptProjectBusy            = errors.New("当前项目已有运行中的受限脚本，请等待完成或先停止。")
	errScriptVersionNotFound        = errors.New("所选脚本版本不存在或不属于当前项目。")
	errScriptInputNotFound          = errors.New("所选输入文件版本不存在或不属于当前项目。")
	errScriptInvalidType            = errors.New("受限脚本仅支持已保存的 .py 文件版本。")
	errScriptTooLarge               = errors.New("脚本文件不能超过 64 KiB。")
	errScriptInputTooLarge          = errors.New("输入文件不能超过 512 KiB。")
	errScriptRunNotSucceeded        = errors.New("只有已完成的脚本运行才可以发布产物。")
	errScriptArtifactNotFound       = errors.New("指定的脚本产物不存在。")
	errScriptArtifactDigestMismatch = errors.New("产物摘要与运行清单或对象字节不一致，已拒绝发布。")
)

type ScriptRunLimits struct {
	Runner              string `json:"runner"`
	NetworkAccess       bool   `json:"networkAccess"`
	MinTimeoutSeconds   int    `json:"minTimeoutSeconds"`
	MaxTimeoutSeconds   int    `json:"maxTimeoutSeconds"`
	DefaultTimeoutSecs  int    `json:"defaultTimeoutSeconds"`
	MaxScriptBytes      int    `json:"maxScriptBytes"`
	MaxInputBytes       int    `json:"maxInputBytes"`
	MaxOutputFiles      int    `json:"maxOutputFiles"`
	MaxOutputFileBytes  int    `json:"maxOutputFileBytes"`
	MaxOutputTotalBytes int    `json:"maxOutputTotalBytes"`
	MaxLogBytes         int    `json:"maxLogBytes"`
}

func defaultScriptRunLimits() ScriptRunLimits {
	return ScriptRunLimits{
		Runner:              scriptRunnerKind,
		NetworkAccess:       false,
		MinTimeoutSeconds:   minScriptTimeoutSeconds,
		MaxTimeoutSeconds:   maxScriptTimeoutSeconds,
		DefaultTimeoutSecs:  defaultScriptTimeoutSeconds,
		MaxScriptBytes:      maxScriptSourceBytes,
		MaxInputBytes:       maxScriptInputBytes,
		MaxOutputFiles:      maxScriptOutputFiles,
		MaxOutputFileBytes:  maxScriptOutputFileBytes,
		MaxOutputTotalBytes: maxScriptOutputTotalBytes,
		MaxLogBytes:         maxScriptLogBytes,
	}
}

type ScriptRunArtifact struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type ScriptRun struct {
	RunID              string              `json:"runId"`
	ProjectID          string              `json:"projectId"`
	RequestID          string              `json:"requestId"`
	ScriptVersionID    string              `json:"scriptVersionId"`
	ScriptPath         string              `json:"scriptPath"`
	ScriptSHA256       string              `json:"scriptSha256"`
	InputVersionID     string              `json:"inputVersionId,omitempty"`
	InputPath          string              `json:"inputPath,omitempty"`
	InputSHA256        string              `json:"inputSha256,omitempty"`
	TimeoutSeconds     int                 `json:"timeoutSeconds"`
	MaxOutputFiles     int                 `json:"maxOutputFiles"`
	MaxOutputFileBytes int                 `json:"maxOutputFileBytes"`
	MaxLogBytes        int                 `json:"maxLogBytes"`
	Status             string              `json:"status"`
	Phase              string              `json:"phase"`
	ExitCode           *int                `json:"exitCode,omitempty"`
	Stdout             string              `json:"stdout"`
	Stderr             string              `json:"stderr"`
	StdoutTruncated    bool                `json:"stdoutTruncated"`
	StderrTruncated    bool                `json:"stderrTruncated"`
	Artifacts          []ScriptRunArtifact `json:"artifacts"`
	ErrorMessage       string              `json:"errorMessage,omitempty"`
	CreatedAt          string              `json:"createdAt"`
	StartedAt          string              `json:"startedAt,omitempty"`
	FinishedAt         string              `json:"finishedAt,omitempty"`
}

type createScriptRunInput struct {
	RequestID       string `json:"requestId"`
	ScriptVersionID string `json:"scriptVersionId"`
	InputVersionID  string `json:"inputVersionId"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	Confirmed       bool   `json:"confirmed"`
}

func scriptRunActive(status string) bool {
	return status == scriptStatusQueued || status == scriptStatusRunning || status == scriptStatusCancelling
}

func validScriptArtifactName(name string) bool {
	if name == "" || len(name) > 128 || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsAny(name, `/\:`) || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	return validWorkspacePath("scripts/id/" + name)
}

func scanScriptRun(row interface{ Scan(...any) error }) (ScriptRun, error) {
	var (
		run             ScriptRun
		exitCode        sql.NullInt64
		stdoutTruncated int
		stderrTruncated int
		artifactsJSON   string
	)
	err := row.Scan(
		&run.RunID, &run.ProjectID, &run.RequestID,
		&run.ScriptVersionID, &run.ScriptPath, &run.ScriptSHA256,
		&run.InputVersionID, &run.InputPath, &run.InputSHA256,
		&run.TimeoutSeconds, &run.Status, &run.Phase, &exitCode,
		&run.Stdout, &run.Stderr, &stdoutTruncated, &stderrTruncated,
		&artifactsJSON, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
	)
	if err != nil {
		return ScriptRun{}, err
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		run.ExitCode = &code
	}
	run.StdoutTruncated = stdoutTruncated != 0
	run.StderrTruncated = stderrTruncated != 0
	run.MaxOutputFiles = maxScriptOutputFiles
	run.MaxOutputFileBytes = maxScriptOutputFileBytes
	run.MaxLogBytes = maxScriptLogBytes
	run.Artifacts = make([]ScriptRunArtifact, 0)
	if strings.TrimSpace(artifactsJSON) != "" {
		if err := json.Unmarshal([]byte(artifactsJSON), &run.Artifacts); err != nil {
			return ScriptRun{}, err
		}
		if run.Artifacts == nil {
			run.Artifacts = make([]ScriptRunArtifact, 0)
		}
	}
	return run, nil
}

func recoverInterruptedScriptRuns(tx *sql.Tx) error {
	now := time.Now().UTC().Format(createdAtLayout)
	_, err := tx.Exec(`UPDATE script_runs
		SET status = 'INTERRUPTED',
			phase = ?,
			error_message = CASE WHEN error_message = '' THEN ? ELSE error_message END,
			finished_at = CASE WHEN finished_at = '' THEN ? ELSE finished_at END
		WHERE status IN ('QUEUED', 'RUNNING', 'CANCELLING')`,
		scriptPhaseInterrupted, "服务重启中断了受限脚本运行。可确认后重新提交。", now)
	return err
}

func (s *Store) saveObjectBytes(digest string, data []byte) error {
	staged, err := os.CreateTemp(s.objectsDir, ".script-out-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if _, err := staged.Write(data); err != nil {
		return err
	}
	return s.publishObject(staged, digest)
}

func (s *Store) listScriptRuns(ctx context.Context, projectID string, limit int) ([]ScriptRun, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errScriptProjectNotFound
		}
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+scriptRunColumns+` FROM script_runs
		WHERE project_id = ? ORDER BY ordinal DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ScriptRun, 0, limit)
	for rows.Next() {
		run, err := scanScriptRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

func (s *Store) getScriptRun(ctx context.Context, projectID, runID string) (ScriptRun, error) {
	return lookupScriptRun(ctx, s.db, projectID, runID)
}

func lookupScriptRun(ctx context.Context, db workspaceQuery, projectID, runID string) (ScriptRun, error) {
	run, err := scanScriptRun(db.QueryRowContext(ctx, `SELECT `+scriptRunColumns+` FROM script_runs
		WHERE project_id = ? AND run_id = ?`, projectID, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptRun{}, errScriptRunNotFound
	}
	return run, err
}

func (s *Store) createScriptRun(ctx context.Context, projectID string, input createScriptRunInput) (ScriptRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptRun{}, false, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE project_id = ?`, projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ScriptRun{}, false, errScriptProjectNotFound
		}
		return ScriptRun{}, false, err
	}

	existing, err := scanScriptRun(tx.QueryRowContext(ctx, `SELECT `+scriptRunColumns+` FROM script_runs
		WHERE project_id = ? AND request_id = ?`, projectID, input.RequestID))
	if err == nil {
		if existing.ScriptVersionID == input.ScriptVersionID &&
			existing.InputVersionID == input.InputVersionID &&
			existing.TimeoutSeconds == input.TimeoutSeconds {
			return existing, false, nil
		}
		return ScriptRun{}, false, errScriptRequestConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ScriptRun{}, false, err
	}

	var active int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM script_runs
		WHERE project_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING') LIMIT 1`, projectID).Scan(&active)
	if err == nil {
		return ScriptRun{}, false, errScriptProjectBusy
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ScriptRun{}, false, err
	}

	scriptFile, err := lookupWorkspaceVersion(ctx, tx, projectID, input.ScriptVersionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ScriptRun{}, false, errScriptVersionNotFound
		}
		return ScriptRun{}, false, err
	}
	if !strings.EqualFold(path.Ext(scriptFile.Path), ".py") {
		return ScriptRun{}, false, errScriptInvalidType
	}
	if scriptFile.Size > maxScriptSourceBytes {
		return ScriptRun{}, false, errScriptTooLarge
	}

	var inputFile WorkspaceFile
	if input.InputVersionID != "" {
		inputFile, err = lookupWorkspaceVersion(ctx, tx, projectID, input.InputVersionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ScriptRun{}, false, errScriptInputNotFound
			}
			return ScriptRun{}, false, err
		}
		if inputFile.Size > maxScriptInputBytes {
			return ScriptRun{}, false, errScriptInputTooLarge
		}
	}

	runID := rand.Text()
	createdAt := time.Now().UTC().Format(createdAtLayout)
	run, err := scanScriptRun(tx.QueryRowContext(ctx, `INSERT INTO script_runs (
			run_id, project_id, request_id,
			script_version_id, script_path, script_sha256,
			input_version_id, input_path, input_sha256,
			timeout_seconds, status, phase, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+scriptRunColumns,
		runID, projectID, input.RequestID,
		scriptFile.VersionID, scriptFile.Path, scriptFile.SHA256,
		inputFile.VersionID, inputFile.Path, inputFile.SHA256,
		input.TimeoutSeconds, scriptStatusQueued, scriptPhaseQueued, createdAt,
	))
	if err != nil {
		return ScriptRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ScriptRun{}, false, err
	}
	return run, true, nil
}

func (s *Store) publishScriptRunArtifact(ctx context.Context, projectID, runID, name, expectedSHA256 string) (WorkspaceFile, error) {
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	run, err := s.getScriptRun(ctx, projectID, runID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if run.Status != scriptStatusSucceeded {
		return WorkspaceFile{}, errScriptRunNotSucceeded
	}
	var manifestItem *ScriptRunArtifact
	for i := range run.Artifacts {
		if run.Artifacts[i].Name == name {
			manifestItem = &run.Artifacts[i]
			break
		}
	}
	if manifestItem == nil {
		return WorkspaceFile{}, errScriptArtifactNotFound
	}
	var dbSHA256 string
	var dbSize int
	err = s.db.QueryRowContext(ctx, `SELECT sha256, size FROM script_run_artifacts WHERE run_id = ? AND name = ?`,
		run.RunID, name).Scan(&dbSHA256, &dbSize)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceFile{}, errScriptArtifactNotFound
	}
	if err != nil {
		return WorkspaceFile{}, err
	}
	if manifestItem.SHA256 != expectedSHA256 || dbSHA256 != expectedSHA256 || dbSize != manifestItem.Size {
		return WorkspaceFile{}, errScriptArtifactDigestMismatch
	}
	data, err := s.readObject(expectedSHA256)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errObjectDigest) {
			return WorkspaceFile{}, errScriptArtifactDigestMismatch
		}
		return WorkspaceFile{}, err
	}
	sum := sha256.Sum256(data)
	actualDigest := hex.EncodeToString(sum[:])
	if len(data) != manifestItem.Size || actualDigest != expectedSHA256 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return WorkspaceFile{}, errScriptArtifactDigestMismatch
	}
	targetPath := "scripts/" + run.RunID + "/" + name
	if !validWorkspacePath(targetPath) {
		return WorkspaceFile{}, errScriptArtifactNotFound
	}
	return s.saveComparisonArtifact(ctx, projectID, targetPath, string(data))
}
