package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
	"research-agent/internal/literature"
)

func TestAutoTurnStartsWorkflowAndHonorsCheckpoint(t *testing.T) {
	const (
		experimentText = "请运行实验工作流直到生成验收清单。"
		reviewText     = "请启动综述工作流，先固定本轮文献来源。"
		checklist      = "# 验收清单\n\n核对原始记录和采样时间。"
	)
	var dialogues, executions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(input.Tools) == 0 {
			artifacts := []string{"# 观测范围", "# 采样计划", checklist}
			call := int(executions.Add(1))
			if call > len(artifacts) {
				t.Errorf("unexpected workflow execution %d", call)
				http.Error(w, "unexpected workflow execution", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", artifacts[call-1])
			return
		}
		dialogues.Add(1)
		prompt := input.Messages[len(input.Messages)-1].Content
		if len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" || (prompt != experimentText && prompt != reviewText) {
			t.Errorf("unexpected dialogue request: %q", prompt)
			http.Error(w, "unexpected dialogue request", http.StatusBadRequest)
			return
		}
		end := "checklist"
		if prompt == reviewText {
			end = "sources"
		}
		var parameters struct {
			Properties map[string]struct{ Enum []string }
		}
		if err := json.Unmarshal(input.Tools[0].Function.Parameters, &parameters); err != nil || !slices.Contains(parameters.Properties["throughStep"].Enum, end) {
			t.Errorf("proposal tool did not allow the requested endpoint %q", end)
		}
		arguments := fmt.Sprintf(`{"instruction":%q,"throughStep":%q}`, prompt, end)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已整理运行范围。\",\"tool_calls\":[{\"index\":0,\"id\":\"call-workflow\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", arguments)
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
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	createProject := func(template string) Project {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: template, Title: "河流观测", TemplateID: template})
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	request := func(method, path, body string, status int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	type proposal struct {
		Status, Action, RunID, Message string
		Automatic, Checkpoints         bool
		Scope                          WorkflowScope
	}
	type turnReceipt struct {
		TurnID, Mode, Status, ProposedTaskID string
		WorkflowProposal                     *proposal
	}
	send := func(path, body string) turnReceipt {
		t.Helper()
		var started turnReceipt
		request(http.MethodPost, path+"/turns", body, http.StatusAccepted, &started)
		stream := request(http.MethodGet, path+"/turns/"+started.TurnID+"/stream", "", http.StatusOK, nil)
		var history struct{ Items []turnReceipt }
		request(http.MethodGet, path+"/turns", "", http.StatusOK, &history)
		if len(history.Items) != 1 || history.Items[0].TurnID != started.TurnID {
			t.Fatalf("wrong turn history: %+v", history.Items)
		}
		saved := history.Items[0]
		if saved.Mode != "AUTO" || saved.Status != "COMPLETED" || saved.WorkflowProposal == nil || saved.ProposedTaskID != "" {
			t.Fatalf("missing saved AUTO workflow proposal: %+v, stream=%s", saved, stream)
		}
		done := 0
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
				t.Fatalf("decode SSE event: %v", err)
			}
			if event.Kind == "done" {
				done++
				if event.Status != saved.Status || event.ProposedTaskID != "" || event.WorkflowProposal == nil || *event.WorkflowProposal != *saved.WorkflowProposal {
					t.Fatalf("terminal event differs from history: %s", data)
				}
			}
		}
		if done != 1 {
			t.Fatalf("wanted one terminal event, got %d: %s", done, stream)
		}
		return saved
	}
	readRun := func(path string) *WorkflowRun {
		t.Helper()
		var snapshot struct{ Run *WorkflowRun }
		request(http.MethodGet, path+"/workflow", "", http.StatusOK, &snapshot)
		return snapshot.Run
	}
	waitFor := func(path, status string) *WorkflowRun {
		t.Helper()
		for {
			run := readRun(path)
			if run != nil && run.Status == status {
				return run
			}
			if run == nil || (run.Status != taskQueued && run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	experiment := projectsPath + "/" + createProject("experiment").ProjectID
	const start = `{"requestId":"auto-run","mode":"AUTO","text":"` + experimentText + `"}`
	turn := send(experiment, start)
	dispatched := turn.WorkflowProposal
	wantScope := WorkflowScope{StartStep: "scope", EndStep: "checklist"}
	if dispatched.Status != "CONFIRMED" || !dispatched.Automatic || dispatched.RunID == "" || !dispatched.Checkpoints || dispatched.Scope != wantScope {
		t.Fatalf("wrong automatic dispatch receipt: %+v", dispatched)
	}
	checkpoint := waitFor(experiment, "WAITING_CHECKPOINT")
	if checkpoint.RunID != dispatched.RunID || !checkpoint.Checkpoints || checkpoint.Scope != wantScope || len(checkpoint.Steps) != 3 ||
		checkpoint.Steps[0].Key != "scope" || checkpoint.Steps[0].Status != taskSucceeded ||
		checkpoint.Steps[1].Key != "plan" || checkpoint.Steps[1].Status != taskSucceeded ||
		checkpoint.Steps[2].Key != "checklist" || checkpoint.Steps[2].Status != "PENDING" || executions.Load() != 2 {
		t.Fatalf("automatic execution did not stop at the plan checkpoint: %+v, executions=%d", checkpoint, executions.Load())
	}
	var replay turnReceipt
	request(http.MethodPost, experiment+"/turns", start, http.StatusOK, &replay)
	if replay.TurnID != turn.TurnID || replay.WorkflowProposal == nil || *replay.WorkflowProposal != *dispatched {
		t.Fatalf("replay returned a different workflow receipt: %+v, proposal=%+v", replay, replay.WorkflowProposal)
	}
	if run := readRun(experiment); run == nil || run.RunID != checkpoint.RunID || run.Status != checkpoint.Status ||
		run.Revision != checkpoint.Revision || executions.Load() != 2 || dialogues.Load() != 1 {
		t.Fatalf("replay changed the wait state: run=%+v, executions=%d, dialogues=%d", run, executions.Load(), dialogues.Load())
	}
	resume := fmt.Sprintf(`{"requestId":"approve-plan","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, experiment+"/workflow/continue", resume, http.StatusAccepted, nil)
	finished := waitFor(experiment, "COMPLETED")
	if finished.RunID != checkpoint.RunID || executions.Load() != 3 {
		t.Fatalf("manual continuation did not finish the same run: %+v, executions=%d", finished, executions.Load())
	}
	if got := request(http.MethodGet, experiment+"/workflow/"+finished.RunID+"/steps/checklist/artifact", "", http.StatusOK, nil); got != checklist {
		t.Fatalf("wrong checklist artifact: %q", got)
	}

	reviewProject := createProject("literature_review")
	review := projectsPath + "/" + reviewProject.ProjectID
	preferences := fmt.Sprintf(`{"requestId":"prefer-auto","expectedRevision":%d,"defaultTurnMode":"AUTO"}`, reviewProject.Revision)
	request(http.MethodPatch, review, preferences, http.StatusOK, nil)
	blocked := send(review, `{"requestId":"auto-review","text":"`+reviewText+`"}`)
	pending := blocked.WorkflowProposal
	if pending.Status != "PROPOSED" || pending.Automatic || pending.RunID != "" || strings.TrimSpace(pending.Message) == "" || readRun(review) != nil {
		t.Fatalf("missing sources did not leave a proposed run with a reason: %+v", pending)
	}
	const doi = "10.5555/river-observations"
	if _, _, err := store.saveCitation(ctx, reviewProject.ProjectID, literature.Work{DOI: doi, Title: "River observations"}); err != nil {
		t.Fatal(err)
	}
	var confirmed proposal
	request(http.MethodPost, review+"/turns/"+blocked.TurnID+"/workflow-proposal/confirm", "", http.StatusOK, &confirmed)
	if confirmed.Status != "CONFIRMED" || confirmed.Automatic || confirmed.RunID == "" {
		t.Fatalf("manual confirmation did not recover the blocked proposal: %+v", confirmed)
	}
	completed := waitFor(review, "RANGE_COMPLETED")
	if completed.RunID != confirmed.RunID || completed.SourceCount != 1 || executions.Load() != 3 || dialogues.Load() != 2 {
		t.Fatalf("manual confirmation did not freeze the new source: %+v, executions=%d, dialogues=%d", completed, executions.Load(), dialogues.Load())
	}
}
