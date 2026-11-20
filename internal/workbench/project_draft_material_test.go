package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestDraftMaterialsStayScopedAndReachTheFinalizedProject(t *testing.T) {
	const (
		oldest      = "The oldest sheet is outside the planning window."
		oldNotes    = "These station notes were superseded."
		newNotes    = "Use the revised paired station notes."
		firstText   = "Twelve monthly observations are available."
		secondText  = "The field team recorded water conductivity."
		thirdText   = "The series includes daily water levels."
		foreignText = "A separate draft has unrelated observations."
		within      = "The last line of the selected excerpt."
		beyond      = "This line is outside the 8 KiB excerpt."
	)
	latestNotes := newNotes + "\n" + strings.Repeat("x", (8<<10)-len(newNotes)-1-len(within)) + within + beyond
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing planning prompt", http.StatusBadRequest)
			return
		}
		var prompt strings.Builder
		for _, message := range input.Messages {
			prompt.WriteString(message.Content)
		}
		for _, text := range []string{newNotes, firstText, secondText, thirdText, within} {
			if !strings.Contains(prompt.String(), text) {
				t.Errorf("planning prompt is missing %q", text)
			}
		}
		for _, text := range []string{oldNotes, oldest, foreignText, within + beyond[:1]} {
			if strings.Contains(prompt.String(), text) {
				t.Errorf("planning prompt included excluded text %q", text)
			}
		}
		const plan = `{"title":"River monitoring","templateId":"experiment","goal":"Compare paired stations.","deliverables":"A field protocol.","question":""}`
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", plan)
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
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, contentType, body string, status int, out any) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		}
		return res.Body.String()
	}
	createDraft := func(id string) projectDraft {
		t.Helper()
		var draft projectDraft
		body := fmt.Sprintf(`{"requestId":%q,"prompt":"Plan a river monitoring study."}`, id)
		request(http.MethodPost, projectDraftsPath, "application/json", body, http.StatusCreated, &draft)
		return draft
	}
	type uploadReceipt struct {
		Draft    projectDraft `json:"draft"`
		Material Material     `json:"material"`
	}
	upload := func(draft projectDraft, id, name, body string, status int) uploadReceipt {
		t.Helper()
		query := url.Values{"name": {name}, "requestId": {id}, "expectedRevision": {strconv.Itoa(draft.Revision)}}
		path := projectDraftsPath + "/" + draft.DraftID + "/materials?" + query.Encode()
		var receipt uploadReceipt
		var out any
		if status == http.StatusCreated || status == http.StatusOK {
			out = &receipt
		}
		request(http.MethodPost, path, "application/octet-stream", body, status, out)
		return receipt
	}

	draft := createDraft("main-draft")
	draftPath := projectDraftsPath + "/" + draft.DraftID
	first := upload(draft, "oldest", "oldest.txt", oldest, http.StatusCreated)
	replay := upload(draft, "oldest", "oldest.txt", oldest, http.StatusOK)
	if first.Draft.Revision != draft.Revision+1 || replay.Draft.Revision != first.Draft.Revision || replay.Material != first.Material {
		t.Fatalf("wrong upload revision or replay: first=%+v replay=%+v", first, replay)
	}
	upload(draft, "oldest", "oldest.txt", "Changed bytes.", http.StatusConflict)
	upload(draft, "stale", "stale.txt", "Stale upload.", http.StatusConflict)

	type original struct {
		Material
		body string
	}
	originals := []original{{first.Material, oldest}}
	draft = first.Draft
	for index, item := range []struct{ name, body string }{
		{"observations.txt", firstText}, {"station notes.txt", oldNotes}, {"conductivity.txt", secondText},
		{"levels.txt", thirdText}, {"station notes.txt", latestNotes}, {"equipment.bin", "\x00\xff"},
	} {
		stored := upload(draft, fmt.Sprintf("upload-%d", index), item.name, item.body, http.StatusCreated)
		if stored.Draft.Revision != draft.Revision+1 {
			t.Fatalf("upload %q did not advance the draft revision", item.name)
		}
		originals = append(originals, original{stored.Material, item.body})
		draft = stored.Draft
	}
	other := createDraft("other-draft")
	otherMaterial := upload(other, "other", "station notes.txt", foreignText, http.StatusCreated).Material
	otherPath := projectDraftsPath + "/" + other.DraftID + "/materials"
	request(http.MethodGet, otherPath+"/"+first.Material.MaterialID+"/download", "", "", http.StatusNotFound, nil)
	request(http.MethodGet, draftPath+"/materials/"+otherMaterial.MaterialID+"/download", "", "", http.StatusNotFound, nil)
	if got := request(http.MethodGet, draftPath+"/materials/"+first.Material.MaterialID+"/download", "", "", http.StatusOK, nil); got != oldest {
		t.Fatalf("draft download changed the original: %q", got)
	}
	var draftList, projectList struct{ Items []Material }
	request(http.MethodGet, draftPath+"/materials?cursor=", "", "", http.StatusOK, &draftList)
	if len(draftList.Items) != len(originals) {
		t.Fatalf("draft list returned %d materials, want %d", len(draftList.Items), len(originals))
	}
	for _, item := range draftList.Items {
		if item.MaterialID == otherMaterial.MaterialID {
			t.Fatal("another draft's material appeared in the list")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("uploading draft materials called the model")
	}

	var planned projectDraft
	planBody := fmt.Sprintf(`{"requestId":"plan","expectedRevision":%d}`, draft.Revision)
	request(http.MethodPost, draftPath+"/plan", "application/json", planBody, http.StatusOK, &planned)
	if planned.Title != "River monitoring" || calls.Load() != 1 {
		t.Fatalf("unexpected planning result: draft=%+v calls=%d", planned, calls.Load())
	}
	var project Project
	finalizeBody := fmt.Sprintf(`{"requestId":"finalize","expectedRevision":%d}`, planned.Revision)
	request(http.MethodPost, draftPath+"/finalize", "application/json", finalizeBody, http.StatusCreated, &project)
	materialsPath := projectsPath + "/" + project.ProjectID + "/materials"
	request(http.MethodGet, materialsPath, "", "", http.StatusOK, &projectList)
	moved := make(map[string]Material, len(projectList.Items))
	for _, item := range projectList.Items {
		moved[item.MaterialID] = item
	}
	if len(moved) != len(originals) {
		t.Fatalf("finalization transferred %d materials, want %d", len(moved), len(originals))
	}
	for _, item := range originals {
		stored := moved[item.MaterialID]
		if stored != item.Material || stored.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(item.body))) {
			t.Fatalf("finalization changed material %q: %+v", item.Name, stored)
		}
		if got := request(http.MethodGet, materialsPath+"/"+item.MaterialID+"/download", "", "", http.StatusOK, nil); got != item.body {
			t.Fatalf("project download changed bytes for %q", item.Name)
		}
	}
	request(http.MethodGet, materialsPath+"/"+otherMaterial.MaterialID+"/download", "", "", http.StatusNotFound, nil)
}
