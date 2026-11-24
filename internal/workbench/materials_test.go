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
	handler := NewHandler(store, nil, HandlerConfig{})
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
	handler = NewHandler(open(), nil, HandlerConfig{})

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

	const pdf = "%PDF-1.7\n"
	var document Material
	uploaded := request(http.MethodPost, base+"?name=notes.PDF", pdf, http.StatusCreated)
	if err := json.Unmarshal(uploaded.Body.Bytes(), &document); err != nil || document.Format != "pdf" {
		t.Fatalf("PDF upload did not return its format: %v, %s", err, uploaded.Body)
	}
	route := base + "/" + document.MaterialID + "/download"
	partial := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+route, nil)
	partial.Header.Set("Range", "bytes=0-4")
	rangeResult := httptest.NewRecorder()
	handler.ServeHTTP(rangeResult, partial)
	if rangeResult.Code != http.StatusPartialContent || rangeResult.Body.String() != "%PDF-" {
		t.Fatalf("PDF range did not match the uploaded bytes: %d %q", rangeResult.Code, rangeResult.Body)
	}
	if original := request(http.MethodGet, route, "", http.StatusOK); original.Body.String() != pdf {
		t.Fatalf("PDF download changed the uploaded bytes: %q", original.Body)
	}
}

func TestMaterialsNameSearchFiltersProjectAndKeepsVersions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	create := func(title string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: title, Title: title, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	first, second := create("采样项目"), create("对照项目")
	handler := NewHandler(store, nil, HandlerConfig{})
	upload := func(projectID, name, body string) Material {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+projectID+"/materials?name="+url.QueryEscape(name), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusCreated {
			t.Fatalf("upload %s: got %d: %s", name, response.Code, response.Body)
		}
		var material Material
		if err := json.Unmarshal(response.Body.Bytes(), &material); err != nil {
			t.Fatal(err)
		}
		return material
	}
	firstVersion := upload(first, "水样记录.md", "第一版水温")
	secondVersion := upload(first, "水样记录.md", "第二版水温")
	other := upload(first, "气温记录.md", "气温")
	foreign := upload(second, "水样记录.md", "其他项目的水温")
	percent := upload(first, "100%.txt", "百分号")
	if firstVersion.MaterialID == secondVersion.MaterialID || firstVersion.Name != secondVersion.Name {
		t.Fatalf("same-name revisions were not kept: %+v %+v", firstVersion, secondVersion)
	}

	get := func(route string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+route, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("GET %s: got %d, want %d: %s", route, response.Code, want, response.Body)
		}
		return response
	}
	type materialPage struct {
		Items      []Material
		NextCursor string
	}
	list := func(route string) materialPage {
		t.Helper()
		var page materialPage
		if err := json.Unmarshal(get(route, http.StatusOK).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	base := projectsPath + "/" + first + "/materials"
	page := list(base + "?q=" + url.QueryEscape("  水样 "))
	if page.NextCursor != "" || len(page.Items) != 2 {
		t.Fatalf("partial name should return both versions: %+v", page.Items)
	}
	found := map[string]Material{}
	for _, item := range page.Items {
		found[item.MaterialID] = item
		if item.Name != "水样记录.md" || item.MaterialID == foreign.MaterialID || item.MaterialID == other.MaterialID {
			t.Fatalf("search returned the wrong material: %+v", item)
		}
	}
	if found[firstVersion.MaterialID].SHA256 == "" || found[secondVersion.MaterialID].SHA256 == "" {
		t.Fatalf("missing revisions: %+v", found)
	}
	bodies := map[string]string{firstVersion.MaterialID: "第一版水温", secondVersion.MaterialID: "第二版水温"}
	for _, item := range []Material{firstVersion, secondVersion} {
		download := get(base+"/"+item.MaterialID+"/download", http.StatusOK)
		kind, params, err := mime.ParseMediaType(download.Header().Get("Content-Disposition"))
		if err != nil || kind != "attachment" || params["filename"] != item.Name || download.Body.String() != bodies[item.MaterialID] {
			t.Fatalf("download did not preserve %s: %s %q", item.Name, download.Header().Get("Content-Disposition"), download.Body)
		}
	}
	if foreignPage := list(projectsPath + "/" + second + "/materials?q=" + url.QueryEscape("水样")); len(foreignPage.Items) != 1 || foreignPage.Items[0].MaterialID != foreign.MaterialID {
		t.Fatalf("other project search leaked or dropped its file: %+v", foreignPage.Items)
	}
	if literal := list(base + "?q=" + url.QueryEscape("%")); len(literal.Items) != 1 || literal.Items[0].MaterialID != percent.MaterialID {
		t.Fatalf("percent sign should match literally: %+v", literal.Items)
	}
	if blank := list(base + "?q=" + url.QueryEscape("   ")); len(blank.Items) != 4 {
		t.Fatalf("blank search should list the whole project: %d", len(blank.Items))
	}
	get(base+"?q="+url.QueryEscape("a\x00b"), http.StatusBadRequest)

	matched, err := store.listMaterials(ctx, first, "记录", 0, 2)
	if err != nil || len(matched) != 2 || !strings.Contains(matched[0].Name, "记录") || !strings.Contains(matched[1].Name, "记录") {
		t.Fatalf("first filtered page: %+v, %v", matched, err)
	}
	rest, err := store.listMaterials(ctx, first, "记录", matched[len(matched)-1].ordinal, 2)
	if err != nil || len(rest) != 1 || rest[0].ordinal >= matched[len(matched)-1].ordinal || !strings.Contains(rest[0].Name, "记录") {
		t.Fatalf("filtered cursor page: %+v, %v", rest, err)
	}
}
