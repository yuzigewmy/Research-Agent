package workbench

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"research-agent/internal/inference"
)

func TestVisionModelConnectionChecksFixedImage(t *testing.T) {
	const credential = "fixture-vision-credential"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
			Stream          bool   `json:"stream"`
			Messages        []struct {
				Role    string
				Content []struct {
					Type     string
					Text     string
					ImageURL struct{ URL string } `json:"image_url"`
				}
			}
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+credential ||
			json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "fixture-vision" || input.ReasoningEffort != "low" || !input.Stream ||
			len(input.Messages) != 1 || input.Messages[0].Role != "user" || len(input.Messages[0].Content) != 2 {
			t.Error("unexpected vision probe request")
			http.Error(w, "unexpected probe", http.StatusBadRequest)
			return
		}
		content := input.Messages[0].Content
		encoded, ok := strings.CutPrefix(content[1].ImageURL.URL, "data:image/png;base64,")
		if !ok || content[0].Type != "text" || !strings.Contains(content[0].Text, "方块") || content[1].Type != "image_url" {
			t.Error("missing text or PNG in the same message")
			http.Error(w, "missing image", http.StatusBadRequest)
			return
		}
		imageBytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid encoding", http.StatusBadRequest)
			return
		}
		picture, err := png.Decode(bytes.NewReader(imageBytes))
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid PNG", http.StatusBadRequest)
			return
		}
		count := func(x int) int {
			var total int
			var previous bool
			for y := range picture.Bounds().Dy() {
				red, _, _, _ := picture.At(x, y).RGBA()
				black := red == 0
				if black && !previous {
					total++
				}
				previous = black
			}
			return total
		}
		left, right := count(picture.Bounds().Dx()/4), count(picture.Bounds().Dx()*3/4)
		if left != 2 || right != 3 {
			t.Errorf("test image has %d and %d blocks, want 2 and 3", left, right)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			left, right = right, left
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d,\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"%d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", left, right)
	}))
	defer upstream.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	model, err := inference.New(inference.Config{BaseURL: upstream.URL + "/v1", Model: "fixture-text", APIKey: credential})
	if err != nil {
		t.Fatal(err)
	}
	request := func(config HandlerConfig, method, suffix, body string, want int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1/api/v1/model-connection"+suffix, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		NewHandler(store, nil, config).ServeHTTP(res, req)
		text := strings.TrimSpace(res.Body.String())
		if strings.Contains(text, credential) || strings.Contains(text, upstream.URL) || strings.Contains(text, "data:image") {
			t.Fatal("workbench response exposed private connection or request data")
		}
		if res.Code != want {
			t.Fatalf("%s %s status %d, want %d: %s", method, suffix, res.Code, want, text)
		}
		return text
	}
	const body = `{"modelId":"vision"}`
	request(HandlerConfig{Model: model}, http.MethodPost, "/test", body, http.StatusBadRequest)
	config := HandlerConfig{Model: model, VisionModelName: "fixture-vision", VisionReasoningEffort: "low"}
	var info modelConnectionInfo
	if err := json.Unmarshal([]byte(request(config, http.MethodGet, "", "", http.StatusOK)), &info); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range info.ModelOptions {
		found = found || (role.ID == "vision" && role.Model == "fixture-vision" && role.ReasoningEffort == "low")
	}
	if !found || calls.Load() != 0 {
		t.Fatalf("vision option missing or settings read contacted the provider: %+v", info.ModelOptions)
	}
	if got := request(config, http.MethodPost, "/test", body, http.StatusBadGateway); !strings.Contains(got, `"code":"VISION_TEST_MISMATCH"`) {
		t.Fatalf("wrong image reading was reported incorrectly: %s", got)
	}
	if got := request(config, http.MethodPost, "/test", body, http.StatusOK); got != `{"ok":true}` || calls.Load() != 2 {
		t.Fatalf("vision probe retry: %s (calls %d)", got, calls.Load())
	}
}
