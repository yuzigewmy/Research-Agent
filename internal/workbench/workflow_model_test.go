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
	if snapshot := string(request(http.MethodGet, "", "", http.StatusOK)); !strings.Contains(snapshot, `"modelOptions"`) {
		t.Fatalf("model selection is missing from workflow snapshot: %s", snapshot)
	}
	var started struct {
		RunID           string `json:"runId"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		Status          string `json:"status"`
	}
	const start = `{"requestId":"workflow-1","modelId":"workflow","throughStep":"scope"}`
	if err := json.Unmarshal(request(http.MethodPost, "", start, http.StatusCreated), &started); err != nil ||
		started.Status != taskQueued || started.Model != "fixture-workflow" || started.ReasoningEffort != "max" {
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
	select {
	case got := <-requests:
		if got != (modelRequest{"fixture-workflow", "max"}) {
			t.Fatalf("restarted worker requested %+v", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("queued workflow did not reach the model")
	}
	deadline := time.Now().Add(4 * time.Second)
	var snapshot struct{ Run WorkflowRun }
	for {
		if err := json.Unmarshal(request(http.MethodGet, "", "", http.StatusOK), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == workflowRangeCompleted {
			break
		}
		if snapshot.Run.Status == taskFailed || time.Now().After(deadline) {
			t.Fatalf("queued workflow did not complete: %+v", snapshot.Run)
		}
		time.Sleep(10 * time.Millisecond)
	}
	replayed := string(request(http.MethodPost, "", start, http.StatusOK))
	if !strings.Contains(replayed, `"model":"fixture-workflow"`) || !strings.Contains(replayed, `"reasoningEffort":"max"`) {
		t.Fatalf("workflow replay did not preserve its model: %s", replayed)
	}
	request(http.MethodPost, "", strings.Replace(start, `"workflow"`, `"default"`, 1), http.StatusConflict)

	moved, err := inference.New(inference.Config{BaseURL: upstream.URL + "/new", Model: "fixture-new-default", APIKey: credential})
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: moved})
	resume := fmt.Sprintf(`{"requestId":"resume","runId":%q,"expectedRevision":%d}`, snapshot.Run.RunID, snapshot.Run.Revision)
	if body := string(request(http.MethodPost, "/continue", resume, http.StatusConflict)); !strings.Contains(body, `"code":"WORKFLOW_MODEL_CONNECTION_CHANGED"`) {
		t.Fatalf("wrong connection change error: %s", body)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: changed})
	request(http.MethodPost, "/continue", resume, http.StatusAccepted)
	select {
	case got := <-requests:
		if got != (modelRequest{"fixture-workflow", "max"}) {
			t.Fatalf("continued workflow requested %+v", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("restored connection did not reach the model")
	}
}
