package workbench

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceHTMLPreviewPinsSourceAndKeepsStaticMarkup(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(id string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := create("preview-project"), create("other-project")
	save := func(path, base, content string) WorkspaceDocument {
		t.Helper()
		file, err := store.saveWorkspaceDocument(ctx, project, path, base, content)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	source := "\uFEFF<!doctype html>\r\n" + `<html lang="zh-CN"><head><meta http-equiv="refresh" content="5"><style>h1{color:navy}</style></head><body>` +
		`<h1 onclick="false">原始图表</h1><script>document.title='交互视图'</script><form><button>提交</button></form>` +
		`<a href="https://example.invalid/">参考站点</a><iframe title="外部页面"></iframe><svg><circle/></svg>` +
		`<img src="https://example.invalid/image.png" alt="外部图"><img src="data:image/png;base64,AA==" alt="内嵌图">` +
		`<table><tr><th scope="col">水位</th></tr><tr><td style="text-align:right">12</td></tr></table></body></html>`
	old := save("report/水位.HTML", "", source)
	latest := save(old.Path, old.SHA256, "<h1>新版图表</h1>")
	handler := NewHandler(store, nil, HandlerConfig{})
	get := func(method, projectID, versionID, action string, status int) *httptest.ResponseRecorder {
		t.Helper()
		route := projectsPath + "/" + projectID + "/files/versions/" + versionID + "/html/" + action
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: %d, want %d: %s", method, route, response.Code, status, response.Body)
		}
		return response
	}
	preview := get(http.MethodGet, project, old.VersionID, "preview", http.StatusOK)
	for _, text := range []string{"<!doctype html>", "<h1>原始图表</h1>", "<style>h1{color:navy}</style>", "参考站点", "data:image/png;base64,AA==", `<th scope="col">水位</th>`} {
		if !strings.Contains(preview.Body.String(), text) {
			t.Errorf("preview omitted %q: %s", text, preview.Body)
		}
	}
	for _, text := range []string{"onclick", "<script", "<iframe", "<form", "http-equiv", "href=", "https://example.invalid", "<svg", "新版图表"} {
		if strings.Contains(preview.Body.String(), text) {
			t.Errorf("preview retained %q: %s", text, preview.Body)
		}
	}
	if headers := preview.Header(); headers.Get("Content-Type") != "text/html; charset=utf-8" ||
		headers.Get("Content-Security-Policy") != workspaceHTMLPolicy || headers.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unexpected preview headers: %v", headers)
	}
	download := get(http.MethodGet, project, old.VersionID, "download", http.StatusOK)
	for _, response := range []*httptest.ResponseRecorder{preview, download} {
		if headers := response.Header(); headers.Get("X-Source-Version-ID") != old.VersionID || headers.Get("X-Source-SHA256") != old.SHA256 {
			t.Fatalf("response lost original version identity: %v", headers)
		}
	}
	typ, params, err := mime.ParseMediaType(download.Header().Get("Content-Disposition"))
	if err != nil || typ != "attachment" || params["filename"] != "水位.HTML" || download.Body.String() != source {
		t.Fatalf("download did not return the original HTML: %v", err)
	}
	if head := get(http.MethodHead, project, latest.VersionID, "preview", http.StatusOK); head.Body.Len() != 0 || head.Header().Get("X-Source-Version-ID") != latest.VersionID {
		t.Fatalf("HEAD lost version identity or wrote a body: %v", head.Header())
	}
	get(http.MethodGet, other, old.VersionID, "preview", http.StatusNotFound)
	get(http.MethodGet, other, old.VersionID, "download", http.StatusNotFound)
	text := save("report/notes.md", "", "备注")
	get(http.MethodGet, project, text.VersionID, "preview", http.StatusUnprocessableEntity)
}
