package workbench

import (
	"database/sql"
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
		var payload struct {
			modelRequest
			Messages []inference.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || r.Header.Get("Authorization") != "Bearer "+credential ||
			len(payload.Messages) != 2 || !strings.HasPrefix(payload.Messages[1].Content, `项目标题："河流采样"`) {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		requests <- payload.modelRequest
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
		WorkflowModelTemplates: " experiment, freeform, unknown ",
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
	var catalog struct {
		ModelOptions []modelOption
	}
	snapshot := request(http.MethodGet, "", "", http.StatusOK)
	if err := json.Unmarshal(snapshot, &catalog); err != nil || len(catalog.ModelOptions) != 2 ||
		catalog.ModelOptions[0].ID != alternateWorkflowModelID || !strings.Contains(string(snapshot), `"requiresModel":true`) {
		t.Fatalf("template model is missing from the workflow snapshot: %s (%v)", snapshot, err)
	}
	const start = `{"requestId":"workflow-1","throughStep":"scope","stepModelIds":{"scope":"default"}}`
	request(http.MethodPost, "", strings.Replace(start, `"scope":"default"`, `"unknown-step":"default"`, 1), http.StatusBadRequest)
	var started WorkflowRun
	if err := json.Unmarshal(request(http.MethodPost, "", start, http.StatusCreated), &started); err != nil ||
		started.Status != taskQueued || started.Model != "fixture-workflow" || started.ReasoningEffort != "max" ||
		started.ProjectTitle != project.Title || len(started.Steps) != 3 || started.Steps[0].Model != "fixture-chat" || started.Steps[0].ReasoningEffort != "low" || started.Steps[1].Model != "" {
		t.Fatalf("unexpected workflow start receipt: %+v (%v)", started, err)
	}
	if _, err := store.patchProject(ctx, project.ProjectID, patchProjectInput{
		RequestID: "rename", ExpectedRevision: project.Revision,
		Title: projectPatchField{sql.NullString{String: "流域采样", Valid: true}},
	}); err != nil {
		t.Fatal(err)
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
	request(http.MethodPost, "", strings.Replace(start, `"throughStep"`, `"modelId":"default","throughStep"`, 1), http.StatusConflict)

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

func TestWorkflowModelRequestMigrationPreservesLegacyReplay(t *testing.T) {
	ctx, directory := t.Context(), t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "采样记录", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	request := workflowRequest{RequestID: "legacy", ThroughStep: "scope"}
	original, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, request, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER notify_review_status; DROP TRIGGER notify_workflow_status; DROP TRIGGER notify_task_status; DROP TABLE task_notifications;
		DROP TABLE manuscript_pdf_citations;
		DROP TABLE manuscript_pdf_links;
		DROP TABLE manuscript_comparison_links;
		DROP TABLE image_generation_sources;
		DROP TABLE image_generations;
		DROP TABLE image_reviews;
		ALTER TABLE workflow_requests DROP COLUMN requested_model_id;
		ALTER TABLE projects DROP COLUMN created_title;
		ALTER TABLE project_patch_requests DROP COLUMN title;
		ALTER TABLE workflow_runs DROP COLUMN project_title;
		ALTER TABLE tasks DROP COLUMN result_json;
		ALTER TABLE tasks RENAME COLUMN kind TO comparison_kind;
		ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK (kind IN ('', 'manuscript-check', 'focused-research'));
		UPDATE tasks SET kind = comparison_kind WHERE comparison_kind <> '';
		ALTER TABLE tasks DROP COLUMN comparison_kind;
		PRAGMA user_version = 47`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, modelID := range []string{"", defaultModelID} {
		request.ModelID = modelID
		replayed, applied, err := reopened.applyWorkflow(ctx, project.ProjectID, workflowStart, request, false)
		if err != nil || applied || replayed.RunID != original.RunID || replayed.Revision != original.Revision || replayed.ProjectTitle != project.Title {
			t.Fatalf("legacy replay with model %q changed: %+v (%v)", modelID, replayed, err)
		}
	}
}
