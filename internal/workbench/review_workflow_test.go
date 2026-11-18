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
	"research-agent/internal/literature"
)

func TestReviewWorkflowKeepsSourcesAndRejectsUnknownCitations(t *testing.T) {
	const originalDOI, laterDOI = "10.5555/field-study", "10.5555/later-study"
	citation := "[@" + citationKey(originalDOI) + "]"
	var calls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		last := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(last, originalDOI) || strings.Contains(last, laterDOI) ||
			(call > 1 && !strings.Contains(last, "# 主题规划")) {
			http.Error(w, "wrong evidence or preceding stage", http.StatusBadRequest)
			return
		}
		output := "# 主题规划\n\n河流水温观测 " + citation
		if call == 2 {
			output = "# 综述\n\n未经核验 [@ref-unknown]"
		} else if call == 3 {
			output = "# 综述\n\n观测证据 " + citation
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	defer modelServer.Close()
	model, err := inference.New(inference.Config{BaseURL: modelServer.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	open := func() *Store {
		t.Helper()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	store := open()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "review", Title: "河流水温的观测方法", TemplateID: "literature_review"})
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, status int) string {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		return res.Body.String()
	}
	wait := func(status string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run WorkflowRun }
			if err := json.Unmarshal([]byte(request("GET", "", "", http.StatusOK)), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == status {
				return snapshot.Run
			}
			if (snapshot.Run.Status != taskQueued && snapshot.Run.Status != taskRunning) || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	add := func(doi string) {
		t.Helper()
		if _, _, err := store.saveCitation(ctx, project.ProjectID, literature.Work{DOI: doi, Title: "水温监测"}); err != nil {
			t.Fatal(err)
		}
	}
	const start = `{"requestId":"start-review","checkpoints":true}`
	request("POST", "", start, http.StatusUnprocessableEntity)
	add(originalDOI)
	request("POST", "", start, http.StatusCreated)
	checkpoint := wait(workflowWaiting)
	if checkpoint.SourceCount != 1 || checkpoint.TargetPaperCount != 20 || calls.Load() != 1 {
		t.Fatalf("unexpected checkpoint: %+v, model calls %d", checkpoint, calls.Load())
	}
	add(laterDOI)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	request("POST", "", start, http.StatusOK)
	advance := func(run WorkflowRun, id string) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, id, run.RunID, run.Revision)
		request("POST", "/continue", body, http.StatusAccepted)
	}
	advance(checkpoint, "write-draft")
	failed := wait(taskFailed)
	if !strings.Contains(failed.Steps[2].Message, "来源清单之外") {
		t.Fatalf("unexpected citation error: %+v", failed)
	}
	artifact := "/" + checkpoint.RunID + "/steps/manuscript/artifact"
	request("GET", artifact, "", http.StatusNotFound)
	advance(failed, "write-again")
	wait(workflowDone)
	body := request("GET", artifact, "", http.StatusOK)
	if !strings.Contains(body, citation) || calls.Load() != 3 {
		t.Fatalf("wrong manuscript or too many calls: %s, calls %d", body, calls.Load())
	}
	publish := func() WorkspaceFile {
		t.Helper()
		var file WorkspaceFile
		if err := json.Unmarshal([]byte(request("POST", "/"+checkpoint.RunID+"/manuscript", "", http.StatusOK)), &file); err != nil {
			t.Fatal(err)
		}
		return file
	}
	first := publish()
	doc, err := store.readWorkspaceDocument(first)
	if err != nil || doc.Content != body {
		t.Fatalf("workspace differs from the workflow result: %v", err)
	}
	edited, err := store.saveWorkspaceDocument(ctx, project.ProjectID, first.Path, first.SHA256, body+"\n编辑补充。")
	if err != nil {
		t.Fatal(err)
	}
	if reopened := publish(); reopened.VersionID != edited.VersionID {
		t.Fatalf("reopening changed the edited version: %+v", reopened)
	}
}
