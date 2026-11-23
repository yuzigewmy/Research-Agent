package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestTurnManuscriptSelectionsKeepOrderAndSavedVersions(t *testing.T) {
	const question = "请比较这两份稿件的选文。"
	const resultQuote, methodQuote = "两处观测一致。", "河岸🌿样点"
	const omitted, newQuote = "这是未选中的附录。", "这是新版采样方法。"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	project := func(id string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	save := func(id, path, base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, id, path, base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	selectText := func(doc WorkspaceDocument, text string) manuscriptSelection {
		t.Helper()
		start := strings.Index(doc.Content, text)
		if start < 0 {
			t.Fatalf("missing fixture selection in %s", doc.Path)
		}
		return manuscriptSelection{
			manuscriptSelectionInput: manuscriptSelectionInput{VersionID: doc.VersionID, StartByte: start, EndByte: start + len(text)},
			Path:                     doc.Path, SHA256: doc.SHA256, Text: text,
		}
	}
	id := project("river")
	results := save(id, "paper/z-results.markdown", "", "# 结果\n\n"+resultQuote+"\n"+omitted+strings.Repeat("x", 8<<10))
	original := save(id, "paper/a-method.md", "", "# 方法\r\n\r\n"+methodQuote+"。")
	latest := save(id, original.Path, original.SHA256, "# 方法\r\n\r\n"+newQuote)
	foreign := save(project("other"), original.Path, "", original.Content)
	wantSelections := []manuscriptSelection{selectText(results, resultQuote), selectText(original, methodQuote)}
	result, method := wantSelections[0].manuscriptSelectionInput, wantSelections[1].manuscriptSelectionInput

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("model did not receive a conversation prompt")
			http.Error(w, "invalid model input", http.StatusBadRequest)
			return
		}
		var prompt struct {
			Request              string
			ManuscriptSelection  *manuscriptSelection
			ManuscriptSelections []manuscriptSelection
		}
		last := input.Messages[len(input.Messages)-1].Content
		_, payload, _ := strings.Cut(last, "{")
		if err := json.Unmarshal([]byte("{"+payload), &prompt); err != nil || prompt.Request != question ||
			prompt.ManuscriptSelection != nil || !slices.Equal(prompt.ManuscriptSelections, wantSelections) {
			t.Errorf("wrong model selections: %+v, %v", prompt, err)
		}
		for _, msg := range input.Messages {
			if strings.Contains(msg.Content, omitted) || strings.Contains(msg.Content, newQuote) {
				t.Error("model received unselected text or text from the newer version")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已比较选文。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := "http://127.0.0.1" + projectsPath + "/" + id + "/turns"
	request := func(method, suffix, body string, status int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, suffix, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	type receipt struct {
		TurnID, Status, Answer string
		ManuscriptSelection    *manuscriptSelection
		ManuscriptSelections   []manuscriptSelection
	}
	body := func(id string, selections []manuscriptSelectionInput, single *manuscriptSelectionInput) string {
		t.Helper()
		data, err := json.Marshal(struct {
			RequestID            string                     `json:"requestId"`
			Text                 string                     `json:"text"`
			ManuscriptSelections []manuscriptSelectionInput `json:"manuscriptSelections"`
			ManuscriptSelection  *manuscriptSelectionInput  `json:"manuscriptSelection,omitempty"`
		}{id, question, selections, single})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	assertReceipt := func(got receipt, id, status string) {
		t.Helper()
		if id == "" || got.TurnID != id || got.Status != status || got.ManuscriptSelection != nil || !slices.Equal(got.ManuscriptSelections, wantSelections) {
			t.Fatalf("wrong turn receipt for %s: %+v", status, got)
		}
	}

	sent := body("compare", []manuscriptSelectionInput{result, method}, nil)
	var accepted receipt
	request(http.MethodPost, "", sent, http.StatusAccepted, &accepted)
	assertReceipt(accepted, accepted.TurnID, "RUNNING")
	stream := request(http.MethodGet, "/"+accepted.TurnID+"/stream", "", http.StatusOK, nil)
	if !strings.Contains(stream, `"status":"COMPLETED"`) {
		t.Fatalf("comparison did not finish: %s", stream)
	}

	outOfBounds, splitRune, tooLong := method, method, result
	outOfBounds.EndByte = len(original.Content) + 1
	splitRune.StartByte++
	tooLong.EndByte = tooLong.StartByte + (8 << 10) + 1
	tooMany := []manuscriptSelectionInput{result, method}
	for n := 1; n <= 3; n++ {
		doc := save(id, fmt.Sprintf("paper/extra-%d.md", n), "", "补充记录")
		tooMany = append(tooMany, selectText(doc, "补充记录").manuscriptSelectionInput)
	}
	for _, test := range []struct {
		id         string
		selections []manuscriptSelectionInput
		single     *manuscriptSelectionInput
	}{
		{id: "foreign", selections: []manuscriptSelectionInput{result, selectText(foreign, methodQuote).manuscriptSelectionInput}},
		{id: "same-path", selections: []manuscriptSelectionInput{method, selectText(latest, newQuote).manuscriptSelectionInput}},
		{id: "out-of-bounds", selections: []manuscriptSelectionInput{outOfBounds}},
		{id: "split-rune", selections: []manuscriptSelectionInput{splitRune}},
		{id: "too-long", selections: []manuscriptSelectionInput{tooLong}},
		{id: "empty", selections: []manuscriptSelectionInput{}},
		{id: "too-many", selections: tooMany},
		{id: "both-fields", selections: []manuscriptSelectionInput{result}, single: &method},
	} {
		var problem struct{ Error apiError }
		request(http.MethodPost, "", body(test.id, test.selections, test.single), http.StatusUnprocessableEntity, &problem)
		if problem.Error.Code != "INVALID_MANUSCRIPT_SELECTION" {
			t.Fatalf("%s: wrong selection error %+v", test.id, problem.Error)
		}
	}
	request(http.MethodPost, "", body("compare", []manuscriptSelectionInput{method, result}, nil), http.StatusConflict, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	var replay receipt
	request(http.MethodPost, "", sent, http.StatusOK, &replay)
	assertReceipt(replay, accepted.TurnID, "COMPLETED")
	var history struct{ Items []receipt }
	request(http.MethodGet, "", "", http.StatusOK, &history)
	if len(history.Items) != 1 || calls.Load() != 1 || replay.Answer != "已比较选文。" {
		t.Fatalf("wrong saved comparison: turns=%d, model calls=%d, answer=%q", len(history.Items), calls.Load(), replay.Answer)
	}
	assertReceipt(history.Items[0], accepted.TurnID, "COMPLETED")
}
