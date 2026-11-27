package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWordMaterialPreviewAndConversationKeepSource(t *testing.T) {
	const name = "观测.docx"
	const oldText = "配对样点 A 的水温为 18°C。"
	const newText = "复测水温为 22°C。"
	const cell = "水温对照"
	original := wordMaterialFixture(t, oldText, cell)
	replacement := wordMaterialFixture(t, newText, cell)
	digest := fmt.Sprintf("%x", sha256.Sum256(original))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "invalid model input", http.StatusBadRequest)
			return
		}
		last := request.Messages[len(request.Messages)-1].Content
		_, payload, _ := strings.Cut(last, "\n")
		var input struct {
			Request   string                `json:"request"`
			Materials []turnMaterialExcerpt `json:"materials"`
		}
		if err := json.Unmarshal([]byte(payload), &input); err != nil || len(input.Materials) != 1 {
			t.Errorf("invalid source excerpt in model input: %v", err)
		} else {
			want := oldText + "\n\n" + cell
			if input.Request == "仅核对表格选区" {
				want = "\n" + cell
			}
			got := input.Materials[0]
			if got.Text != want || got.SHA256 != digest || got.WordQuote != "" || len(got.Sections) != 0 {
				t.Errorf("model input did not retain the expected original excerpt: %+v", got)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已读取观测\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	project := func(id string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: "观测", TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return projectsPath + "/" + p.ProjectID
	}
	base, foreign := project("word"), project("foreign")
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	send := func(method, route, media string, body io.Reader, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, body)
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, w.Code, want, w.Body)
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder, out any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var receipt struct {
		MaterialID, Kind, Format, SHA256 string
	}
	upload := func(name string, body []byte) {
		t.Helper()
		decode(send(http.MethodPost, base+"/materials?name="+url.QueryEscape(name), "application/octet-stream", bytes.NewReader(body), http.StatusCreated), &receipt)
	}
	upload(name, original)
	if receipt.Kind != "file" || receipt.Format != "word" || receipt.SHA256 != digest {
		t.Fatalf("invalid source receipt: %+v", receipt)
	}
	selectedID := receipt.MaterialID
	upload(name, replacement)
	item := base + "/materials/" + selectedID
	var excerpt MaterialPreview
	decode(send(http.MethodGet, item+"/preview", "", nil, http.StatusOK), &excerpt)
	if !strings.Contains(excerpt.Text, oldText) || !strings.Contains(excerpt.Text, cell) || excerpt.Truncated ||
		len(excerpt.Sections) != 2 || strings.TrimSpace(excerpt.Sections[0]) != oldText || strings.TrimSpace(excerpt.Sections[1]) != cell {
		t.Fatalf("wrong preview: %+v", excerpt)
	}
	if got := send(http.MethodGet, item+"/download", "", nil, http.StatusOK).Body.Bytes(); !bytes.Equal(got, original) {
		t.Fatal("download changed the original bytes")
	}
	send(http.MethodGet, foreign+"/materials/"+selectedID+"/preview", "", nil, http.StatusNotFound)
	body := fmt.Sprintf(`{"requestId":"reference","text":"读取水温记录","referenceIds":[%q]}`, selectedID)
	send(http.MethodPost, foreign+"/turns", "application/json", strings.NewReader(body), http.StatusUnprocessableEntity)
	var turn Turn
	decode(send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(body), http.StatusAccepted), &turn)
	if len(turn.References) != 1 || turn.References[0].MaterialID != selectedID || turn.References[0].SHA256 != digest {
		t.Fatalf("wrong pinned reference: %+v", turn.References)
	}
	if stream := send(http.MethodGet, base+"/turns/"+turn.TurnID+"/stream", "", nil, http.StatusOK).Body.String(); !strings.Contains(stream, `"status":"COMPLETED"`) {
		t.Fatalf("Word reference did not complete: %s", stream)
	}
	selectedBody := func(sha, text string) string {
		return fmt.Sprintf(`{"requestId":"word-selection","text":"仅核对表格选区","referenceIds":[%q],"wordQuotes":{%q:{"sha256":%q,"text":%q}}}`,
			selectedID, selectedID, sha, text)
	}
	selected := selectedBody(digest, "\n"+cell)
	send(http.MethodPost, foreign+"/turns", "application/json", strings.NewReader(selected), http.StatusUnprocessableEntity)
	send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(selectedBody(receipt.SHA256, "\n"+cell)), http.StatusUnprocessableEntity)
	send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(selectedBody(digest, newText)), http.StatusUnprocessableEntity)
	var quoted Turn
	decode(send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(selected), http.StatusAccepted), &quoted)
	if len(quoted.References) != 1 || quoted.References[0].WordQuote != "\n"+cell || quoted.References[0].SHA256 != digest {
		t.Fatalf("wrong saved Word quote: %+v", quoted.References)
	}
	if stream := send(http.MethodGet, base+"/turns/"+quoted.TurnID+"/stream", "", nil, http.StatusOK).Body.String(); !strings.Contains(stream, `"status":"COMPLETED"`) {
		t.Fatalf("Word quote did not complete: %s", stream)
	}
	send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(selected), http.StatusOK)
	send(http.MethodPost, base+"/turns", "application/json", strings.NewReader(selectedBody(digest, cell)), http.StatusConflict)

	upload("损坏.docx", []byte{0, 1, 2})
	send(http.MethodGet, base+"/materials/"+receipt.MaterialID+"/preview", "", nil, http.StatusUnprocessableEntity)
}

func wordMaterialFixture(t *testing.T, paragraph, cell string) []byte {
	t.Helper()
	var body bytes.Buffer
	archive := zip.NewWriter(&body)
	parts := [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + paragraph + `</w:t><w:br w:type="page"/></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>` + cell + `</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`},
	}
	for _, part := range parts {
		writer, err := archive.Create(part[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, part[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
