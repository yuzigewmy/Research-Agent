package workbench

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWorkflowRerunKeepsArtifactsAndUsesCurrentDependencies(t *testing.T) {
	const (
		oldScope = "# 初版研究范围"
		oldPlan  = "# Initial sampling plan"
		oldCheck = "# Initial field checklist"
		newScope = "# Revised research scope"
		newPlan  = "# Revised sampling plan"
		newCheck = "# Revised field checklist"
	)
	outputs := []string{oldScope, oldPlan, oldCheck, newScope, newPlan, newCheck}
	var calls atomic.Int32
	rerunStarted, finishRerun := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		call := int(calls.Add(1))
		if call > len(outputs) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		if call == 4 {
			close(rerunStarted)
			select {
			case <-finishRerun:
			case <-r.Context().Done():
				return
			}
		}
		if call >= 5 {
			prompt := input.Messages[len(input.Messages)-1].Content
			if !strings.Contains(prompt, newScope) || (call == 6 && !strings.Contains(prompt, newPlan)) ||
				strings.Contains(prompt, oldScope) || strings.Contains(prompt, oldPlan) || strings.Contains(prompt, oldCheck) {
				t.Errorf("model call %d received the wrong predecessor artifacts", call)
				http.Error(w, "wrong predecessor artifacts", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", outputs[call-1])
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
	request := func(method, path, body string, status int) string {
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
		return res.Body.String()
	}
	readRun := func() WorkflowRun {
		t.Helper()
		var page struct{ Run *WorkflowRun }
		if err := json.Unmarshal([]byte(request(http.MethodGet, base, "", http.StatusOK)), &page); err != nil || page.Run == nil {
			t.Fatalf("missing workflow run: %v", err)
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

	request(http.MethodPost, base, `{"requestId":"start"}`, http.StatusCreated)
	initial := waitFor("COMPLETED")
	stepPath := base + "/" + initial.RunID + "/steps/"
	rerunBody := func(id string, rev int, step string) string {
		return fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"stepKey":%q}`, id, initial.RunID, rev, step)
	}
	rerun := rerunBody("rerun-scope", initial.Revision, "scope")
	request(http.MethodPost, base+"/rerun", rerun, http.StatusAccepted)
	select {
	case <-rerunStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("scope rerun did not start")
	}
	request(http.MethodGet, stepPath+"scope/artifact", "", http.StatusNotFound)
	request(http.MethodPost, base+"/rerun", rerunBody("while-running", readRun().Revision, "scope"), http.StatusConflict)
	close(finishRerun)

	paused := waitFor("RANGE_COMPLETED")
	if paused.RunID != initial.RunID || paused.Scope != (WorkflowScope{StartStep: "scope", EndStep: "scope"}) || calls.Load() != 4 ||
		len(paused.Steps) != 3 || paused.Steps[0].Status != "SUCCEEDED" || paused.Steps[1].Status != "STALE" || paused.Steps[2].Status != "STALE" {
		t.Fatalf("unexpected workflow after rerunning the scope: %+v, calls=%d", paused, calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{Model: model})
	var replayed WorkflowRun
	body := request(http.MethodPost, base+"/rerun", rerun, http.StatusOK)
	if err := json.Unmarshal([]byte(body), &replayed); err != nil || replayed.RunID != initial.RunID || replayed.Revision != paused.Revision {
		t.Fatalf("wrong rerun receipt after reopening: %s", body)
	}
	assertConflict := func(body, code string) {
		t.Helper()
		var failure struct{ Error apiError }
		if err := json.Unmarshal([]byte(body), &failure); err != nil || failure.Error.Code != code {
			t.Fatalf("want %s, got %s", code, body)
		}
	}
	conflict := request(http.MethodPost, base+"/rerun", rerunBody("rerun-scope", initial.Revision, "plan"), http.StatusConflict)
	assertConflict(conflict, "REQUEST_ID_CONFLICT")
	conflict = request(http.MethodPost, base+"/rerun", rerunBody("old-revision", initial.Revision, "scope"), http.StatusConflict)
	assertConflict(conflict, "REVISION_CONFLICT")
	conflict = request(http.MethodPost, base+"/rerun", rerunBody("requires-plan", paused.Revision, "checklist"), http.StatusConflict)
	assertConflict(conflict, "WORKFLOW_DEPENDENCY_REQUIRED")
	if current := readRun(); current.Revision != paused.Revision || calls.Load() != 4 {
		t.Fatalf("replay or rejected rerun changed the workflow: %+v, calls=%d", current, calls.Load())
	}

	for i, key := range []string{"scope", "plan", "checklist"} {
		step := stepPath + key
		wantHistory := map[string]bool{outputs[i]: false}
		if key == "scope" {
			wantHistory[newScope] = true
			if got := request(http.MethodGet, step+"/artifact", "", http.StatusOK); got != newScope {
				t.Fatalf("scope artifact: got %q, want %q", got, newScope)
			}
		} else {
			request(http.MethodGet, step+"/artifact", "", http.StatusNotFound)
		}
		var history struct {
			Items []struct {
				ArtifactID string `json:"artifactId"`
				CreatedAt  string `json:"createdAt"`
				Current    *bool  `json:"current"`
			}
		}
		body := request(http.MethodGet, step+"/artifacts", "", http.StatusOK)
		if err := json.Unmarshal([]byte(body), &history); err != nil || len(history.Items) != len(wantHistory) {
			t.Fatalf("unexpected %s artifact history: %s", key, body)
		}
		for _, artifact := range history.Items {
			if artifact.ArtifactID == "" || artifact.CreatedAt == "" || artifact.Current == nil {
				t.Fatalf("incomplete artifact record: %+v", artifact)
			}
			got := request(http.MethodGet, step+"/artifacts/"+url.PathEscape(artifact.ArtifactID), "", http.StatusOK)
			wantCurrent, expected := wantHistory[got]
			if !expected || *artifact.Current != wantCurrent {
				t.Fatalf("unexpected %s history entry: current=%v, artifact=%q", key, *artifact.Current, got)
			}
			delete(wantHistory, got)
		}
	}

	if current := paused.Steps[0]; current.ArtifactName != "experiment-scope.md" || current.ArtifactSize == nil || *current.ArtifactSize != int64(len(newScope)) {
		t.Fatalf("step did not report its original file: %+v", current)
	}
	const bundleSuffix = "/files/bundle"
	bundleBody := fmt.Sprintf(`{"items":[{"kind":"artifact","id":%q},{"kind":"artifact","id":%q}]}`, initial.Steps[0].ArtifactID, paused.Steps[0].ArtifactID)
	bundle := request(http.MethodPost, projectsPath+"/"+project.ProjectID+bundleSuffix, bundleBody, http.StatusOK)
	archive, err := zip.NewReader(strings.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	readEntry := func(name string) []byte {
		t.Helper()
		file, err := archive.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var manifest workspaceBundleManifest
	if err := json.Unmarshal(readEntry("manifest.json"), &manifest); err != nil || manifest.ProjectID != project.ProjectID || len(manifest.Files) != 2 {
		t.Fatalf("incorrect workflow bundle manifest: %+v (%v)", manifest, err)
	}
	for i, expected := range []struct{ id, body string }{{initial.Steps[0].ArtifactID, oldScope}, {paused.Steps[0].ArtifactID, newScope}} {
		entry := manifest.Files[i]
		if entry.Kind != "artifact" || entry.ArtifactID != expected.id || entry.RunID != initial.RunID || entry.StepKey != "scope" ||
			entry.Path != "experiment-scope.md" || entry.Size != int64(len(expected.body)) || entry.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(expected.body))) || string(readEntry(entry.Name)) != expected.body {
			t.Fatalf("bundle did not retain original artifact: %+v", entry)
		}
	}
	request(http.MethodPost, projectsPath+"/other-project"+bundleSuffix, bundleBody, http.StatusNotFound)

	resume := fmt.Sprintf(`{"requestId":"resume","runId":%q,"expectedRevision":%d}`, paused.RunID, paused.Revision)
	request(http.MethodPost, base+"/continue", resume, http.StatusAccepted)
	finished := waitFor("COMPLETED")
	if finished.RunID != initial.RunID || calls.Load() != 6 {
		t.Fatalf("unexpected workflow after recomputing downstream: %+v, calls=%d", finished, calls.Load())
	}
	if got := request(http.MethodGet, stepPath+"checklist/artifact", "", http.StatusOK); got != newCheck {
		t.Fatalf("recomputed checklist: got %q, want %q", got, newCheck)
	}
}
