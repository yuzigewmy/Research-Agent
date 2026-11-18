package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMaterialsKeepOriginalBytesAndProjectScope(t *testing.T) {
	directory := t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	create := func(title string) string {
		t.Helper()
		project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: title, Title: title, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	first, second := create("河流记录"), create("其他项目")
	base := projectsPath + "/" + first + "/materials"
	handler := NewHandler(store, nil, nil)
	request := func(method, route, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+route, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, response.Code, want, response.Body)
		}
		return response
	}
	const name, text = "水样记录.md", "# 采样\n记录 a 点水温。\n"
	upload := func(content string, status int) Material {
		t.Helper()
		var material Material
		response := request(http.MethodPost, base+"?name="+url.QueryEscape(name), content, status)
		if err := json.Unmarshal(response.Body.Bytes(), &material); err != nil {
			t.Fatal(err)
		}
		return material
	}
	material := upload(text, http.StatusCreated)
	if material.Name != name || material.Kind != "text" || material.Size != int64(len(text)) || material.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) {
		t.Fatalf("invalid material receipt: %+v", material)
	}
	if upload(text, http.StatusOK).MaterialID != material.MaterialID || upload(text+"第二份", http.StatusCreated).MaterialID == material.MaterialID {
		t.Fatalf("replay or same-name revision changed the wrong record")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, nil)

	item := base + "/" + material.MaterialID
	preview := request(http.MethodGet, item+"/preview", "", http.StatusOK)
	var excerpt struct {
		Text      string
		Truncated bool
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &excerpt); err != nil || excerpt.Text != text || excerpt.Truncated {
		t.Fatalf("preview changed: %v, %s", err, preview.Body.String())
	}
	download := request(http.MethodGet, item+"/download", "", http.StatusOK)
	kind, params, err := mime.ParseMediaType(download.Header().Get("Content-Disposition"))
	if err != nil || kind != "attachment" || params["filename"] != name || download.Body.String() != text {
		t.Fatalf("download did not preserve the original: %s, %q", download.Header(), download.Body)
	}
	request(http.MethodGet, projectsPath+"/"+second+"/materials/"+material.MaterialID+"/download", "", http.StatusNotFound)
	request(http.MethodPost, base+"?name=..%2Fsecret.txt", "data", http.StatusBadRequest)
}
