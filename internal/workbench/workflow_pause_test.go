package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWorkflowPauseKeepsCompletedStepsAndRange(t *testing.T) {
	var calls atomic.Int32
	planStarted, planStopped := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		call := calls.Add(1)
		if call > 3 || (call > 1 && !strings.Contains(string(input), "# Sampling scope")) {
			http.Error(w, "missing completed scope or duplicate call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 2 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Partial plan\"}}]}\n\n")
			w.(http.Flusher).Flush()
			close(planStarted)
			<-r.Context().Done()
			close(planStopped)
			return
		}
		artifact := "# Sampling scope"
		if call == 3 {
			artifact = "# Sampling plan"
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", artifact)
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "Field sampling", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, workflowRequest{RequestID: "start", ThroughStep: "plan"}, true)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, suffix, body string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: want HTTP %d, got %d: %s", method, suffix, status, response.Code, response.Body)
		}
		return response.Body.Bytes()
	}
	post := func(action, id string, run WorkflowRun, status int) {
		t.Helper()
		request(http.MethodPost, action, fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, id, run.RunID, run.Revision), status)
	}
	waitFor := func(status string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run WorkflowRun }
			if err := json.Unmarshal(request(http.MethodGet, "", "", http.StatusOK), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == status {
				return snapshot.Run
			}
			if snapshot.Run.Status == "FAILED" || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	await := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(4 * time.Second):
			t.Fatal("model stream did not change state")
		}
	}

	post("/pause", "pause-queued", queued, http.StatusOK)
	paused := waitFor("PAUSED")
	if calls.Load() != 0 || paused.Steps[0].Status != "CANCELLED" || paused.Scope != queued.Scope {
		t.Fatalf("queued pause changed the range or dispatched work: %+v", paused)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	post("/continue", "resume-scope", paused, http.StatusAccepted)
	await(planStarted)
	running := waitFor("RUNNING")
	post("/pause", "pause-stale", paused, http.StatusConflict)
	post("/pause", "pause-plan", running, http.StatusOK)
	await(planStopped)
	paused = waitFor("PAUSED")
	post("/pause", "pause-plan", running, http.StatusOK)
	if latest := waitFor("PAUSED"); latest.Revision != paused.Revision || latest.Steps[0].Status != "SUCCEEDED" || latest.Steps[1].Status != "CANCELLED" {
		t.Fatalf("pause changed completed output or replay mutated the workflow: %+v", latest)
	}
	artifacts := "/" + paused.RunID + "/steps"
	request(http.MethodGet, artifacts+"/plan/artifact", "", http.StatusNotFound)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{Model: model})
	post("/continue", "resume-plan", waitFor("PAUSED"), http.StatusAccepted)
	finished := waitFor("RANGE_COMPLETED")
	if finished.RunID != queued.RunID || finished.Scope != queued.Scope || finished.Steps[2].Status != "PENDING" || calls.Load() != 3 {
		t.Fatalf("resume did not stop at the original range end: %+v, %d calls", finished, calls.Load())
	}
	if artifact := request(http.MethodGet, artifacts+"/scope/artifact", "", http.StatusOK); string(artifact) != "# Sampling scope" {
		t.Fatalf("first artifact changed: %q", artifact)
	}
}
