package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestProjectCreationSurvivesReopening(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "project data #1")
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	web := fstest.MapFS{"index.html": {Data: []byte("<title>Workspace</title>")}}
	handler := NewHandler(store, web)
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/?project=p1", nil))
	if index.Code != http.StatusOK || index.Body.String() != "<title>Workspace</title>" {
		t.Fatalf("unexpected index response: %d %s", index.Code, index.Body)
	}

	request := func(method, path, body string, wantStatus int, target any) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != wantStatus {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, wantStatus, res.Body)
		}
		if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}

	const collection = "/api/v1/projects"
	const input = `{"requestId":"req-1","title":"Field notes","templateId":"experiment"}`
	var created, replayed Project
	request(http.MethodPost, collection, input, http.StatusCreated, &created)
	if created.ProjectID == "" || created.Title != "Field notes" || created.TemplateID != "experiment" || created.Status != "READY" || created.Revision != 1 {
		t.Fatalf("unexpected new project: %+v", created)
	}
	if _, err := time.Parse(time.RFC3339, created.CreatedAt); err != nil {
		t.Fatalf("invalid creation timestamp: %v", err)
	}
	request(http.MethodPost, collection, input, http.StatusOK, &replayed)
	if replayed != created {
		t.Fatalf("replay changed the project: %+v", replayed)
	}

	var conflict struct {
		Error struct{ Code, Message string }
	}
	request(http.MethodPost, collection, `{"requestId":"req-1","title":"Other notes","templateId":"experiment"}`, http.StatusConflict, &conflict)
	if conflict.Error.Code == "" || conflict.Error.Message == "" {
		t.Fatalf("expected a structured conflict: %+v", conflict)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler = NewHandler(reopened, nil)

	request(http.MethodPost, collection, input, http.StatusOK, &replayed)
	var fetched Project
	request(http.MethodGet, collection+"/"+created.ProjectID, "", http.StatusOK, &fetched)
	var page struct{ Items []Project }
	request(http.MethodGet, collection, "", http.StatusOK, &page)
	if replayed != created || fetched != created || len(page.Items) != 1 || page.Items[0] != created {
		t.Fatalf("unexpected state after reopening: replay=%+v fetched=%+v items=%+v", replayed, fetched, page.Items)
	}
}
