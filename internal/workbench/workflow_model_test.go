package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWorkflowKeepsSelectedModelAfterQueueRestart(t *testing.T) {
	const credential = "fixture-workflow-key"
	type modelRequest struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	requests := make(chan modelRequest, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request modelRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# 采样方案\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := func(name, effort string) *inference.Client {
		t.Helper()
		model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: name, APIKey: credential, ReasoningEffort: effort})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	ctx := t.Context()
	directory := t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "sampling", Title: "河流采样", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the first step queued so the reopened worker has to read its saved model selection.
	store.tasks.active = map[string]*taskWorker{project.ProjectID: {}}
	base := projectsPath + "/" + project.ProjectID + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{
		Model: client("fixture-chat", "low"), WorkflowModelName: "fixture-workflow", WorkflowReasoningEffort: "max",
	})
	request := func(method, suffix, body string, status int) []byte {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s got %d, want %d: %s", method, suffix, res.Code, status, res.Body)
		}
		if strings.Contains(res.Body.String(), credential) || strings.Contains(res.Body.String(), upstream.URL) {
			t.Fatal("workflow response exposed the deployment connection")
		}
		return res.Body.Bytes()
	}
	if snapshot := string(request(http.MethodGet, "", "", http.StatusOK)); !strings.Contains(snapshot, `"modelOptions"`) ||
		!strings.Contains(snapshot, `"requiresModel":true`) {
		t.Fatalf("model selection is missing from workflow snapshot: %s", snapshot)
	}
	const start = `{"requestId":"workflow-1","modelId":"workflow","throughStep":"scope","stepModelIds":{"scope":"default"}}`
	request(http.MethodPost, "", strings.Replace(start, `"scope":"default"`, `"unknown-step":"default"`, 1), http.StatusBadRequest)
	var started WorkflowRun
	if err := json.Unmarshal(request(http.MethodPost, "", start, http.StatusCreated), &started); err != nil ||
		started.Status != taskQueued || started.Model != "fixture-workflow" || started.ReasoningEffort != "max" ||
		len(started.Steps) != 3 || started.Steps[0].Model != "fixture-chat" || started.Steps[0].ReasoningEffort != "low" || started.Steps[1].Model != "" {
		t.Fatalf("unexpected workflow start receipt: %+v (%v)", started, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	changed := client("fixture-new-default", "low")
	handler = NewHandler(store, nil, HandlerConfig{Model: changed})
	if err := store.ResumeQueuedTasks(ctx, changed); err != nil {
		t.Fatal(err)
	}
	checkCall := func(want modelRequest) {
		t.Helper()
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("workflow requested %+v, want %+v", got, want)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("queued workflow did not reach the model")
		}
	}
	waitForRange := func() WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run WorkflowRun }
			if err := json.Unmarshal(request(http.MethodGet, "", "", http.StatusOK), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == workflowRangeCompleted {
				return snapshot.Run
			}
			if snapshot.Run.Status == taskFailed || time.Now().After(deadline) {
				t.Fatalf("workflow did not complete its range: %+v", snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	checkCall(modelRequest{"fixture-chat", "low"})
	first := waitForRange()
	var replayed WorkflowRun
	if err := json.Unmarshal(request(http.MethodPost, "", start, http.StatusOK), &replayed); err != nil ||
		replayed.RunID != first.RunID || replayed.Model != "fixture-workflow" || replayed.Steps[0].Model != "fixture-chat" {
		t.Fatalf("workflow replay did not preserve model choices: %+v (%v)", replayed, err)
	}
	request(http.MethodPost, "", strings.Replace(start, `"scope":"default"`, `"scope":"workflow"`, 1), http.StatusConflict)

	moved, err := inference.New(inference.Config{BaseURL: upstream.URL + "/new", Model: "fixture-new-default", APIKey: credential})
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: moved})
	resume := fmt.Sprintf(`{"requestId":"resume","runId":%q,"expectedRevision":%d,"throughStep":"plan"}`, first.RunID, first.Revision)
	if body := string(request(http.MethodPost, "/continue", resume, http.StatusConflict)); !strings.Contains(body, `"code":"WORKFLOW_MODEL_CONNECTION_CHANGED"`) {
		t.Fatalf("wrong connection change error: %s", body)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: changed})
	request(http.MethodPost, "/continue", resume, http.StatusAccepted)
	checkCall(modelRequest{"fixture-workflow", "max"})
	second := waitForRange()
	rerun := fmt.Sprintf(`{"requestId":"rerun-scope","runId":%q,"expectedRevision":%d,"stepKey":"scope"}`, second.RunID, second.Revision)
	request(http.MethodPost, "/rerun", rerun, http.StatusAccepted)
	checkCall(modelRequest{"fixture-chat", "low"})
	finished := waitForRange()
	if finished.Steps[0].ArtifactCount != 2 || finished.Steps[0].Model != "fixture-chat" || finished.Steps[1].Status != workflowStepStale {
		t.Fatalf("rerun did not preserve its model or invalidate the derived step: %+v", finished.Steps)
	}
}
