package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestConversationManuscriptCheckRequiresCurrentVersionAndPreservesWorkflow(t *testing.T) {
	const (
		manuscriptPath = "study.markdown"
		oldText        = "# 河流观测\n\nSuperseded manuscript text.\n"
		originalText   = "# 河流观测\n\nOriginal manuscript text.\n"
		updatedText    = "# 河流观测\n\n## 摘要\nUpdated manuscript text.\n\nTODO: record the sampling schedule.\n"
		foreignText    = "Foreign manuscript text."
		notesText      = "Local notes outside the manuscript list."
		instruction    = "核对稿件结构与缺失项。"
		revisedPrompt  = "按新版本核对摘要和采样说明。"
	)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	project := func(id string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: "河流观测", TemplateID: "experiment"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	save := func(projectID, path, base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, projectID, path, base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	projectID := project("river")
	older := save(projectID, manuscriptPath, "", oldText)
	original := save(projectID, manuscriptPath, older.SHA256, originalText)
	notes := save(projectID, "notes.txt", "", notesText)
	foreign := save(project("other"), manuscriptPath, "", foreignText)

	// Seed a completed workflow step without consuming a model call.
	if _, _, err := store.applyWorkflow(ctx, projectID, workflowStart, workflowRequest{RequestID: "run", ThroughStep: "scope"}, true); err != nil {
		t.Fatal(err)
	}
	step, err := store.claimQueuedTask(ctx, projectID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, projectID, step, taskSucceeded, "", taskResult{markdown: "# Sampling scope"}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct {
				Function struct {
					Name       string
					Parameters struct {
						Properties map[string]struct{ Enum []string }
					}
				}
			}
		}
		call := calls.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || call > 2 || len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			t.Errorf("unexpected model call %d", call)
			return
		}
		if got := input.Tools[0].Function.Parameters.Properties["checkVersionId"].Enum; !slices.Equal(got, []string{original.VersionID}) {
			t.Errorf("tool must enumerate the one eligible manuscript version, got %v", got)
		}
		var versions []WorkspaceFile
		for _, message := range input.Messages {
			for _, excluded := range []string{older.VersionID, foreign.VersionID, notes.VersionID, oldText, originalText, foreignText, notesText} {
				if strings.Contains(message.Content, strings.TrimSpace(excluded)) {
					t.Errorf("manuscript lookup exposed %q", excluded)
				}
			}
			if strings.Contains(message.Content, `"manuscriptVersions"`) {
				_, payload, _ := strings.Cut(message.Content, "{")
				var data struct{ ManuscriptVersions []WorkspaceFile }
				if err := json.Unmarshal([]byte("{"+payload), &data); err != nil {
					t.Errorf("invalid manuscript metadata: %v", err)
				}
				versions = append(versions, data.ManuscriptVersions...)
			}
		}
		if len(versions) != 1 || versions[0].Path != original.Path || versions[0].VersionID != original.VersionID || versions[0].SHA256 != original.SHA256 {
			t.Errorf("wrong manuscript metadata: %+v", versions)
		}
		versionID := original.VersionID
		if call == 2 {
			versionID = foreign.VersionID
		}
		args := fmt.Sprintf(`{"instruction":%q,"checkVersionId":%q}`, instruction, versionID)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请确认稿件核对。\",\"tool_calls\":[{\"index\":0,\"id\":\"call-check\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
	}))
	defer upstream.Close()
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + projectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, want int, target any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if target != nil {
			if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	type checkTask struct {
		TaskID, SourceTurnID, Instruction, Kind, Status string
		Revision                                        int
		Inputs                                          []WorkspaceFile
	}
	list := func(turnID string) []checkTask {
		t.Helper()
		var page struct{ Items []checkTask }
		request(http.MethodGet, "/tasks?sourceTurnId="+url.QueryEscape(turnID), "", http.StatusOK, &page)
		return page.Items
	}
	ask := func(id string) (Turn, TurnEvent) {
		t.Helper()
		var turn Turn
		body := fmt.Sprintf(`{"requestId":%q,"text":"请检查我保存的稿件。","mode":"CONFIRM"}`, id)
		request(http.MethodPost, "/turns", body, http.StatusAccepted, &turn)
		stream := request(http.MethodGet, "/turns/"+turn.TurnID+"/stream", "", http.StatusOK, nil).Body.String()
		var terminal TurnEvent
		for line := range strings.SplitSeq(stream, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var event TurnEvent
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					t.Fatalf("decode SSE: %v", err)
				}
				if event.Kind == "done" {
					terminal = event
				}
			}
		}
		request(http.MethodPost, "/turns", body, http.StatusOK, &turn)
		return turn, terminal
	}

	turn, terminal := ask("check")
	if turn.Status != "COMPLETED" || turn.ProposedTaskID == "" || terminal.Status != turn.Status || terminal.ProposedTaskID != turn.ProposedTaskID {
		t.Fatalf("turn did not publish a check proposal: turn=%+v, event=%+v", turn, terminal)
	}
	invalid, invalidTerminal := ask("foreign-check")
	if invalid.ProposedTaskID != "" || invalidTerminal.ProposedTaskID != "" || len(list(invalid.TurnID)) != 0 {
		t.Fatalf("foreign manuscript was proposed: %+v", invalid)
	}
	tasks := list(turn.TurnID)
	if len(tasks) != 1 {
		t.Fatalf("want one proposed check, got %+v", tasks)
	}
	check := tasks[0]
	assertInput := func(task checkTask, source WorkspaceDocument, status, wantInstruction string) {
		t.Helper()
		if task.TaskID != turn.ProposedTaskID || task.SourceTurnID != turn.TurnID || task.Kind != "manuscript-check" ||
			task.Status != status || task.Instruction != wantInstruction || len(task.Inputs) != 1 ||
			task.Inputs[0].VersionID != source.VersionID || task.Inputs[0].Path != source.Path || task.Inputs[0].SHA256 != source.SHA256 {
			t.Fatalf("task did not retain the expected manuscript binding: %+v", task)
		}
	}
	assertInput(check, original, "PROPOSED", instruction)
	taskPath := "/tasks/" + check.TaskID
	// A persisted check can be approved when the dialogue model is unavailable.
	handler = NewHandler(store, nil, HandlerConfig{})
	request(http.MethodGet, taskPath+"/artifact", "", http.StatusNotFound, nil)
	before := request(http.MethodGet, "/workflow", "", http.StatusOK, nil).Body.String()

	updated := save(projectID, manuscriptPath, original.SHA256, updatedText)
	confirm := func(id string, revision int, status int, out any) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, id, revision)
		request(http.MethodPost, taskPath+"/confirm", body, status, out)
	}
	var conflict struct{ Error apiError }
	confirm("stale", check.Revision, http.StatusConflict, &conflict)
	if conflict.Error.Code != "TASK_INPUT_CHANGED" {
		t.Fatalf("wrong outdated manuscript error: %+v", conflict.Error)
	}
	patch := func(id, versionID string) string {
		return fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d,"instruction":%q,"inputVersionIds":[%q]}`, id, check.Revision, revisedPrompt, versionID)
	}
	request(http.MethodPatch, taskPath, patch("edit-notes", notes.VersionID), http.StatusUnprocessableEntity, nil)
	var edited checkTask
	request(http.MethodPatch, taskPath, patch("edit", updated.VersionID), http.StatusOK, &edited)
	assertInput(edited, updated, "PROPOSED", revisedPrompt)
	confirm("approve", edited.Revision, http.StatusAccepted, nil)
	for {
		current := list(turn.TurnID)
		if len(current) != 1 || current[0].TaskID != check.TaskID || ctx.Err() != nil {
			t.Fatalf("check did not finish on its original card: %+v", current)
		}
		if current[0].Status == taskSucceeded {
			break
		}
		if current[0].Status != taskQueued && current[0].Status != taskRunning {
			t.Fatalf("check failed: %+v", current[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	var replayed checkTask
	confirm("approve", edited.Revision, http.StatusOK, &replayed)
	assertInput(replayed, updated, "SUCCEEDED", revisedPrompt)
	if calls.Load() != 2 {
		t.Fatalf("expected only the two conversation model calls, got %d", calls.Load())
	}

	artifact := request(http.MethodGet, taskPath+"/artifact", "", http.StatusOK, nil)
	mediaType, params, err := mime.ParseMediaType(artifact.Header().Get("Content-Disposition"))
	report := artifact.Body.String()
	if err != nil || mediaType != "attachment" || params["filename"] != "manuscript-check.md" ||
		artifact.Header().Get("X-Source-SHA256") != updated.SHA256 || !strings.Contains(report, updated.SHA256) ||
		!strings.Contains(report, "第 3 行") || !strings.Contains(report, "第 6 行") || strings.Contains(report, original.SHA256) {
		t.Fatalf("wrong manuscript check artifact: headers=%v, body=%q", artifact.Header(), report)
	}
	var saved, current WorkspaceDocument
	request(http.MethodGet, "/files/versions/"+original.VersionID, "", http.StatusOK, &saved)
	request(http.MethodGet, "/files/text?path="+url.QueryEscape(manuscriptPath), "", http.StatusOK, &current)
	after := request(http.MethodGet, "/workflow", "", http.StatusOK, nil).Body.String()
	if saved.VersionID != original.VersionID || saved.SHA256 != original.SHA256 || saved.Content != original.Content ||
		current.VersionID != updated.VersionID || current.SHA256 != updated.SHA256 || current.Content != updated.Content || after != before {
		t.Fatal("manuscript check changed a saved version or the workflow")
	}
}
