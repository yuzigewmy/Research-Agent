package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBackupRestoresVersionsAndRejectsDamagedObjects(t *testing.T) {
	ctx := t.Context()
	source := t.TempDir()
	store, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "sampling", Title: "河流采样", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	const original = "# 河流记录\n\n原始读数。\n"
	const updated = "# 河流记录\n\n完成第二轮。\n"
	material, _, err := store.saveMaterial(ctx, project.ProjectID, "notes.md", strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/main.md", "", original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, project.ProjectID, first.Path, first.SHA256, updated); err != nil {
		t.Fatal(err)
	}
	draft, _, err := store.createProjectDraft(ctx, "draft", "整理下次采样方案。")
	if err != nil {
		t.Fatal(err)
	}
	const draftText = "下次采样安排。"
	_, draftMaterial, _, err := store.saveDraftMaterial(ctx, draft.DraftID, draftChangeInput{
		RequestID: "draft-material", ExpectedRevision: draft.Revision,
	}, "draft.txt", strings.NewReader(draftText))
	if err != nil {
		t.Fatal(err)
	}
	proposal := taskRequest{ProjectID: project.ProjectID, Action: proposeTask, RequestID: "plan", Instruction: "整理采样计划。"}
	planned, _, err := store.applyTaskRequest(ctx, proposal, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyTaskRequest(ctx, taskRequest{
		ProjectID: project.ProjectID, TaskID: planned.TaskID, Action: confirmTask, RequestID: "confirm-plan", ExpectedRevision: planned.Revision,
	}, true); err != nil {
		t.Fatal(err)
	}
	task, err := store.claimQueuedTask(ctx, project.ProjectID, true)
	if err != nil {
		t.Fatal(err)
	}
	const plan = "# 采样计划\n\n按固定时段记录。\n"
	if err := store.finishTask(ctx, project.ProjectID, task, taskSucceeded, "", taskResult{markdown: plan}); err != nil {
		t.Fatal(err)
	}
	orphan := fmt.Sprintf("%x", sha256.Sum256([]byte("unreferenced")))
	if err := os.WriteFile(store.materialObject(orphan), []byte("unreferenced"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/api/v1/workspace-backup", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected private backup download, got %d %v", response.Code, response.Header())
	}
	archive := response.Body.Bytes()
	entries, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	manifestFile, err := entries.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion int
		Objects       []struct{ SHA256 string }
	}
	decodeErr := json.NewDecoder(manifestFile).Decode(&manifest)
	_ = manifestFile.Close()
	if decodeErr != nil || manifest.SchemaVersion != workspaceSchemaVersion || len(manifest.Objects) != 3 || len(entries.File) != 5 {
		t.Fatalf("expected database and three referenced objects: %+v (%v)", manifest, decodeErr)
	}
	root := t.TempDir()
	backupFile := filepath.Join(root, "河流备份.zip")
	if err := os.WriteFile(backupFile, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if err := RestoreWorkspace(ctx, backupFile, dest); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWorkspace(ctx, backupFile, dest); err == nil {
		t.Fatal("restore accepted an existing workspace")
	}
	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	savedProject, err := restored.getProject(ctx, project.ProjectID)
	if err != nil || savedProject != project {
		t.Fatalf("project changed: %+v (%v)", savedProject, err)
	}
	read := func(file WorkspaceFile, want string) {
		t.Helper()
		doc, err := restored.readWorkspaceDocument(file)
		if err != nil || doc.Content != want {
			t.Fatalf("wrong restored version %+v (%v)", doc, err)
		}
	}
	latest, err := restored.workspaceFile(ctx, project.ProjectID, first.Path)
	if err != nil {
		t.Fatal(err)
	}
	read(latest, updated)
	read(first.WorkspaceFile, original)
	savedMaterial, err := restored.getMaterial(ctx, project.ProjectID, material.MaterialID)
	if err != nil || savedMaterial.SHA256 != material.SHA256 {
		t.Fatalf("material reference changed: %+v (%v)", savedMaterial, err)
	}
	savedDraftMaterial, err := restored.draftMaterial(ctx, draft.DraftID, draftMaterial.MaterialID)
	if err != nil || savedDraftMaterial.SHA256 != draftMaterial.SHA256 {
		t.Fatalf("draft material reference changed: %+v (%v)", savedDraftMaterial, err)
	}
	if excerpt, err := restored.previewMaterial(ctx, savedDraftMaterial, 100); err != nil || excerpt.Text != draftText {
		t.Fatalf("draft material was not restored: %+v (%v)", excerpt, err)
	}
	replayed, created, err := restored.applyTaskRequest(ctx, proposal, false)
	if err != nil || created || replayed.TaskID != planned.TaskID || replayed.Status != taskSucceeded {
		t.Fatalf("completed task receipt changed: %+v (%v)", replayed, err)
	}
	if artifact, err := restored.readTaskArtifact(ctx, project.ProjectID, replayed.TaskID); err != nil || artifact.markdown != plan {
		t.Fatalf("completed task artifact changed: %+v (%v)", artifact, err)
	}

	var damaged bytes.Buffer
	writer := zip.NewWriter(&damaged)
	for _, entry := range entries.File {
		if entry.Name == "objects/"+material.SHA256 {
			output, err := writer.CreateHeader(&zip.FileHeader{Name: entry.Name, Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(output, strings.Repeat("!", len(original))); err != nil {
				t.Fatal(err)
			}
		} else if err := writer.Copy(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(root, "damaged.zip")
	if err := os.WriteFile(badFile, damaged.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	badDestination := filepath.Join(root, "damaged")
	if err := RestoreWorkspace(ctx, badFile, badDestination); err == nil || !strings.Contains(err.Error(), material.SHA256) {
		t.Fatalf("damaged object was accepted or not identified: %v", err)
	}
	if _, err := os.Stat(badDestination); !os.IsNotExist(err) {
		t.Fatalf("failed restore published a directory: %v", err)
	}

	upload := func(body []byte, size int64, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/api/v1/workspace-restore", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/zip")
		req.ContentLength = size
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("upload restore: got %d want %d: %s", response.Code, status, response.Body)
		}
		return response
	}
	upload(nil, (1<<30)+1, http.StatusRequestEntityTooLarge)
	upload(damaged.Bytes(), int64(damaged.Len()), http.StatusUnprocessableEntity)
	if copies, err := filepath.Glob(filepath.Join(source, "restored-*")); err != nil || len(copies) != 0 {
		t.Fatalf("failed upload published a restore: %v (%v)", copies, err)
	}
	limitDestination := filepath.Join(root, "database-limit")
	if err := restoreWorkspace(ctx, bytes.NewReader(archive), int64(len(archive)), limitDestination, 1); !errors.Is(err, errRestoreDatabaseSize) {
		t.Fatalf("expected browser database size error, got %v", err)
	}
	if _, err := os.Lstat(limitDestination); !os.IsNotExist(err) {
		t.Fatalf("size check published a restore: %v", err)
	}

	var receipt struct{ Directory string }
	response = upload(archive, int64(len(archive)), http.StatusCreated)
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || filepath.Dir(receipt.Directory) != source ||
		!strings.HasPrefix(filepath.Base(receipt.Directory), "restored-") {
		t.Fatalf("unexpected restore destination: %+v (%v)", receipt, err)
	}
	if stages, err := filepath.Glob(filepath.Join(source, ".research-restore-*")); err != nil || len(stages) != 0 {
		t.Fatalf("upload left staging data: %v (%v)", stages, err)
	}
	copied, err := Open(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copied.Close() })
	version, err := copied.workspaceVersion(ctx, project.ProjectID, first.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if doc, err := copied.readWorkspaceDocument(version); err != nil || doc.Content != original {
		t.Fatalf("browser restore did not preserve the historical version: %+v (%v)", doc, err)
	}
	if current, err := store.workspaceFile(ctx, project.ProjectID, first.Path); err != nil || current != latest {
		t.Fatalf("browser restore changed the active workspace: %+v (%v)", current, err)
	}
}

func TestWorkspaceBackupRestoresSchema50AndOpensWithCurrentVersion(t *testing.T) {
	ctx, root := t.Context(), t.TempDir()
	legacy, err := sql.Open("sqlite", filepath.Join(root, "legacy.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	if err := migrate(legacy, 50); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: legacy, objectsDir: filepath.Join(root, "objects")}
	if err := os.Mkdir(store.objectsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "legacy", Title: "采样历史", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/main.md", "", "# 原始稿件\n")
	if err != nil {
		t.Fatal(err)
	}
	proposal := taskRequest{ProjectID: project.ProjectID, Action: proposeTask, RequestID: "legacy-check",
		Kind: manuscriptCheckTask, Instruction: "核对初稿", InputVersionIDs: []string{doc.VersionID}}
	stored, _, err := store.applyTaskRequest(ctx, proposal, false)
	if err != nil {
		t.Fatal(err)
	}
	const markdown = "# 旧版结构报告\n"
	if _, err := legacy.ExecContext(ctx, `UPDATE tasks SET status = 'SUCCEEDED', markdown = ? WHERE project_id = ? AND task_id = ?`,
		markdown, project.ProjectID, stored.TaskID); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	var archive bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, stage, &archive); err != nil {
		t.Fatal(err)
	}
	archivePath, destination := filepath.Join(root, "legacy.zip"), filepath.Join(root, "restored")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWorkspace(ctx, archivePath, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var version int
	if err := restored.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != workspaceSchemaVersion {
		t.Fatalf("restored schema was not upgraded: %d (%v)", version, err)
	}
	if got, err := restored.getProject(ctx, project.ProjectID); err != nil || got != project {
		t.Fatalf("legacy project changed after upgrade: %+v (%v)", got, err)
	}
	replayed, created, err := restored.applyTaskRequest(ctx, proposal, false)
	if err != nil || created || replayed.TaskID != stored.TaskID || replayed.Kind != manuscriptCheckTask || replayed.Status != taskSucceeded {
		t.Fatalf("legacy task receipt changed after upgrade: %+v (%v)", replayed, err)
	}
	artifact, err := restored.readTaskArtifact(ctx, project.ProjectID, replayed.TaskID)
	if err != nil || artifact.markdown != markdown || artifact.sourceSHA256 != doc.SHA256 {
		t.Fatalf("legacy artifact changed after upgrade: %+v (%v)", artifact, err)
	}
}

func TestWorkspaceBackupRejectsIncompleteDatabase(t *testing.T) {
	ctx, root := t.Context(), t.TempDir()
	databasePath := filepath.Join(root, "incomplete.sqlite3")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE materials (sha256 TEXT, size INTEGER);
		CREATE TABLE workspace_versions (sha256 TEXT, size INTEGER);
		CREATE TABLE project_draft_materials (sha256 TEXT, size INTEGER);
		PRAGMA user_version = %d;`, workspaceSchemaVersion))
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("prepare incomplete database: %v %v", err, closeErr)
	}
	body, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: workspaceDatabase, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(body); err != nil {
		t.Fatal(err)
	}
	entry, err = writer.CreateHeader(&zip.FileHeader{Name: backupManifestName, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	manifest := backupManifest{Version: backupVersion, SchemaVersion: workspaceSchemaVersion,
		Database: backupFile{SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: int64(len(body))}}
	if err := json.NewEncoder(entry).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "incomplete.zip")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err := RestoreWorkspace(ctx, archivePath, destination); err == nil || !strings.Contains(err.Error(), "结构") {
		t.Fatalf("expected an error about the missing workspace schema, got %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("restore published an incomplete database: %v", err)
	}
}

func TestWorkspaceBackupUploadChecksDirectoryRecords(t *testing.T) {
	for _, count := range []int{3, 65535} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			for i := range count {
				if _, err := writer.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("objects/%064x", i), Method: zip.Store}); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data, size := archive.Bytes(), int64(archive.Len())
			if err := validateRestoreDirectory(bytes.NewReader(data), size); err != nil {
				t.Fatalf("directory from the ZIP writer was rejected: %v", err)
			}
			if count == 3 {
				footer := data[len(data)-22:]
				binary.LittleEndian.PutUint16(footer[8:10], 2)
				binary.LittleEndian.PutUint16(footer[10:12], 2)
				if err := validateRestoreDirectory(bytes.NewReader(data), size); !errors.Is(err, zip.ErrFormat) {
					t.Fatalf("expected three records with a count of two to be rejected: %v", err)
				}
			}
		})
	}
}

func TestWorkspaceBackupRejectsInvalidMemberRanges(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{workspaceDatabase, backupManifestName} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	size := int64(archive.Len())
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), size)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backupArchiveEntries(t.Context(), reader, size); err != nil {
		t.Fatalf("regular member ranges were rejected: %v", err)
	}
	readCtx, cancel := context.WithCancel(t.Context())
	cancelable, err := zip.NewReader(restoreReaderAt{readCtx, bytes.NewReader(archive.Bytes())}, size)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backupArchiveEntries(t.Context(), cancelable, size); !errors.Is(err, context.Canceled) {
		t.Fatalf("member header read lost cancellation: %v", err)
	}
	first, second := reader.File[0], reader.File[1]
	start, err := first.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	secondStart, err := second.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	first.CompressedSize64, first.UncompressedSize64 = uint64(secondStart-start+1), uint64(secondStart-start+1)
	if _, err := backupArchiveEntries(t.Context(), reader, size); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("overlapping member ranges were accepted: %v", err)
	}
	first.CompressedSize64, first.UncompressedSize64 = uint64(size), uint64(size)
	if _, err := backupArchiveEntries(t.Context(), reader, size); err == nil || !strings.Contains(err.Error(), "范围无效") {
		t.Fatalf("out-of-bounds member range was accepted: %v", err)
	}
}
