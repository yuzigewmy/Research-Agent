package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestWordExportKeepsTheSelectedVersion(t *testing.T) {
	python := os.Getenv("WORDEXPORT_TEST_PYTHON")
	if python == "" {
		t.Skip("set WORDEXPORT_TEST_PYTHON to a Python executable with python-docx")
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "paper", Title: "监测稿件", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/监测.md", "", "# 初稿\n\n观测记录。")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.saveWorkspaceDocument(ctx, project.ProjectID, first.Path, first.SHA256, "# 修订稿")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/files/versions/"
	handler := NewHandler(store, nil, HandlerConfig{WordPython: python})
	check := func(method, versionID, suffix string, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(method, base+versionID+"/word"+suffix, nil))
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		return res
	}
	created := check(http.MethodPost, first.VersionID, "", http.StatusOK)
	var receipt wordExport
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil || receipt.SourceSHA256 != first.SHA256 || receipt.Filename != "监测.docx" {
		t.Fatalf("invalid Word export receipt: %v, %+v", err, receipt)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	cached := check(http.MethodPost, first.VersionID, "", http.StatusOK)
	if !bytes.Equal(created.Body.Bytes(), cached.Body.Bytes()) {
		t.Fatalf("repeat export changed the receipt: %s", cached.Body)
	}
	file := check(http.MethodGet, first.VersionID, "/download", http.StatusOK)
	if file.Header().Get("Content-Type") != wordContentType || file.Body.Len() != receipt.Size ||
		fmt.Sprintf("%x", sha256.Sum256(file.Body.Bytes())) != receipt.SHA256 {
		t.Fatal("the downloaded file differs from the saved receipt")
	}
	check(http.MethodGet, second.VersionID, "/download", http.StatusNotFound)
}
