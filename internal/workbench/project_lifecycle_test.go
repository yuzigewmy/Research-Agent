package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestProjectLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path, body string, status int, target any) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
			t.Fatalf("%s %s: invalid JSON: %v", method, path, err)
		}
	}
	type observedProject struct {
		ProjectID       string `json:"projectId"`
		Title           string `json:"title"`
		DisplayName     string `json:"displayName"`
		Status          string `json:"status"`
		Revision        int    `json:"revision"`
		DefaultTurnMode string `json:"defaultTurnMode"`
	}
	var renamed, archived, restored, current observedProject
	var created struct {
		observedProject
		DisplayName *string `json:"displayName"`
	}
	request(http.MethodPost, projectsPath, `{"requestId":"create","title":"河流基线","templateId":"experiment"}`, http.StatusCreated, &created)
	if created.DisplayName == nil || *created.DisplayName != "" || created.DefaultTurnMode != "CHAT" {
		t.Fatalf("unexpected project defaults: %+v", created)
	}
	path := projectsPath + "/" + created.ProjectID
	const rename = `{"requestId":"rename","expectedRevision":1,"displayName":"  河流_监测  ","defaultTurnMode":"CONFIRM"}`
	request(http.MethodPatch, path, rename, http.StatusOK, &renamed)
	if renamed.Title != created.Title || renamed.DisplayName != "河流_监测" || renamed.Status != "READY" ||
		renamed.Revision != 2 || renamed.DefaultTurnMode != "CONFIRM" {
		t.Fatalf("unexpected renamed project: %+v", renamed)
	}

	assertList := func(query string, expected ...observedProject) {
		t.Helper()
		var page struct{ Items []observedProject }
		request(http.MethodGet, projectsPath+"?"+query, "", http.StatusOK, &page)
		if page.Items == nil || !slices.Equal(page.Items, expected) {
			t.Fatalf("query %s: got %+v, want %+v", query, page.Items, expected)
		}
	}
	assertList("q="+url.QueryEscape(" 基线 "), renamed)
	assertList("q=_", renamed)
	assertList("q=%25")

	const archive = `{"requestId":"archive","expectedRevision":2,"status":"ARCHIVED"}`
	request(http.MethodPatch, path, archive, http.StatusOK, &archived)
	if archived.Status != "ARCHIVED" || archived.Revision != 3 || archived.DisplayName != renamed.DisplayName {
		t.Fatalf("unexpected archived project: %+v", archived)
	}
	assertList("")
	assertList("status=ARCHIVED&q=_", archived)
	request(http.MethodGet, path, "", http.StatusOK, &current)
	if current != archived {
		t.Fatalf("archived project is not accessible: %+v", current)
	}

	var conflict struct{ Error apiError }
	request(http.MethodPatch, path, `{"requestId":"archive","expectedRevision":2,"status":"READY"}`, http.StatusConflict, &conflict)
	if conflict.Error.Code != "REQUEST_ID_CONFLICT" {
		t.Fatalf("wrong request-ID conflict: %+v", conflict.Error)
	}
	request(http.MethodPatch, path, `{"requestId":"restore","expectedRevision":2,"status":"READY"}`, http.StatusConflict, &conflict)
	if conflict.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("wrong revision conflict: %+v", conflict.Error)
	}
	request(http.MethodPatch, path, `{"requestId":"restore","expectedRevision":3,"status":"READY","displayName":""}`, http.StatusOK, &restored)
	if restored.Status != "READY" || restored.DisplayName != "" || restored.Title != created.Title || restored.Revision != 4 {
		t.Fatalf("unexpected restored project: %+v", restored)
	}
	assertList("", restored)
	assertList("status=ARCHIVED")
	assertList("q=_")

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{})
	request(http.MethodPatch, path, archive, http.StatusOK, &current)
	if current != restored || current.DefaultTurnMode != "CONFIRM" {
		t.Fatalf("project settings did not survive reopening: %+v", current)
	}
	request(http.MethodPatch, path, strings.Replace(rename, "CONFIRM", "CHAT", 1), http.StatusConflict, &conflict)
	if conflict.Error.Code != "REQUEST_ID_CONFLICT" {
		t.Fatalf("wrong mode replay conflict: %+v", conflict.Error)
	}

	request(http.MethodPatch, path, `{"requestId":"empty-title","expectedRevision":4,"title":" "}`, http.StatusBadRequest, &conflict)
	request(http.MethodPatch, path, `{"requestId":"revise-title","expectedRevision":3,"title":"流域水温观测"}`, http.StatusConflict, &conflict)
	if conflict.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("wrong stale title error: %+v", conflict.Error)
	}
	const reviseTitle = `{"requestId":"revise-title","expectedRevision":4,"title":"  流域水温观测  "}`
	var titled observedProject
	request(http.MethodPatch, path, reviseTitle, http.StatusOK, &titled)
	if titled.Title != "流域水温观测" || titled.DisplayName != restored.DisplayName || titled.Revision != 5 {
		t.Fatalf("unexpected updated title: %+v", titled)
	}
	request(http.MethodPatch, path, reviseTitle, http.StatusOK, &current)
	if current != titled {
		t.Fatalf("title request replay changed the project: %+v", current)
	}
	request(http.MethodPatch, path, strings.Replace(reviseTitle, "流域水温观测", "对照实验", 1), http.StatusConflict, &conflict)
	request(http.MethodPost, projectsPath, `{"requestId":"create","title":"河流基线","templateId":"experiment"}`, http.StatusOK, &current)
	if current != titled {
		t.Fatalf("original creation request did not recover the renamed project: %+v", current)
	}
	request(http.MethodPost, projectsPath, `{"requestId":"create","title":"流域水温观测","templateId":"experiment"}`, http.StatusConflict, &conflict)
	assertList("q="+url.QueryEscape(titled.Title), titled)
	assertList("q=" + url.QueryEscape(created.Title))
}
