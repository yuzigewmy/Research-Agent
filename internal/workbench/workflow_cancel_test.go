package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkflowCancelStopsCurrentStepAndPreservesCompletedArtifact(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "River observations", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	id := project.ProjectID
	start := func(requestID string) WorkflowRun {
		t.Helper()
		run, applied, err := store.applyWorkflow(ctx, id, workflowStart, workflowRequest{RequestID: requestID}, true)
		if err != nil || !applied {
			t.Fatalf("start workflow: applied=%t, error=%v", applied, err)
		}
		return run
	}
	send := func(method, suffix, body string, want int) []byte {
		t.Helper()
		url := "http://127.0.0.1" + projectsPath + "/" + id + "/workflow" + suffix
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		NewHandler(store, nil, HandlerConfig{}).ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	lookup := func() WorkflowRun {
		t.Helper()
		run, err := store.latestWorkflow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	cancel := func(requestID string, run WorkflowRun) WorkflowRun {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, requestID, run.RunID, run.Revision)
		var result WorkflowRun
		if err := json.Unmarshal(send(http.MethodPost, "/cancel", body, http.StatusOK), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	queued := start("first-run")
	cancelled := cancel("cancel-queued", queued)
	if cancelled.Status != taskCancelled || cancelled.Steps[0].Status != taskCancelled {
		t.Fatalf("queued step was not removed: %+v", cancelled)
	}

	run := start("second-run")
	first, err := store.claimQueuedTask(ctx, id)
	if err != nil || first.workflowRunID != run.RunID || first.workflowStep != 0 {
		t.Fatalf("claim after cancel: %+v, %v", first, err)
	}
	const artifact = "# Recorded scope"
	if err := store.finishTask(ctx, id, first, taskSucceeded, "", taskResult{markdown: artifact}); err != nil {
		t.Fatal(err)
	}
	second, err := store.claimQueuedTask(ctx, id)
	if err != nil || second.workflowStep != 1 {
		t.Fatalf("claim next step: %+v, %v", second, err)
	}
	running := lookup()
	stopping := cancel("cancel-running", running)
	if stopping.Status != taskCancelling || stopping.Steps[1].Status != taskCancelling {
		t.Fatalf("expected pending cancellation: %+v", stopping)
	}
	if err := store.finishTask(ctx, id, second, taskSucceeded, "", taskResult{markdown: "late output"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	terminal := lookup()
	if terminal.Status != taskCancelled || terminal.Steps[0].Status != taskSucceeded || terminal.Steps[1].Status != taskCancelled || terminal.Steps[2].Status != "PENDING" {
		t.Fatalf("late output advanced the cancelled workflow: %+v", terminal)
	}
	if replay := cancel("cancel-running", running); replay.Revision != terminal.Revision || replay.Status != taskCancelled {
		t.Fatalf("cancel replay changed the terminal state: %+v", replay)
	}
	for _, action := range []string{"continue", "rerun"} {
		body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"stepKey":"scope"}`, action, run.RunID, terminal.Revision)
		send(http.MethodPost, "/"+action, body, http.StatusConflict)
	}
	third := start("third-run")
	base := "/" + run.RunID + "/steps"
	if got := string(send(http.MethodGet, base+"/scope/artifact", "", http.StatusOK)); got != artifact {
		t.Fatalf("completed artifact changed: %q", got)
	}
	send(http.MethodGet, base+"/plan/artifact", "", http.StatusNotFound)

	if task, err := store.claimQueuedTask(ctx, id); err != nil || task.workflowRunID != third.RunID {
		t.Fatalf("claim third run: %+v, %v", task, err)
	}
	cancel("cancel-before-restart", lookup())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	if restored := lookup(); restored.RunID != third.RunID || restored.Status != taskCancelled || restored.Steps[0].Status != taskCancelled {
		t.Fatalf("recovery lost the cancellation: %+v", restored)
	}
}
