package workbench

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestWave3BWorkflowStepOrder(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mu sync.Mutex
	var executed []string
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		stepNum := len(executed) + 1
		if len(body.Messages) > 0 {
			executed = append(executed, body.Messages[len(body.Messages)-1].Content)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": fmt.Sprintf("# 步骤 %d 产物\n\n## 摘要\n\n内容。\n\n## 结论\n\n完成。\n", stepNum)}},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	t.Cleanup(mockLLM.Close)

	client, err := inference.New(inference.Config{
		BaseURL: mockLLM.URL,
		APIKey:  "test-key",
		Model:   "stub-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: client})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "w3b-order-proj",
		Title:      "自定义步骤顺序测试",
		TemplateID: "full_research",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Violating hard dependency: placing manuscript before experiment must fail with 422 INVALID_STEP_ORDER.
	badOrderBody, _ := json.Marshal(map[string]any{
		"requestId":   "w3b-order-bad-1",
		"checkpoints": false,
		"stepOrder":   []string{"boundary", "literature", "contract", "manuscript", "experiment", "check"},
	})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workflow", bytes.NewReader(badOrderBody))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for hard-dep violation, got %d: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "INVALID_STEP_ORDER") {
		t.Fatalf("expected INVALID_STEP_ORDER error code, got: %s", res.Body)
	}

	// 2. Duplicate/incomplete stepOrder must fail with 422 INVALID_STEP_ORDER.
	dupOrderBody, _ := json.Marshal(map[string]any{
		"requestId":   "w3b-order-bad-2",
		"checkpoints": false,
		"stepOrder":   []string{"boundary", "boundary", "contract", "experiment", "manuscript", "check"},
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workflow", bytes.NewReader(dupOrderBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "INVALID_STEP_ORDER") {
		t.Fatalf("expected 422 INVALID_STEP_ORDER for duplicate key, got %d: %s", res.Code, res.Body)
	}

	// 3. Valid custom order swapping peer steps: literature before boundary; run through boundary.
	customOrder := []string{"literature", "boundary", "contract", "experiment", "manuscript", "check"}
	goodOrderBody, _ := json.Marshal(map[string]any{
		"requestId":   "w3b-order-good-1",
		"checkpoints": false,
		"stepOrder":   customOrder,
		"throughStep": "boundary",
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workflow", bytes.NewReader(goodOrderBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid custom stepOrder, got %d: %s", res.Code, res.Body)
	}
	var started WorkflowRun
	if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(started.StepOrder, customOrder) {
		t.Fatalf("expected stepOrder %v, got %v", customOrder, started.StepOrder)
	}
	if len(started.Steps) != 6 || started.Steps[0].Key != "literature" || started.Steps[1].Key != "boundary" {
		t.Fatalf("steps not returned in custom order: %+v", started.Steps)
	}
	if started.Scope.StartStep != "literature" || started.Scope.EndStep != "boundary" {
		t.Fatalf("unexpected scope for custom order: %+v", started.Scope)
	}

	// Wait for range completion (literature -> boundary).
	deadline := time.Now().Add(5 * time.Second)
	var latest WorkflowRun
	for time.Now().Before(deadline) {
		getRes := httptest.NewRecorder()
		handler.ServeHTTP(getRes, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workflow/runs/"+started.RunID, nil))
		if getRes.Code != http.StatusOK {
			t.Fatalf("GET run failed: %d %s", getRes.Code, getRes.Body)
		}
		if err := json.Unmarshal(getRes.Body.Bytes(), &latest); err != nil {
			t.Fatal(err)
		}
		if latest.Status == workflowRangeCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if latest.Status != workflowRangeCompleted {
		t.Fatalf("expected RANGE_COMPLETED, got %s", latest.Status)
	}
	if !slices.Equal(latest.StepOrder, customOrder) || latest.Steps[0].Key != "literature" || latest.Steps[0].Status != taskSucceeded ||
		latest.Steps[1].Key != "boundary" || latest.Steps[1].Status != taskSucceeded || latest.Steps[2].Status != "PENDING" {
		t.Fatalf("custom order execution/history mismatch: %+v", latest.Steps)
	}

	// Verify artifact download by step key works under custom order.
	artRes := httptest.NewRecorder()
	handler.ServeHTTP(artRes, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workflow/"+started.RunID+"/steps/literature/artifact", nil))
	if artRes.Code != http.StatusOK || !strings.Contains(artRes.Body.String(), "步骤 1 产物") {
		t.Fatalf("expected literature step to hold first artifact, got %d: %s", artRes.Code, artRes.Body)
	}
}

func TestWave3BBatchVersionRestoreAndSnapshots(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "w3b-restore-1", Title: "整段恢复项目", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := store.createProject(ctx, createProjectInput{RequestID: "w3b-restore-2", Title: "隔离项目", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	otherDoc, err := store.saveWorkspaceDocument(ctx, other.ProjectID, "paper/a.md", "", "other project")
	if err != nil {
		t.Fatal(err)
	}

	aV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/a.md", "", "# A v1\nline 2\n")
	if err != nil {
		t.Fatal(err)
	}
	aV2, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/a.md", aV1.SHA256, "# A v2\nline 2\nline 3\n")
	if err != nil {
		t.Fatal(err)
	}
	bV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/b.md", "", "# B v1\n")
	if err != nil {
		t.Fatal(err)
	}
	bV2, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/b.md", bV1.SHA256, "# B v2\nchanged content\n")
	if err != nil {
		t.Fatal(err)
	}

	// Cross-project version must be rejected with 404.
	crossBody, _ := json.Marshal(map[string]any{
		"items": []map[string]string{{"path": "paper/a.md", "targetVersionId": otherDoc.VersionID}},
	})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workspace/restore-preview", bytes.NewReader(crossBody))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project restore preview, got %d: %s", res.Code, res.Body)
	}

	// Preview batch restore of both files back to v1.
	previewBody, _ := json.Marshal(map[string]any{
		"items": []map[string]string{
			{"path": "paper/a.md", "targetVersionId": aV1.VersionID},
			{"path": "paper/b.md", "targetVersionId": bV1.VersionID},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workspace/restore-preview", bytes.NewReader(previewBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("restore-preview failed: %d %s", res.Code, res.Body)
	}
	var previewRes struct {
		Items []workspaceRestorePreviewItem `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &previewRes); err != nil {
		t.Fatal(err)
	}
	if len(previewRes.Items) != 2 || !previewRes.Items[0].Changed || !previewRes.Items[1].Changed ||
		previewRes.Items[0].CurrentSHA256 != aV2.SHA256 || previewRes.Items[1].CurrentSHA256 != bV2.SHA256 {
		t.Fatalf("unexpected preview response: %+v", previewRes.Items)
	}

	// Stale baseline on second file must fail atomically with 409 VERSION_CONFLICT.
	conflictBody, _ := json.Marshal(map[string]any{
		"items": []map[string]string{
			{"path": "paper/a.md", "targetVersionId": aV1.VersionID, "expectedCurrentSha256": aV2.SHA256},
			{"path": "paper/b.md", "targetVersionId": bV1.VersionID, "expectedCurrentSha256": bV1.SHA256},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workspace/restore-batch", bytes.NewReader(conflictBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "VERSION_CONFLICT") {
		t.Fatalf("expected 409 VERSION_CONFLICT, got %d: %s", res.Code, res.Body)
	}
	// Verify paper/a.md was NOT modified on conflict (atomic transaction).
	stillA, err := store.workspaceFile(ctx, proj.ProjectID, "paper/a.md")
	if err != nil || stillA.VersionID != aV2.VersionID {
		t.Fatalf("expected paper/a.md unchanged after conflict, got %+v err=%v", stillA, err)
	}

	// Execute valid batch restore.
	batchBody, _ := json.Marshal(map[string]any{
		"items": []map[string]string{
			{"path": "paper/a.md", "targetVersionId": aV1.VersionID, "expectedCurrentSha256": aV2.SHA256},
			{"path": "paper/b.md", "targetVersionId": bV1.VersionID, "expectedCurrentSha256": bV2.SHA256},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workspace/restore-batch", bytes.NewReader(batchBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("restore-batch failed: %d %s", res.Code, res.Body)
	}
	var restored workspaceRestoreBatchResult
	if err := json.Unmarshal(res.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.BeforeSnapshots) != 2 || len(restored.RestoredFiles) != 2 {
		t.Fatalf("unexpected restore-batch result: %+v", restored)
	}
	if restored.BeforeSnapshots[0].VersionID != aV2.VersionID || restored.RestoredFiles[0].SHA256 != aV1.SHA256 {
		t.Fatalf("unexpected snapshot/restored state for a.md: %+v", restored)
	}
	curA, err := store.workspaceFile(ctx, proj.ProjectID, "paper/a.md")
	if err != nil || curA.SHA256 != aV1.SHA256 {
		t.Fatalf("expected paper/a.md at v1 sha, got %+v err=%v", curA, err)
	}

	// Rollback using beforeSnapshots!
	rollbackBody, _ := json.Marshal(map[string]any{
		"items": []map[string]string{
			{"path": "paper/a.md", "targetVersionId": restored.BeforeSnapshots[0].VersionID, "expectedCurrentSha256": restored.RestoredFiles[0].SHA256},
			{"path": "paper/b.md", "targetVersionId": restored.BeforeSnapshots[1].VersionID, "expectedCurrentSha256": restored.RestoredFiles[1].SHA256},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/workspace/restore-batch", bytes.NewReader(rollbackBody))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("rollback restore-batch failed: %d %s", res.Code, res.Body)
	}
	rolledA, _ := store.workspaceFile(ctx, proj.ProjectID, "paper/a.md")
	rolledB, _ := store.workspaceFile(ctx, proj.ProjectID, "paper/b.md")
	if rolledA.SHA256 != aV2.SHA256 || rolledB.SHA256 != bV2.SHA256 {
		t.Fatalf("rollback failed to restore v2 contents: a=%s b=%s", rolledA.SHA256, rolledB.SHA256)
	}
}

func TestWave3BEditHistoryFilterAndUndo(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "w3b-hist-1", Title: "编辑历史项目", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	aV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/a.md", "", "原始第一段。\n")
	if err != nil {
		t.Fatal(err)
	}
	aV2, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/a.md", aV1.SHA256, "修订第一段。\n")
	if err != nil {
		t.Fatal(err)
	}
	bV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/b.md", "", "原始第二段。\n")
	if err != nil {
		t.Fatal(err)
	}
	bV2, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/b.md", bV1.SHA256, "批量修订第二段。\n")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(createdAtLayout)
	selJSON, _ := json.Marshal(EditSuggestion{Replacement: "修订第一段。", Reason: "润色表述"})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, status, created_at, markdown, selection_edit)
		VALUES (?, 'edit-sel-1', '润色第一段', 'SUCCEEDED', ?, ?, 1)`, proj.ProjectID, now, string(selJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO selection_edits (project_id, task_id, request_id, source_version_id, start_byte, end_byte, original, applied_version_id)
		VALUES (?, 'edit-sel-1', 'req-sel-1', ?, 0, 15, '原始第一段。', ?)`, proj.ProjectID, aV1.VersionID, aV2.VersionID); err != nil {
		t.Fatal(err)
	}

	// Unapplied edit on paper/b.md.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, status, created_at, markdown, selection_edit)
		VALUES (?, 'edit-sel-2', '待应用润色', 'SUCCEEDED', ?, ?, 1)`, proj.ProjectID, now, string(selJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO selection_edits (project_id, task_id, request_id, source_version_id, start_byte, end_byte, original)
		VALUES (?, 'edit-sel-2', 'req-sel-2', ?, 0, 15, '原始第二段。')`, proj.ProjectID, bV1.VersionID); err != nil {
		t.Fatal(err)
	}

	// Applied batch edit on paper/b.md.
	batchMapJSON, _ := json.Marshal(map[string]EditSuggestion{
		bV1.VersionID: {Replacement: "批量修订第二段。", Reason: "批量统一术语"},
	})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, status, created_at, markdown, selection_edit)
		VALUES (?, 'edit-batch-1', '批量改稿', 'SUCCEEDED', ?, ?, 1)`, proj.ProjectID, now, string(batchMapJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO batch_selection_edits (project_id, task_id, source_version_id, position, start_byte, end_byte, original, applied_version_id)
		VALUES (?, 'edit-batch-1', ?, 0, 0, 15, '原始第二段。', ?)`, proj.ProjectID, bV1.VersionID, bV2.VersionID); err != nil {
		t.Fatal(err)
	}

	// Filter by path.
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history?path="+url.QueryEscape("paper/a"), nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET edit-history failed: %d %s", res.Code, res.Body)
	}
	var page EditHistoryPage
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].EditID != "edit-sel-1" || page.Items[0].Status != "APPLIED" || page.Items[0].Reason != "润色表述" {
		t.Fatalf("unexpected path-filtered edit history: %+v", page)
	}

	// Filter by status=APPLIED with limit=1.
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history?status=APPLIED&limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET edit-history status filter failed: %d %s", res.Code, res.Body)
	}
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.HasMore || page.Items[0].Status != "APPLIED" {
		t.Fatalf("unexpected status-filtered paged edit history: %+v", page)
	}

	// Undoing an unapplied edit fails with 409 EDIT_NOT_APPLIED.
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history/edit-sel-2/undo", strings.NewReader(`{}`)))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "EDIT_NOT_APPLIED") {
		t.Fatalf("expected 409 EDIT_NOT_APPLIED, got %d: %s", res.Code, res.Body)
	}

	// Undo applied selection edit on paper/a.md.
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history/edit-sel-1/undo", strings.NewReader(`{}`)))
	if res.Code != http.StatusOK {
		t.Fatalf("undo selection edit failed: %d %s", res.Code, res.Body)
	}
	var undoRes EditHistoryUndoResult
	if err := json.Unmarshal(res.Body.Bytes(), &undoRes); err != nil {
		t.Fatal(err)
	}
	if undoRes.RestoredVersion.SHA256 != aV1.SHA256 || undoRes.BeforeSnapshot.VersionID != aV2.VersionID {
		t.Fatalf("unexpected undo result: %+v", undoRes)
	}
	curA, err := store.workspaceFile(ctx, proj.ProjectID, "paper/a.md")
	if err != nil || curA.SHA256 != aV1.SHA256 {
		t.Fatalf("expected paper/a.md restored to v1 content, got %+v err=%v", curA, err)
	}

	// Advance paper/b.md to v3, then verify non-forced undo conflicts and forced undo succeeds.
	if _, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/b.md", bV2.SHA256, "后续人工修订。\n"); err != nil {
		t.Fatal(err)
	}
	batchEditID := url.PathEscape("edit-batch-1:" + bV1.VersionID)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history/"+batchEditID+"/undo", strings.NewReader(`{}`)))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "VERSION_CONFLICT") {
		t.Fatalf("expected 409 VERSION_CONFLICT when newer version exists, got %d: %s", res.Code, res.Body)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/edit-history/"+batchEditID+"/undo", strings.NewReader(`{"force":true}`)))
	if res.Code != http.StatusOK {
		t.Fatalf("forced undo batch edit failed: %d %s", res.Code, res.Body)
	}
	curB, _ := store.workspaceFile(ctx, proj.ProjectID, "paper/b.md")
	if curB.SHA256 != bV1.SHA256 {
		t.Fatalf("expected paper/b.md restored to v1 content, got %+v", curB)
	}
}

func TestWave3BStructuredPreview(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "w3b-prev-1", Title: "结构化预览项目", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. ZIP with a safe file and a path-traversal entry.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f1, _ := zw.Create("src/model.py")
	_, _ = f1.Write([]byte("print('ok')\n"))
	f2, _ := zw.Create("../escape.txt")
	_, _ = f2.Write([]byte("bad"))
	_ = zw.Close()

	zipMat, _, err := store.saveMaterial(ctx, proj.ProjectID, "bundle.zip", bytes.NewReader(zipBuf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/materials/"+zipMat.MaterialID+"/structured-preview", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("ZIP structured-preview failed: %d %s", res.Code, res.Body)
	}
	var zipPrev MaterialStructuredPreview
	if err := json.Unmarshal(res.Body.Bytes(), &zipPrev); err != nil {
		t.Fatal(err)
	}
	if zipPrev.Kind != "archive" || zipPrev.Format != "zip" || zipPrev.TotalEntries != 2 || zipPrev.UnsafeCount != 1 {
		t.Fatalf("unexpected zip structured preview: %+v", zipPrev)
	}
	if zipPrev.Entries[0].Blocked || !zipPrev.Entries[1].Blocked {
		t.Fatalf("expected second zip entry blocked for path traversal: %+v", zipPrev.Entries)
	}

	// 2. tar.gz archive preview.
	var tgzBuf bytes.Buffer
	gzw := gzip.NewWriter(&tgzBuf)
	tw := tar.NewWriter(gzw)
	body := []byte("hello tar")
	_ = tw.WriteHeader(&tar.Header{Name: "notes/readme.md", Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.WriteHeader(&tar.Header{Name: "../../secret.txt", Mode: 0o644, Size: 3})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gzw.Close()

	tgzMat, _, err := store.saveMaterial(ctx, proj.ProjectID, "src.tar.gz", bytes.NewReader(tgzBuf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/materials/"+tgzMat.MaterialID+"/structured-preview", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("tar.gz structured-preview failed: %d %s", res.Code, res.Body)
	}
	var tgzPrev MaterialStructuredPreview
	if err := json.Unmarshal(res.Body.Bytes(), &tgzPrev); err != nil {
		t.Fatal(err)
	}
	if tgzPrev.Kind != "archive" || tgzPrev.Format != "tar.gz" || tgzPrev.UnsafeCount != 1 {
		t.Fatalf("unexpected tar.gz structured preview: %+v", tgzPrev)
	}

	// 3. LaTeX (.tex) preview.
	texContent := "\\documentclass{article}\n\\begin{document}\nHello LaTeX\n\\end{document}\n"
	texMat, _, err := store.saveMaterial(ctx, proj.ProjectID, "template.tex", strings.NewReader(texContent))
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/materials/"+texMat.MaterialID+"/structured-preview", nil))
	if res.Code != http.StatusOK {
		t.Fatalf(".tex structured-preview failed: %d %s", res.Code, res.Body)
	}
	var texPrev MaterialStructuredPreview
	if err := json.Unmarshal(res.Body.Bytes(), &texPrev); err != nil {
		t.Fatal(err)
	}
	if texPrev.Kind != "code" || texPrev.Language != "latex" || len(texPrev.Lines) < 4 || texPrev.Lines[0].Number != 1 {
		t.Fatalf("unexpected .tex structured preview: %+v", texPrev)
	}

	// 4. Python (.py) with 220 lines must truncate to 200 lines.
	var pyBuilder strings.Builder
	for i := 1; i <= 220; i++ {
		fmt.Fprintf(&pyBuilder, "x_%d = %d\n", i, i)
	}
	pyMat, _, err := store.saveMaterial(ctx, proj.ProjectID, "train.py", strings.NewReader(pyBuilder.String()))
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/materials/"+pyMat.MaterialID+"/structured-preview", nil))
	if res.Code != http.StatusOK {
		t.Fatalf(".py structured-preview failed: %d %s", res.Code, res.Body)
	}
	var pyPrev MaterialStructuredPreview
	if err := json.Unmarshal(res.Body.Bytes(), &pyPrev); err != nil {
		t.Fatal(err)
	}
	if pyPrev.Kind != "code" || pyPrev.Language != "python" || !pyPrev.Truncated || len(pyPrev.Lines) != 200 || pyPrev.Lines[199].Number != 200 {
		t.Fatalf("unexpected .py structured preview: len=%d truncated=%v lang=%s", len(pyPrev.Lines), pyPrev.Truncated, pyPrev.Language)
	}
}

func TestWave3BEvidenceImpactAndMissingEvidence(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "w3b-ev-1", Title: "证据影响与缺证据测试", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}

	covered, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/covered.md", "", "# 有证据稿件\n\n核心论点提升 2.0。\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/uncovered.md", "", "# 缺证据稿件\n\n未绑定任何来源。\n"); err != nil {
		t.Fatal(err)
	}
	csvV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "data/metrics.csv", "", "a,b\n1,3\n")
	if err != nil {
		t.Fatal(err)
	}
	pdfV1, _, err := store.saveMaterial(ctx, proj.ProjectID, "evidence.pdf", strings.NewReader("%PDF-1.4\nv1"))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(createdAtLayout)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, kind, inputs_json, result_json)
		VALUES (?, 'task-ev-comp', 'compare', 1, 'SUCCEEDED', ?, 'experiment-comparison', '[]', '{}')`, proj.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_comparison_links
		(link_id, project_id, manuscript_version_id, task_id, input_version_id, metric, value, start_byte, end_byte, selected_text)
		VALUES ('ev-comp-1', ?, ?, 'task-ev-comp', ?, 'meanDifference', '2.0', 0, 12, '核心论点提升 2.0')`,
		proj.ProjectID, covered.VersionID, csvV1.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_pdf_links
		(link_id, project_id, manuscript_version_id, material_id, page, quote, start_byte, end_byte, selected_text)
		VALUES ('ev-pdf-1', ?, ?, ?, 2, 'supporting quote', 0, 12, '核心论点提升 2.0')`,
		proj.ProjectID, covered.VersionID, pdfV1.MaterialID); err != nil {
		t.Fatal(err)
	}

	// Before source updates: no affected claims, uncovered.md flagged as missing evidence.
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/memory-summary", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("memory-summary failed: %d %s", res.Code, res.Body)
	}
	var before ProjectMemorySummary
	if err := json.Unmarshal(res.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.AffectedClaims) != 0 {
		t.Fatalf("expected 0 affectedClaims before source updates, got %+v", before.AffectedClaims)
	}
	if len(before.MissingEvidenceFiles) != 1 || before.MissingEvidenceFiles[0].Path != "paper/uncovered.md" {
		t.Fatalf("expected only paper/uncovered.md in missingEvidenceFiles, got %+v", before.MissingEvidenceFiles)
	}

	// Replace CSV and upload newer version of evidence.pdf.
	if _, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "data/metrics.csv", csvV1.SHA256, "a,b\n1,5\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveMaterial(ctx, proj.ProjectID, "evidence.pdf", strings.NewReader("%PDF-1.4\nv2-updated")); err != nil {
		t.Fatal(err)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/memory-summary", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("memory-summary after source update failed: %d %s", res.Code, res.Body)
	}
	var after ProjectMemorySummary
	if err := json.Unmarshal(res.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.AffectedClaims) != 2 {
		t.Fatalf("expected 2 affectedClaims after CSV & PDF updates, got %+v", after.AffectedClaims)
	}
	if len(after.MissingEvidenceFiles) != 1 || after.MissingEvidenceFiles[0].Path != "paper/uncovered.md" {
		t.Fatalf("expected paper/uncovered.md still flagged, got %+v", after.MissingEvidenceFiles)
	}
}
