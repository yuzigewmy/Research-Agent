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

func TestTurnManuscriptSelectionReadsSavedBytesAndReplays(t *testing.T) {
	const prefix, quote = "# 观测\r\n\r\n", "河岸🌿样点"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("missing model input")
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		message := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(message, `"text":"`+quote+`"`) || strings.Contains(message, "未选中的句子") || strings.Contains(message, "新版稿件") {
			t.Errorf("wrong manuscript evidence: %s", message)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已核对样点。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	project := func(id string) string {
		t.Helper()
		p, _, err := s.createProject(t.Context(), createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	save := func(id, base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := s.saveWorkspaceDocument(t.Context(), id, "paper/main.md", base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	id := project("河流观测")
	original := save(id, "", prefix+quote+"。未选中的句子")
	save(id, original.SHA256, "新版稿件")
	other := save(project("资料备份"), "", prefix+quote)
	base := projectsPath + "/" + id + "/turns"
	handler := NewHandler(s, nil, HandlerConfig{Model: model})
	request := func(method, suffix, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, suffix, res.Code, status, res.Body)
		}
		return res
	}
	body := func(versionID string, start, end int) string {
		return fmt.Sprintf(`{"requestId":"quote","text":"请解释选文。","manuscriptSelection":{"versionId":%q,"startByte":%d,"endByte":%d}}`, versionID, start, end)
	}
	start, end := len(prefix), len(prefix)+len(quote)
	request(http.MethodPost, "", body(other.VersionID, start, end), http.StatusUnprocessableEntity)
	request(http.MethodPost, "", body(original.VersionID, start+1, end), http.StatusUnprocessableEntity)
	sent := body(original.VersionID, start, end)
	var turn struct{ TurnID string }
	if err := json.Unmarshal(request(http.MethodPost, "", sent, http.StatusAccepted).Body.Bytes(), &turn); err != nil || turn.TurnID == "" {
		t.Fatalf("missing turn receipt: %v", err)
	}
	request(http.MethodGet, "/"+turn.TurnID+"/stream", "", http.StatusOK)
	request(http.MethodPost, "", body(original.VersionID, start, end-len("点")), http.StatusConflict)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(s, nil, HandlerConfig{})
	history := request(http.MethodGet, "", "", http.StatusOK).Body.String()
	replayed := request(http.MethodPost, "", sent, http.StatusOK).Body.String()
	for _, text := range []string{history, replayed} {
		for _, value := range []string{turn.TurnID, original.VersionID, original.Path, original.SHA256, quote, `"status":"COMPLETED"`} {
			if !strings.Contains(text, value) {
				t.Fatalf("saved turn lost %q: %s", value, text)
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("replay dispatched a model request: %d calls", calls.Load())
	}
}
