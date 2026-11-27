package workbench

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scriptRunTestPython(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"SCRIPT_RUN_TEST_PYTHON", "RESEARCH_SCRIPT_PYTHON"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	t.Skip("set SCRIPT_RUN_TEST_PYTHON or RESEARCH_SCRIPT_PYTHON to run real Python script tests")
	return ""
}

func doScriptJSON(t *testing.T, handler http.Handler, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	} else {
		payload = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func waitForScriptRun(t *testing.T, handler http.Handler, projectID, runID string, predicate func(ScriptRun) bool) ScriptRun {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res := doScriptJSON(t, handler, http.MethodGet, "http://127.0.0.1/api/v1/projects/"+projectID+"/script-runs/"+runID, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("get script run: status %d body %s", res.Code, res.Body.String())
		}
		var run ScriptRun
		if err := json.Unmarshal(res.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if predicate(run) {
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for script run %s", runID)
	return ScriptRun{}
}

func TestScriptRunUnconfiguredAndValidation(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "unconf-proj", Title: "未配置脚本测试", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	scriptDoc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "analysis/calc.py", "", "print('ok')\n")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{})

	listRes := doScriptJSON(t, handler, http.MethodGet, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", nil)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list script runs: got %d %s", listRes.Code, listRes.Body.String())
	}
	var listBody struct {
		Configured bool            `json:"configured"`
		Limits     ScriptRunLimits `json:"limits"`
		Items      []ScriptRun     `json:"items"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if listBody.Configured || listBody.Limits.MaxOutputFiles != 4 || listBody.Limits.MaxLogBytes != 16384 || listBody.Limits.NetworkAccess {
		t.Fatalf("unexpected unconfigured metadata: %+v", listBody)
	}

	// Missing explicit confirmation -> 400
	noConfirm := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-no-confirm",
		"scriptVersionId": scriptDoc.VersionID,
		"timeoutSeconds":  5,
		"confirmed":       false,
	})
	if noConfirm.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirmation, got %d %s", noConfirm.Code, noConfirm.Body.String())
	}

	// Confirmed but runner unconfigured -> 503
	unconf := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-unconf",
		"scriptVersionId": scriptDoc.VersionID,
		"timeoutSeconds":  5,
		"confirmed":       true,
	})
	if unconf.Code != http.StatusServiceUnavailable || !strings.Contains(unconf.Body.String(), "RESEARCH_SCRIPT_PYTHON") {
		t.Fatalf("expected 503 when RESEARCH_SCRIPT_PYTHON unset, got %d %s", unconf.Code, unconf.Body.String())
	}
}

func TestScriptRunEndToEndIsolationReplayAndPublish(t *testing.T) {
	python := scriptRunTestPython(t)
	ctx := t.Context()

	t.Setenv("RESEARCH_MODEL_API_KEY", "secret-model-key-must-not-leak")
	t.Setenv("RESEARCH_COMPUTE_NODE_TOKEN", "secret-node-token-must-not-leak")

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	projA, _, err := store.createProject(ctx, createProjectInput{RequestID: "proj-a", Title: "项目甲", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	projB, _, err := store.createProject(ctx, createProjectInput{RequestID: "proj-b", Title: "项目乙", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}

	csvDoc, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, "data/samples.csv", "", "group,value\nA,10\nB,20\nC,30\n")
	if err != nil {
		t.Fatal(err)
	}
	scriptSource := `import csv, json, os, sys

assert "RESEARCH_MODEL_API_KEY" not in os.environ, "model key leaked"
assert "RESEARCH_COMPUTE_NODE_TOKEN" not in os.environ, "node token leaked"

in_path = os.environ["RESEARCH_INPUT_PATH"]
out_dir = os.environ["RESEARCH_OUTPUT_DIR"]

values = []
with open(in_path, "r", encoding="utf-8", newline="") as f:
    for row in csv.DictReader(f):
        values.append(float(row["value"]))

summary = {
    "count": len(values),
    "sum": sum(values),
    "mean": sum(values) / len(values),
}
with open(os.path.join(out_dir, "summary.json"), "w", encoding="utf-8") as f:
    json.dump(summary, f, ensure_ascii=False, sort_keys=True)

print("计算完成:", summary["mean"])
print("诊断提示", file=sys.stderr)
`
	scriptDoc, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, "scripts/analyze.py", "", scriptSource)
	if err != nil {
		t.Fatal(err)
	}
	nonPyDoc, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, "notes/readme.txt", "", "hello")
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{ScriptPython: python})

	// Reject non-.py script
	badType := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-non-py",
		"scriptVersionId": nonPyDoc.VersionID,
		"confirmed":       true,
	})
	if badType.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for non-.py file, got %d %s", badType.Code, badType.Body.String())
	}

	// Reject cross-project script/input versions
	crossScript := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projB.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-cross-script",
		"scriptVersionId": scriptDoc.VersionID,
		"confirmed":       true,
	})
	if crossScript.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project script version, got %d %s", crossScript.Code, crossScript.Body.String())
	}
	projBScript, err := store.saveWorkspaceDocument(ctx, projB.ProjectID, "run.py", "", "print('b')\n")
	if err != nil {
		t.Fatal(err)
	}
	crossInput := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projB.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-cross-input",
		"scriptVersionId": projBScript.VersionID,
		"inputVersionId":  csvDoc.VersionID,
		"confirmed":       true,
	})
	if crossInput.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project input version, got %d %s", crossInput.Code, crossInput.Body.String())
	}

	// Submit authorized run
	createPayload := map[string]any{
		"requestId":       "req-run-1",
		"scriptVersionId": scriptDoc.VersionID,
		"inputVersionId":  csvDoc.VersionID,
		"timeoutSeconds":  10,
		"confirmed":       true,
	}
	createRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs", createPayload)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create script run: got %d %s", createRes.Code, createRes.Body.String())
	}
	var created ScriptRun
	if err := json.Unmarshal(createRes.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.TimeoutSeconds != 10 || created.MaxOutputFiles != 4 || created.MaxOutputFileBytes != 524288 || created.MaxLogBytes != 16384 {
		t.Fatalf("missing resource limits in receipt: %+v", created)
	}

	// Replay with identical requestId & params -> 200 OK same runId
	replayRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs", createPayload)
	if replayRes.Code != http.StatusOK {
		t.Fatalf("replay script run: got %d %s", replayRes.Code, replayRes.Body.String())
	}
	var replayed ScriptRun
	if err := json.Unmarshal(replayRes.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.RunID != created.RunID {
		t.Fatalf("replay returned different runId: got %s want %s", replayed.RunID, created.RunID)
	}

	// Same requestId with different params -> 409 Conflict
	conflictRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-run-1",
		"scriptVersionId": scriptDoc.VersionID,
		"inputVersionId":  csvDoc.VersionID,
		"timeoutSeconds":  15,
		"confirmed":       true,
	})
	if conflictRes.Code != http.StatusConflict {
		t.Fatalf("expected 409 for conflicting requestId, got %d %s", conflictRes.Code, conflictRes.Body.String())
	}

	// Wait for completion
	done := waitForScriptRun(t, handler, projA.ProjectID, created.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if done.Status != scriptStatusSucceeded || done.Phase != scriptPhaseSucceeded || done.ExitCode == nil || *done.ExitCode != 0 {
		t.Fatalf("expected SUCCEEDED run, got %+v", done)
	}
	if !strings.Contains(done.Stdout, "计算完成: 20.0") || !strings.Contains(done.Stderr, "诊断提示") {
		t.Fatalf("unexpected logs: stdout=%q stderr=%q", done.Stdout, done.Stderr)
	}
	if len(done.Artifacts) != 1 || done.Artifacts[0].Name != "summary.json" || !validObjectDigest(done.Artifacts[0].SHA256) {
		t.Fatalf("unexpected artifacts: %+v", done.Artifacts)
	}
	artifact := done.Artifacts[0]

	// Cross-project GET, cancel, publish all rejected with 404
	if res := doScriptJSON(t, handler, http.MethodGet, "http://127.0.0.1/api/v1/projects/"+projB.ProjectID+"/script-runs/"+done.RunID, nil); res.Code != http.StatusNotFound {
		t.Fatalf("cross-project get: got %d", res.Code)
	}
	if res := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projB.ProjectID+"/script-runs/"+done.RunID+"/cancel", map[string]any{}); res.Code != http.StatusNotFound {
		t.Fatalf("cross-project cancel: got %d", res.Code)
	}
	if res := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projB.ProjectID+"/script-runs/"+done.RunID+"/artifacts/publish", map[string]any{
		"name": artifact.Name, "sha256": artifact.SHA256,
	}); res.Code != http.StatusNotFound {
		t.Fatalf("cross-project publish: got %d", res.Code)
	}

	// Wrong digest rejected with 422
	badDigest := strings.Repeat("0", 64)
	if badDigest == artifact.SHA256 {
		badDigest = strings.Repeat("1", 64)
	}
	badPub := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs/"+done.RunID+"/artifacts/publish", map[string]any{
		"name": artifact.Name, "sha256": badDigest,
	})
	if badPub.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for wrong artifact digest, got %d %s", badPub.Code, badPub.Body.String())
	}

	// Publish with matching digest -> workspace scripts/<runId>/summary.json
	pubRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs/"+done.RunID+"/artifacts/publish", map[string]any{
		"name": artifact.Name, "sha256": artifact.SHA256,
	})
	if pubRes.Code != http.StatusOK {
		t.Fatalf("publish artifact: got %d %s", pubRes.Code, pubRes.Body.String())
	}
	var published WorkspaceFile
	if err := json.Unmarshal(pubRes.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	wantPath := "scripts/" + done.RunID + "/summary.json"
	if published.Path != wantPath || published.SHA256 != artifact.SHA256 || published.Size != artifact.Size {
		t.Fatalf("unexpected published workspace file: %+v", published)
	}

	// Edit published file in workspace, then repeat publish -> returns original version & preserves later edit
	editedDoc, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, wantPath, published.SHA256, `{"count": 3, "note": "人工补充"}`)
	if err != nil {
		t.Fatal(err)
	}
	repubRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+projA.ProjectID+"/script-runs/"+done.RunID+"/artifacts/publish", map[string]any{
		"name": artifact.Name, "sha256": artifact.SHA256,
	})
	if repubRes.Code != http.StatusOK {
		t.Fatalf("repeat publish: got %d %s", repubRes.Code, repubRes.Body.String())
	}
	var republished WorkspaceFile
	if err := json.Unmarshal(repubRes.Body.Bytes(), &republished); err != nil {
		t.Fatal(err)
	}
	if republished.VersionID != published.VersionID {
		t.Fatalf("repeat publish should return original version %s, got %s", published.VersionID, republished.VersionID)
	}
	latestDoc, err := store.workspaceFile(ctx, projA.ProjectID, wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if latestDoc.VersionID != editedDoc.VersionID {
		t.Fatalf("repeat publish overwrote later edit: latest=%s want=%s", latestDoc.VersionID, editedDoc.VersionID)
	}
}

func TestScriptRunNetworkBlockedStopAndTimeout(t *testing.T) {
	python := scriptRunTestPython(t)
	ctx := t.Context()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "guard-proj", Title: "边界验证项目", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	netScript, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "net.py", "", "import socket\nsocket.socket()\n")
	if err != nil {
		t.Fatal(err)
	}
	sleepScript, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "sleep.py", "", "import os, time\nprint('sleeping', flush=True)\ntime.sleep(20)\nopen(os.path.join(os.environ['RESEARCH_OUTPUT_DIR'], 'late.txt'), 'w').write('late')\n")
	if err != nil {
		t.Fatal(err)
	}
	timeoutScript, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "slow.py", "", "import time\ntime.sleep(5)\n")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{ScriptPython: python})

	// 1. Network blocked
	netRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-net",
		"scriptVersionId": netScript.VersionID,
		"timeoutSeconds":  5,
		"confirmed":       true,
	})
	if netRes.Code != http.StatusCreated {
		t.Fatalf("create net run: got %d %s", netRes.Code, netRes.Body.String())
	}
	var netRun ScriptRun
	_ = json.Unmarshal(netRes.Body.Bytes(), &netRun)
	netDone := waitForScriptRun(t, handler, project.ProjectID, netRun.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if netDone.Status != scriptStatusFailed || !strings.Contains(netDone.Stderr, "受限脚本模式禁止网络访问") {
		t.Fatalf("expected network PermissionError, got %+v", netDone)
	}

	// 2. Cancel running script
	sleepRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-sleep",
		"scriptVersionId": sleepScript.VersionID,
		"timeoutSeconds":  25,
		"confirmed":       true,
	})
	if sleepRes.Code != http.StatusCreated {
		t.Fatalf("create sleep run: got %d %s", sleepRes.Code, sleepRes.Body.String())
	}
	var sleepRun ScriptRun
	_ = json.Unmarshal(sleepRes.Body.Bytes(), &sleepRun)
	_ = waitForScriptRun(t, handler, project.ProjectID, sleepRun.RunID, func(r ScriptRun) bool {
		return r.Status == scriptStatusRunning && r.Phase == scriptPhaseExecuting
	})
	// Concurrency guard: second concurrent run in same project -> 409
	busyRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-busy",
		"scriptVersionId": timeoutScript.VersionID,
		"timeoutSeconds":  5,
		"confirmed":       true,
	})
	if busyRes.Code != http.StatusConflict {
		t.Fatalf("expected 409 when project already running a script, got %d %s", busyRes.Code, busyRes.Body.String())
	}

	cancelRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs/"+sleepRun.RunID+"/cancel", map[string]any{})
	if cancelRes.Code != http.StatusOK {
		t.Fatalf("cancel sleep run: got %d %s", cancelRes.Code, cancelRes.Body.String())
	}
	sleepDone := waitForScriptRun(t, handler, project.ProjectID, sleepRun.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if sleepDone.Status != scriptStatusCancelled || sleepDone.Phase != scriptPhaseCancelled || len(sleepDone.Artifacts) != 0 {
		t.Fatalf("expected CANCELLED with no artifacts, got %+v", sleepDone)
	}

	// 3. Timeout (1s limit vs 5s sleep)
	toRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-timeout",
		"scriptVersionId": timeoutScript.VersionID,
		"timeoutSeconds":  1,
		"confirmed":       true,
	})
	if toRes.Code != http.StatusCreated {
		t.Fatalf("create timeout run: got %d %s", toRes.Code, toRes.Body.String())
	}
	var toRun ScriptRun
	_ = json.Unmarshal(toRes.Body.Bytes(), &toRun)
	toDone := waitForScriptRun(t, handler, project.ProjectID, toRun.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if toDone.Status != scriptStatusTimedOut || toDone.Phase != scriptPhaseTimedOut || len(toDone.Artifacts) != 0 {
		t.Fatalf("expected TIMED_OUT with no artifacts, got %+v", toDone)
	}

	// 4. Bounded log truncation (> 16 KiB)
	verboseScript, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "verbose.py", "", "print('A' * 20000)\n")
	if err != nil {
		t.Fatal(err)
	}
	verbRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-verbose",
		"scriptVersionId": verboseScript.VersionID,
		"timeoutSeconds":  5,
		"confirmed":       true,
	})
	if verbRes.Code != http.StatusCreated {
		t.Fatalf("create verbose run: got %d %s", verbRes.Code, verbRes.Body.String())
	}
	var verbRun ScriptRun
	_ = json.Unmarshal(verbRes.Body.Bytes(), &verbRun)
	verbDone := waitForScriptRun(t, handler, project.ProjectID, verbRun.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if verbDone.Status != scriptStatusSucceeded || !verbDone.StdoutTruncated || len(verbDone.Stdout) != maxScriptLogBytes {
		t.Fatalf("expected truncated 16 KiB stdout, got status=%s trunc=%v len=%d", verbDone.Status, verbDone.StdoutTruncated, len(verbDone.Stdout))
	}
}

func TestScriptRunRestartRecoveryAndBackupRestore(t *testing.T) {
	python := scriptRunTestPython(t)
	ctx := t.Context()

	sourceDir := t.TempDir()
	store, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "backup-script-proj", Title: "备份恢复脚本项目", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	scriptDoc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "emit.py", "",
		"import os\nopen(os.path.join(os.environ['RESEARCH_OUTPUT_DIR'], 'result.txt'), 'w', encoding='utf-8').write('备份校验内容\\n')\nprint('done')\n")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{ScriptPython: python})

	// Run a script to SUCCEEDED, keep its artifact UNPUBLISHED before backup
	createRes := doScriptJSON(t, handler, http.MethodPost, "http://127.0.0.1/api/v1/projects/"+project.ProjectID+"/script-runs", map[string]any{
		"requestId":       "req-backup-ok",
		"scriptVersionId": scriptDoc.VersionID,
		"timeoutSeconds":  10,
		"confirmed":       true,
	})
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create run: %d %s", createRes.Code, createRes.Body.String())
	}
	var okRun ScriptRun
	_ = json.Unmarshal(createRes.Body.Bytes(), &okRun)
	okDone := waitForScriptRun(t, handler, project.ProjectID, okRun.RunID, func(r ScriptRun) bool {
		return !scriptRunActive(r.Status)
	})
	if okDone.Status != scriptStatusSucceeded || len(okDone.Artifacts) != 1 {
		t.Fatalf("expected SUCCEEDED run with 1 artifact, got %+v", okDone)
	}

	// Simulate an unfinished RUNNING run left in DB across restart
	inflight, created, err := store.createScriptRun(ctx, project.ProjectID, createScriptRunInput{
		RequestID:       "req-inflight",
		ScriptVersionID: scriptDoc.VersionID,
		TimeoutSeconds:  10,
		Confirmed:       true,
	})
	if err != nil || !created {
		t.Fatalf("seed inflight run: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE script_runs SET status = 'RUNNING', phase = ? WHERE run_id = ?`,
		scriptPhaseExecuting, inflight.RunID); err != nil {
		t.Fatal(err)
	}

	// Create backup before restart
	backupRes := httptest.NewRecorder()
	handler.ServeHTTP(backupRes, httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/api/v1/workspace-backup", nil))
	if backupRes.Code != http.StatusOK {
		t.Fatalf("workspace backup: got %d %s", backupRes.Code, backupRes.Body.String())
	}
	backupFile := filepath.Join(t.TempDir(), "v65-backup.zip")
	if err := os.WriteFile(backupFile, backupRes.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Close & reopen source store -> inflight run must become INTERRUPTED
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, err := reopened.getScriptRun(ctx, project.ProjectID, inflight.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != scriptStatusInterrupted || recovered.Phase != scriptPhaseInterrupted || recovered.FinishedAt == "" {
		t.Fatalf("expected INTERRUPTED after restart, got %+v", recovered)
	}

	// Restore v65 backup to fresh directory -> verify both runs and unpublished artifact object survived
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := RestoreWorkspace(ctx, backupFile, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })

	restoredInflight, err := restored.getScriptRun(ctx, project.ProjectID, inflight.RunID)
	if err != nil || restoredInflight.Status != scriptStatusInterrupted {
		t.Fatalf("expected restored inflight run to be INTERRUPTED, got %+v (%v)", restoredInflight, err)
	}
	restoredOk, err := restored.getScriptRun(ctx, project.ProjectID, okDone.RunID)
	if err != nil || restoredOk.Status != scriptStatusSucceeded || len(restoredOk.Artifacts) != 1 || restoredOk.Artifacts[0] != okDone.Artifacts[0] {
		t.Fatalf("restored script run mismatch: %+v (%v)", restoredOk, err)
	}
	published, err := restored.publishScriptRunArtifact(ctx, project.ProjectID, okDone.RunID, okDone.Artifacts[0].Name, okDone.Artifacts[0].SHA256)
	if err != nil || published.SHA256 != okDone.Artifacts[0].SHA256 {
		t.Fatalf("publish restored artifact failed: %+v (%v)", published, err)
	}
}
