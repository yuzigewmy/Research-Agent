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

func TestWorkflowSkipPersistsAndAllowsChecklistRerun(t *testing.T) {
	const checklist = "# Field checklist"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", checklist)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
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
	base := projectsPath + "/" + id + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{})
	send := func(method, suffix, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	type observedRun struct {
		WorkflowRun
		SkipStepKey string `json:"skipStepKey"`
	}
	decode := func(body []byte, target any) {
		t.Helper()
		if err := json.Unmarshal(body, target); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	readRun := func() observedRun {
		t.Helper()
		var snapshot struct{ Run *observedRun }
		decode(send(http.MethodGet, "", "", http.StatusOK), &snapshot)
		if snapshot.Run == nil {
			t.Fatal("missing workflow run")
		}
		return *snapshot.Run
	}
	apply := func(action string, input workflowRequest) WorkflowRun {
		t.Helper()
		run, applied, err := store.applyWorkflow(ctx, id, action, input, true)
		if err != nil || !applied {
			t.Fatalf("%s: applied=%t, error=%v", action, applied, err)
		}
		return run
	}
	finish := func(runID string, step int, markdown string) {
		t.Helper()
		task, err := store.claimQueuedTask(ctx, id, true)
		if err != nil || task.workflowRunID != runID || task.workflowStep != step {
			t.Fatalf("claim step %d of %s: task=%+v, error=%v", step, runID, task, err)
		}
		if err := store.finishTask(ctx, id, task, taskSucceeded, "", taskResult{markdown: markdown}); err != nil {
			t.Fatal(err)
		}
	}
	skipBody := func(requestID string, run observedRun, step, reason string) string {
		return fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"stepKey":%q,"reason":%q}`, requestID, run.RunID, run.Revision, step, reason)
	}
	const reason = "本轮不需要单独的验收清单"
	started := apply(workflowStart, workflowRequest{RequestID: "start", Checkpoints: new(true), ThroughStep: "scope"})
	finish(started.RunID, 0, "# Research scope")
	beforePlan := readRun()
	send(http.MethodPost, "/skip", skipBody("required-plan", beforePlan, "plan", reason), http.StatusConflict)
	send(http.MethodPost, "/skip", skipBody("missing-plan", beforePlan, "checklist", reason), http.StatusConflict)
	if beforePlan.WorkflowKey != "experiment-v2" || beforePlan.SkipStepKey != "" {
		t.Fatalf("wrong experiment revision or premature skip: %+v", beforePlan)
	}

	apply(workflowContinue, workflowRequest{RequestID: "continue", RunID: started.RunID, ExpectedRevision: beforePlan.Revision})
	finish(started.RunID, 1, "# Experiment plan")
	checkpoint := readRun()
	if checkpoint.Status != "WAITING_CHECKPOINT" || checkpoint.SkipStepKey != "checklist" || len(checkpoint.Steps) != 3 {
		t.Fatalf("expected a skippable checklist at the checkpoint: %+v", checkpoint)
	}
	send(http.MethodPost, "/skip", skipBody("old-revision", beforePlan, "checklist", reason), http.StatusConflict)
	body := skipBody("skip-checklist", checkpoint, "checklist", " \t"+reason+" \n")
	var skipped observedRun
	decode(send(http.MethodPost, "/skip", body, http.StatusOK), &skipped)
	assertSkipped := func(run observedRun) {
		t.Helper()
		if run.RunID != started.RunID || run.Status != "COMPLETED" || run.SkipStepKey != "" || len(run.Steps) != 3 {
			t.Fatalf("wrong skip result: %+v", run)
		}
		for i := range 2 {
			if run.Steps[i].Status != "SUCCEEDED" || run.Steps[i].ArtifactID == "" || run.Steps[i].ArtifactID != checkpoint.Steps[i].ArtifactID {
				t.Fatalf("skip must preserve completed step %d: %+v", i, run.Steps[i])
			}
		}
		step := run.Steps[2]
		if step.Status != "SKIPPED" || step.Message != reason || step.ArtifactID != "" || step.ArtifactCount != 0 {
			t.Fatalf("skipped checklist should record the reason without an artifact: %+v", step)
		}
	}
	assertSkipped(skipped)
	artifactPath := "/" + started.RunID + "/steps/checklist/artifact"
	send(http.MethodGet, artifactPath, "", http.StatusNotFound)
	if _, err := store.claimQueuedTask(ctx, id, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("skipping a step left a task available to claim: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{})
	assertSkipped(readRun())
	var replay observedRun
	decode(send(http.MethodPost, "/skip", body, http.StatusOK), &replay)
	assertSkipped(replay)
	if replay.Revision != skipped.Revision {
		t.Fatalf("skip replay changed the revision: %+v", replay)
	}
	send(http.MethodPost, "/skip", skipBody("skip-checklist", checkpoint, "checklist", "Different reason"), http.StatusConflict)

	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	rerun := fmt.Sprintf(`{"requestId":"rerun-checklist","runId":%q,"expectedRevision":%d,"stepKey":"checklist"}`, skipped.RunID, skipped.Revision)
	send(http.MethodPost, "/rerun", rerun, http.StatusAccepted)
	var completed observedRun
	deadline := time.Now().Add(4 * time.Second)
	for {
		completed = readRun()
		if completed.Status == "COMPLETED" {
			break
		}
		if completed.Status == "FAILED" || time.Now().After(deadline) {
			t.Fatalf("checklist rerun did not complete: %+v", completed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.RunID != started.RunID || len(completed.Steps) != 3 || completed.Steps[2].Status != "SUCCEEDED" || calls.Load() != 1 {
		t.Fatalf("expected one checklist execution after the skip: %+v, calls=%d", completed, calls.Load())
	}
	if got := string(send(http.MethodGet, artifactPath, "", http.StatusOK)); got != checklist {
		t.Fatalf("unexpected checklist artifact: %q", got)
	}
	for _, request := range []struct{ suffix, body string }{{"/skip", body}, {"/rerun", rerun}} {
		var latest observedRun
		decode(send(http.MethodPost, request.suffix, request.body, http.StatusOK), &latest)
		if latest.RunID != completed.RunID || latest.Revision != completed.Revision || latest.Status != "COMPLETED" || len(latest.Steps) != 3 ||
			latest.Steps[2].ArtifactID != completed.Steps[2].ArtifactID || latest.Steps[2].ArtifactCount != 1 || calls.Load() != 1 {
			t.Fatalf("replay of %s changed the rerun result: %+v, calls=%d", request.suffix, latest, calls.Load())
		}
	}

	handler = NewHandler(store, nil, HandlerConfig{})
	legacy := apply(workflowStart, workflowRequest{RequestID: "legacy", Checkpoints: new(true)})
	finish(legacy.RunID, 0, "# Legacy scope")
	finish(legacy.RunID, 1, "# Legacy plan")
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_runs SET workflow_key = 'experiment-v1' WHERE project_id = ? AND run_id = ?`, id, legacy.RunID); err != nil {
		t.Fatal(err)
	}
	oldRun := readRun()
	if oldRun.WorkflowKey != "experiment-v1" || oldRun.Status != "WAITING_CHECKPOINT" || oldRun.SkipStepKey != "" {
		t.Fatalf("legacy experiment must keep the checklist required: %+v", oldRun)
	}
	send(http.MethodPost, "/skip", skipBody("legacy-skip", oldRun, "checklist", reason), http.StatusConflict)
}
