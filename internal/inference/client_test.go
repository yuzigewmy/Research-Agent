package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model               string    `json:"model"`
			Messages            []Message `json:"messages"`
			Stream              bool      `json:"stream"`
			ReasoningEffort     string    `json:"reasoning_effort"`
			MaxCompletionTokens int       `json:"max_completion_tokens"`
			MaxTokens           int       `json:"max_tokens"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "gpt-6.2-sol" || !request.Stream ||
			request.ReasoningEffort != "max" || request.MaxCompletionTokens != 8192 || request.MaxTokens != 0 ||
			len(request.Messages) != 1 || request.Messages[0].Content != "梳理实验变量" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `: keepalive

data: {"choices":[{"delta":{"reasoning_content":"private"}}]}

data: {"choices":[{"delta":{"content":"自变量"}}]}

data: {"choices":[{"delta":{"content":"与因变量"},"finish_reason":"stop"}]}

data: [DONE]

`)
	}))
	defer upstream.Close()

	client, err := New(Config{BaseURL: upstream.URL + "/v1", Model: "gpt-6.2-sol", APIKey: "fixture", ReasoningEffort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	var reply strings.Builder
	err = client.Stream(context.Background(), []Message{{Role: "user", Content: "梳理实验变量"}}, func(text string) error {
		reply.WriteString(text)
		return nil
	})
	if err != nil || reply.String() != "自变量与因变量" {
		t.Fatalf("reply = %q, err = %v", reply.String(), err)
	}
}

func TestDroppedStreamHasNoSuccessReceipt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer upstream.Close()
	client, err := New(Config{BaseURL: upstream.URL, Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stream(context.Background(), []Message{{Role: "user", Content: "问题"}}, func(string) error { return nil }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want incomplete stream, got %v", err)
	}
}
