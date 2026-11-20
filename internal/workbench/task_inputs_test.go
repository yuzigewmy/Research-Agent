package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestTaskInputsRequireReconfirmationAndRemainPinnedAfterClaim(t *testing.T) {
	const (
		path        = "notes/sampling.md"
		pinned      = "Approved paired sample counts."
		includedEnd = "End of the approved excerpt."
		excludedEnd = "This text falls outside the excerpt."
		latest      = "Unapproved counts saved after claim."
		plan        = "# Sampling plan"
	)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing model prompt", http.StatusBadRequest)
			return
		}
		text := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(text, pinned) || !strings.Contains(text, includedEnd) ||
			strings.Contains(text, latest) || strings.Contains(text, includedEnd+excludedEnd[:1]) {
			t.Error("model did not receive exactly the approved version's bounded excerpt")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", plan)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	createProject := func(id string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: "River observations", TemplateID: "experiment"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	save := func(projectID, baseHash, content string) WorkspaceDocument {
		t.Helper()
		version, err := store.saveWorkspaceDocument(ctx, projectID, path, baseHash, content)
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	projectID := createProject("project")
	first := save(projectID, "", "Initial sampling notes.")
	foreign := save(createProject("other"), "", first.Content)
	base := projectsPath + "/" + projectID + "/tasks"
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, suffix, body string, want int, out any) {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
	}
	assertError := func(method, suffix, body string, status int, code string) {
		t.Helper()
		var response struct{ Error apiError }
		request(method, suffix, body, status, &response)
		if response.Error.Code != code {
			t.Fatalf("got %q, want %q", response.Error.Code, code)
		}
	}
	proposal := func(id string, versions ...string) string {
		ids, _ := json.Marshal(versions)
		return fmt.Sprintf(`{"requestId":%q,"instruction":"Plan a sampling study","inputVersionIds":%s}`, id, ids)
	}
	type observedTask struct {
		Task
		Inputs []WorkspaceFile `json:"inputs"`
	}
	assertInput := func(task observedTask, version WorkspaceDocument) {
		t.Helper()
		if len(task.Inputs) != 1 || task.Inputs[0].VersionID != version.VersionID ||
			task.Inputs[0].Path != version.Path || task.Inputs[0].SHA256 != version.SHA256 {
			t.Fatalf("wrong inputs for task %s: %+v", task.TaskID, task.Inputs)
		}
	}
	const proposalID = "plan"
	var proposed observedTask
	request(http.MethodPost, "", proposal(proposalID, first.VersionID), http.StatusCreated, &proposed)
	assertInput(proposed, first)
	suffix := "/" + proposed.TaskID
	assertError(http.MethodPost, "", proposal("foreign", foreign.VersionID), http.StatusUnprocessableEntity, "INVALID_TASK_INPUT")

	patch := func(id string, revision int, inputs string) observedTask {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d,"instruction":"Compare the sites"%s}`, id, revision, inputs)
		var updated observedTask
		request(http.MethodPatch, suffix, body, http.StatusOK, &updated)
		return updated
	}
	setInput := func(versionID string) string { return fmt.Sprintf(`,"inputVersionIds":[%q]`, versionID) }
	retained := patch("retain", proposed.Revision, "")
	assertInput(retained, first)
	second := save(projectID, first.SHA256, "Updated sampling notes.")
	assertError(http.MethodPost, "", proposal("duplicate-path", first.VersionID, second.VersionID), http.StatusUnprocessableEntity, "INVALID_TASK_INPUT")
	assertError(http.MethodPost, "", proposal("old-version", first.VersionID), http.StatusConflict, "TASK_INPUT_CHANGED")
	assertError(http.MethodPost, "", proposal(proposalID, second.VersionID), http.StatusConflict, "REQUEST_ID_CONFLICT")
	assertError(http.MethodPost, suffix+"/confirm", fmt.Sprintf(`{"requestId":"stale-confirm","expectedRevision":%d}`, retained.Revision), http.StatusConflict, "TASK_INPUT_CHANGED")

	// Drive the queue directly to make the edit between confirmation and claim deterministic.
	confirm := func(id string, revision int) Task {
		t.Helper()
		task, applied, err := store.applyTaskRequest(ctx, taskRequest{
			ProjectID: projectID, TaskID: proposed.TaskID, Action: confirmTask, RequestID: id, ExpectedRevision: revision,
		}, true)
		if err != nil || !applied || task.Status != taskQueued {
			t.Fatalf("confirm: task=%+v, applied=%t, err=%v", task, applied, err)
		}
		return task
	}
	refreshed := patch("use-second", retained.Revision, setInput(second.VersionID))
	queued := confirm("confirm-second", refreshed.Revision)
	thirdContent := pinned + strings.Repeat("x", (8<<10)-len(pinned)-len(includedEnd)) + includedEnd + excludedEnd
	third := save(projectID, second.SHA256, thirdContent)
	if task, err := store.claimQueuedTask(ctx, projectID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claim should return the changed task for confirmation: task=%+v, err=%v", task, err)
	}
	var page struct{ Items []observedTask }
	request(http.MethodGet, "", "", http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].TaskID != proposed.TaskID || page.Items[0].Status != taskProposed ||
		page.Items[0].Revision != queued.Revision+1 || page.Items[0].Message == "" || calls.Load() != 0 {
		t.Fatalf("input change should return the task to PROPOSED: %+v, calls=%d", page.Items, calls.Load())
	}
	cleared := patch("clear", page.Items[0].Revision, `,"inputVersionIds":[]`)
	if len(cleared.Inputs) != 0 {
		t.Fatalf("empty input array did not clear the task: %+v", cleared.Inputs)
	}
	approved := patch("use-third", cleared.Revision, setInput(third.VersionID))
	assertInput(approved, third)
	const confirmID = "confirm-third"
	confirm(confirmID, approved.Revision)
	claimed, err := store.claimQueuedTask(ctx, projectID)
	if err != nil || claimed.TaskID != proposed.TaskID || claimed.Status != taskRunning {
		t.Fatalf("claim after reconfirmation: task=%+v, err=%v", claimed, err)
	}
	save(projectID, third.SHA256, latest)
	result, err := store.generateTaskResult(ctx, model, projectID, claimed)
	if err != nil || result.markdown != plan || calls.Load() != 1 {
		t.Fatalf("generation with the pinned input: result=%+v, err=%v, calls=%d", result, err, calls.Load())
	}
	if err := store.finishTask(ctx, projectID, claimed, taskSucceeded, "", result); err != nil {
		t.Fatal(err)
	}
	var replayed observedTask
	request(http.MethodPost, suffix+"/confirm", fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, confirmID, approved.Revision), http.StatusOK, &replayed)
	if replayed.TaskID != claimed.TaskID || replayed.Status != taskSucceeded || calls.Load() != 1 {
		t.Fatalf("confirmation replay changed the finished task: %+v, calls=%d", replayed, calls.Load())
	}
	assertInput(replayed, third)
}
