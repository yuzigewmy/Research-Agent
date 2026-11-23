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
