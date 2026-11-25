package inference

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	BaseURL         string
	Model           string
	APIKey          string
	ReasoningEffort string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Tool describes a Chat Completions function and its parameter schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Client struct {
	endpoint        string
	imageEndpoint   string
	model           string
	apiKey          string
	reasoningEffort string
	http            *http.Client
}

func New(config Config) (*Client, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("模型服务地址无效")
	}
	host := base.Hostname()
	if base.Scheme != "https" && (base.Scheme != "http" || (host != "localhost" && !net.ParseIP(host).IsLoopback())) {
		return nil, errors.New("模型服务须使用 HTTPS 或本机 HTTP")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("请指定模型名称")
	}
	return &Client{
		endpoint:        base.JoinPath("chat", "completions").String(),
		imageEndpoint:   base.JoinPath("images", "generations").String(),
		model:           config.Model,
		apiKey:          config.APIKey,
		reasoningEffort: config.ReasoningEffort,
		http: &http.Client{
			Timeout: 3 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// ModelInfo reports the configured model and reasoning effort for diagnostics.
func (c *Client) ModelInfo() (model, reasoningEffort string) {
	return c.model, c.reasoningEffort
}

// ForModel selects a model on the same deployment connection and transport.
func (c *Client) ForModel(model, reasoningEffort string) *Client {
	selected := *c
	selected.model, selected.reasoningEffort = model, reasoningEffort
	return &selected
}

// ConnectionFingerprint identifies the configured endpoint without storing its address or credentials.
func (c *Client) ConnectionFingerprint() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(c.endpoint)))
}

type chatTool struct {
	Type     string `json:"type"`
	Function Tool   `json:"function"`
}

type chatRequest struct {
	Model               string     `json:"model"`
	Messages            any        `json:"messages"`
	Stream              bool       `json:"stream"`
	ReasoningEffort     string     `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int        `json:"max_completion_tokens,omitempty"`
	MaxTokens           int        `json:"max_tokens,omitempty"`
	Tools               []chatTool `json:"tools,omitempty"`
	ToolChoice          string     `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool      `json:"parallel_tool_calls,omitempty"`
}

// Stream forwards public text from the Chat Completions SSE response.
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/
func (c *Client) Stream(ctx context.Context, messages []Message, emit func(string) error) error {
	return c.StreamWithBudget(ctx, messages, 0, emit)
}

// StreamWithBudget extends the default output allowance for long writing stages.
func (c *Client) StreamWithBudget(ctx context.Context, messages []Message, outputTokens int, emit func(string) error) error {
	_, err := c.stream(ctx, messages, outputTokens, nil, emit)
	return err
}

// StreamWithTool forwards public text and returns arguments for an optional function call.
// A text-only reply returns nil arguments. Callers validate the returned arguments against the tool schema.
func (c *Client) StreamWithTool(ctx context.Context, messages []Message, tool Tool, emit func(string) error) (json.RawMessage, error) {
	if !toolNamePattern.MatchString(tool.Name) || (len(tool.Parameters) != 0 && !isJSONObject(tool.Parameters)) {
		return nil, errors.New("模型工具定义无效")
	}
	return c.stream(ctx, messages, 0, &tool, emit)
}

func (c *Client) stream(ctx context.Context, messages []Message, outputTokens int, tool *Tool, emit func(string) error) (json.RawMessage, error) {
	outputTokens = max(2048, outputTokens)
	payload := chatRequest{Model: c.model, Messages: messages, Stream: true, ReasoningEffort: c.reasoningEffort}
	if c.reasoningEffort == "" {
		payload.MaxTokens = outputTokens
	} else {
		payload.MaxCompletionTokens = max(8192, outputTokens+4096)
	}
	var toolName string
	if tool != nil {
		toolName = tool.Name
		payload.Tools = []chatTool{{Type: "function", Function: *tool}}
		payload.ToolChoice = "auto"
		parallel := false
		payload.ParallelToolCalls = &parallel
	}
	return c.sendStream(ctx, payload, toolName, emit)
}

func (c *Client) sendStream(ctx context.Context, payload chatRequest, toolName string, emit func(string) error) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("模型服务连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型服务返回 HTTP %d", resp.StatusCode)
	}
	return readStream(resp.Body, toolName, emit)
}

func readStream(body io.Reader, toolName string, emit func(string) error) (json.RawMessage, error) {
	const maxEvent = 1 << 20
	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 8192), maxEvent)
	var data strings.Builder
	var call streamedToolCall
	var finishReason string
	for lines.Scan() {
		line := lines.Text()
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len()+len(value) >= maxEvent {
				return nil, errors.New("模型事件太大")
			}
			data.WriteString(strings.TrimPrefix(value, " "))
			data.WriteByte('\n')
			continue
		}
		if line != "" || data.Len() == 0 {
			continue
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "[DONE]" {
			if finishReason == "tool_calls" {
				args := json.RawMessage(call.arguments.String())
				if call.name != toolName || !isJSONObject(args) {
					return nil, errInvalidToolCall
				}
				return args, nil
			}
			if call.seen {
				return nil, errInvalidToolCall
			}
			return nil, nil
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Message
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil || (len(event.Error) != 0 && string(event.Error) != "null") {
			return nil, errors.New("模型服务返回了无法使用的事件")
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]
		if toolName != "" {
			if len(event.Choices) != 1 || choice.Index != 0 || finishReason != "" {
				return nil, errInvalidToolCall
			}
			if err := call.addFragments(toolName, choice.Delta.ToolCalls); err != nil {
				return nil, err
			}
			finishReason = choice.FinishReason
		}
		if choice.Delta.Content != "" {
			if err := emit(choice.Delta.Content); err != nil {
				return nil, err
			}
		}
		if choice.FinishReason == "tool_calls" && toolName != "" {
			if !call.seen {
				return nil, errInvalidToolCall
			}
			continue
		}
		if choice.FinishReason != "" && choice.FinishReason != "stop" {
			if toolName != "" && choice.FinishReason != "length" && choice.FinishReason != "content_filter" {
				return nil, errors.New("模型未完成本轮")
			}
			return nil, fmt.Errorf("模型未完成本轮: %s", choice.FinishReason)
		}
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

var errInvalidToolCall = errors.New("模型服务返回了无效的工具调用")

type streamedToolCall struct {
	seen      bool
	id, name  string
	arguments strings.Builder
}

func (call *streamedToolCall) addFragments(toolName string, data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	var fragments []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &fragments); err != nil || len(fragments) > 1 {
		return errInvalidToolCall
	}
	if len(fragments) == 0 {
		return nil
	}
	fragment := fragments[0]
	if fragment.Index == nil || *fragment.Index != 0 || (fragment.Type != "" && fragment.Type != "function") {
		return errInvalidToolCall
	}
	if fragment.ID != "" {
		if call.id != "" && call.id != fragment.ID {
			return errInvalidToolCall
		}
		call.id = fragment.ID
	}
	const maxArguments = 64 << 10
	if len(fragment.Function.Name) > len(toolName)-len(call.name) || len(fragment.Function.Arguments) > maxArguments-call.arguments.Len() {
		return errInvalidToolCall
	}
	call.name += fragment.Function.Name
	if !strings.HasPrefix(toolName, call.name) {
		return errInvalidToolCall
	}
	call.arguments.WriteString(fragment.Function.Arguments)
	call.seen = true
	return nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) != 0 && trimmed[0] == '{' && json.Valid(data)
}
