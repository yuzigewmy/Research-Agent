package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestStopConversationPreservesPartialReplyAndAllowsNextTurn(t *testing.T) {
	const partial = "已整理采样范围。"
	const followup = "接下来可以核对样本量。"
	const toolFragment = `{"instruction":"这份建议尚未完成`
	var calls atomic.Int32
	cancelled, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(string(body), "propose_research_task") {
				t.Error("the first turn did not offer the proposal tool")
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q,\"tool_calls\":[{\"index\":0,\"id\":\"call-partial\",\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]}}]}\n\n", partial, toolFragment)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-release:
			}
		case 2:
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", followup)
		default:
			t.Error("unexpected model dispatch")
			http.Error(w, "unexpected model dispatch", http.StatusBadRequest)
		}
	}))
	t.Cleanup(upstream.Close)
	defer close(release)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	createProject := func(requestID string) Project {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: "River monitoring", TemplateID: "experiment"})
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	project, other := createProject("main"), createProject("other")
	base := projectsPath + "/" + project.ProjectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, want int, target any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if target != nil {
			if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}

	const ask = `{"requestId":"first","text":"请拟定河流水温观测计划。","mode":"CONFIRM"}`
	var started Turn
	request(http.MethodPost, base+"/turns", ask, http.StatusAccepted, &started)
	var saved Turn
	for {
		saved, err = store.getTurn(ctx, project.ProjectID, started.TurnID)
		if err != nil || saved.Status != "RUNNING" {
			t.Fatalf("wait for the first saved model fragment: %+v, %v", saved, err)
		}
		if saved.Answer == partial {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	turnPath := base + "/turns/" + started.TurnID
	request(http.MethodPost, projectsPath+"/"+other.ProjectID+"/turns/"+started.TurnID+"/stop", "", http.StatusNotFound, nil)
	if current, err := store.getTurn(ctx, project.ProjectID, started.TurnID); err != nil || !reflect.DeepEqual(current, saved) {
		t.Fatalf("cross-project stop changed the running turn: %+v, %v", current, err)
	}
	var stopped Turn
	request(http.MethodPost, turnPath+"/stop", "", http.StatusOK, &stopped)
	if stopped.TurnID != started.TurnID || stopped.RequestID != started.RequestID || stopped.Text != started.Text ||
		stopped.Mode != started.Mode || stopped.CreatedAt != started.CreatedAt || stopped.Status != "STOPPED" ||
		strings.TrimSpace(stopped.Message) == "" || stopped.Answer != partial || stopped.ProposedTaskID != "" || stopped.AfterSeq <= saved.AfterSeq {
		t.Fatalf("stop did not preserve the partial reply: %+v", stopped)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("the upstream stream context was not cancelled")
	}
	for _, operation := range []struct{ path, body string }{{turnPath + "/stop", ""}, {base + "/turns", ask}} {
		var replayed Turn
		request(http.MethodPost, operation.path, operation.body, http.StatusOK, &replayed)
		if !reflect.DeepEqual(replayed, stopped) {
			t.Fatalf("replay changed the stopped turn: %+v", replayed)
		}
	}
	stream := request(http.MethodGet, turnPath+"/stream", "", http.StatusOK, nil)
	var events []TurnEvent
	for line := range strings.SplitSeq(stream, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var event TurnEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("invalid SSE event %q: %v", data, err)
			}
			events = append(events, event)
		}
	}
	if len(events) != 2 || events[0].Kind != "delta" || events[0].Text != partial || events[0].Seq != saved.AfterSeq ||
		events[1].Kind != "done" || events[1].Status != "STOPPED" || events[1].Seq != stopped.AfterSeq ||
		events[1].Message != stopped.Message || events[1].ProposedTaskID != "" {
		t.Fatalf("want one public delta and one STOPPED terminal event, got %+v", events)
	}

	const next = `{"requestId":"next","text":"接下来核对什么？","mode":"CHAT"}`
	var later, completed, stoppedLater Turn
	request(http.MethodPost, base+"/turns", next, http.StatusAccepted, &later)
	laterPath := base + "/turns/" + later.TurnID
	request(http.MethodGet, laterPath+"/stream", "", http.StatusOK, nil)
	request(http.MethodPost, base+"/turns", next, http.StatusOK, &completed)
	request(http.MethodPost, laterPath+"/stop", "", http.StatusOK, &stoppedLater)
	if later.TurnID == started.TurnID || completed.Status != "COMPLETED" || completed.Answer != followup ||
		!reflect.DeepEqual(stoppedLater, completed) || calls.Load() != 2 {
		t.Fatalf("the next turn did not complete or was changed by a late stop: %+v, calls=%d", stoppedLater, calls.Load())
	}
	var tasks taskPage
	request(http.MethodGet, base+"/tasks", "", http.StatusOK, &tasks)
	if len(tasks.Items) != 0 {
		t.Fatalf("the stopped turn published a proposal: %+v", tasks.Items)
	}
	if extra, err := store.readTurnEvents(ctx, stopped.TurnID, stopped.AfterSeq); err != nil || len(extra) != 0 {
		t.Fatalf("events appended after the STOPPED terminal event: %+v, %v", extra, err)
	}
}
