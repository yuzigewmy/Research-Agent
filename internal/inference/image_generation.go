package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxGeneratedPNGBytes = 8 << 20

var errInvalidGeneratedImage = errors.New("模型服务未返回有效的内联图像")

// GeneratePNG requests one PNG and returns the decoded inline bytes.
// The caller validates the PNG content and dimensions.
// https://developers.openai.com/api/reference/resources/images/methods/generate.md
func (c *Client) GeneratePNG(ctx context.Context, prompt, size, quality string) ([]byte, error) {
	payload := struct {
		Model        string `json:"model"`
		Prompt       string `json:"prompt"`
		N            int    `json:"n"`
		Size         string `json:"size"`
		Quality      string `json:"quality"`
		OutputFormat string `json:"output_format"`
	}{Model: c.model, Prompt: prompt, N: 1, Size: size, Quality: quality, OutputFormat: "png"}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法编码图像生成请求")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.imageEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法创建图像生成请求")
	}
	// Prevent net/http from replaying a generation POST after dispatch.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("图像生成服务连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("图像生成服务返回 HTTP %d", resp.StatusCode)
	}
	png, err := readGeneratedPNG(resp.Body)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return png, err
}

func readGeneratedPNG(body io.Reader) ([]byte, error) {
	maxEncoded := base64.StdEncoding.EncodedLen(maxGeneratedPNGBytes)
	maxResponse := maxEncoded + (64 << 10) // Allow room for JSON metadata.
	data, err := io.ReadAll(io.LimitReader(body, int64(maxResponse)+1))
	if err != nil {
		return nil, errors.New("无法读取图像生成服务的响应")
	}
	if len(data) > maxResponse {
		return nil, errors.New("图像生成服务的响应过大")
	}
	var response struct {
		// A second entry, even null, makes the result unusable.
		Data imageResponseField[[2]json.RawMessage] `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, errInvalidGeneratedImage
	}
	images := response.Data.value
	if len(images[0]) == 0 || len(images[1]) != 0 {
		return nil, errInvalidGeneratedImage
	}
	var image struct {
		Base64 imageResponseField[string] `json:"b64_json"`
	}
	if err := json.Unmarshal(images[0], &image); err != nil || image.Base64.value == "" {
		return nil, errInvalidGeneratedImage
	}
	encoded := image.Base64.value
	if len(encoded) > maxEncoded {
		return nil, errors.New("生成图像超过 8 MiB")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, errInvalidGeneratedImage
	}
	if len(decoded) > maxGeneratedPNGBytes {
		return nil, errors.New("生成图像超过 8 MiB")
	}
	return decoded, nil
}

// imageResponseField rejects repeated values for the fields that carry the image.
type imageResponseField[T any] struct {
	value T
	seen  bool
}

func (f *imageResponseField[T]) UnmarshalJSON(data []byte) error {
	if f.seen {
		return errInvalidGeneratedImage
	}
	f.seen = true
	return json.Unmarshal(data, &f.value)
}
