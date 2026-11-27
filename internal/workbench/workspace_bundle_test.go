package workbench

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestWorkspaceBundleKeepsSelectedVersions(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(key string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: key, Title: key, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := create("水位记录"), create("其他项目")
	save := func(projectID, path, base, content string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, projectID, path, base, content)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	old := save(project, "paper/原稿.MD", "", "\ufeff# 初稿\r\n原始读数\r\n")
	latest := save(project, old.Path, old.SHA256, "# 新稿\n复核读数\n")
	foreign := save(other, old.Path, "", "# 其他项目")
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(ids []string, status int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string][]string{"versionIds": ids})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+project+"/files/bundle", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("bundle returned %d, want %d: %s", res.Code, status, res.Body)
		}
		return res
	}
	ids := []string{latest.VersionID, old.VersionID}
	res := request(ids, http.StatusOK)
	if res.Header().Get("Content-Type") != "application/zip" || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected download headers: %v", res.Header())
	}
	archive, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 3 {
		t.Fatalf("expected two saved files and a manifest, got %d entries", len(archive.File))
	}
	read := func(name string) []byte {
		t.Helper()
		f, err := archive.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var manifest workspaceBundleManifest
	if err := json.Unmarshal(read("manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ProjectID != project || len(manifest.Files) != 2 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	for i, doc := range []WorkspaceDocument{latest, old} {
		entry := manifest.Files[i]
		if entry.Name != []string{"files/01.md", "files/02.md"}[i] || entry.VersionID != doc.VersionID ||
			entry.Path != doc.Path || entry.SHA256 != doc.SHA256 || entry.Size != doc.Size || string(read(entry.Name)) != doc.Content {
			t.Fatalf("ZIP did not retain selected version: %+v", entry)
		}
	}
	request([]string{old.VersionID, old.VersionID}, http.StatusBadRequest)
	request([]string{old.VersionID, foreign.VersionID}, http.StatusNotFound)
	request(make([]string, workspaceBundleLimit+1), http.StatusBadRequest)
	if err := os.WriteFile(store.materialObject(old.SHA256), bytes.Repeat([]byte{'x'}, old.Size), 0600); err != nil {
		t.Fatal(err)
	}
	if failed := request(ids, http.StatusInternalServerError); failed.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("corrupt source yielded a partial ZIP: %v", failed.Header())
	}
}
