package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceConflictAndRestore(t *testing.T) {
	dir := t.TempDir()
	open := func() *Store {
		t.Helper()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s := open()
	project, _, err := s.createProject(context.Background(), createProjectInput{RequestID: "test-project", Title: "河流记录", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + project.ProjectID + "/files"
	handler := NewHandler(s, nil, HandlerConfig{})
	request := func(method, path string, input any, status int, output any) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://127.0.0.1"+base+path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, status, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), output); err != nil {
			t.Fatal(err)
		}
	}
	const name = "paper/main.md"
	save := func(content, baseHash string, status int, output any) {
		t.Helper()
		request(http.MethodPut, "/text", map[string]string{"path": name, "content": content, "baseSha256": baseHash}, status, output)
	}
	var first, second, restored, current WorkspaceDocument
	save("# 初稿\n水温为 18°C。", "", http.StatusOK, &first)
	save("# 修订稿\n水温为 19°C。", first.SHA256, http.StatusOK, &second)
	var rejected struct{ Error apiError }
	save("另一页里的草稿", first.SHA256, http.StatusConflict, &rejected)
	if rejected.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("unexpected conflict: %+v", rejected)
	}
	request(http.MethodPost, "/versions/"+first.VersionID+"/restore", map[string]string{"baseSha256": second.SHA256}, http.StatusOK, &restored)
	if restored.VersionID == first.VersionID || restored.Content != first.Content || restored.SHA256 != first.SHA256 {
		t.Fatalf("restore should publish the first content as a new version: %+v", restored)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{})
	request(http.MethodGet, "/text?path=paper%2Fmain.md", nil, http.StatusOK, &current)
	var versions struct{ Items []WorkspaceFile }
	request(http.MethodGet, "/versions?path=paper%2Fmain.md", nil, http.StatusOK, &versions)
	if current.VersionID != restored.VersionID || len(versions.Items) != 3 || versions.Items[1].VersionID != second.VersionID {
		t.Fatalf("reopening changed current or historical versions: %+v, %+v", current, versions)
	}
}
