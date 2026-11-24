package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"research-agent/internal/inference"
)

func TestModelConnectionChecksOnlyConfiguredService(t *testing.T) {
	const credential = "fixture-credential"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model           string              `json:"model"`
			ReasoningEffort string              `json:"reasoning_effort"`
			Messages        []inference.Message `json:"messages"`
			Stream          bool                `json:"stream"`
			Tools           json.RawMessage     `json:"tools"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+credential ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "fixture-chat" || request.ReasoningEffort != "max" ||
			!request.Stream || len(request.Tools) != 0 || len(request.Messages) != 1 ||
			request.Messages[0] != (inference.Message{Role: "user", Content: "这是一条连接测试，请只回复：正常。"}) {
			t.Error("unexpected model connection request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"正常\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.Error(w, credential, http.StatusUnauthorized)
	}))
	defer upstream.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model, err := inference.New(inference.Config{
		BaseURL: upstream.URL + "/v1", Model: "fixture-chat", APIKey: credential, ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatal(err)
	}
	const route = "/api/v1/model-connection"
	probe := route + "/test"
	request := func(handler http.Handler, method, path, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		output := strings.TrimSpace(res.Body.String())
		if strings.Contains(output, credential) || strings.Contains(output, upstream.URL) {
			t.Fatal("model connection response exposed private configuration")
		}
		return output
	}

	offline := NewHandler(store, nil, HandlerConfig{})
	if got := request(offline, http.MethodGet, route, "", http.StatusOK); got != `{"configured":false}` {
		t.Fatalf("unconfigured status: %s", got)
	}
	request(offline, http.MethodPost, probe, `{}`, http.StatusServiceUnavailable)

	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	const want = `{"configured":true,"model":"fixture-chat","reasoningEffort":"max"}`
	if got := request(handler, http.MethodGet, route, "", http.StatusOK); got != want {
		t.Fatalf("configured status: %s", got)
	}
	request(handler, http.MethodPost, probe, `{"baseUrl":"https://other.invalid"}`, http.StatusBadRequest)
	if calls.Load() != 0 {
		t.Fatal("reading configuration or sending invalid input contacted the model")
	}
	if got := request(handler, http.MethodPost, probe, `{}`, http.StatusOK); got != `{"ok":true}` {
		t.Fatalf("connection check: %s", got)
	}
	if got := request(handler, http.MethodPost, probe, `{}`, http.StatusBadGateway); !strings.Contains(got, `"code":"MODEL_CONNECTION_FAILED"`) {
		t.Fatalf("connection failure: %s", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want 2", calls.Load())
	}
}
