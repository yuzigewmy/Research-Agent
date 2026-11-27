package workbench

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectEventsSnapshotCursorSSEIsolationAndBackup(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	projectA, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "ev-proj-a",
		Title:      "项目事件流验证 A",
		TemplateID: "experiment",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectB, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "ev-proj-b",
		Title:      "项目事件流隔离 B",
		TemplateID: "freeform",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Trigger file.saved and task.updated events in project A, and file.saved in project B
	doc1, err := store.saveWorkspaceDocument(ctx, projectA.ProjectID, "notes/plan.md", "", "# 初稿\n正文内容\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, projectB.ProjectID, "other/b.md", "", "隔离内容\n"); err != nil {
		t.Fatal(err)
	}
	proposed, _, err := store.applyTaskRequest(ctx, taskRequest{
		ProjectID:   projectA.ProjectID,
		Action:      proposeTask,
		RequestID:   "ev-task-1",
		Instruction: "整理实验数据",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyTaskRequest(ctx, taskRequest{
		ProjectID:        projectA.ProjectID,
		TaskID:           proposed.TaskID,
		Action:           confirmTask,
		RequestID:        "ev-task-1-confirm",
		ExpectedRevision: proposed.Revision,
	}, true); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimQueuedTask(ctx, projectA.ProjectID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, projectA.ProjectID, claimed, taskSucceeded, "", taskResult{markdown: "# 整理结果\n"}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	eventsURL := func(projectID, query string) string {
		u := "http://127.0.0.1" + projectsPath + "/" + projectID + "/events"
		if query != "" {
			u += "?" + query
		}
		return u
	}

	// 2. JSON snapshot & monotonic sequence verification
	getSnapshot := func(projectID, query, lastEventID string, wantStatus int) ProjectEventsSnapshot {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, eventsURL(projectID, query), nil)
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("GET events (%s, %s) returned %d, want %d: %s", projectID, query, rec.Code, wantStatus, rec.Body)
		}
		var snap ProjectEventsSnapshot
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
		}
		return snap
	}

	getSnapshot("nonexistent-project", "", "", http.StatusNotFound)
	getSnapshot(projectA.ProjectID, "after=-1", "", http.StatusBadRequest)
	getSnapshot(projectA.ProjectID, "limit=201", "", http.StatusBadRequest)

	snap1 := getSnapshot(projectA.ProjectID, "limit=2", "", http.StatusOK)
	if snap1.ProjectID != projectA.ProjectID || snap1.AfterSeq != 0 || len(snap1.Items) != 2 || snap1.LatestSeq != snap1.Items[1].Seq {
		t.Fatalf("unexpected paged snapshot 1: %+v", snap1)
	}
	if snap1.Items[0].Kind != projectEventFileSaved || snap1.Items[0].EntityID != doc1.VersionID {
		t.Fatalf("expected first event file.saved for %s, got %+v", doc1.VersionID, snap1.Items[0])
	}
	if strings.Contains(string(snap1.Items[0].Payload), "正文内容") {
		t.Fatalf("event payload leaked document body: %s", string(snap1.Items[0].Payload))
	}
	snap2 := getSnapshot(projectA.ProjectID, fmt.Sprintf("after=%d&limit=50", snap1.LatestSeq), "", http.StatusOK)
	if len(snap2.Items) != 2 || snap2.Items[0].Seq <= snap1.LatestSeq || snap2.Items[1].Seq <= snap2.Items[0].Seq {
		t.Fatalf("unexpected incremental snapshot 2: %+v", snap2)
	}
	// Verify project B events did not leak into project A
	for _, ev := range append(snap1.Items, snap2.Items...) {
		if strings.Contains(ev.Summary, "other/b.md") {
			t.Fatalf("cross-project event leaked: %+v", ev)
		}
	}

	// Last-Event-ID header cursor continuation with no new events
	emptySnap := getSnapshot(projectA.ProjectID, "", fmt.Sprintf("%d", snap2.LatestSeq), http.StatusOK)
	if len(emptySnap.Items) != 0 || emptySnap.LatestSeq != snap2.LatestSeq || emptySnap.AfterSeq != snap2.LatestSeq {
		t.Fatalf("expected empty incremental snapshot at latestSeq=%d, got %+v", snap2.LatestSeq, emptySnap)
	}

	// 3. SSE streaming mode: sends snapshot + incremental events + live new event, then exits cleanly on cancel
	server := httptest.NewServer(handler)
	defer server.Close()

	sseCtx, sseCancel := context.WithTimeout(ctx, 5*time.Second)
	defer sseCancel()
	sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet,
		server.URL+projectsPath+"/"+projectA.ProjectID+"/events?after="+fmt.Sprintf("%d", snap1.LatestSeq), nil)
	if err != nil {
		t.Fatal(err)
	}
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK || !strings.HasPrefix(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected SSE response: %d %s", sseResp.StatusCode, sseResp.Header.Get("Content-Type"))
	}

	// Save a new document concurrently so live SSE picks it up
	doc2Ch := make(chan WorkspaceDocument, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		d, saveErr := store.saveWorkspaceDocument(ctx, projectA.ProjectID, doc1.Path, doc1.SHA256, "# 第二版\n更新正文\n")
		if saveErr == nil {
			doc2Ch <- d
		}
	}()

	scanner := bufio.NewScanner(sseResp.Body)
	var sawSnapshot bool
	var streamedKinds []string
	var lastStreamedSeq int64
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if currentEvent == "snapshot" {
				sawSnapshot = true
				var snap ProjectEventsSnapshot
				if err := json.Unmarshal([]byte(data), &snap); err != nil || len(snap.Items) != 2 {
					t.Fatalf("invalid SSE snapshot frame: %s (%v)", data, err)
				}
			} else {
				var ev ProjectEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatalf("invalid SSE event frame: %s (%v)", data, err)
				}
				if ev.Seq <= lastStreamedSeq {
					t.Fatalf("non-monotonic SSE seq: prev=%d cur=%d", lastStreamedSeq, ev.Seq)
				}
				lastStreamedSeq = ev.Seq
				streamedKinds = append(streamedKinds, ev.Kind)
				if ev.Kind == projectEventFileSaved {
					sseCancel()
					break
				}
			}
			currentEvent = ""
		}
	}
	doc2 := <-doc2Ch
	if !sawSnapshot || len(streamedKinds) < 3 || streamedKinds[len(streamedKinds)-1] != projectEventFileSaved {
		t.Fatalf("unexpected SSE stream events: sawSnapshot=%v kinds=%v doc2=%s", sawSnapshot, streamedKinds, doc2.VersionID)
	}

	// 4. Backup & restore at v67 preserves project events, and deleting project cleans them up
	var archive bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, t.TempDir(), &archive); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "v67.zip")
	if err := os.WriteFile(backupPath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := RestoreWorkspace(ctx, backupPath, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	restoredSnap, err := restored.readProjectEventsSnapshot(ctx, projectA.ProjectID, 0, 50)
	if err != nil || len(restoredSnap.Items) != 5 || restoredSnap.LatestSeq != lastStreamedSeq {
		t.Fatalf("restored v67 lost project events: %+v (%v)", restoredSnap, err)
	}

	if err := restored.deleteProject(ctx, projectA.ProjectID, deleteProjectInput{
		ExpectedRevision: projectA.Revision,
		ConfirmTitle:     projectA.Title,
	}); err != nil {
		t.Fatalf("delete project with events failed: %v", err)
	}
	var remaining int
	if err := restored.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_events WHERE project_id = ?`, projectA.ProjectID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("project_events not cleaned on delete: count=%d err=%v", remaining, err)
	}

	// 5. Verify v66 -> v67 upgrade preserves existing projects and enables project_events
	v66Dir := filepath.Join(t.TempDir(), "v66")
	if err := os.MkdirAll(v66Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite", filepath.Join(v66Dir, workspaceDatabase))
	if err != nil {
		t.Fatal(err)
	}
	legacyDB.SetMaxOpenConns(1)
	if err := migrate(legacyDB, 66); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO projects (project_id, request_id, title, created_title, template_id, status, revision, created_at)
		VALUES ('p-v66', 'req-v66', '旧库课题', '旧库课题', 'freeform', 'READY', 1, '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(v66Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err := upgraded.saveWorkspaceDocument(ctx, "p-v66", "draft.md", "", "# 升级后保存\n"); err != nil {
		t.Fatal(err)
	}
	upSnap, err := upgraded.readProjectEventsSnapshot(ctx, "p-v66", 0, 50)
	if err != nil || len(upSnap.Items) != 1 || upSnap.Items[0].Kind != projectEventFileSaved {
		t.Fatalf("upgraded v66->v67 failed to record project events: %+v (%v)", upSnap, err)
	}
}
