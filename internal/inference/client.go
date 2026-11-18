package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	BaseURL string
	Model   string
	APIKey  string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	endpoint string
	model    string
	apiKey   string
	http     *http.Client
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
		endpoint: base.JoinPath("chat", "completions").String(),
		model:    config.Model,
		apiKey:   config.APIKey,
		http: &http.Client{
			Timeout: 3 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Stream forwards public text from the Chat Completions SSE response.
// https://api-docs.deepseek.com/api/create-chat-completion/#request
func (c *Client) Stream(ctx context.Context, messages []Message, emit func(string) error) error {
	body, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens"`
	}{c.model, messages, true, 2048})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("模型服务连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("模型服务返回 HTTP %d", resp.StatusCode)
	}
	return readStream(resp.Body, emit)
}

func readStream(body io.Reader, emit func(string) error) error {
	const maxEvent = 1 << 20
	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 8192), maxEvent)
	var data strings.Builder
	for lines.Scan() {
		line := lines.Text()
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len()+len(value) >= maxEvent {
				return errors.New("模型事件太大")
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
			return nil
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta        Message `json:"delta"`
				FinishReason string  `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil || (len(event.Error) != 0 && string(event.Error) != "null") {
			return errors.New("模型服务返回了无法使用的事件")
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]
		if choice.Delta.Content != "" {
			if err := emit(choice.Delta.Content); err != nil {
				return err
			}
		}
		if choice.FinishReason != "" && choice.FinishReason != "stop" {
			return fmt.Errorf("模型未完成本轮: %s", choice.FinishReason)
		}
	}
	if err := lines.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
