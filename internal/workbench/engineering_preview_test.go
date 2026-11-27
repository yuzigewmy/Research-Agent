package workbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestEngineeringPreviewPinsVersionAndInvalidatesOldDocuments(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path string, input any, status int, output any) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		if input != nil {
			if err := json.NewEncoder(&body).Encode(input); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+path, &body)
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	createProject := func(requestID string) string {
		t.Helper()
		var project Project
		request(http.MethodPost, projectsPath, createProjectInput{RequestID: requestID, Title: "水位记录仪", TemplateID: "engineering_project"}, http.StatusCreated, &project)
		return projectsPath + "/" + project.ProjectID
	}
	project, other := createProject("prototype"), createProject("other")
	base := project + "/preview"
	save := func(projectPath, path, baseHash, content string) WorkspaceDocument {
		t.Helper()
		var doc WorkspaceDocument
		input := map[string]string{"path": path, "baseSha256": baseHash, "content": content}
		request(http.MethodPut, projectPath+"/files/text", input, http.StatusOK, &doc)
		return doc
	}
	type preview struct {
		PreviewID string        `json:"previewId"`
		File      WorkspaceFile `json:"file"`
	}
	assertInactive := func(method, path string) {
		t.Helper()
		var state struct{ Preview json.RawMessage }
		request(method, path, nil, http.StatusOK, &state)
		if string(state.Preview) != "null" {
			t.Fatalf("expected an explicit null preview from %s %s, got %s", method, path, state.Preview)
		}
	}
	open := func(doc WorkspaceDocument, status int) preview {
		t.Helper()
		var state struct{ Preview *preview }
		request(http.MethodPost, base, map[string]string{"versionId": doc.VersionID}, status, &state)
		if state.Preview == nil || state.Preview.PreviewID == "" || state.Preview.File != doc.WorkspaceFile {
			t.Fatalf("preview did not identify the selected version %s: %+v", doc.VersionID, state.Preview)
		}
		return *state.Preview
	}
	assertDocument := func(path string, doc WorkspaceDocument) {
		t.Helper()
		res := request(http.MethodGet, path, nil, http.StatusOK, nil)
		headers := res.Header()
		mediaType, params, err := mime.ParseMediaType(headers.Get("Content-Type"))
		if err != nil || mediaType != "text/html" || params["charset"] != "utf-8" || headers.Get("Cache-Control") != "no-store" ||
			headers.Get("X-Source-SHA256") != fmt.Sprintf("%x", sha256.Sum256([]byte(doc.Content))) || res.Body.String() != doc.Content {
			t.Fatalf("wrong HTML preview for %s: headers=%v body=%q", doc.VersionID, headers, res.Body.String())
		}
		policy := headers.Get("Content-Security-Policy")
		directives := strings.Split(policy, ";")
		for _, want := range []string{"sandbox allow-scripts", "frame-ancestors 'self'", "default-src 'none'"} {
			if !slices.ContainsFunc(directives, func(directive string) bool { return strings.TrimSpace(directive) == want }) {
				t.Fatalf("missing CSP directive %q: %s", want, policy)
			}
		}
	}

	assertInactive(http.MethodGet, base)
	first := save(project, "demo/index.html", "", "<!doctype html>\r\n<meta charset=\"utf-8\">\r\n<p>初版水位读数</p>\r\n")
	opened := open(first, http.StatusCreated)
	if replay := open(first, http.StatusOK); replay != opened {
		t.Fatalf("opening the same version changed the receipt: %+v", replay)
	}
	firstPath := base + "/" + opened.PreviewID
	request(http.MethodGet, other+"/preview/"+opened.PreviewID+"/document", nil, http.StatusNotFound, nil)

	latest := save(project, first.Path, first.SHA256, "<!doctype html>\n<p>新版水位读数</p>\n")
	secondary := save(project, "demo/help.htm", "", "<!doctype html>\n<p>操作提示</p>\n")
	notes := save(project, "demo/notes.md", "", "# 观测记录\n")
	foreign := save(other, first.Path, "", "<!doctype html>\n<p>其他工程</p>\n")
	var list struct{ Items []WorkspaceFile }
	request(http.MethodGet, project+"/files?kind=html", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || !slices.Contains(list.Items, latest.WorkspaceFile) || !slices.Contains(list.Items, secondary.WorkspaceFile) {
		t.Fatalf("HTML list should contain the current .html and .htm files: %+v", list.Items)
	}
	request(http.MethodPost, base, map[string]string{"versionId": foreign.VersionID}, http.StatusNotFound, nil)
	request(http.MethodPost, base, map[string]string{"versionId": notes.VersionID}, http.StatusUnprocessableEntity, nil)
	assertDocument(firstPath+"/document", first)

	replaced := open(latest, http.StatusCreated)
	if replaced.PreviewID == opened.PreviewID {
		t.Fatal("opening a different version reused the old preview ID")
	}
	request(http.MethodGet, firstPath+"/document", nil, http.StatusNotFound, nil)
	var conflict struct{ Error apiError }
	request(http.MethodDelete, firstPath, nil, http.StatusConflict, &conflict)
	if conflict.Error.Code != "PREVIEW_STATE_CONFLICT" {
		t.Fatalf("wrong stale preview error: %+v", conflict.Error)
	}
	var active struct{ Preview *preview }
	request(http.MethodGet, base, nil, http.StatusOK, &active)
	if active.Preview == nil || *active.Preview != replaced {
		t.Fatalf("wrong active preview after stale close: %+v", active.Preview)
	}
	currentPath := base + "/" + replaced.PreviewID
	assertDocument(currentPath+"/document", latest)
	assertInactive(http.MethodDelete, currentPath)
	request(http.MethodGet, currentPath+"/document", nil, http.StatusNotFound, nil)
	assertInactive(http.MethodGet, base)
}

func TestEngineeringPreviewMultiFileAssetsIsolationAndInvalidation(t *testing.T) {
	dataDir := t.TempDir()
	openStore := func() (*Store, http.Handler) {
		store, err := Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		return store, NewHandler(store, nil, HandlerConfig{})
	}
	store, handler := openStore()
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	req := func(h http.Handler, method, path string, input any, status int, output any) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		if input != nil {
			if err := json.NewEncoder(&body).Encode(input); err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1:8080"+path, &body)
		if input != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, r)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}

	var project Project
	req(handler, http.MethodPost, projectsPath, createProjectInput{
		RequestID:  "multifile-eng",
		Title:      "多文件静态工程",
		TemplateID: "engineering_project",
	}, http.StatusCreated, &project)
	projectBase := projectsPath + "/" + project.ProjectID
	previewBase := projectBase + "/preview"

	save := func(filePath, baseHash, content string) WorkspaceDocument {
		t.Helper()
		var doc WorkspaceDocument
		req(handler, http.MethodPut, projectBase+"/files/text", map[string]string{
			"path": filePath, "baseSha256": baseHash, "content": content,
		}, http.StatusOK, &doc)
		return doc
	}

	entryHTML := save("app/index.html", "", `<!doctype html><html><head><link rel="stylesheet" href="./style.css"></head><body><h1 id="title">初版</h1><script src="./app.js"></script></body></html>`)
	styleV1 := save("app/style.css", "", `body { color: rgb(220, 38, 38); }`)
	scriptV1 := save("app/app.js", "", `document.getElementById('title').textContent = '脚本已执行-V1';`)
	iconV1 := save("app/assets/logo.svg", "", `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"></svg>`)
	dataV1 := save("app/config.json", "", `{"version":"v1"}`)
	noteV1 := save("app/readme.txt", "", `hello v1`)
	// Files that must NOT be exposed by app/index.html preview:
	save("other/secret.txt", "", `sibling directory secret`)
	save("app/notes.md", "", `# unsupported markdown`)

	var started engineeringPreviewSnapshot
	req(handler, http.MethodPost, previewBase, map[string]string{"versionId": entryHTML.VersionID}, http.StatusCreated, &started)
	if started.Preview == nil || len(started.Preview.Assets) != 5 {
		t.Fatalf("expected 5 frozen assets, got %+v", started.Preview)
	}
	previewPath := previewBase + "/" + started.Preview.PreviewID

	// Verify document headers include nosniff and scoped CSP allowing this preview prefix
	docRes := req(handler, http.MethodGet, previewPath+"/document", nil, http.StatusOK, nil)
	if docRes.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff on preview document: %v", docRes.Header())
	}
	wantPrefix := "http://127.0.0.1:8080" + previewPath + "/"
	if !strings.Contains(docRes.Header().Get("Content-Security-Policy"), wantPrefix) {
		t.Fatalf("expected CSP to allow scoped preview prefix %q, got %q", wantPrefix, docRes.Header().Get("Content-Security-Policy"))
	}

	assertAsset := func(h http.Handler, rel, wantCT, wantBody, wantSHA string) {
		t.Helper()
		res := req(h, http.MethodGet, previewPath+"/"+rel, nil, http.StatusOK, nil)
		if res.Header().Get("Content-Type") != wantCT ||
			res.Header().Get("X-Content-Type-Options") != "nosniff" ||
			res.Header().Get("X-Source-SHA256") != wantSHA ||
			res.Body.String() != wantBody {
			t.Fatalf("unexpected asset %s: ct=%q nosniff=%q sha=%q body=%q",
				rel, res.Header().Get("Content-Type"), res.Header().Get("X-Content-Type-Options"), res.Header().Get("X-Source-SHA256"), res.Body.String())
		}
	}

	assertAsset(handler, "style.css", "text/css; charset=utf-8", styleV1.Content, styleV1.SHA256)
	assertAsset(handler, "app.js", "application/javascript; charset=utf-8", scriptV1.Content, scriptV1.SHA256)
	assertAsset(handler, "assets/logo.svg", "image/svg+xml", iconV1.Content, iconV1.SHA256)
	assertAsset(handler, "config.json", "application/json; charset=utf-8", dataV1.Content, dataV1.SHA256)
	assertAsset(handler, "readme.txt", "text/plain; charset=utf-8", noteV1.Content, noteV1.SHA256)

	// Unsupported extension and path traversal must be blocked
	req(handler, http.MethodGet, previewPath+"/notes.md", nil, http.StatusNotFound, nil)
	req(handler, http.MethodGet, previewPath+"/%2e%2e/other/secret.txt", nil, http.StatusBadRequest, nil)

	// Editing CSS/JS and adding a new file after starting preview must NOT mutate the frozen snapshot
	save("app/style.css", styleV1.SHA256, `body { color: rgb(0, 128, 0); }`)
	save("app/app.js", scriptV1.SHA256, `document.getElementById('title').textContent = '脚本已执行-V2';`)
	save("app/later.js", "", `console.log('after freeze')`)
	assertAsset(handler, "style.css", "text/css; charset=utf-8", styleV1.Content, styleV1.SHA256)
	assertAsset(handler, "app.js", "application/javascript; charset=utf-8", scriptV1.Content, scriptV1.SHA256)
	req(handler, http.MethodGet, previewPath+"/later.js", nil, http.StatusNotFound, nil)

	// Reopening the database preserves the frozen multi-file snapshot
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openStore()
	assertAsset(handler, "style.css", "text/css; charset=utf-8", styleV1.Content, styleV1.SHA256)
	assertAsset(handler, "app.js", "application/javascript; charset=utf-8", scriptV1.Content, scriptV1.SHA256)
	req(handler, http.MethodGet, previewPath+"/later.js", nil, http.StatusNotFound, nil)

	// Stopping preview immediately invalidates document and all assets
	req(handler, http.MethodDelete, previewPath, nil, http.StatusOK, nil)
	req(handler, http.MethodGet, previewPath+"/document", nil, http.StatusNotFound, nil)
	req(handler, http.MethodGet, previewPath+"/style.css", nil, http.StatusNotFound, nil)
	req(handler, http.MethodGet, previewPath+"/app.js", nil, http.StatusNotFound, nil)

	// Starting a new preview freezes the updated assets, and archiving the project invalidates them immediately
	entryV2 := save("app/index.html", entryHTML.SHA256, `<!doctype html><html><head><link rel="stylesheet" href="./style.css"></head><body><h1 id="title">新版</h1><script src="./app.js"></script></body></html>`)
	var restarted engineeringPreviewSnapshot
	req(handler, http.MethodPost, previewBase, map[string]string{"versionId": entryV2.VersionID}, http.StatusCreated, &restarted)
	newPath := previewBase + "/" + restarted.Preview.PreviewID
	req(handler, http.MethodGet, newPath+"/later.js", nil, http.StatusOK, nil)

	req(handler, http.MethodPatch, projectBase, map[string]any{
		"requestId": "archive-eng", "expectedRevision": project.Revision, "status": "ARCHIVED",
	}, http.StatusOK, nil)
	req(handler, http.MethodGet, newPath+"/document", nil, http.StatusNotFound, nil)
	req(handler, http.MethodGet, newPath+"/style.css", nil, http.StatusNotFound, nil)
	req(handler, http.MethodGet, newPath+"/app.js", nil, http.StatusNotFound, nil)
}
