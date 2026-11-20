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

func TestConversationProposalWaitsForConfirmation(t *testing.T) {
	const instruction = "制定河流水温比较的研究计划，交付采样范围和对照表。"
	const resultText = "# 已完成水温研究计划"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function struct{ Name string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1, 2:
			if len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" {
				t.Error("confirmation mode did not offer the research plan action")
			}
			args := fmt.Sprintf(`{"instruction":%q}`, instruction)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请核对计划。\",\"tool_calls\":[{\"index\":0,\"id\":\"call-plan\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", args)
			if call == 1 {
				fmt.Fprint(w, "data: [DONE]\n\n")
			}
		case 3, 4:
			if len(input.Tools) != 0 {
				t.Error("task execution and chat-only mode must use the text interface")
			}
			last := input.Messages[len(input.Messages)-1].Content
			text := resultText
			if call == 3 && !strings.Contains(last, instruction) {
				t.Error("confirmed execution did not receive the saved instruction")
			}
			if call == 4 {
				if !strings.Contains(input.Messages[len(input.Messages)-2].Content, resultText) {
					t.Error("the task result was not attached to the next conversation")
				}
				text = "已查看成果。"
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", text)
		default:
			t.Error("unexpected model dispatch")
		}
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	project, _, err := store.createProject(t.Context(), createProjectInput{RequestID: "river", Title: "水温比较", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + project.ProjectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	ctx, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	request := func(method, path, body string, want int, output any) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+base+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		if output != nil {
			if err := json.Unmarshal(w.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}
	type response struct {
		TurnID, Status, ProposedTaskID string
	}
	const ask = `{"requestId":"plan","text":"帮我拟定水温研究计划","mode":"CONFIRM"}`
	var start, completed response
	request(http.MethodPost, "/turns", ask, http.StatusAccepted, &start)
	stream := request(http.MethodGet, "/turns/"+start.TurnID+"/stream", "", http.StatusOK, nil)
	request(http.MethodPost, "/turns", ask, http.StatusOK, &completed)
	if completed.Status != "COMPLETED" || completed.ProposedTaskID == "" || !strings.Contains(stream, `"proposedTaskId":"`+completed.ProposedTaskID+`"`) {
		t.Fatalf("task proposal was missing from the completed turn: %+v, %s", completed, stream)
	}
	request(http.MethodPost, "/turns", strings.Replace(ask, "CONFIRM", "CHAT", 1), http.StatusConflict, nil)
	var incomplete response
	request(http.MethodPost, "/turns", strings.Replace(ask, `"plan"`, `"truncated"`, 1), http.StatusAccepted, &incomplete)
	if stream := request(http.MethodGet, "/turns/"+incomplete.TurnID+"/stream", "", http.StatusOK, nil); !strings.Contains(stream, `"status":"FAILED"`) || strings.Contains(stream, `"proposedTaskId"`) {
		t.Fatalf("truncated tool call did not fail cleanly: %s", stream)
	}
	var tasks taskPage
	request(http.MethodGet, "/tasks", "", http.StatusOK, &tasks)
	if len(tasks.Items) != 1 || tasks.Items[0].Status != taskProposed || tasks.Items[0].SourceTurnID != start.TurnID ||
		tasks.Items[0].TaskID != completed.ProposedTaskID || tasks.Items[0].Instruction != instruction || calls.Load() != 2 {
		t.Fatalf("a proposal must remain unexecuted until confirmation: %+v, calls=%d", tasks.Items, calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	var reopened response
	request(http.MethodPost, "/turns", ask, http.StatusOK, &reopened)
	if reopened != completed {
		t.Fatalf("the persisted turn lost its proposal: %+v", reopened)
	}
	taskPath := "/tasks/" + completed.ProposedTaskID
	request(http.MethodPost, taskPath+"/confirm", `{"requestId":"confirm","expectedRevision":1}`, http.StatusAccepted, nil)
	for {
		request(http.MethodGet, "/tasks", "", http.StatusOK, &tasks)
		if tasks.Items[0].Status == taskSucceeded {
			break
		}
		if tasks.Items[0].Status == taskFailed || ctx.Err() != nil {
			t.Fatalf("confirmed task did not complete: %+v", tasks.Items[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	if content := request(http.MethodGet, taskPath+"/artifact", "", http.StatusOK, nil); content != resultText {
		t.Fatalf("unexpected task artifact: %q", content)
	}
	var later response
	request(http.MethodPost, "/turns", `{"requestId":"later","text":"看看完成的成果"}`, http.StatusAccepted, &later)
	if stream := request(http.MethodGet, "/turns/"+later.TurnID+"/stream", "", http.StatusOK, nil); !strings.Contains(stream, `"status":"COMPLETED"`) || strings.Contains(stream, `"proposedTaskId"`) || calls.Load() != 4 {
		t.Fatalf("chat-only follow-up dispatched an unexpected proposal: %s, calls=%d", stream, calls.Load())
	}
}
