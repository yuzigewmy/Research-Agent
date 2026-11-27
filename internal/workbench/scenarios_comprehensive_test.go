package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

// TestScenarioT01ConcurrentIdempotentCreateAndEdit verifies T01:
// 5 concurrent creates with the same requestId produce 1 project,
// reusing requestId with a different title returns 409, and dual-tab
// concurrent PATCH conflicts on stale revision then succeeds after refresh.
func TestScenarioT01ConcurrentIdempotentCreateAndEdit(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})

	const reqID = "t01-concurrent-create"
	const payload = `{"requestId":"` + reqID + `","title":"水文模型并发建项验证","templateId":"experiment"}`

	const workers = 5
	start := make(chan struct{})
	type result struct {
		status    int
		projectID string
		body      string
	}
	results := make([]result, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+projectsPath, strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var p Project
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			results[idx] = result{status: rec.Code, projectID: p.ProjectID, body: rec.Body.String()}
		}(i)
	}
	close(start)
	wg.Wait()

	createdCount := 0
	var sharedID string
	for i, res := range results {
		if res.status != http.StatusCreated && res.status != http.StatusOK {
			t.Fatalf("worker %d returned HTTP %d: %s", i, res.status, res.body)
		}
		if res.status == http.StatusCreated {
			createdCount++
		}
		if res.projectID == "" {
			t.Fatalf("worker %d missing projectId: %s", i, res.body)
		}
		if sharedID == "" {
			sharedID = res.projectID
		} else if res.projectID != sharedID {
			t.Fatalf("concurrent create produced multiple projectIds: %s vs %s", sharedID, res.projectID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly 1 HTTP 201 Created, got %d", createdCount)
	}

	// Verify only 1 project exists in listing
	listReq := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+projectsPath, nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list projects returned %d: %s", listRec.Code, listRec.Body)
	}
	var page projectPage
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ProjectID != sharedID {
		t.Fatalf("expected 1 project in store, got %+v", page.Items)
	}

	// Reusing the same requestId with a different title must return 409 REQUEST_ID_CONFLICT
	conflictReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+projectsPath,
		strings.NewReader(`{"requestId":"`+reqID+`","title":"不同标题复用编号","templateId":"experiment"}`))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictRec := httptest.NewRecorder()
	handler.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != http.StatusConflict || !strings.Contains(conflictRec.Body.String(), "REQUEST_ID_CONFLICT") {
		t.Fatalf("expected 409 REQUEST_ID_CONFLICT, got %d: %s", conflictRec.Code, conflictRec.Body)
	}

	// Dual-tab PATCH conflict and recovery with refreshed revision
	projectURL := "http://127.0.0.1" + projectsPath + "/" + sharedID
	patchJSON := func(body string, wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPatch, projectURL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("PATCH %s got %d want %d: %s", body, rec.Code, wantStatus, rec.Body)
		}
		return rec
	}

	// Tab A updates from revision 1 -> 2
	tabARec := patchJSON(`{"requestId":"t01-patch-a","expectedRevision":1,"displayName":"标签页A简称"}`, http.StatusOK)
	var afterA Project
	if err := json.Unmarshal(tabARec.Body.Bytes(), &afterA); err != nil || afterA.Revision != 2 || afterA.DisplayName != "标签页A简称" {
		t.Fatalf("unexpected tab A update: %+v (%v)", afterA, err)
	}
	// Tab B attempts PATCH with stale revision 1 -> 409 REVISION_CONFLICT
	tabBConflict := patchJSON(`{"requestId":"t01-patch-b-stale","expectedRevision":1,"title":"标签页B修订标题"}`, http.StatusConflict)
	if !strings.Contains(tabBConflict.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("expected REVISION_CONFLICT, got %s", tabBConflict.Body)
	}
	// Tab B refreshes revision (2) and continues editing successfully -> revision 3
	tabBBody := `{"requestId":"t01-patch-b-ok","expectedRevision":2,"title":"标签页B修订标题"}`
	tabBRec := patchJSON(tabBBody, http.StatusOK)
	var afterB Project
	if err := json.Unmarshal(tabBRec.Body.Bytes(), &afterB); err != nil || afterB.Revision != 3 || afterB.Title != "标签页B修订标题" {
		t.Fatalf("unexpected tab B update: %+v (%v)", afterB, err)
	}
	// Disconnect replay of Tab B's accepted PATCH returns identical state without advancing revision
	replayRec := patchJSON(tabBBody, http.StatusOK)
	var replayedB Project
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayedB); err != nil || replayedB.Revision != 3 || replayedB.Title != "标签页B修订标题" {
		t.Fatalf("replay changed project state: %+v (%v)", replayedB, err)
	}
}

// TestScenarioT02WorkflowPauseRestartRerunAndEvents verifies T02:
// 3-step experiment workflow -> step 1 pause/range checkpoint -> close & reopen DB (restart) ->
// receipt & event seq preserved -> idempotent checkpoint confirmation -> complete remaining steps ->
// rerun step 1 preserves old downloadable artifact and marks downstream steps STALE ->
// project event stream records workflow.step/workflow.checkpoint monotonically and resumes without gap/dup.
func TestScenarioT02WorkflowPauseRestartRerunAndEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	var modelCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := modelCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		content := fmt.Sprintf("# 实验工作流第 %d 次固定产出\n\n确定性结果内容。", call)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", content)
	}))
	t.Cleanup(upstream.Close)

	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture-t02"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed && store != nil {
			_ = store.Close()
		}
	})

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "t02-exp",
		Title:      "三步实验流程恢复回归",
		TemplateID: "experiment",
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	baseURL := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID

	callJSON := func(method, suffix, body string, want int, out any) []byte {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, baseURL+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, suffix, rec.Code, want, rec.Body)
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", rec.Body, err)
			}
		}
		return rec.Body.Bytes()
	}

	waitStatus := func(want string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			var snap struct {
				Run WorkflowRun `json:"run"`
			}
			callJSON(http.MethodGet, "/workflow", "", http.StatusOK, &snap)
			if snap.Run.Status == want {
				return snap.Run
			}
			if snap.Run.Status == taskFailed || time.Now().After(deadline) {
				t.Fatalf("workflow did not reach %s; current=%+v", want, snap.Run)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}

	// Step 1: start workflow through step 1 (scope) with checkpoints enabled
	var started WorkflowRun
	callJSON(http.MethodPost, "/workflow", `{"requestId":"t02-start","checkpoints":true,"throughStep":"scope"}`, http.StatusCreated, &started)
	afterStep1 := waitStatus(workflowRangeCompleted)
	if afterStep1.Steps[0].Status != taskSucceeded || afterStep1.Steps[0].ArtifactID == "" {
		t.Fatalf("step 1 did not produce artifact: %+v", afterStep1.Steps)
	}
	oldStep1ArtifactID := afterStep1.Steps[0].ArtifactID
	oldStep1Bytes := callJSON(http.MethodGet, "/workflow/"+afterStep1.RunID+"/steps/scope/artifacts/"+oldStep1ArtifactID, "", http.StatusOK, nil)

	var eventsBeforeRestart ProjectEventsSnapshot
	callJSON(http.MethodGet, "/events?after=0&limit=50", "", http.StatusOK, &eventsBeforeRestart)
	if len(eventsBeforeRestart.Items) < 2 || eventsBeforeRestart.LatestSeq == 0 {
		t.Fatalf("expected workflow events before restart, got %+v", eventsBeforeRestart)
	}
	checkpointSeq := eventsBeforeRestart.LatestSeq

	// Simulate service restart: close DB and reopen
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed = false
	handler = NewHandler(store, nil, HandlerConfig{Model: model})

	// Verify receipt and project events survived restart
	var snapAfterRestart struct {
		Run WorkflowRun `json:"run"`
	}
	callJSON(http.MethodGet, "/workflow", "", http.StatusOK, &snapAfterRestart)
	if snapAfterRestart.Run.RunID != afterStep1.RunID ||
		snapAfterRestart.Run.Revision != afterStep1.Revision ||
		snapAfterRestart.Run.Status != workflowRangeCompleted ||
		snapAfterRestart.Run.Steps[0].ArtifactID != oldStep1ArtifactID {
		t.Fatalf("workflow receipt changed across restart: before=%+v after=%+v", afterStep1, snapAfterRestart.Run)
	}
	var eventsAfterRestart ProjectEventsSnapshot
	callJSON(http.MethodGet, "/events?after=0&limit=50", "", http.StatusOK, &eventsAfterRestart)
	if eventsAfterRestart.LatestSeq != checkpointSeq || len(eventsAfterRestart.Items) != len(eventsBeforeRestart.Items) {
		t.Fatalf("project events changed across restart: before=%+v after=%+v", eventsBeforeRestart, eventsAfterRestart)
	}

	// Continue from step 1 pause with idempotent replay check -> reaches step 2 (plan) checkpoint WAITING_CHECKPOINT
	continueStep2Body := fmt.Sprintf(`{"requestId":"t02-continue-plan","runId":%q,"expectedRevision":%d}`, afterStep1.RunID, afterStep1.Revision)
	callJSON(http.MethodPost, "/workflow/continue", continueStep2Body, http.StatusAccepted, nil)
	var replayContinueStep2 WorkflowRun
	callJSON(http.MethodPost, "/workflow/continue", continueStep2Body, http.StatusOK, &replayContinueStep2)
	if replayContinueStep2.RunID != afterStep1.RunID {
		t.Fatalf("duplicate continue changed runId: %+v", replayContinueStep2)
	}
	atCheckpoint := waitStatus(workflowWaiting)
	if atCheckpoint.Steps[1].Status != taskSucceeded || modelCalls.Load() != 2 {
		t.Fatalf("expected step 2 succeeded at checkpoint with 2 calls, got %+v calls=%d", atCheckpoint.Steps, modelCalls.Load())
	}

	// Confirm checkpoint with duplicate requestId -> idempotent -> completes step 3 (checklist)
	confirmCheckpointBody := fmt.Sprintf(`{"requestId":"t02-confirm-checkpoint","runId":%q,"expectedRevision":%d}`, atCheckpoint.RunID, atCheckpoint.Revision)
	callJSON(http.MethodPost, "/workflow/continue", confirmCheckpointBody, http.StatusAccepted, nil)
	callJSON(http.MethodPost, "/workflow/continue", confirmCheckpointBody, http.StatusOK, nil)
	completed := waitStatus(workflowDone)
	if completed.Steps[2].Status != taskSucceeded || modelCalls.Load() != 3 {
		t.Fatalf("expected all 3 steps completed with 3 calls, got %+v calls=%d", completed.Steps, modelCalls.Load())
	}

	// Rerun upstream step 1 (scope): old artifact remains downloadable, downstream steps marked STALE
	rerunBody := fmt.Sprintf(`{"requestId":"t02-rerun-scope","runId":%q,"expectedRevision":%d,"stepKey":"scope"}`, completed.RunID, completed.Revision)
	callJSON(http.MethodPost, "/workflow/rerun", rerunBody, http.StatusAccepted, nil)
	afterRerun := waitStatus(workflowRangeCompleted)
	if afterRerun.Steps[0].Status != taskSucceeded || afterRerun.Steps[0].ArtifactCount != 2 || afterRerun.Steps[0].ArtifactID == oldStep1ArtifactID {
		t.Fatalf("step 1 rerun did not produce second artifact: %+v", afterRerun.Steps[0])
	}
	if afterRerun.Steps[1].Status != workflowStepStale || afterRerun.Steps[2].Status != workflowStepStale {
		t.Fatalf("downstream steps were not marked STALE: step2=%+v step3=%+v", afterRerun.Steps[1], afterRerun.Steps[2])
	}
	// Old step 1 artifact is still preserved and downloadable verbatim
	downloadedOld := callJSON(http.MethodGet, "/workflow/"+afterRerun.RunID+"/steps/scope/artifacts/"+oldStep1ArtifactID, "", http.StatusOK, nil)
	if string(downloadedOld) != string(oldStep1Bytes) {
		t.Fatalf("old step 1 artifact changed after rerun: got %q want %q", downloadedOld, oldStep1Bytes)
	}

	// Verify full event stream ordering & cursor continuation from checkpointSeq (no gaps, no duplicates)
	var allEvents, tailEvents ProjectEventsSnapshot
	callJSON(http.MethodGet, "/events?after=0&limit=200", "", http.StatusOK, &allEvents)
	callJSON(http.MethodGet, fmt.Sprintf("/events?after=%d&limit=200", checkpointSeq), "", http.StatusOK, &tailEvents)

	var sawStep, sawCheckpoint bool
	var prevSeq int64
	for _, ev := range allEvents.Items {
		if ev.Seq <= prevSeq {
			t.Fatalf("event seq not strictly monotonic: prev=%d cur=%d", prevSeq, ev.Seq)
		}
		prevSeq = ev.Seq
		if ev.Kind == projectEventWorkflowStep {
			sawStep = true
		}
		if ev.Kind == projectEventWorkflowCheckpoint {
			sawCheckpoint = true
		}
	}
	if !sawStep || !sawCheckpoint {
		t.Fatalf("expected both workflow.step and workflow.checkpoint events, got %+v", allEvents.Items)
	}
	expectedTail := make([]ProjectEvent, 0)
	for _, ev := range allEvents.Items {
		if ev.Seq > checkpointSeq {
			expectedTail = append(expectedTail, ev)
		}
	}
	if len(tailEvents.Items) != len(expectedTail) || len(tailEvents.Items) == 0 {
		t.Fatalf("cursor resume mismatch: tail=%d expected=%d", len(tailEvents.Items), len(expectedTail))
	}
	for i := range expectedTail {
		if tailEvents.Items[i].Seq != expectedTail[i].Seq || tailEvents.Items[i].Kind != expectedTail[i].Kind {
			t.Fatalf("cursor resume item %d mismatch: got %+v want %+v", i, tailEvents.Items[i], expectedTail[i])
		}
	}
}

// TestScenarioT05ConflictBatchEditAndExportRecovery verifies T05:
// Dual-tab file save conflict (409 preserves latest & client draft) +
// conversation batch edit across 2 manuscripts (stale conflict on file 1 does not block file 2 apply) +
// LaTeX PDF compile failure returns diagnostics without modifying manuscript, then retry succeeds on same version.
func TestScenarioT05ConflictBatchEditAndExportRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "t05-project",
		Title:      "跨文件冲突与补导出验证",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}

	baseURL := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID

	// 1) Dual-tab save conflict on paper/intro.md
	noModelHandler := NewHandler(store, nil, HandlerConfig{})
	putFile := func(h http.Handler, path, baseSHA, content string, wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{
			"path":       path,
			"baseSha256": baseSHA,
			"content":    content,
		})
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, baseURL+"/files/text", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("PUT %s returned %d, want %d: %s", path, rec.Code, wantStatus, rec.Body)
		}
		return rec
	}

	var introV1, introV2 WorkspaceDocument
	if err := json.Unmarshal(putFile(noModelHandler, "paper/intro.md", "", "# 引言\n第一版内容。\n", http.StatusOK).Body.Bytes(), &introV1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(putFile(noModelHandler, "paper/intro.md", introV1.SHA256, "# 引言\n标签A保存的第二版。\n", http.StatusOK).Body.Bytes(), &introV2); err != nil {
		t.Fatal(err)
	}
	// Tab B submits stale baseSha256 (introV1) -> 409 conflict, latest remains introV2
	conflictRec := putFile(noModelHandler, "paper/intro.md", introV1.SHA256, "# 引言\n标签B本地草稿。\n", http.StatusConflict)
	if !strings.Contains(conflictRec.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("expected REVISION_CONFLICT on stale file save, got %s", conflictRec.Body)
	}
	currentIntro, err := store.workspaceFile(ctx, project.ProjectID, "paper/intro.md")
	if err != nil || currentIntro.SHA256 != introV2.SHA256 {
		t.Fatalf("stale save altered latest file: %+v (%v)", currentIntro, err)
	}

	// 2) Conversation cross-file batch edit across 2 manuscripts (sec1 conflicts, sec2 applies cleanly)
	type docSpec struct {
		path, before, selected, after, replacement string
		saved                                      WorkspaceDocument
	}
	docs := []docSpec{
		{path: "paper/sec1.md", before: "# 第一节\n", selected: "旧方法描述。", after: "\n实验说明。", replacement: "新方法描述。"},
		{path: "paper/sec2.md", before: "# 第二节\n\n## 引言\n", selected: "待润色句。", after: "\n实验结论。", replacement: "已完成润色的结论句。"},
	}
	selections := make([]manuscriptSelectionInput, 0, len(docs))
	for i := range docs {
		d, saveErr := store.saveWorkspaceDocument(ctx, project.ProjectID, docs[i].path, "", docs[i].before+docs[i].selected+docs[i].after)
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		docs[i].saved = d
		selections = append(selections, manuscriptSelectionInput{
			VersionID: d.VersionID,
			StartByte: len(docs[i].before),
			EndByte:   len(docs[i].before) + len(docs[i].selected),
		})
	}

	const editInstruction = "统一两份稿件的表述。"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Tools []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(reqBody.Tools) > 0 {
			args := fmt.Sprintf(`{"instruction":%q,"editSelection":true}`, editInstruction)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请核对跨文件修订建议。\",\"tool_calls\":[{\"index\":0,\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
			return
		}
		output := fmt.Sprintf(`{"edits":[{"versionId":%q,"replacement":%q,"reason":"更新方法表述"},{"versionId":%q,"replacement":%q,"reason":"润色结论表述"}]}`,
			docs[0].saved.VersionID, docs[0].replacement, docs[1].saved.VersionID, docs[1].replacement)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture-t05"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model, EditModelName: "fixture-t05-edit"})

	callAPI := func(h http.Handler, method, suffix, body string, want int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, baseURL+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, suffix, rec.Code, want, rec.Body)
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", rec.Body, err)
			}
		}
		return rec.Body.String()
	}

	turnPayload, _ := json.Marshal(sendTurnInput{
		RequestID:            "t05-batch-turn",
		Text:                 "请润色这两段稿件。",
		Mode:                 "CONFIRM",
		ManuscriptSelections: selections,
	})
	var turn Turn
	callAPI(handler, http.MethodPost, "/turns", string(turnPayload), http.StatusAccepted, &turn)
	callAPI(handler, http.MethodGet, "/turns/"+turn.TurnID+"/stream", "", http.StatusOK, nil)

	var tasks taskPage
	callAPI(handler, http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &tasks)
	if len(tasks.Items) != 1 || tasks.Items[0].Status != taskProposed {
		t.Fatalf("expected 1 proposed batch edit task, got %+v", tasks.Items)
	}
	editTask := tasks.Items[0]
	callAPI(handler, http.MethodPost, "/tasks/"+editTask.TaskID+"/confirm",
		fmt.Sprintf(`{"requestId":"t05-confirm-edit","expectedRevision":%d}`, editTask.Revision), http.StatusAccepted, nil)

	// Wait for batch edit task to succeed
	deadline := time.Now().Add(6 * time.Second)
	for {
		callAPI(handler, http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &tasks)
		if tasks.Items[0].Status == taskSucceeded {
			break
		}
		if (tasks.Items[0].Status != taskQueued && tasks.Items[0].Status != taskRunning) || time.Now().After(deadline) {
			t.Fatalf("batch edit did not succeed: %+v", tasks.Items[0])
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Mutate sec1.md concurrently so its edit becomes conflicted while sec2.md stays clean
	sec1Newer, err := store.saveWorkspaceDocument(ctx, project.ProjectID, docs[0].path, docs[0].saved.SHA256, docs[0].saved.Content+"\n并行人工修订。")
	if err != nil {
		t.Fatal(err)
	}
	var sec1Conflict struct{ Error apiError }
	callAPI(handler, http.MethodPost, "/files/versions/"+docs[0].saved.VersionID+"/edits/"+editTask.TaskID+"/apply", "", http.StatusConflict, &sec1Conflict)
	if sec1Conflict.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("expected REVISION_CONFLICT for sec1, got %+v", sec1Conflict)
	}
	// Apply sec2.md cleanly despite sec1.md conflict
	var sec2Applied WorkspaceDocument
	callAPI(handler, http.MethodPost, "/files/versions/"+docs[1].saved.VersionID+"/edits/"+editTask.TaskID+"/apply", "", http.StatusOK, &sec2Applied)
	wantSec2 := docs[1].before + docs[1].replacement + docs[1].after
	if sec2Applied.Content != wantSec2 || sec2Applied.VersionID == docs[1].saved.VersionID {
		t.Fatalf("sec2 apply unexpected: %+v", sec2Applied)
	}
	// Verify sec1.md was untouched by conflict
	var sec1Now WorkspaceDocument
	callAPI(handler, http.MethodGet, "/files/text?path="+url.QueryEscape(docs[0].path), "", http.StatusOK, &sec1Now)
	if sec1Now.VersionID != sec1Newer.VersionID {
		t.Fatalf("sec1 version changed unexpectedly: got %s want %s", sec1Now.VersionID, sec1Newer.VersionID)
	}

	// 3) LaTeX PDF compile failure -> diagnostics without overwriting manuscript -> fixed config retries same version successfully
	failStub := createLatexStubScript(t, "t05-xelatex-fail",
		"echo ! Undefined control sequence.\r\necho l.18 \\badcmd\r\nexit /b 1",
		"echo '! Undefined control sequence.'\necho 'l.18 \\badcmd'\nexit 1",
	)
	failHandler := NewHandler(store, nil, HandlerConfig{LatexBin: failStub})
	latexSuffix := "/files/versions/" + sec2Applied.VersionID + "/latex"
	var failResp latexExportResponse
	callAPI(failHandler, http.MethodPost, latexSuffix, `{"preset":"zh_article","compilePdf":true}`, http.StatusOK, &failResp)
	if failResp.Compiled || len(failResp.Diagnostics) == 0 || failResp.Diagnostics[0].Line != 18 {
		t.Fatalf("expected compile failure diagnostics, got %+v", failResp)
	}
	// Confirm manuscript was NOT modified on failed export
	sec2AfterFail, err := store.workspaceFile(ctx, project.ProjectID, docs[1].path)
	if err != nil || sec2AfterFail.VersionID != sec2Applied.VersionID || sec2AfterFail.SHA256 != sec2Applied.SHA256 {
		t.Fatalf("failed LaTeX compile modified manuscript: %+v (%v)", sec2AfterFail, err)
	}

	// Retry on the exact same input version after fixing compiler config
	okStub := createLatexStubScript(t, "t05-xelatex-ok",
		"echo %* | findstr /C:\"-no-shell-escape\" >nul || exit /b 2\r\necho %%PDF-1.4 t05-ok>main.pdf\r\nexit /b 0",
		"echo \"$*\" | grep -q -- '-no-shell-escape' || exit 2\nprintf '%%PDF-1.4 t05-ok' > main.pdf\nexit 0",
	)
	okHandler := NewHandler(store, nil, HandlerConfig{LatexBin: okStub})
	var okResp latexExportResponse
	callAPI(okHandler, http.MethodPost, latexSuffix, `{"preset":"zh_article","compilePdf":true}`, http.StatusOK, &okResp)
	if !okResp.Compiled || okResp.VersionID != sec2Applied.VersionID || okResp.SourceSHA256 != sec2Applied.SHA256 || okResp.PdfSize == 0 {
		t.Fatalf("unexpected retry export response: %+v", okResp)
	}
	pdfBytes, err := base64.StdEncoding.DecodeString(okResp.PdfBase64)
	if err != nil || !strings.HasPrefix(string(pdfBytes), "%PDF-1.4") {
		t.Fatalf("invalid PDF bytes after recovery retry: %q (%v)", string(pdfBytes), err)
	}
}

// TestScenarioT16ThreeReviewKindsEndStates verifies T16:
// On the same pinned manuscript version:
// 1) Automatic review hitting MAX_ROUNDS (2 rounds < targetScore, both rounds preserved & downloadable)
// 2) Automatic review hitting NO_PROGRESS early (score stagnates with repeated finding before maxRounds)
// 3) Independent simulated review + staged deterministic structure check (independent receipts, no overwrite)
// 4) Saving a new manuscript version leaves all prior reviews/checks pointing to old sourceSha256 != latest SHA256.
func TestScenarioT16ThreeReviewKindsEndStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	const line2Original = "样本来源与采样间隔需要补充。"
	const line2MaxRoundsV2 = "样本来自三处站点，每月采样一次。"
	const line2NoProgressV2 = "样本来源与采样间隔需要补充。已追加说明。"
	const manuscriptV1 = "# 河流观测实验初稿\n" + line2Original + "\n\n## 摘要\n本文研究水文指标变化。\n\n## 引言\nTODO: 补充相关研究。\n"

	var maxRoundPhase, noProgressPhase atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "invalid review request", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")

		var output string
		switch {
		case strings.Contains(prompt, `投稿目标："轮次耗尽目标"`):
			step := maxRoundPhase.Add(1)
			if step == 1 {
				output = fmt.Sprintf(`{"score":5,"recommendation":"REVISE","summary":"第一轮：缺少采样细节。","findings":[{"line":2,"quote":%q,"severity":"major","finding":"采样细节不足","suggestion":"补充站点与周期"}]}`, line2Original)
			} else {
				output = fmt.Sprintf(`{"score":6,"recommendation":"REVISE","summary":"第二轮：仍需补充误差分析。","findings":[{"line":2,"quote":%q,"severity":"minor","finding":"缺少误差范围","suggestion":"补充误差统计"}]}`, line2MaxRoundsV2)
			}
		case strings.Contains(prompt, `补充评审标准："max-rounds-case"`):
			output = fmt.Sprintf(`{"edits":[{"line":2,"replacement":%q}]}`, line2MaxRoundsV2)
		case strings.Contains(prompt, `投稿目标："无进展终止目标"`):
			step := noProgressPhase.Add(1)
			if step == 1 {
				output = fmt.Sprintf(`{"score":5,"recommendation":"REVISE","summary":"第一轮：缺少对照组说明。","findings":[{"line":2,"quote":%q,"severity":"major","finding":"缺少对照组说明","suggestion":"补充对照组设计"}]}`, line2Original)
			} else {
				// Same score (5) and identical finding anchor -> triggers NO_PROGRESS before maxRounds=4
				output = fmt.Sprintf(`{"score":5,"recommendation":"REVISE","summary":"第二轮：仍缺少对照组说明。","findings":[{"line":2,"quote":%q,"severity":"major","finding":"缺少对照组说明","suggestion":"补充对照组设计"}]}`, line2Original)
			}
		case strings.Contains(prompt, `补充评审标准："no-progress-case"`):
			output = fmt.Sprintf(`{"edits":[{"line":2,"replacement":%q}]}`, line2NoProgressV2)
		default:
			// Independent simulated manuscript review
			output = fmt.Sprintf(`{"score":74,"recommendation":"REVISE","summary":"独立模拟评审：结构较完整，建议补全实验细节。","findings":[{"line":2,"quote":%q,"severity":"major","finding":"实验方法描述偏简略","suggestion":"补充实验参数表"}]}`, line2Original)
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	t.Cleanup(upstream.Close)

	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture-t16"})
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "t16-project",
		Title:      "三类审查终态回归",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	docV1, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/manuscript.md", "", manuscriptV1)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{Model: model, ReviewModelName: "fixture-t16-review"})
	baseURL := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID
	verURL := baseURL + "/files/versions/" + docV1.VersionID

	callJSON := func(method, fullURL, body string, want int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, fullURL, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, fullURL, rec.Code, want, rec.Body)
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", rec.Body, err)
			}
		}
		return rec
	}

	waitAutoReview := func(reviewID string) automaticReview {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			var cur automaticReview
			callJSON(http.MethodGet, verURL+"/automatic-reviews/"+reviewID, "", http.StatusOK, &cur)
			if cur.Status == taskSucceeded || cur.Status == taskFailed {
				return cur
			}
			if time.Now().After(deadline) {
				t.Fatalf("automatic review %s timed out: %+v", reviewID, cur)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}

	// 1) Automatic review: MAX_ROUNDS (maxRounds=2, targetScore=8)
	var autoMaxStart automaticReview
	callJSON(http.MethodPost, verURL+"/automatic-reviews",
		`{"requestId":"t16-auto-max","target":"轮次耗尽目标","maxRounds":2,"targetScore":8,"criteria":"max-rounds-case"}`,
		http.StatusAccepted, &autoMaxStart)
	autoMax := waitAutoReview(autoMaxStart.ReviewID)
	if autoMax.Status != taskSucceeded || autoMax.StopReason != automaticReviewMaxRounds ||
		len(autoMax.Rounds) != 2 || autoMax.Rounds[0].Score != 5 || autoMax.Rounds[1].Score != 6 ||
		autoMax.SourceSHA256 != docV1.SHA256 {
		t.Fatalf("MAX_ROUNDS end state mismatch: %+v", autoMax)
	}
	for roundNo, wantSubstr := range map[int]string{1: line2Original, 2: line2MaxRoundsV2} {
		dl := callJSON(http.MethodGet, fmt.Sprintf("%s/automatic-reviews/%s/rounds/%d/manuscript", verURL, autoMax.ReviewID, roundNo), "", http.StatusOK, nil)
		sum := fmt.Sprintf("%x", sha256.Sum256(dl.Body.Bytes()))
		if !strings.Contains(dl.Body.String(), wantSubstr) || sum != autoMax.Rounds[roundNo-1].SHA256 {
			t.Fatalf("round %d manuscript mismatch: got %q sha=%s", roundNo, dl.Body.String(), sum)
		}
	}

	// 2) Automatic review: NO_PROGRESS early stop (maxRounds=4, targetScore=8 -> stops at round 2)
	var autoNoProgStart automaticReview
	callJSON(http.MethodPost, verURL+"/automatic-reviews",
		`{"requestId":"t16-auto-noprog","target":"无进展终止目标","maxRounds":4,"targetScore":8,"criteria":"no-progress-case"}`,
		http.StatusAccepted, &autoNoProgStart)
	autoNoProg := waitAutoReview(autoNoProgStart.ReviewID)
	if autoNoProg.Status != taskSucceeded || autoNoProg.StopReason != automaticReviewNoProgress ||
		len(autoNoProg.Rounds) != 2 || autoNoProg.SourceSHA256 != docV1.SHA256 ||
		autoNoProg.ReviewID == autoMax.ReviewID {
		t.Fatalf("NO_PROGRESS end state mismatch: %+v", autoNoProg)
	}

	// 3) Independent simulated review + staged deterministic structure check
	var simStart manuscriptReview
	callJSON(http.MethodPost, verURL+"/reviews", `{"target":"独立模拟评审期刊"}`, http.StatusAccepted, &simStart)
	var simDone manuscriptReview
	deadline := time.Now().Add(8 * time.Second)
	for {
		callJSON(http.MethodGet, verURL+"/reviews/"+simStart.ReviewID, "", http.StatusOK, &simDone)
		if simDone.Status == taskSucceeded || simDone.Status == taskFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("simulated review timed out: %+v", simDone)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if simDone.Status != taskSucceeded || simDone.SourceSHA256 != docV1.SHA256 || simDone.Report == "" {
		t.Fatalf("unexpected simulated review receipt: %+v", simDone)
	}

	var checkReport paperCheckReport
	callJSON(http.MethodPost, verURL+"/check", `{"stage":"final"}`, http.StatusOK, &checkReport)
	if checkReport.SourceVersionID != docV1.VersionID || checkReport.SourceSHA256 != docV1.SHA256 ||
		checkReport.Stage != "final" || len(checkReport.Diagnostics) == 0 {
		t.Fatalf("unexpected deterministic check report: %+v", checkReport)
	}

	// Verify all three review kinds keep independent receipts and did not overwrite each other
	var rereadAutoMax, rereadAutoNoProg automaticReview
	var rereadSim manuscriptReview
	callJSON(http.MethodGet, verURL+"/automatic-reviews/"+autoMax.ReviewID, "", http.StatusOK, &rereadAutoMax)
	callJSON(http.MethodGet, verURL+"/automatic-reviews/"+autoNoProg.ReviewID, "", http.StatusOK, &rereadAutoNoProg)
	callJSON(http.MethodGet, verURL+"/reviews/"+simDone.ReviewID, "", http.StatusOK, &rereadSim)
	if rereadAutoMax.StopReason != automaticReviewMaxRounds ||
		rereadAutoNoProg.StopReason != automaticReviewNoProgress ||
		rereadSim.ReviewID != simDone.ReviewID {
		t.Fatalf("review receipts interfered with each other")
	}

	// Verify review.updated events were emitted in project event stream
	var evSnap ProjectEventsSnapshot
	callJSON(http.MethodGet, baseURL+"/events?after=0&limit=100", "", http.StatusOK, &evSnap)
	reviewEventEntities := make([]string, 0)
	for _, ev := range evSnap.Items {
		if ev.Kind == projectEventReviewUpdated {
			reviewEventEntities = append(reviewEventEntities, ev.EntityID)
		}
	}
	for _, wantID := range []string{autoMax.ReviewID, autoNoProg.ReviewID, simDone.ReviewID} {
		if !slices.Contains(reviewEventEntities, wantID) {
			t.Fatalf("missing review.updated event for %s; got entities=%v", wantID, reviewEventEntities)
		}
	}

	// 4) Save revised manuscript v2 -> verify all prior reviews/checks have sourceSha256 != latest SHA256 (stale indicator)
	docV2, err := store.saveWorkspaceDocument(ctx, project.ProjectID, docV1.Path, docV1.SHA256,
		manuscriptV1+"\n## 修订补充\n已根据审稿意见补全对照组与参数。\n")
	if err != nil {
		t.Fatal(err)
	}
	if docV2.SHA256 == docV1.SHA256 {
		t.Fatal("revised manuscript unexpectedly has identical SHA-256")
	}
	if rereadAutoMax.SourceSHA256 == docV2.SHA256 ||
		rereadAutoNoProg.SourceSHA256 == docV2.SHA256 ||
		rereadSim.SourceSHA256 == docV2.SHA256 ||
		checkReport.SourceSHA256 == docV2.SHA256 {
		t.Fatalf("old review/check sourceSha256 unexpectedly matched latest manuscript version %s", docV2.SHA256)
	}
}
