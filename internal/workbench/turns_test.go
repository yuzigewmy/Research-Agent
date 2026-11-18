package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"research-agent/internal/inference"
)

func TestConversationReceiptAndReplaySurviveReopening(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"基线\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"与对照组\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: "project", Title: "实验记录", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project.ProjectID + "/turns"

	request := func(handler http.Handler, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d (%s)", method, path, res.Code, res.Body.String())
		}
		return res
	}

	handler := NewHandler(store, nil, model)
	const input = `{"requestId":"turn-1","text":"设计实验"}`
	started := request(handler, "POST", base, input, http.StatusAccepted)
	var turn Turn
	if err := json.Unmarshal(started.Body.Bytes(), &turn); err != nil || turn.TurnID == "" {
		t.Fatalf("invalid receipt: %v, %s", err, started.Body.String())
	}
	request(handler, "POST", base, `{"requestId":"turn-1","text":"另一项实验"}`, http.StatusConflict)

	streamPath := base + "/" + turn.TurnID + "/stream"
	stream := request(handler, "GET", streamPath, "", http.StatusOK)
	var firstDelta int64
	for _, line := range strings.Split(stream.Body.String(), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var event TurnEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			if event.Text == "基线" {
				firstDelta = event.Seq
			}
		}
	}
	if firstDelta == 0 || !strings.Contains(stream.Body.String(), `"status":"COMPLETED"`) {
		t.Fatalf("incomplete stream: %s", stream.Body.String())
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler = NewHandler(reopened, nil, nil)
	stored := request(handler, "POST", base, input, http.StatusOK)
	if err := json.Unmarshal(stored.Body.Bytes(), &turn); err != nil || turn.Answer != "基线与对照组" || turn.Status != "COMPLETED" {
		t.Fatalf("invalid saved turn: %v, %s", err, stored.Body.String())
	}
	replay := request(handler, "GET", streamPath+"?after="+strconv.FormatInt(firstDelta, 10), "", http.StatusOK)
	if strings.Contains(replay.Body.String(), `"text":"基线"`) || !strings.Contains(replay.Body.String(), `"text":"与对照组"`) {
		t.Fatalf("cursor was not respected: %s", replay.Body.String())
	}
}
