package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const restrictedScriptBootstrap = `
import runpy
import socket
import sys

def _deny_network(*args, **kwargs):
    raise PermissionError("受限脚本模式禁止网络访问")

socket.socket = _deny_network
socket.create_connection = _deny_network
socket.create_server = _deny_network
socket.socketpair = _deny_network
socket.fromfd = _deny_network
if hasattr(socket, "SocketType"):
    socket.SocketType = _deny_network

try:
    import _socket
    _socket.socket = _deny_network
    if hasattr(_socket, "socketpair"):
        _socket.socketpair = _deny_network
except Exception:
    pass

script_path = sys.argv[1]
sys.argv = [script_path]
runpy.run_path(script_path, run_name="__main__")
`

type scriptWorkers struct {
	mu      sync.Mutex
	active  map[string]*scriptWorker
	closing bool
	wg      sync.WaitGroup
}

type scriptWorker struct {
	projectID     string
	cancel        context.CancelFunc
	userCancelled bool
}

func (w *scriptWorkers) close() {
	w.mu.Lock()
	w.closing = true
	for _, worker := range w.active {
		if worker.cancel != nil {
			worker.cancel()
		}
	}
	w.mu.Unlock()
	w.wg.Wait()
}

func (s *Store) startScriptRun(run ScriptRun, pythonExecutable string) {
	workers := &s.scripts
	workers.mu.Lock()
	if workers.closing {
		workers.mu.Unlock()
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusInterrupted, scriptPhaseInterrupted, nil,
			"", "", false, false, nil, "服务正在关闭，受限脚本未启动。")
		return
	}
	if workers.active == nil {
		workers.active = make(map[string]*scriptWorker)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	worker := &scriptWorker{projectID: run.ProjectID, cancel: cancel}
	workers.active[run.RunID] = worker
	workers.wg.Add(1)
	workers.mu.Unlock()

	go s.executeScriptRun(cancelCtx, worker, run, pythonExecutable)
}

func (s *Store) cancelScriptRun(ctx context.Context, projectID, runID string) (ScriptRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptRun{}, err
	}
	defer tx.Rollback()

	run, err := lookupScriptRun(ctx, tx, projectID, runID)
	if err != nil {
		return ScriptRun{}, err
	}
	if !scriptRunActive(run.Status) {
		return run, nil
	}
	if run.Status == scriptStatusQueued || run.Status == scriptStatusRunning {
		if _, err := tx.ExecContext(ctx, `UPDATE script_runs SET status = 'CANCELLING', phase = ?
			WHERE project_id = ? AND run_id = ? AND status IN ('QUEUED', 'RUNNING')`,
			scriptPhaseCancelling, projectID, runID); err != nil {
			return ScriptRun{}, err
		}
		run, err = lookupScriptRun(ctx, tx, projectID, runID)
		if err != nil {
			return ScriptRun{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ScriptRun{}, err
	}

	s.scripts.mu.Lock()
	worker := s.scripts.active[runID]
	if worker != nil {
		worker.userCancelled = true
		worker.cancel()
	}
	s.scripts.mu.Unlock()

	if worker == nil && run.Status == scriptStatusCancelling {
		// No live worker holds this run anymore; finish the transition immediately.
		s.finalizeScriptRunCancelled(runID, "", "", false, false)
		return s.getScriptRun(ctx, projectID, runID)
	}
	return run, nil
}

func (s *Store) executeScriptRun(cancelCtx context.Context, worker *scriptWorker, run ScriptRun, pythonExecutable string) {
	defer func() {
		s.scripts.mu.Lock()
		delete(s.scripts.active, run.RunID)
		s.scripts.mu.Unlock()
		worker.cancel()
		s.scripts.wg.Done()
	}()

	isUserCancelled := func() bool {
		s.scripts.mu.Lock()
		defer s.scripts.mu.Unlock()
		return worker.userCancelled
	}
	isServerClosing := func() bool {
		s.scripts.mu.Lock()
		defer s.scripts.mu.Unlock()
		return s.scripts.closing
	}

	startedAt := time.Now().UTC().Format(createdAtLayout)
	if ok := s.transitionScriptRun(run.RunID,
		`UPDATE script_runs SET status = 'RUNNING', phase = ?, started_at = ? WHERE run_id = ? AND status = 'QUEUED'`,
		scriptPhasePreparing, startedAt, run.RunID); !ok {
		if isUserCancelled() {
			s.finalizeScriptRunCancelled(run.RunID, "", "", false, false)
		} else {
			s.finalizeScriptRunTerminal(run.RunID, scriptStatusInterrupted, scriptPhaseInterrupted, nil,
				"", "", false, false, nil, "受限脚本在启动前已中断。")
		}
		return
	}

	sandboxDir, err := os.MkdirTemp("", "research-script-run-")
	if err != nil {
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusFailed, scriptPhaseFailed, nil,
			"", "", false, false, nil, "无法创建临时沙箱目录。")
		return
	}
	defer os.RemoveAll(sandboxDir)

	scriptPath, inputFilePath, outputDir, tmpDir, err := s.prepareScriptSandbox(sandboxDir, run)
	if err != nil {
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusFailed, scriptPhaseFailed, nil,
			"", "", false, false, nil, err.Error())
		return
	}

	if ok := s.transitionScriptRun(run.RunID,
		`UPDATE script_runs SET phase = ? WHERE run_id = ? AND status = 'RUNNING'`,
		scriptPhaseExecuting, run.RunID); !ok {
		if isUserCancelled() {
			s.finalizeScriptRunCancelled(run.RunID, "", "", false, false)
		} else {
			s.finalizeScriptRunTerminal(run.RunID, scriptStatusInterrupted, scriptPhaseInterrupted, nil,
				"", "", false, false, nil, "受限脚本在执行前已中断。")
		}
		return
	}

	runCtx, stopTimeout := context.WithTimeout(cancelCtx, time.Duration(run.TimeoutSeconds)*time.Second)
	defer stopTimeout()

	cmd := exec.CommandContext(runCtx, pythonExecutable, "-I", "-B", "-X", "utf8", "-c", restrictedScriptBootstrap, scriptPath)
	cmd.Dir = sandboxDir
	cmd.Env = restrictedScriptEnv(os.Environ(), tmpDir, outputDir, inputFilePath)
	cmd.WaitDelay = 2 * time.Second
	stdoutBuf := newBoundedLogBuffer(maxScriptLogBytes)
	stderrBuf := newBoundedLogBuffer(maxScriptLogBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	runErr := cmd.Run()
	stdoutText, stdoutTrunc := stdoutBuf.text()
	stderrText, stderrTrunc := stderrBuf.text()

	if isUserCancelled() {
		s.finalizeScriptRunCancelled(run.RunID, stdoutText, stderrText, stdoutTrunc, stderrTrunc)
		return
	}
	if isServerClosing() && cancelCtx.Err() != nil {
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusInterrupted, scriptPhaseInterrupted, nil,
			stdoutText, stderrText, stdoutTrunc, stderrTrunc, nil, "服务关闭中断了受限脚本运行。可确认后重新提交。")
		return
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusTimedOut, scriptPhaseTimedOut, nil,
			stdoutText, stderrText, stdoutTrunc, stderrTrunc, nil,
			fmt.Sprintf("脚本运行超过 %d 秒时限，已终止。", run.TimeoutSeconds))
		return
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			s.finalizeScriptRunTerminal(run.RunID, scriptStatusFailed, scriptPhaseFailed, &code,
				stdoutText, stderrText, stdoutTrunc, stderrTrunc, nil,
				fmt.Sprintf("脚本执行失败（退出码 %d）。", code))
			return
		}
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusFailed, scriptPhaseFailed, nil,
			stdoutText, stderrText, stdoutTrunc, stderrTrunc, nil,
			"无法启动受限脚本运行器，请检查服务端配置。")
		return
	}

	if ok := s.transitionScriptRun(run.RunID,
		`UPDATE script_runs SET phase = ? WHERE run_id = ? AND status = 'RUNNING'`,
		scriptPhaseValidating, run.RunID); !ok {
		s.finalizeScriptRunCancelled(run.RunID, stdoutText, stderrText, stdoutTrunc, stderrTrunc)
		return
	}

	artifacts, err := s.collectScriptArtifacts(run.RunID, outputDir)
	if err != nil {
		zero := 0
		s.finalizeScriptRunTerminal(run.RunID, scriptStatusFailed, scriptPhaseFailed, &zero,
			stdoutText, stderrText, stdoutTrunc, stderrTrunc, nil, err.Error())
		return
	}

	zero := 0
	s.finalizeScriptRunSucceeded(run.RunID, &zero, stdoutText, stderrText, stdoutTrunc, stderrTrunc, artifacts)
}

func (s *Store) prepareScriptSandbox(sandboxDir string, run ScriptRun) (scriptPath, inputPath, outputDir, tmpDir string, err error) {
	inputsDir := filepath.Join(sandboxDir, "inputs")
	outputDir = filepath.Join(sandboxDir, "outputs")
	tmpDir = filepath.Join(sandboxDir, "tmp")
	for _, dir := range []string{inputsDir, outputDir, tmpDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", "", "", "", errors.New("无法初始化沙箱子目录。")
		}
	}
	scriptBytes, err := s.readObject(run.ScriptSHA256)
	if err != nil {
		return "", "", "", "", errors.New("无法读取所选脚本版本的原字节。")
	}
	if digestHex(scriptBytes) != run.ScriptSHA256 || len(scriptBytes) > maxScriptSourceBytes {
		return "", "", "", "", errors.New("脚本版本摘要或大小校验失败。")
	}
	scriptPath = filepath.Join(sandboxDir, "script.py")
	if err := os.WriteFile(scriptPath, scriptBytes, 0o400); err != nil {
		return "", "", "", "", errors.New("无法写入沙箱脚本文件。")
	}
	if run.InputVersionID != "" {
		inputBytes, err := s.readObject(run.InputSHA256)
		if err != nil {
			return "", "", "", "", errors.New("无法读取所选输入版本的原字节。")
		}
		if digestHex(inputBytes) != run.InputSHA256 || len(inputBytes) > maxScriptInputBytes {
			return "", "", "", "", errors.New("输入文件版本摘要或大小校验失败。")
		}
		safeName := safeSandboxFilename(path.Base(run.InputPath))
		inputPath = filepath.Join(inputsDir, safeName)
		if err := os.WriteFile(inputPath, inputBytes, 0o400); err != nil {
			return "", "", "", "", errors.New("无法写入沙箱输入文件。")
		}
	}
	return scriptPath, inputPath, outputDir, tmpDir, nil
}

func safeSandboxFilename(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." || len(base) > 128 || !utf8.ValidString(base) ||
		strings.ContainsAny(base, `/\:*?"<>|`) || strings.ContainsFunc(base, unicode.IsControl) {
		return "input.dat"
	}
	return base
}

func restrictedScriptEnv(parent []string, tmpDir, outputDir, inputPath string) []string {
	env := make([]string, 0, 16)
	for _, entry := range parent {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "LANG", "LC_ALL", "LC_CTYPE":
			env = append(env, entry)
		}
	}
	env = append(env,
		"TEMP="+tmpDir,
		"TMP="+tmpDir,
		"TMPDIR="+tmpDir,
		"NO_PROXY=*",
		"no_proxy=*",
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"ALL_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"all_proxy=",
		"RESEARCH_OUTPUT_DIR="+outputDir,
	)
	if inputPath != "" {
		env = append(env, "RESEARCH_INPUT_PATH="+inputPath)
	}
	return env
}

func (s *Store) collectScriptArtifacts(runID, outputDir string) ([]ScriptRunArtifact, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, errors.New("无法扫描产物输出目录。")
	}
	if len(entries) > maxScriptOutputFiles {
		return nil, fmt.Errorf("产物数量超过上限（最多 %d 份）。", maxScriptOutputFiles)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)

	artifacts := make([]ScriptRunArtifact, 0, len(names))
	totalBytes := 0
	for _, name := range names {
		if !validScriptArtifactName(name) || !validWorkspacePath("scripts/"+runID+"/"+name) {
			return nil, fmt.Errorf("产物文件名 %q 不符合安全规范。", name)
		}
		fullPath := filepath.Join(outputDir, name)
		info, err := os.Lstat(fullPath)
		if err != nil {
			return nil, errors.New("无法读取产物文件信息。")
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("产物 %q 不是单层常规文件，已拒绝采集。", name)
		}
		if info.Size() > maxScriptOutputFileBytes {
			return nil, fmt.Errorf("产物 %q 超过单份 512 KiB 上限。", name)
		}
		file, err := os.OpenInRoot(outputDir, name)
		if err != nil {
			return nil, fmt.Errorf("无法安全打开产物 %q。", name)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxScriptOutputFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("读取产物 %q 失败。", name)
		}
		if len(data) > maxScriptOutputFileBytes {
			return nil, fmt.Errorf("产物 %q 超过单份 512 KiB 上限。", name)
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil, fmt.Errorf("产物 %q 不是有效的 UTF-8 文本文件，无法发布至文本工作区。", name)
		}
		totalBytes += len(data)
		if totalBytes > maxScriptOutputTotalBytes {
			return nil, errors.New("本次运行的产物总大小超过 1 MiB 上限。")
		}
		digest := digestHex(data)
		if err := s.saveObjectBytes(digest, data); err != nil {
			return nil, fmt.Errorf("保存产物 %q 内容对象失败。", name)
		}
		artifacts = append(artifacts, ScriptRunArtifact{
			Name:   name,
			Size:   len(data),
			SHA256: digest,
		})
	}
	return artifacts, nil
}

func (s *Store) transitionScriptRun(runID, query string, args ...any) bool {
	ctx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		slog.Error("transition script run", "run_id", runID, "error", err)
		return false
	}
	affected, err := res.RowsAffected()
	return err == nil && affected == 1
}

func (s *Store) finalizeScriptRunCancelled(runID, stdout, stderr string, stdoutTrunc, stderrTrunc bool) {
	ctx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	now := time.Now().UTC().Format(createdAtLayout)
	_, err := s.db.ExecContext(ctx, `UPDATE script_runs
		SET status = 'CANCELLED', phase = ?, exit_code = NULL,
			stdout = ?, stderr = ?, stdout_truncated = ?, stderr_truncated = ?,
			artifacts_json = '[]', error_message = '', finished_at = ?
		WHERE run_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`,
		scriptPhaseCancelled, stdout, stderr, boolToInt(stdoutTrunc), boolToInt(stderrTrunc), now, runID)
	if err != nil {
		slog.Error("finalize cancelled script run", "run_id", runID, "error", err)
	}
}

func (s *Store) finalizeScriptRunTerminal(runID, status, phase string, exitCode *int, stdout, stderr string, stdoutTrunc, stderrTrunc bool, artifacts []ScriptRunArtifact, errMsg string) {
	ctx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	now := time.Now().UTC().Format(createdAtLayout)
	var code sql.NullInt64
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	encoded := "[]"
	if len(artifacts) > 0 {
		if raw, err := json.Marshal(artifacts); err == nil {
			encoded = string(raw)
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE script_runs
		SET status = ?, phase = ?, exit_code = ?,
			stdout = ?, stderr = ?, stdout_truncated = ?, stderr_truncated = ?,
			artifacts_json = ?, error_message = ?, finished_at = ?
		WHERE run_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`,
		status, phase, code, stdout, stderr, boolToInt(stdoutTrunc), boolToInt(stderrTrunc),
		encoded, errMsg, now, runID)
	if err != nil {
		slog.Error("finalize script run", "run_id", runID, "status", status, "error", err)
	}
}

func (s *Store) finalizeScriptRunSucceeded(runID string, exitCode *int, stdout, stderr string, stdoutTrunc, stderrTrunc bool, artifacts []ScriptRunArtifact) {
	ctx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("begin finalize succeeded script run", "run_id", runID, "error", err)
		return
	}
	defer tx.Rollback()

	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM script_runs WHERE run_id = ?`, runID).Scan(&currentStatus); err != nil {
		slog.Error("read script run status before commit", "run_id", runID, "error", err)
		return
	}
	now := time.Now().UTC().Format(createdAtLayout)
	if currentStatus != scriptStatusRunning {
		// Reject late artifacts if cancellation arrived before commit.
		if currentStatus == scriptStatusCancelling {
			if _, err := tx.ExecContext(ctx, `UPDATE script_runs
				SET status = 'CANCELLED', phase = ?, exit_code = NULL,
					stdout = ?, stderr = ?, stdout_truncated = ?, stderr_truncated = ?,
					artifacts_json = '[]', error_message = '', finished_at = ?
				WHERE run_id = ? AND status = 'CANCELLING'`,
				scriptPhaseCancelled, stdout, stderr, boolToInt(stdoutTrunc), boolToInt(stderrTrunc), now, runID); err != nil {
				slog.Error("cancel late script run", "run_id", runID, "error", err)
				return
			}
			_ = tx.Commit()
		}
		return
	}

	if artifacts == nil {
		artifacts = make([]ScriptRunArtifact, 0)
	}
	encoded, err := json.Marshal(artifacts)
	if err != nil {
		slog.Error("marshal script artifacts", "run_id", runID, "error", err)
		return
	}
	for _, artifact := range artifacts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO script_run_artifacts (run_id, name, sha256, size)
			VALUES (?, ?, ?, ?)`, runID, artifact.Name, artifact.SHA256, artifact.Size); err != nil {
			slog.Error("insert script artifact", "run_id", runID, "name", artifact.Name, "error", err)
			return
		}
	}
	var code sql.NullInt64
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE script_runs
		SET status = 'SUCCEEDED', phase = ?, exit_code = ?,
			stdout = ?, stderr = ?, stdout_truncated = ?, stderr_truncated = ?,
			artifacts_json = ?, error_message = '', finished_at = ?
		WHERE run_id = ? AND status = 'RUNNING'`,
		scriptPhaseSucceeded, code, stdout, stderr, boolToInt(stdoutTrunc), boolToInt(stderrTrunc),
		string(encoded), now, runID); err != nil {
		slog.Error("update succeeded script run", "run_id", runID, "error", err)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("commit succeeded script run", "run_id", runID, "error", err)
	}
}

type boundedLogBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func newBoundedLogBuffer(limit int) *boundedLogBuffer {
	return &boundedLogBuffer{limit: limit}
}

func (b *boundedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedLogBuffer) text() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw := b.buf.Bytes()
	cleaned := strings.ReplaceAll(strings.ToValidUTF8(string(raw), "\uFFFD"), "\x00", "")
	return cleaned, b.truncated
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
