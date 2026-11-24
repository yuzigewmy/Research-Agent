package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceStorageReportsManagedFileSizes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "流域资料库")
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "create", Title: "采样记录", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	const source = "# 观测记录\n"
	if _, _, err := store.saveMaterial(ctx, project.ProjectID, "记录.md", strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/记录.md", "", source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.objectsDir, ".upload-pending"), []byte("仅用于测试"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "local-settings.txt"), []byte("本机设置样例"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/api/v1/workspace-storage", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("workspace storage response: %d %s", response.Code, response.Body)
	}
	var usage struct {
		Directory     string
		SchemaVersion int
		DatabaseBytes int64
		ObjectBytes   int64
		ObjectCount   int
		TotalBytes    int64
	}
	if err := json.Unmarshal(response.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	var databaseBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(filepath.Join(directory, workspaceDatabase+suffix))
		if err != nil {
			t.Fatal(err)
		}
		databaseBytes += info.Size()
	}
	if usage.Directory != directory || usage.SchemaVersion != workspaceSchemaVersion || usage.DatabaseBytes != databaseBytes ||
		usage.ObjectBytes != int64(len(source)) || usage.ObjectCount != 1 || usage.TotalBytes != databaseBytes+int64(len(source)) {
		t.Fatalf("workspace storage did not measure the database and single stored object: %+v", usage)
	}
}
