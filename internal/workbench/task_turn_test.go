package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestTaskKeepsItsCompletedTurnAndReferences(t *testing.T) {
	const (
		originalText  = "Compare upstream and downstream sampling methods."
		savedAnswer   = "Use paired stations and record the sampling conditions."
		citedText     = "The selected survey has 18 paired samples."
		newMaterial   = "The newer survey has 22 paired samples."
		workspaceText = "Use the approved station list."
		editedTask    = "Write the sampling protocol."
		otherTurnText = "Compare different rainfall periods."
		publishedPlan = "# Sampling protocol"
	)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing task prompt", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		for _, text := range []string{originalText, savedAnswer, citedText, workspaceText, editedTask} {
			if !strings.Contains(prompt, text) {
				t.Errorf("task prompt omitted %q", text)
			}
		}
		if strings.Contains(prompt, newMaterial) || strings.Contains(prompt, otherTurnText) {
			t.Error("task prompt included context outside the selected turn and input")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", publishedPlan)
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
	project := func(requestID string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: "River sampling", TemplateID: "experiment"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	projectID := project("study")
	material, _, err := store.saveMaterial(ctx, projectID, "survey.txt", strings.NewReader(citedText))
	if err != nil {
		t.Fatal(err)
	}
	turn := func(id, requestID, text, answer string, references []string) string {
		t.Helper()
		started, _, _, err := store.beginTurn(ctx, id, requestID, text, references, true)
		if err != nil {
			t.Fatal(err)
		}
		if answer != "" {
			if err := store.appendTurnEvent(ctx, started.TurnID, TurnEvent{Kind: "delta", Text: answer}); err != nil {
				t.Fatal(err)
			}
			if err := store.appendTurnEvent(ctx, started.TurnID, TurnEvent{Kind: "done", Status: "COMPLETED"}); err != nil {
				t.Fatal(err)
			}
		}
		return started.TurnID
	}
	sourceID := turn(projectID, "source", originalText, savedAnswer, []string{material.MaterialID})
	otherID := turn(projectID, "other-turn", otherTurnText, "Use the last three months.", nil)
	incompleteID := turn(projectID, "incomplete", "Draft an outline.", "", nil)
	foreignID := turn(project("other-project"), "source", originalText, savedAnswer, nil)
	if _, _, err := store.saveMaterial(ctx, projectID, material.Name, strings.NewReader(newMaterial)); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.saveWorkspaceDocument(ctx, projectID, "notes/stations.md", "", workspaceText)
	if err != nil {
		t.Fatal(err)
	}

	base := projectsPath + "/" + projectID + "/tasks"
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	send := func(method, suffix, body string, want int) *httptest.ResponseRecorder {
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
		return res
	}
	decode := func(res *httptest.ResponseRecorder, dst any) {
		t.Helper()
		if err := json.Unmarshal(res.Body.Bytes(), dst); err != nil {
			t.Fatalf("invalid response %s: %v", res.Body, err)
		}
	}
	type sourcedTask struct {
		Task
		SourceTurnID string `json:"sourceTurnId"`
	}
	proposal := func(requestID, turnID string) string {
		return fmt.Sprintf(`{"requestId":%q,"instruction":"Draft a plan","sourceTurnId":%q,"inputVersionIds":[%q]}`, requestID, turnID, workspace.VersionID)
	}
	var created, replayed, updated sourcedTask
	decode(send(http.MethodPost, "", proposal("propose", sourceID), http.StatusCreated), &created)
	decode(send(http.MethodPost, "", proposal("propose", sourceID), http.StatusOK), &replayed)
	if created.Status != taskProposed || created.TaskID == "" || created.SourceTurnID != sourceID ||
		replayed.TaskID != created.TaskID || replayed.SourceTurnID != sourceID || replayed.Revision != created.Revision {
		t.Fatalf("proposal or replay lost its source: created=%+v replayed=%+v", created, replayed)
	}
	send(http.MethodPost, "", proposal("propose", otherID), http.StatusConflict)
	for _, id := range []string{incompleteID, foreignID} {
		var failure struct{ Error apiError }
		decode(send(http.MethodPost, "", proposal("invalid-source", id), http.StatusUnprocessableEntity), &failure)
		if failure.Error.Code != "INVALID_TASK_SOURCE" {
			t.Fatalf("wrong source error: %+v", failure.Error)
		}
	}
	send(http.MethodPost, "", proposal("other-proposal", otherID), http.StatusCreated)
	path := "/" + created.TaskID
	patch := fmt.Sprintf(`{"requestId":"edit","expectedRevision":%d,"instruction":%q}`, created.Revision, editedTask)
	decode(send(http.MethodPatch, path, patch, http.StatusOK), &updated)
	if updated.SourceTurnID != sourceID || updated.Instruction != editedTask {
		t.Fatalf("editing the task lost its source: %+v", updated)
	}

	filter := "?limit=1&sourceTurnId=" + url.QueryEscape(sourceID)
	confirm := fmt.Sprintf(`{"requestId":"confirm","expectedRevision":%d}`, updated.Revision)
	send(http.MethodPost, path+"/confirm", confirm, http.StatusAccepted)
	for {
		var page struct {
			Items      []sourcedTask
			NextCursor string
		}
		decode(send(http.MethodGet, filter, "", http.StatusOK), &page)
		if len(page.Items) != 1 || page.Items[0].TaskID != created.TaskID || page.Items[0].SourceTurnID != sourceID || page.NextCursor != "" {
			t.Fatalf("filtered tasks do not match the selected turn: %+v", page)
		}
		if page.Items[0].Status == taskSucceeded {
			break
		}
		if page.Items[0].Status == taskFailed || ctx.Err() != nil {
			t.Fatalf("task did not complete: %+v", page.Items[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	decode(send(http.MethodPost, path+"/confirm", confirm, http.StatusOK), &replayed)
	if replayed.TaskID != created.TaskID || replayed.SourceTurnID != sourceID || replayed.Status != taskSucceeded || calls.Load() != 1 {
		t.Fatalf("confirmation replay lost its source or repeated execution: %+v, calls=%d", replayed, calls.Load())
	}
	if content := send(http.MethodGet, path+"/artifact", "", http.StatusOK).Body.String(); content != publishedPlan {
		t.Fatalf("wrong result for the sourced task: %q", content)
	}
}
