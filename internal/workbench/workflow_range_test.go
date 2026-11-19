package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWorkflowRangesResumeAfterReopening(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var prompt struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil || len(prompt.Messages) == 0 {
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		stage := calls.Add(1)
		for previous := int32(1); previous < stage; previous++ {
			if !strings.Contains(prompt.Messages[len(prompt.Messages)-1].Content, fmt.Sprintf("# Saved stage %d", previous)) {
				http.Error(w, "missing preceding artifact", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Saved stage %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", stage)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
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
	project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: "project", Title: "River sampling", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + project.ProjectID + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		return res
	}
	assertError := func(res *httptest.ResponseRecorder, code string) {
		t.Helper()
		var response struct{ Error apiError }
		if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil || response.Error.Code != code {
			t.Fatalf("want error %s, got %s", code, res.Body)
		}
	}
	readSnapshot := func() (*WorkflowRun, []WorkflowStep) {
		t.Helper()
		var snapshot struct {
			Run   *WorkflowRun
			Steps []WorkflowStep
		}
		if err := json.Unmarshal(request(http.MethodGet, base, "", http.StatusOK).Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot.Run, snapshot.Steps
	}
	waitFor := func(status string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			run, _ := readSnapshot()
			if run == nil {
				t.Fatal("missing workflow run")
			}
			if run.Status == status {
				return *run
			}
			if run.Status == "FAILED" || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	assertRange := func(run WorkflowRun, first, last string, callsExpected int32) {
		t.Helper()
		if run.Scope != (WorkflowScope{StartStep: first, EndStep: last}) || calls.Load() != callsExpected {
			t.Fatalf("unexpected scope or stage count: %+v, model calls=%d", run, calls.Load())
		}
	}

	if run, steps := readSnapshot(); run != nil || len(steps) != 3 || steps[0].Key != "scope" || steps[0].Title == "" || steps[2].Key != "checklist" {
		t.Fatalf("expected ordered template steps before starting: run=%+v, steps=%+v", run, steps)
	}
	invalid := request(http.MethodPost, base, `{"requestId":"invalid","throughStep":"unknown"}`, http.StatusBadRequest)
	assertError(invalid, "INVALID_INPUT")
	const start = `{"requestId":"start","checkpoints":true,"throughStep":"scope"}`
	request(http.MethodPost, base, start, http.StatusCreated)
	first := waitFor("RANGE_COMPLETED")
	assertRange(first, "scope", "scope", 1)
	if first.Steps[0].Status != "SUCCEEDED" || first.Steps[1].Status != "PENDING" {
		t.Fatalf("first range must stop before the plan: %+v", first.Steps)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{Model: model})
	restored := waitFor("RANGE_COMPLETED")
	if restored.RunID != first.RunID || restored.Revision != first.Revision || restored.Scope != first.Scope {
		t.Fatalf("range changed after reopening: %+v", restored)
	}
	request(http.MethodPost, base, start, http.StatusOK)
	conflict := request(http.MethodPost, base, `{"requestId":"start","checkpoints":true,"throughStep":"plan"}`, http.StatusConflict)
	assertError(conflict, "REQUEST_ID_CONFLICT")

	continueBody := func(id string, run WorkflowRun, through string) string {
		return fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"throughStep":%q}`, id, run.RunID, run.Revision, through)
	}
	request(http.MethodPost, base+"/continue", continueBody("continue-plan", first, "scope"), http.StatusBadRequest)
	plan := continueBody("continue-plan", first, "plan")
	request(http.MethodPost, base+"/continue", plan, http.StatusAccepted)
	second := waitFor("RANGE_COMPLETED")
	assertRange(second, "plan", "plan", 2)
	if second.Steps[1].Status != "SUCCEEDED" || second.Steps[2].Status != "PENDING" {
		t.Fatalf("range end must take precedence over the plan checkpoint: %+v", second.Steps)
	}

	request(http.MethodPost, base+"/continue", continueBody("continue-rest", second, ""), http.StatusAccepted)
	finished := waitFor("COMPLETED")
	assertRange(finished, "checklist", "checklist", 3)
	request(http.MethodPost, base+"/continue", plan, http.StatusOK)
	request(http.MethodPost, base+"/continue", continueBody("continue-plan", first, "checklist"), http.StatusConflict)
	replayed := waitFor("COMPLETED")
	if replayed.RunID != first.RunID || replayed.Revision != finished.Revision || replayed.Scope != finished.Scope || calls.Load() != 3 {
		t.Fatalf("continuation replay changed the saved run: %+v, model calls=%d", replayed, calls.Load())
	}
}
