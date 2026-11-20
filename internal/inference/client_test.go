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
			Model               string          `json:"model"`
			Messages            []Message       `json:"messages"`
			Stream              bool            `json:"stream"`
			ReasoningEffort     string          `json:"reasoning_effort"`
			MaxCompletionTokens int             `json:"max_completion_tokens"`
			MaxTokens           int             `json:"max_tokens"`
			Tools               json.RawMessage `json:"tools"`
			ToolChoice          json.RawMessage `json:"tool_choice"`
			ParallelToolCalls   json.RawMessage `json:"parallel_tool_calls"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "gpt-6.2-sol" || !request.Stream ||
			request.ReasoningEffort != "max" || request.MaxCompletionTokens != 8192 || request.MaxTokens != 0 ||
			len(request.Tools) != 0 || len(request.ToolChoice) != 0 || len(request.ParallelToolCalls) != 0 ||
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

func TestChatStreamWithOptionalTool(t *testing.T) {
	const text = `{"choices":[{"delta":{"content":"Review the sampling plan.","reasoning_content":"private"}}]}`
	const first = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"propose_","arguments":"{\"instruction\":\"Review "}}]}}]}`
	const rest = `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"task","arguments":"the notes\"}"}}]}}]}`
	const toolFinish = `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`
	const stop = `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
	const truncated = `{"choices":[{"delta":{},"finish_reason":"length"}]}`
	const another = `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"propose_task","arguments":"{}"}}]}}]}`
	const mismatch = `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"other_task","arguments":"{}"}}]}}]}`
	const incomplete = `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"propose_task","arguments":"{"}}]}}]}`
	const done = "[DONE]"
	tool := Tool{Name: "propose_task", Description: "Suggest a research task.", Parameters: json.RawMessage(`{"type":"object"}`)}
	const expected = `{"instruction":"Review the notes"}`
	padding := strings.Repeat(" ", (64<<10)-len(expected))
	argumentDelta := func(fragment string) string {
		return fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":%q}}]}}]}`, fragment)
	}

	for _, test := range []struct {
		name   string
		events []string
		args   string
		fail   bool
	}{
		{name: "text and fragmented tool", events: []string{first, rest, toolFinish, `{"choices":[]}`, done}, args: expected},
		{name: "text only", events: []string{stop, done}},
		{name: "missing DONE", events: []string{first, rest, toolFinish}, fail: true},
		{name: "missing tool finish", events: []string{first, rest, done}, fail: true},
		{name: "truncated", events: []string{first, rest, truncated, done}, fail: true},
		{name: "incomplete arguments", events: []string{incomplete, toolFinish, done}, fail: true},
		{name: "multiple calls", events: []string{first, rest, another, toolFinish, done}, fail: true},
		{name: "unexpected name", events: []string{mismatch, toolFinish, done}, fail: true},
		{name: "argument limit", events: []string{first, rest, argumentDelta(padding), toolFinish, done}, args: expected + padding},
		{name: "over argument limit", events: []string{first, rest, argumentDelta(padding), argumentDelta(" "), toolFinish, done}, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools []struct {
						Type     string `json:"type"`
						Function Tool   `json:"function"`
					} `json:"tools"`
					ToolChoice        string `json:"tool_choice"`
					MaxTokens         int    `json:"max_tokens"`
					ParallelToolCalls *bool  `json:"parallel_tool_calls"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.ToolChoice != "auto" || request.MaxTokens != 2048 ||
					request.ParallelToolCalls == nil || *request.ParallelToolCalls ||
					len(request.Tools) != 1 || request.Tools[0].Type != "function" || request.Tools[0].Function.Name != tool.Name ||
					request.Tools[0].Function.Description != tool.Description || string(request.Tools[0].Function.Parameters) != string(tool.Parameters) {
					t.Error("request did not define exactly one optional function tool")
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", text)
				for _, event := range test.events {
					fmt.Fprintf(w, "data: %s\n\n", event)
				}
			}))
			t.Cleanup(upstream.Close)
			client, err := New(Config{BaseURL: upstream.URL, Model: "fixture-model"})
			if err != nil {
				t.Fatal(err)
			}
			var reply strings.Builder
			args, err := client.StreamWithTool(t.Context(), []Message{{Role: "user", Content: "Review this plan."}}, tool, func(text string) error {
				reply.WriteString(text)
				return nil
			})
			if (err != nil) != test.fail || string(args) != test.args || (test.args == "" && args != nil) {
				t.Fatalf("arguments = %q, err = %v", args, err)
			}
			if reply.String() != "Review the sampling plan." {
				t.Fatalf("unexpected public reply %q", reply.String())
			}
		})
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
