package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestTurnPolicyRunsChecksButRequiresWorkflowConfirmation(t *testing.T) {
	const (
		chatText     = "请说明这项稿件检查的用途。"
		chatReply    = "可以核对稿件结构和待补标记。"
		manualText   = "请提议检查已保存的稿件。"
		checkText    = "请检查已保存的稿件。"
		workflowText = "请运行实验工作流到计划步骤。"
		checkRequest = `{"requestId":"check","text":"` + checkText + `"}`
	)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "river", Title: "河流观测", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	manuscript, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/main.md", "", "# 河流观测\n\n## 摘要\nTODO: 补充采样日期。\n")
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("missing conversation prompt")
			http.Error(w, "missing conversation prompt", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		if prompt == chatText {
			if len(input.Tools) != 0 {
				t.Error("CHAT request included tools")
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chatReply)
			return
		}
		if len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" {
			t.Errorf("unexpected model request for %q", prompt)
			http.Error(w, "expected proposal tool", http.StatusBadRequest)
			return
		}
		var arguments string
		switch prompt {
		case manualText, checkText:
			arguments = fmt.Sprintf(`{"instruction":"核对稿件结构与待补标记。","checkVersionId":%q}`, manuscript.VersionID)
		case workflowText:
			arguments = `{"instruction":"先界定范围，再编制计划。","throughStep":"plan"}`
		default:
			t.Errorf("unexpected conversation input %q", prompt)
			http.Error(w, "unexpected conversation input", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已整理本轮请求。\",\"tool_calls\":[{\"index\":0,\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", arguments)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID
	request := func(method, path, body string, status int, output any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	send := func(body, mode string) Turn {
		t.Helper()
		var started, finished Turn
		request(http.MethodPost, "/turns", body, http.StatusAccepted, &started)
		stream := request(http.MethodGet, "/turns/"+started.TurnID+"/stream", "", http.StatusOK, nil).Body.String()
		var history struct{ Items []Turn }
		request(http.MethodGet, "/turns", "", http.StatusOK, &history)
		for _, turn := range history.Items {
			if turn.TurnID == started.TurnID {
				finished = turn
				break
			}
		}
		if finished.TurnID != started.TurnID || finished.Mode != mode || finished.Status != "COMPLETED" {
			t.Fatalf("wrong saved turn for %s: %+v, stream=%s", mode, finished, stream)
		}
		done := 0
		for line := range strings.SplitSeq(stream, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var event TurnEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("decode SSE: %v", err)
			}
			if event.Kind == "done" {
				done++
				if event.Status != finished.Status || event.ProposedTaskID != finished.ProposedTaskID ||
					!reflect.DeepEqual(event.WorkflowProposal, finished.WorkflowProposal) {
					t.Fatalf("terminal event differs from the saved turn: %s", data)
				}
			}
		}
		if done != 1 {
			t.Fatalf("expected one terminal event, got %d: %s", done, stream)
		}
		return finished
	}
	checkTask := func(turn Turn) Task {
		t.Helper()
		var page taskPage
		request(http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &page)
		wantInput := taskInput{VersionID: manuscript.VersionID, Path: manuscript.Path, SHA256: manuscript.SHA256}
		if turn.ProposedTaskID == "" || len(page.Items) != 1 {
			t.Fatalf("expected one check task from turn %s: %+v", turn.TurnID, page.Items)
		}
		task := page.Items[0]
		if task.TaskID != turn.ProposedTaskID || task.SourceTurnID != turn.TurnID || task.Kind != "manuscript-check" ||
			len(task.Inputs) != 1 || task.Inputs[0] != wantInput {
			t.Fatalf("check task lost its turn or fixed manuscript version: %+v", task)
		}
		return task
	}

	chat := send(`{"requestId":"chat","mode":"CHAT","text":"`+chatText+`"}`, "CHAT")
	if chat.Answer != chatReply || chat.ProposedTaskID != "" || chat.WorkflowProposal != nil {
		t.Fatalf("unexpected CHAT response: %+v", chat)
	}
	manual := send(`{"requestId":"manual","mode":"CONFIRM","text":"`+manualText+`"}`, "CONFIRM")
	if task := checkTask(manual); task.Status != taskProposed {
		t.Fatalf("CONFIRM dispatched an unconfirmed task: %+v", task)
	}

	var preferences Project
	patch := fmt.Sprintf(`{"requestId":"mode","expectedRevision":%d,"defaultTurnMode":"CONFIRM_STEPS"}`, project.Revision)
	request(http.MethodPatch, "", patch, http.StatusOK, &preferences)
	if preferences.DefaultTurnMode != "CONFIRM_STEPS" {
		t.Fatalf("wrong saved policy: %q", preferences.DefaultTurnMode)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{Model: model})
	check := send(checkRequest, "CONFIRM_STEPS")
	var completed Task
	for {
		completed = checkTask(check)
		if completed.Status == taskSucceeded {
			break
		}
		if (completed.Status != taskQueued && completed.Status != taskRunning) || ctx.Err() != nil {
			t.Fatalf("CONFIRM_STEPS did not complete its check: %+v", completed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	report := request(http.MethodGet, "/tasks/"+completed.TaskID+"/artifact", "", http.StatusOK, nil)
	if report.Header().Get("X-Source-SHA256") != manuscript.SHA256 || !strings.Contains(report.Body.String(), "第 4 行") {
		t.Fatalf("wrong automatically generated check report: %s", report.Body)
	}
	var replay Turn
	request(http.MethodPost, "/turns", checkRequest, http.StatusOK, &replay)
	if replay.TurnID != check.TurnID || replay.ProposedTaskID != check.ProposedTaskID {
		t.Fatalf("replay changed the turn or task ID: %+v", replay)
	}
	if task := checkTask(replay); task.Status != taskSucceeded || task.Revision != completed.Revision {
		t.Fatalf("replay changed the completed check: %+v", task)
	}

	workflow := send(`{"requestId":"workflow","text":"`+workflowText+`"}`, "CONFIRM_STEPS")
	proposal := workflow.WorkflowProposal
	if proposal == nil || proposal.Status != "PROPOSED" || proposal.RunID != "" || workflow.ProposedTaskID != "" ||
		proposal.Scope != (WorkflowScope{StartStep: "scope", EndStep: "plan"}) {
		t.Fatalf("CONFIRM_STEPS did not retain the workflow confirmation: %+v", proposal)
	}
	var snapshot struct{ Run *WorkflowRun }
	request(http.MethodGet, "/workflow", "", http.StatusOK, &snapshot)
	if snapshot.Run != nil || checkTask(manual).Status != taskProposed || calls.Load() != 4 {
		t.Fatalf("unexpected dispatch, workflow=%+v, model calls=%d", snapshot.Run, calls.Load())
	}
}
