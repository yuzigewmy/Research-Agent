package inference

import (
	"context"
	"encoding/base64"
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

// StreamPNG sends a local PNG as an inline image and streams the text reply.
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/#image-input
func (c *Client) StreamPNG(ctx context.Context, prompt string, pngBytes []byte, emit func(string) error) error {
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	messages := []chatImageMessage{{Role: "user", Content: []chatContentPart{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &chatImageURL{URL: url}},
	}}}
	payload := chatRequest{Model: c.model, Messages: messages, Stream: true, ReasoningEffort: c.reasoningEffort}
	if c.reasoningEffort == "" {
		payload.MaxTokens = 256
	} else {
		payload.MaxCompletionTokens = 4096
	}
	_, err := c.sendStream(ctx, payload, "", emit)
	return err
}
