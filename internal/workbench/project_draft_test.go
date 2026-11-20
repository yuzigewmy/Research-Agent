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

func TestProjectDraftPlanningAndFinalizationSurviveReopening(t *testing.T) {
	const (
		drafts        = "/api/v1/project-drafts"
		prompt        = "Plan a study comparing two river monitoring sites."
		clarification = "We have twelve paired monthly observations at each site."
		question      = "How many paired observations are available?"
		goal          = "Compare the twelve paired monthly observations."
		deliverables  = "A sampling protocol and a reproducible comparison table."
	)
	first := fmt.Sprintf(`{"title":"River study","templateId":"experiment","goal":"Compare river sites.","deliverables":"A draft protocol.","question":%q}`, question)
	second := fmt.Sprintf(`{"title":"Paired river study","templateId":"experiment","goal":%q,"deliverables":%q,"question":""}`, goal, deliverables)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing draft input", http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		var supplied strings.Builder
		for _, message := range input.Messages {
			supplied.WriteString(message.Content)
		}
		if !strings.Contains(supplied.String(), prompt) || strings.Contains(supplied.String(), clarification) != (call > 1) {
			t.Errorf("planning call %d received the wrong saved input", call)
		}
		var request struct {
			AvailableTemplates []struct{ ID string }
		}
		if err := json.Unmarshal([]byte(input.Messages[len(input.Messages)-1].Content), &request); err != nil ||
			len(request.AvailableTemplates) != len(projectTemplates) {
			t.Errorf("planning call %d did not receive the template catalog", call)
		} else {
			for index, choice := range request.AvailableTemplates {
				if choice.ID != projectTemplates[index].ID {
					t.Errorf("wrong planning template at position %d: %q", index, choice.ID)
				}
			}
		}
		output, finish := first, "stop"
		switch call {
		case 2:
			output = second
		case 3:
			output, finish = `{"title":"Incomplete replacement"`, "length"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", output, finish)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	var handler http.Handler
	open := func() *Store {
		t.Helper()
		store, err := Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		handler = NewHandler(store, nil, HandlerConfig{Model: model})
		return store
	}
	store := open()
	request := func(method, path, body string, want int, result any) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if result != nil {
			if err := json.Unmarshal(res.Body.Bytes(), result); err != nil {
				t.Fatalf("invalid response %s: %v", res.Body, err)
			}
		}
	}
	type draftResponse struct {
		DraftID, Prompt, Title, TemplateID, Goal, Deliverables, Clarification string
		Question, Status, UpdatedAt, ProjectID, Message                       string
		Revision                                                              int
	}
	create := fmt.Sprintf(`{"requestId":"create-draft","prompt":%q}`, prompt)
	var created, replayed draftResponse
	request(http.MethodPost, drafts, create, http.StatusCreated, &created)
	request(http.MethodPost, drafts, create, http.StatusOK, &replayed)
	if created.DraftID == "" || created.Status != "DRAFT" || created.Prompt != prompt || created.Revision != 1 || created.UpdatedAt == "" || replayed != created || calls.Load() != 0 {
		t.Fatalf("wrong draft creation or replay: created=%+v replay=%+v calls=%d", created, replayed, calls.Load())
	}
	path := drafts + "/" + created.DraftID
	var draftList struct {
		Items           []draftResponse
		ModelConfigured bool
	}
	var projectList struct{ Items []Project }
	request(http.MethodGet, drafts, "", http.StatusOK, &draftList)
	request(http.MethodGet, projectsPath, "", http.StatusOK, &projectList)
	if !draftList.ModelConfigured || len(draftList.Items) != 1 || draftList.Items[0] != created || len(projectList.Items) != 0 {
		t.Fatalf("draft should only appear in its own list: drafts=%+v projects=%+v", draftList, projectList)
	}
	mutation := func(id string, revision int) string {
		return fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, id, revision)
	}
	var planned draftResponse
	request(http.MethodPost, path+"/plan", mutation("plan-first", created.Revision), http.StatusOK, &planned)
	if planned.DraftID != created.DraftID || planned.Status != "DRAFT" || planned.Question != question || planned.Title != "River study" || planned.Revision != created.Revision+2 || calls.Load() != 1 {
		t.Fatalf("wrong first plan: %+v, calls=%d", planned, calls.Load())
	}
	patch := func(id string, revision int) string {
		return fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d,"title":%q,"templateId":%q,"goal":%q,"deliverables":%q,"clarification":%q}`,
			id, revision, planned.Title, planned.TemplateID, planned.Goal, planned.Deliverables, clarification)
	}
	request(http.MethodPatch, path, patch("stale-edit", created.Revision), http.StatusConflict, nil)
	var edited draftResponse
	request(http.MethodPatch, path, patch("clarify", planned.Revision), http.StatusOK, &edited)
	if edited.Clarification != clarification || edited.Revision != planned.Revision+1 || calls.Load() != 1 {
		t.Fatalf("clarification save was not a separate revision: %+v, calls=%d", edited, calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	var restored draftResponse
	request(http.MethodGet, path, "", http.StatusOK, &restored)
	if restored != edited {
		t.Fatalf("reopening changed the saved draft: %+v", restored)
	}
	var refined draftResponse
	request(http.MethodPost, path+"/plan", mutation("plan-second", restored.Revision), http.StatusOK, &refined)
	if refined.Title != "Paired river study" || refined.Goal != goal || refined.Deliverables != deliverables || refined.Question != "" || refined.Revision != restored.Revision+2 || calls.Load() != 2 {
		t.Fatalf("clarification did not refine the draft: %+v, calls=%d", refined, calls.Load())
	}
	var failed draftResponse
	request(http.MethodPost, path+"/plan", mutation("plan-failed", refined.Revision), http.StatusOK, &failed)
	if failed.DraftID != refined.DraftID || failed.Status != "DRAFT" || failed.Title != refined.Title || failed.TemplateID != refined.TemplateID ||
		failed.Prompt != prompt || failed.Clarification != clarification || failed.Goal != goal || failed.Deliverables != deliverables ||
		failed.Question != refined.Question || strings.TrimSpace(failed.Message) == "" || failed.Revision != refined.Revision+2 || calls.Load() != 3 {
		t.Fatalf("truncated model output changed the saved plan: %+v, calls=%d", failed, calls.Load())
	}
	var otherDraft draftResponse
	request(http.MethodPost, drafts, `{"requestId":"other-draft","prompt":"Draft a separate survey."}`, http.StatusCreated, &otherDraft)
	request(http.MethodPost, path+"/finalize", mutation("stale-finalize", refined.Revision), http.StatusConflict, nil)
	var project, sameProject, readProject Project
	finalize := mutation("finalize", failed.Revision)
	request(http.MethodPost, path+"/finalize", finalize, http.StatusCreated, &project)
	if project.ProjectID == "" || project.Title != refined.Title || project.TemplateID != refined.TemplateID || project.Goal != goal || project.Deliverables != deliverables {
		t.Fatalf("finalization lost the configured project: %+v", project)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	open()
	request(http.MethodPost, path+"/finalize", finalize, http.StatusOK, &sameProject)
	request(http.MethodGet, projectsPath+"/"+project.ProjectID, "", http.StatusOK, &readProject)
	var finalDraft draftResponse
	request(http.MethodGet, path, "", http.StatusOK, &finalDraft)
	request(http.MethodGet, drafts, "", http.StatusOK, &draftList)
	request(http.MethodGet, projectsPath, "", http.StatusOK, &projectList)
	if sameProject != project || readProject != project || finalDraft.Status != "CREATED" || finalDraft.ProjectID != project.ProjectID || finalDraft.Revision != failed.Revision+1 ||
		len(draftList.Items) != 1 || draftList.Items[0] != otherDraft || len(projectList.Items) != 1 || projectList.Items[0].ProjectID != project.ProjectID || calls.Load() != 3 {
		t.Fatalf("finalization replay changed saved state: draft=%+v project=%+v drafts=%+v projects=%+v calls=%d", finalDraft, sameProject, draftList, projectList, calls.Load())
	}
}
