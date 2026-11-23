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

func TestConversationSelectionEditRequiresConfirmationAndAppliesOnce(t *testing.T) {
	const (
		path        = "paper/main.md"
		selected    = "在河边读数🌊。"
		replacement = "在固定站点记录水位。"
		instruction = "明确选文的观测位置。"
		reason      = "明确观测位置和记录内容。"
		distant     = "不应发送的远处段落"
		nearBefore  = "先记录测量单位。\r\n"
		nearAfter   = "\r\n后续核对采样时间。\r\n"
	)
	prefix := "# 河流观测\r\n旁注保留同一句：" + selected + "\r\n" + distant + strings.Repeat("。", 200) + "\r\n"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "river", Title: "河流观测", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	save := func(base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, path, base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	original := save("", prefix+selected+nearAfter)

	// Set up a finished range so accidental workflow progression is observable.
	if _, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, workflowRequest{RequestID: "workflow", ThroughStep: "scope"}, true); err != nil {
		t.Fatal(err)
	}
	step, err := store.claimQueuedTask(ctx, project.ProjectID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, project.ProjectID, step, taskSucceeded, "", taskResult{markdown: "# Sampling scope"}); err != nil {
		t.Fatal(err)
	}

	var chats, edits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		if len(input.Tools) == 0 {
			edits.Add(1)
			var edit struct{ Instruction, Selected, Before, After string }
			if err := json.Unmarshal([]byte(prompt), &edit); err != nil || edit.Instruction != instruction || edit.Selected != selected ||
				!strings.HasSuffix(edit.Before, nearBefore) || !strings.HasPrefix(edit.After, nearAfter) || strings.Contains(prompt, distant) {
				t.Errorf("edit did not use the fixed selection and bounded context: %+v, %v", edit, err)
			}
			output := fmt.Sprintf(`{"replacement":%q,"reason":%q}`, replacement, reason)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
			return
		}
		call := chats.Add(1)
		var schema struct{ Properties map[string]json.RawMessage }
		tool := input.Tools[0].Function
		if len(input.Tools) != 1 || tool.Name != "propose_research_task" || json.Unmarshal(tool.Parameters, &schema) != nil {
			t.Error("confirmation mode did not offer the research proposal tool")
		}
		if hasSelection := call != 2; strings.Contains(prompt, selected) != hasSelection || (hasSelection && schema.Properties["editSelection"] == nil) {
			t.Errorf("wrong selection or edit option for conversation call %d", call)
		}
		extra := ""
		switch call {
		case 3:
			extra = `,"throughStep":"plan"`
		case 4:
			extra = fmt.Sprintf(`,"checkVersionId":%q`, original.VersionID)
		}
		args := fmt.Sprintf(`{"instruction":%q,"editSelection":true%s}`, instruction, extra)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请核对修改建议。\",\"tool_calls\":[{\"index\":0,\"id\":\"call-edit\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID
	request := func(method, path, body string, want int, output any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatalf("decode response %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	propose := func(id string, doc *WorkspaceDocument, wantCard bool) Task {
		t.Helper()
		input := sendTurnInput{RequestID: id, Text: "请修改选区。", Mode: "CONFIRM"}
		if doc != nil {
			start := strings.LastIndex(doc.Content, selected)
			input.ManuscriptSelection = &manuscriptSelectionInput{VersionID: doc.VersionID, StartByte: start, EndByte: start + len(selected)}
		}
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var turn Turn
		request(http.MethodPost, "/turns", string(body), http.StatusAccepted, &turn)
		stream := request(http.MethodGet, "/turns/"+turn.TurnID+"/stream", "", http.StatusOK, nil)
		var done TurnEvent
		for line := range strings.SplitSeq(stream, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var event TurnEvent
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					t.Fatalf("decode SSE event: %v", err)
				}
				if event.Kind == "done" {
					done = event
				}
			}
		}
		var page taskPage
		request(http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &page)
		if !wantCard {
			if done.Kind != "done" || done.ProposedTaskID != "" || done.WorkflowProposal != nil || len(page.Items) != 0 {
				t.Fatalf("invalid edit proposal %s produced a card: %s", id, stream)
			}
			return Task{}
		}
		if done.Status != "COMPLETED" || done.ProposedTaskID == "" || len(page.Items) != 1 {
			t.Fatalf("expected a completed turn with one edit proposal: %s", stream)
		}
		task := page.Items[0]
		wantInput := taskInput{VersionID: doc.VersionID, Path: doc.Path, SHA256: doc.SHA256}
		if task.TaskID != done.ProposedTaskID || task.SourceTurnID != turn.TurnID || task.Kind != "selection-edit" ||
			task.Status != taskProposed || task.Instruction != instruction || len(task.Inputs) != 1 || task.Inputs[0] != wantInput {
			t.Fatalf("wrong proposed selection edit: %+v", task)
		}
		return task
	}
	confirm := func(task Task, status int, out any) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, "confirm-"+task.TaskID, task.Revision)
		request(http.MethodPost, "/tasks/"+task.TaskID+"/confirm", body, status, out)
	}
	assertCurrent := func(want WorkspaceDocument) {
		t.Helper()
		var current WorkspaceDocument
		request(http.MethodGet, "/files/text?path="+url.QueryEscape(path), "", http.StatusOK, &current)
		if current.Path != want.Path || current.VersionID != want.VersionID || current.SHA256 != want.SHA256 || current.Content != want.Content {
			t.Fatalf("wrong current manuscript at %s: version=%s SHA-256=%s, want version=%s SHA-256=%s", current.Path, current.VersionID, current.SHA256, want.VersionID, want.SHA256)
		}
	}

	workflow := request(http.MethodGet, "/workflow", "", http.StatusOK, nil)
	stale := propose("stale", &original, true)
	propose("no-selection", nil, false)
	propose("with-workflow", &original, false)
	propose("with-check", &original, false)
	updated := save(original.SHA256, prefix+nearBefore+selected+nearAfter)
	var problem struct{ Error apiError }
	confirm(stale, http.StatusConflict, &problem)
	if problem.Error.Code != "TASK_INPUT_CHANGED" {
		t.Fatalf("wrong outdated selection error: %+v", problem.Error)
	}

	task := propose("current", &updated, true)
	editPath := "/files/versions/" + updated.VersionID + "/edits/" + task.TaskID
	var proposed selectionEdit
	request(http.MethodGet, editPath, "", http.StatusOK, &proposed)
	if proposed.EditID != task.TaskID || proposed.VersionID != updated.VersionID || proposed.Status != taskProposed || proposed.Original != selected {
		t.Fatalf("missing saved selection in proposed edit: %+v", proposed)
	}
	request(http.MethodPost, editPath+"/apply", "", http.StatusConflict, &problem)
	if problem.Error.Code != "EDIT_STATE_CONFLICT" || edits.Load() != 0 {
		t.Fatalf("edit ran or applied before confirmation: %+v, edit calls=%d", problem.Error, edits.Load())
	}
	assertCurrent(updated)
	confirm(task, http.StatusAccepted, nil)
	for {
		var edit selectionEdit
		request(http.MethodGet, editPath, "", http.StatusOK, &edit)
		if edit.Status == taskSucceeded {
			if edit.EditSuggestion == nil || edit.Replacement != replacement || edit.Reason != reason || edit.AppliedVersionID != "" {
				t.Fatalf("unexpected generated edit: %+v", edit)
			}
			assertCurrent(updated)
			break
		}
		if (edit.Status != taskQueued && edit.Status != taskRunning) || ctx.Err() != nil {
			t.Fatalf("confirmed edit did not succeed: %+v", edit)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var applied, replayed WorkspaceDocument
	request(http.MethodPost, editPath+"/apply", "", http.StatusOK, &applied)
	request(http.MethodPost, editPath+"/apply", "", http.StatusOK, &replayed)
	if applied.VersionID == "" || applied.VersionID == updated.VersionID || applied.Content != prefix+nearBefore+replacement+nearAfter || replayed != applied {
		t.Fatalf("apply did not save exactly the fixed range once: version=%s, replay=%s", applied.VersionID, replayed.VersionID)
	}
	assertCurrent(applied)
	if got := request(http.MethodGet, "/workflow", "", http.StatusOK, nil); got != workflow || chats.Load() != 5 || edits.Load() != 1 {
		t.Fatalf("selection edit changed the workflow or dispatched extra requests, chats=%d, edits=%d", chats.Load(), edits.Load())
	}
}
