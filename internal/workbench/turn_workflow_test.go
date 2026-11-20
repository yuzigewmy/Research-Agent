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

func TestConversationWorkflowProposalConfirmsRangeAndRejectsStalePlan(t *testing.T) {
	const instruction = "整理河流采样方案及验收要求。"
	var chats, executions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function struct{ Name string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "invalid prompt", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(input.Tools) == 0 {
			if !strings.Contains(input.Messages[len(input.Messages)-1].Content, instruction) {
				t.Error("workflow execution did not receive the confirmed instruction")
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Saved stage %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", executions.Add(1))
			return
		}
		if len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" {
			t.Error("confirmation mode did not offer the proposal tool")
		}
		throughStep := "checklist"
		if chats.Add(1) == 1 {
			throughStep = "plan"
		}
		args := fmt.Sprintf(`{"instruction":%q,"throughStep":%q}`, instruction, throughStep)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请核对执行范围。\",\"tool_calls\":[{\"index\":0,\"id\":\"call-range\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "River sampling", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + project.ProjectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, want int, dst any) string {
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
		if dst != nil {
			if err := json.Unmarshal(res.Body.Bytes(), dst); err != nil {
				t.Fatalf("decode response %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	type proposal struct {
		Action, Status, Instruction, RunID string
		Scope                              WorkflowScope
		Checkpoints                        bool
		Steps                              []struct{ Key, Title string }
	}
	propose := func(id, action string, scope WorkflowScope, keys ...string) string {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"text":"建议接下来要执行的实验步骤。","mode":"CONFIRM"}`, id)
		var turn struct {
			TurnID, Status, ProposedTaskID string
			WorkflowProposal               *proposal
		}
		request(http.MethodPost, "/turns", body, http.StatusAccepted, &turn)
		path := "/turns/" + turn.TurnID
		stream := request(http.MethodGet, path+"/stream", "", http.StatusOK, nil)
		request(http.MethodPost, "/turns", body, http.StatusOK, &turn)
		p := turn.WorkflowProposal
		if turn.Status != "COMPLETED" || turn.ProposedTaskID != "" || p == nil || p.Status != "PROPOSED" ||
			p.Action != action || p.Scope != scope || p.Instruction != instruction || !p.Checkpoints || len(p.Steps) != len(keys) {
			t.Fatalf("wrong proposal in completed turn: %+v, proposal=%+v, stream=%s", turn, p, stream)
		}
		for i, key := range keys {
			if p.Steps[i].Key != key || p.Steps[i].Title == "" {
				t.Fatalf("wrong steps in workflow proposal: %+v", p.Steps)
			}
		}
		terminal := 0
		for line := range strings.SplitSeq(stream, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var event struct {
				Kind, Status, ProposedTaskID string
				WorkflowProposal             *proposal
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("invalid stream event: %v", err)
			}
			if event.Kind == "done" {
				terminal++
				if event.Status != "COMPLETED" || event.ProposedTaskID != "" || !reflect.DeepEqual(event.WorkflowProposal, p) {
					t.Fatalf("terminal event differs from saved proposal: %s", data)
				}
			}
		}
		if terminal != 1 {
			t.Fatalf("expected one completion event, got %d", terminal)
		}
		return path + "/workflow-proposal/confirm"
	}
	readWorkflow := func() *WorkflowRun {
		t.Helper()
		var snapshot struct{ Run *WorkflowRun }
		request(http.MethodGet, "/workflow", "", http.StatusOK, &snapshot)
		return snapshot.Run
	}
	waitFor := func(status string) *WorkflowRun {
		t.Helper()
		for {
			run := readWorkflow()
			if run != nil && run.Status == status {
				return run
			}
			if run == nil || (run.Status != taskQueued && run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	confirm := func(path string, scope WorkflowScope) proposal {
		t.Helper()
		var confirmed proposal
		request(http.MethodPost, path, "", http.StatusOK, &confirmed)
		if confirmed.Status != "CONFIRMED" || confirmed.RunID == "" || confirmed.Scope != scope {
			t.Fatalf("wrong confirmation: %+v", confirmed)
		}
		return confirmed
	}

	firstScope := WorkflowScope{StartStep: "scope", EndStep: "plan"}
	first := propose("first", "start", firstScope, "scope", "plan")
	var tasks taskPage
	request(http.MethodGet, "/tasks", "", http.StatusOK, &tasks)
	if run := readWorkflow(); run != nil || len(tasks.Items) != 0 || executions.Load() != 0 {
		t.Fatalf("unconfirmed proposal started work: run=%+v, tasks=%+v, executions=%d", run, tasks.Items, executions.Load())
	}
	confirmed := confirm(first, firstScope)
	partial := waitFor("RANGE_COMPLETED")
	if partial.RunID != confirmed.RunID || partial.Scope != firstScope || !partial.Checkpoints || len(partial.Steps) != 3 ||
		partial.Steps[0].Status != taskSucceeded || partial.Steps[1].Status != taskSucceeded || partial.Steps[2].Status != "PENDING" || executions.Load() != 2 {
		t.Fatalf("confirmation did not stop at the proposed range: %+v, executions=%d", partial, executions.Load())
	}
	if replayed := confirm(first, firstScope); !reflect.DeepEqual(replayed, confirmed) {
		t.Fatalf("confirmation replay changed the proposal: %+v", replayed)
	}

	lastScope := WorkflowScope{StartStep: "checklist", EndStep: "checklist"}
	second := propose("second", "continue", lastScope, "checklist")
	stale := propose("stale", "continue", lastScope, "checklist")
	if run := readWorkflow(); run == nil || run.RunID != partial.RunID || run.Revision != partial.Revision || executions.Load() != 2 {
		t.Fatalf("proposing a continuation changed the run: %+v", run)
	}
	continued := confirm(second, lastScope)
	finished := waitFor("COMPLETED")
	if continued.RunID != confirmed.RunID || finished.RunID != confirmed.RunID || finished.Scope != lastScope || executions.Load() != 3 {
		t.Fatalf("continuation did not finish the original run: %+v, executions=%d", finished, executions.Load())
	}
	var conflict struct{ Error apiError }
	request(http.MethodPost, stale, "", http.StatusConflict, &conflict)
	if conflict.Error.Code != "WORKFLOW_PROPOSAL_STALE" {
		t.Fatalf("unexpected stale proposal error: %+v", conflict.Error)
	}
	var cancelled, replayed proposal
	request(http.MethodPost, strings.TrimSuffix(stale, "/confirm")+"/cancel", "", http.StatusOK, &cancelled)
	request(http.MethodPost, stale, "", http.StatusOK, &replayed)
	if cancelled.Status != "CANCELLED" || !reflect.DeepEqual(replayed, cancelled) {
		t.Fatalf("cancelled proposal did not retain its terminal state: cancelled=%+v replayed=%+v", cancelled, replayed)
	}
	if run := readWorkflow(); run == nil || run.RunID != finished.RunID || run.Revision != finished.Revision || chats.Load() != 3 || executions.Load() != 3 {
		t.Fatalf("replayed or stale confirmation dispatched extra work: run=%+v, chats=%d, executions=%d", run, chats.Load(), executions.Load())
	}
}
