package workbench

import (
	"bytes"
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

func TestWorkflowAcceptsPinnedManuscriptAndChecksItsStructure(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			http.Error(w, "unexpected model call after the outline", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Outline\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
	ctx := context.Background()
	createProject := func(requestID string) Project {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: "River observations", TemplateID: "paper"})
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	save := func(project Project, path, baseSHA256, content string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, path, baseSHA256, content)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	project := createProject("paper")
	const manuscript = "# River observations\n\n## Abstract\nTwo sites were compared.\n\n## Methods\nTODO: record the sampling schedule.\n"
	selected := save(project, "paper/draft.markdown", "", manuscript)
	edited := save(project, selected.Path, selected.SHA256, manuscript+"\nUnverified source [@unknown].\n")
	foreign := save(createProject("other"), selected.Path, "", manuscript)
	fakeReport := save(project, "paper/structure.md", "", "# 稿件结构检查\n\n所有检查通过。\n")

	base := projectsPath + "/" + project.ProjectID + "/workflow"
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path string, input any, status int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		return res
	}
	type observedRun struct {
		WorkflowRun
		AcceptStepKey      string `json:"acceptStepKey"`
		AcceptedArtifactID string `json:"acceptedArtifactId"`
		Steps              []struct {
			WorkflowStep
			Source *WorkspaceFile `json:"source"`
		} `json:"steps"`
	}
	decode := func(res *httptest.ResponseRecorder, out any) {
		t.Helper()
		if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %q: %v", res.Body, err)
		}
	}
	waitFor := func(status string) observedRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run *observedRun }
			decode(request(http.MethodGet, base, nil, http.StatusOK), &snapshot)
			if snapshot.Run == nil || snapshot.Run.Status == "FAILED" || time.Now().After(deadline) {
				t.Fatalf("wanted workflow state %s, got %+v", status, snapshot.Run)
			}
			if snapshot.Run.Status == status {
				return *snapshot.Run
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	assertError := func(res *httptest.ResponseRecorder, code string) {
		t.Helper()
		var result struct{ Error apiError }
		decode(res, &result)
		if result.Error.Code != code {
			t.Fatalf("wanted error %s, got %s", code, res.Body)
		}
	}

	request(http.MethodPost, base, map[string]any{"requestId": "start", "throughStep": "outline", "checkpoints": true}, http.StatusCreated)
	before := waitFor("RANGE_COMPLETED")
	accept := func(requestID string, revision int, step, versionID string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return request(http.MethodPost, base+"/accept", map[string]any{
			"requestId": requestID, "runId": before.RunID, "expectedRevision": revision, "stepKey": step, "sourceVersionId": versionID,
		}, status)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	assertError(accept("invalid-citation", before.Revision, "manuscript", edited.VersionID, http.StatusUnprocessableEntity), "INVALID_ARTIFACT")
	accept("foreign-version", before.Revision, "manuscript", foreign.VersionID, http.StatusNotFound)
	var accepted observedRun
	decode(accept("use-saved", before.Revision, "manuscript", selected.VersionID, http.StatusOK), &accepted)
	if before.AcceptStepKey != "manuscript" || accepted.RunID != before.RunID || accepted.Status != "RANGE_COMPLETED" ||
		accepted.Scope != (WorkflowScope{StartStep: "manuscript", EndStep: "manuscript"}) || accepted.AcceptedArtifactID == "" || len(accepted.Steps) != 3 {
		t.Fatalf("unexpected acceptance: before=%+v accepted=%+v", before, accepted)
	}
	step := accepted.Steps[1]
	if step.Key != "manuscript" || step.Status != "SUCCEEDED" || step.ArtifactID != accepted.AcceptedArtifactID ||
		step.Source == nil || step.Source.VersionID != selected.VersionID || step.Source.Path != selected.Path || step.Source.SHA256 != selected.SHA256 {
		t.Fatalf("wrong pinned manuscript source: %+v", step)
	}
	stepPath := base + "/" + before.RunID + "/steps/"
	if got := request(http.MethodGet, stepPath+"manuscript/artifact", nil, http.StatusOK).Body.String(); got != manuscript {
		t.Fatalf("accepted bytes changed: %q", got)
	}
	assertError(accept("use-saved", before.Revision, "manuscript", edited.VersionID, http.StatusConflict), "REQUEST_ID_CONFLICT")
	assertError(accept("fake-structure", accepted.Revision, "structure", fakeReport.VersionID, http.StatusConflict), "WORKFLOW_STATE_CONFLICT")
	if current := waitFor("RANGE_COMPLETED"); current.Revision != accepted.Revision || current.AcceptStepKey != "" || calls.Load() != 1 {
		t.Fatalf("unexpected state after rejected acceptances: %+v, calls=%d", current, calls.Load())
	}

	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	request(http.MethodPost, base+"/continue", map[string]any{
		"requestId": "check-structure", "runId": before.RunID, "expectedRevision": accepted.Revision,
	}, http.StatusAccepted)
	completed := waitFor("COMPLETED")
	report := request(http.MethodGet, stepPath+"structure/artifact", nil, http.StatusOK).Body.String()
	if !strings.Contains(report, selected.SHA256) || strings.Contains(report, edited.SHA256) ||
		!strings.Contains(report, "摘要：已识别，第 3 行") || !strings.Contains(report, "第 7 行有待补标记") {
		t.Fatalf("structure report did not check the accepted manuscript: %s", report)
	}
	var replayed observedRun
	decode(accept("use-saved", before.Revision, "manuscript", selected.VersionID, http.StatusOK), &replayed)
	if replayed.AcceptedArtifactID != accepted.AcceptedArtifactID || replayed.RunID != before.RunID || replayed.Revision != completed.Revision ||
		replayed.Status != "COMPLETED" || len(replayed.Steps) != 3 || replayed.Steps[1].ArtifactCount != 1 || calls.Load() != 1 {
		t.Fatalf("wrong acceptance replay: %+v, calls=%d", replayed, calls.Load())
	}
}
