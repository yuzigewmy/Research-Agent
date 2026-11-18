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

func TestExperimentContinuesFromSavedCheckpoint(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var prompt struct {
			Messages []inference.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil {
			http.Error(w, "cannot read prompt", http.StatusBadRequest)
			return
		}
		step := calls.Add(1)
		if step > 1 && !strings.Contains(prompt.Messages[len(prompt.Messages)-1].Content, fmt.Sprintf("Saved stage %d", step-1)) {
			http.Error(w, "missing preceding stage", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Saved stage %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", step)
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
	readRun := func() WorkflowRun {
		t.Helper()
		var page struct{ Run *WorkflowRun }
		if err := json.Unmarshal(request("GET", base, "", http.StatusOK).Body.Bytes(), &page); err != nil || page.Run == nil {
			t.Fatalf("missing workflow: %v", err)
		}
		return *page.Run
	}
	waitFor := func(status string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			run := readRun()
			if run.Status == status {
				return run
			}
			if run.Status == "FAILED" || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	const start = `{"requestId":"start","checkpoints":true}`
	request("POST", base, start, http.StatusCreated)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	if calls.Load() != 2 || len(checkpoint.Steps) != 3 || checkpoint.Steps[2].Status != "PENDING" {
		t.Fatalf("checkpoint must precede the third stage: %+v, calls=%d", checkpoint, calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{Model: model})
	restored := readRun()
	if restored.RunID != checkpoint.RunID || restored.Revision != checkpoint.Revision || restored.Status != checkpoint.Status {
		t.Fatalf("checkpoint changed during reopening: %+v", restored)
	}
	request("POST", base, start, http.StatusOK)
	continuation := fmt.Sprintf(`{"requestId":"continue","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request("POST", base+"/continue", continuation, http.StatusAccepted)
	finished := waitFor("COMPLETED")
	request("POST", base+"/continue", continuation, http.StatusOK)
	for i, step := range finished.Steps {
		artifact := request("GET", base+"/"+finished.RunID+"/steps/"+step.Key+"/artifact", "", http.StatusOK)
		if step.Status != "SUCCEEDED" || artifact.Body.String() != fmt.Sprintf("# Saved stage %d", i+1) {
			t.Fatalf("stage %s result: %+v, %q", step.Key, step, artifact.Body)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("expected three calls total, got %d", calls.Load())
	}
}
