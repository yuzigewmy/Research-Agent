package workbench

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestConversationBatchEditAppliesEachManuscriptIndependently(t *testing.T) {
	const instruction = "统一采样记录中的术语。"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "river", Title: "河流观测", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	save := func(path, base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, path, base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	type manuscript struct {
		path, before, selected, after, replacement, reason string
		source                                             WorkspaceDocument
	}
	docs := []manuscript{
		{path: "paper/scope.md", before: "# 范围\r\n", selected: "观测河岸🌿。", after: "\r\n记录天气。", replacement: "在河岸观测站采样。", reason: "说明观测位置。"},
		{path: "paper/method.markdown", before: "# 方法\n", selected: "每月取样。", after: "\n旁注：每月取样。", replacement: "每月固定日期采样。", reason: "统一采样术语。"},
		{path: "paper/records.md", before: "# 记录\n", selected: "记录水温。", after: "\n保存原始读数。", replacement: "记录水温。", reason: "术语已统一。"},
	}
	var selections []manuscriptSelectionInput
	var wantInputs []taskInput
	for i := range docs {
		doc := &docs[i]
		doc.source = save(doc.path, "", doc.before+doc.selected+doc.after)
		selections = append(selections, manuscriptSelectionInput{VersionID: doc.source.VersionID, StartByte: len(doc.before), EndByte: len(doc.before) + len(doc.selected)})
		wantInputs = append(wantInputs, taskInput{VersionID: doc.source.VersionID, Path: doc.path, SHA256: doc.source.SHA256})
	}
	// Keep a completed range to detect unintended changes to the workflow.
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

	var proposals, executions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages        []inference.Message
			Model           string
			ReasoningEffort string `json:"reasoning_effort"`
			Tools           []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(request.Tools) > 0 {
			proposals.Add(1)
			if len(request.Tools) != 1 || request.Tools[0].Function.Name != "propose_research_task" ||
				!strings.Contains(string(request.Tools[0].Function.Parameters), `"editSelection"`) {
				t.Error("the proposal tool did not offer selection editing")
			}
			args := fmt.Sprintf(`{"instruction":%q,"editSelection":true}`, instruction)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请核对修订建议。\",\"tool_calls\":[{\"index\":0,\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
			return
		}

		executions.Add(1)
		if request.Model != "fixture-edit" || request.ReasoningEffort != "high" {
			t.Errorf("confirmed edit used model %q and reasoning effort %q", request.Model, request.ReasoningEffort)
		}
		var input struct {
			Instruction string
			Edits       []struct{ VersionID, Path, Before, Selected, After string }
		}
		prompt := request.Messages[len(request.Messages)-1].Content
		if err := json.Unmarshal([]byte(prompt), &input); err != nil || input.Instruction != instruction || len(input.Edits) != len(docs) {
			t.Errorf("wrong batch edit prompt: %+v, %v", input, err)
			http.Error(w, "unexpected batch edit prompt", http.StatusBadRequest)
			return
		}
		var suggestions []string
		// Reverse the response order to check that suggestions are matched by version ID.
		for i := len(docs) - 1; i >= 0; i-- {
			doc := docs[i]
			index := slices.IndexFunc(input.Edits, func(edit struct{ VersionID, Path, Before, Selected, After string }) bool {
				return edit.VersionID == doc.source.VersionID && edit.Path == doc.path && edit.Selected == doc.selected &&
					strings.HasSuffix(edit.Before, doc.before) && strings.HasPrefix(edit.After, doc.after)
			})
			if index < 0 {
				t.Errorf("missing pinned selection in model input for %s", doc.path)
			}
			suggestions = append(suggestions, fmt.Sprintf(`{"versionId":%q,"replacement":%q,"reason":%q}`, doc.source.VersionID, doc.replacement, doc.reason))
		}
		output := `{"edits":[` + strings.Join(suggestions, ",") + `]}`
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, status int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s returned HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	assertCurrent := func(want WorkspaceDocument) {
		t.Helper()
		var got WorkspaceDocument
		request(http.MethodGet, "/files/text?path="+url.QueryEscape(want.Path), "", http.StatusOK, &got)
		if got.Path != want.Path || got.VersionID != want.VersionID || got.SHA256 != want.SHA256 || got.Content != want.Content {
			t.Fatalf("wrong current manuscript %s at version %s, want %s", got.Path, got.VersionID, want.VersionID)
		}
	}
	applyConflict := func(path, code string) {
		t.Helper()
		var failure struct{ Error apiError }
		request(http.MethodPost, path+"/apply", "", http.StatusConflict, &failure)
		if failure.Error.Code != code {
			t.Fatalf("apply %s returned %q, want %q", path, failure.Error.Code, code)
		}
	}

	workflow := request(http.MethodGet, "/workflow", "", http.StatusOK, nil)
	body, err := json.Marshal(sendTurnInput{RequestID: "batch-edit", Text: "请修改引用的选区。", Mode: "CONFIRM", ManuscriptSelections: selections})
	if err != nil {
		t.Fatal(err)
	}
	var turn, replay Turn
	request(http.MethodPost, "/turns", string(body), http.StatusAccepted, &turn)
	stream := request(http.MethodGet, "/turns/"+turn.TurnID+"/stream", "", http.StatusOK, nil)
	request(http.MethodPost, "/turns", string(body), http.StatusOK, &replay)
	if replay.TurnID != turn.TurnID || replay.Status != "COMPLETED" || replay.ProposedTaskID == "" ||
		!strings.Contains(stream, `"proposedTaskId":"`+replay.ProposedTaskID+`"`) {
		t.Fatalf("the edited selections did not produce one task receipt: %s", stream)
	}
	readTask := func() Task {
		t.Helper()
		var page taskPage
		request(http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &page)
		if len(page.Items) != 1 || page.Items[0].TaskID != replay.ProposedTaskID || page.Items[0].SourceTurnID != turn.TurnID ||
			page.Items[0].Kind != "selection-edit" || page.Items[0].Instruction != instruction || !slices.Equal(page.Items[0].Inputs, wantInputs) {
			t.Fatalf("wrong batch edit task: %+v", page.Items)
		}
		return page.Items[0]
	}
	task := readTask()
	if task.Status != taskProposed || proposals.Load() != 1 || executions.Load() != 0 {
		t.Fatalf("batch edit must wait for confirmation: %+v", task)
	}
	editPath := func(doc manuscript) string {
		return "/files/versions/" + doc.source.VersionID + "/edits/" + task.TaskID
	}
	applyConflict(editPath(docs[0]), "EDIT_STATE_CONFLICT")
	for _, doc := range docs {
		assertCurrent(doc.source)
	}
	confirm := fmt.Sprintf(`{"requestId":"approve-edits","expectedRevision":%d}`, task.Revision)
	confirmPath := "/tasks/" + task.TaskID + "/confirm"
	handler = NewHandler(store, nil, HandlerConfig{Model: model, EditModelName: "fixture-edit", EditReasoningEffort: "high"})
	request(http.MethodPost, confirmPath, confirm, http.StatusAccepted, nil)
	var finished Task
	for {
		finished = readTask()
		if finished.Status == taskSucceeded {
			break
		}
		if (finished.Status != taskQueued && finished.Status != taskRunning) || ctx.Err() != nil {
			t.Fatalf("confirmed batch edit did not succeed: %+v", finished)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, doc := range docs {
		var edit struct {
			selectionEdit
			Unchanged bool `json:"unchanged"`
		}
		request(http.MethodGet, editPath(doc), "", http.StatusOK, &edit)
		if edit.EditID != task.TaskID || edit.VersionID != doc.source.VersionID || edit.Status != taskSucceeded || edit.Original != doc.selected ||
			edit.EditSuggestion == nil || edit.Replacement != doc.replacement || edit.Reason != doc.reason ||
			edit.AppliedVersionID != "" || edit.Unchanged != (doc.selected == doc.replacement) {
			t.Fatalf("wrong edit suggestion for %s: %+v", doc.path, edit)
		}
		assertCurrent(doc.source)
	}

	conflicted := docs[0]
	newer := save(conflicted.path, conflicted.source.SHA256, conflicted.source.Content+"\n其他修改已保存。")
	applyConflict(editPath(conflicted), "REVISION_CONFLICT")
	applyConflict(editPath(docs[2]), "EDIT_NO_CHANGE")
	target := docs[1]
	var applied, appliedAgain WorkspaceDocument
	request(http.MethodPost, editPath(target)+"/apply", "", http.StatusOK, &applied)
	request(http.MethodPost, editPath(target)+"/apply", "", http.StatusOK, &appliedAgain)
	if applied.VersionID == "" || applied.VersionID == target.source.VersionID || applied.Path != target.path ||
		applied.Content != target.before+target.replacement+target.after || appliedAgain != applied {
		t.Fatalf("application did not replace the fixed range once: version=%s, replay=%s", applied.VersionID, appliedAgain.VersionID)
	}
	assertCurrent(applied)
	assertCurrent(newer)
	assertCurrent(docs[2].source)
	var confirmedAgain Task
	request(http.MethodPost, confirmPath, confirm, http.StatusOK, &confirmedAgain)
	if confirmedAgain.TaskID != task.TaskID || confirmedAgain.Status != taskSucceeded || confirmedAgain.Revision != finished.Revision || proposals.Load() != 1 || executions.Load() != 1 {
		t.Fatalf("request replay changed task execution: %+v, proposals=%d, executions=%d", confirmedAgain, proposals.Load(), executions.Load())
	}
	if current := request(http.MethodGet, "/workflow", "", http.StatusOK, nil); current != workflow {
		t.Fatal("batch edit changed the existing workflow")
	}
}
