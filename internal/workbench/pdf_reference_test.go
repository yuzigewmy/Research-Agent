package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestConversationKeepsPDFSelectionAndPage(t *testing.T) {
	const excerpt = "Water temperature at station A"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("missing model messages")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(content, `"page":2`) || !strings.Contains(content, `"text":"`+excerpt+`"`) {
			t.Errorf("model did not receive the quoted page and selection: %s", content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已核对选区\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "pdf-project", Title: "河流观测", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := store.saveMaterial(ctx, project.ProjectID, "river.pdf", strings.NewReader("%PDF-1.7\n"))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := projectsPath + "/" + project.ProjectID + "/turns"
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		return res
	}
	body := fmt.Sprintf(`{"requestId":"pdf-quote","text":"读取观测","referenceIds":[%q],"pdfQuotes":{%q:{"sha256":%q,"page":2,"text":%q}}}`,
		material.MaterialID, material.MaterialID, material.SHA256, excerpt)
	request(http.MethodPost, base, strings.Replace(body, material.SHA256, strings.Repeat("a", 64), 1), http.StatusUnprocessableEntity)
	request(http.MethodPost, base, strings.Replace(body, `"page":2`, `"page":0`, 1), http.StatusBadRequest)
	var turn struct{ TurnID string }
	if err := json.Unmarshal(request(http.MethodPost, base, body, http.StatusAccepted).Body.Bytes(), &turn); err != nil || turn.TurnID == "" {
		t.Fatalf("missing turn id: %v", err)
	}
	request(http.MethodPost, base, body, http.StatusOK)
	request(http.MethodPost, base, strings.Replace(body, `"page":2`, `"page":1`, 1), http.StatusConflict)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		res := request(http.MethodGet, base, "", http.StatusOK).Body.String()
		if strings.Contains(res, `"status":"COMPLETED"`) {
			for _, expected := range []string{`"format":"pdf"`, `"page":2`, `"quote":"` + excerpt + `"`, material.SHA256} {
				if !strings.Contains(res, expected) {
					t.Fatalf("conversation history lost %s: %s", expected, res)
				}
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("PDF selection conversation did not complete")
}
