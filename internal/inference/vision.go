package inference

import (
	"context"
	"encoding/base64"
	"errors"
)

type chatImageURL struct {
	URL string `json:"url"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageMessage struct {
	Role    string            `json:"role"`
	Content []chatContentPart `json:"content"`
}

// StreamImage sends a local PNG or JPEG as an inline image and streams the text reply.
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/#image-input
func (c *Client) StreamImage(ctx context.Context, prompt, format string, data []byte, outputTokens int, emit func(string) error) error {
	if format != "png" && format != "jpeg" {
		return errors.New("模型图像格式无效")
	}
	url := "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data)
	messages := []chatImageMessage{{Role: "user", Content: []chatContentPart{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &chatImageURL{URL: url}},
	}}}
	payload := chatRequest{Model: c.model, Messages: messages, Stream: true, ReasoningEffort: c.reasoningEffort}
	if c.reasoningEffort == "" {
		payload.MaxTokens = outputTokens
	} else {
		payload.MaxCompletionTokens = max(4096, outputTokens+2048)
	}
	_, err := c.sendStream(ctx, payload, "", emit)
	return err
}
