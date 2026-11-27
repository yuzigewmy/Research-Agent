package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"research-agent/internal/literature"
)

func TestProjectDeletePreviewAndSafety(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	projA, _, err := store.createProject(ctx, createProjectInput{RequestID: "del-a", Title: "项目删除验证A", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	projB, _, err := store.createProject(ctx, createProjectInput{RequestID: "del-b", Title: "隔离保留项目B", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}

	// Populate project A across multiple dependent tables (materials, workspace, citations, workflow, tasks, turns, evidence).
	matA, _, err := store.saveMaterial(ctx, projA.ProjectID, "notes.txt", strings.NewReader("alpha data"))
	if err != nil {
		t.Fatal(err)
	}
	pdfA, _, err := store.saveMaterial(ctx, projA.ProjectID, "paper.pdf", strings.NewReader("%PDF-1.4\n%test"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.saveMaterial(ctx, projB.ProjectID, "keep.txt", strings.NewReader("alpha data"))
	if err != nil {
		t.Fatal(err)
	}

	docA, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, "paper/main.md", "", "# Title\n\nEvidence sentence here.")
	if err != nil {
		t.Fatal(err)
	}
	csvA, err := store.saveWorkspaceDocument(ctx, projA.ProjectID, "data/scores.csv", "", "baseline,variant\n1,2\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveCitation(ctx, projA.ProjectID, literature.Work{
		DOI: "10.1000/del-test", Title: "Deletion Paper", Authors: []string{"Alice"}, Year: 2025,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(createdAtLayout)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO workflow_runs (run_id, project_id, workflow_key, status, revision, checkpoints, created_at, project_title)
		VALUES ('run-del-1', ?, 'experiment-v2', 'SUCCEEDED', 1, 1, ?, ?)`, projA.ProjectID, now, projA.Title); err != nil {
		t.Fatal(err)
	}
	// Insert one succeeded comparison task and one active RUNNING task.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, kind, inputs_json, result_json)
		VALUES (?, 'task-comp-1', 'compare', 1, 'SUCCEEDED', ?, 'experiment-comparison', '[]', '{}')`, projA.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, inputs_json)
		VALUES (?, 'task-active-1', 'running work', 1, 'RUNNING', ?, '[]')`, projA.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_comparison_links
		(link_id, project_id, manuscript_version_id, task_id, input_version_id, metric, value, start_byte, end_byte, selected_text)
		VALUES ('clink-1', ?, ?, 'task-comp-1', ?, 'meanDifference', '1', 0, 7, '# Title')`,
		projA.ProjectID, docA.VersionID, csvA.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_pdf_links
		(link_id, project_id, manuscript_version_id, material_id, page, quote, start_byte, end_byte, selected_text)
		VALUES ('plink-1', ?, ?, ?, 1, 'quote', 0, 7, '# Title')`,
		projA.ProjectID, docA.VersionID, pdfA.MaterialID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_pdf_citations (link_id, project_id, doi)
		VALUES ('plink-1', ?, '10.1000/del-test')`, projA.ProjectID); err != nil {
		t.Fatal(err)
	}

	// 1. Delete preview while active task exists -> canDelete must be false.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+projA.ProjectID+"/delete-preview", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("delete-preview failed: %d %s", res.Code, res.Body)
	}
	var preview ProjectDeletePreview
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.CanDelete || preview.ActiveTaskCount != 1 || preview.MaterialCount != 2 ||
		preview.FileVersionCount != 2 || preview.LiteratureCount != 1 || preview.WorkflowRunCount != 1 || preview.TotalBytes <= 0 {
		t.Fatalf("unexpected busy delete preview: %+v", preview)
	}

	// 2. Delete while busy -> 409 PROJECT_BUSY.
	doDelete := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1"+projectsPath+"/"+projA.ProjectID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	busyRes := doDelete(fmt.Sprintf(`{"expectedRevision":%d,"confirmTitle":%q}`, projA.Revision, projA.Title))
	if busyRes.Code != http.StatusConflict || !strings.Contains(busyRes.Body.String(), "PROJECT_BUSY") {
		t.Fatalf("expected 409 PROJECT_BUSY, got %d %s", busyRes.Code, busyRes.Body)
	}

	// 3. Complete active task; verify revision conflict & title mismatch are rejected.
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET status = 'SUCCEEDED' WHERE task_id = 'task-active-1'`); err != nil {
		t.Fatal(err)
	}
	revRes := doDelete(fmt.Sprintf(`{"expectedRevision":99,"confirmTitle":%q}`, projA.Title))
	if revRes.Code != http.StatusConflict || !strings.Contains(revRes.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("expected 409 REVISION_CONFLICT, got %d %s", revRes.Code, revRes.Body)
	}
	titleRes := doDelete(fmt.Sprintf(`{"expectedRevision":%d,"confirmTitle":"错误题目"}`, projA.Revision))
	if titleRes.Code != http.StatusBadRequest || !strings.Contains(titleRes.Body.String(), "CONFIRM_TITLE_MISMATCH") {
		t.Fatalf("expected 400 CONFIRM_TITLE_MISMATCH, got %d %s", titleRes.Code, titleRes.Body)
	}

	// 4. Preview now reports canDelete: true, then confirm delete succeeds cleanly.
	req2 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+projA.ProjectID+"/delete-preview", nil)
	res2 := httptest.NewRecorder()
	handler.ServeHTTP(res2, req2)
	if err := json.Unmarshal(res2.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.CanDelete || preview.ActiveTaskCount != 0 {
		t.Fatalf("expected deletable preview, got %+v", preview)
	}

	okRes := doDelete(fmt.Sprintf(`{"expectedRevision":%d,"confirmTitle":%q}`, projA.Revision, projA.Title))
	if okRes.Code != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d %s", okRes.Code, okRes.Body)
	}

	// 5. Project A is gone, Project B and shared content object remain intact.
	if _, err := store.getProject(ctx, projA.ProjectID); err == nil {
		t.Fatalf("project A still exists after delete")
	}
	matsB, err := store.listMaterials(ctx, projB.ProjectID, "", 0, 10)
	if err != nil || len(matsB) != 1 {
		t.Fatalf("project B materials damaged: %v %+v", err, matsB)
	}
	data, err := store.readObject(matA.SHA256)
	if err != nil || string(data) != "alpha data" {
		t.Fatalf("shared content object damaged after deleting project A: %v %q", err, string(data))
	}
}

func TestProjectArchiveExportPreviewAndImport(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	src, _, err := store.createProject(ctx, createProjectInput{RequestID: "arc-src", Title: "归档源项目", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	m1, _, err := store.saveMaterial(ctx, src.ProjectID, "dataset.csv", strings.NewReader("id,val\n1,10\n"))
	if err != nil {
		t.Fatal(err)
	}
	m2, _, err := store.saveMaterial(ctx, src.ProjectID, "dataset.csv", strings.NewReader("id,val\n1,20\n"))
	if err != nil {
		t.Fatal(err)
	}
	doc1, err := store.saveWorkspaceDocument(ctx, src.ProjectID, "research/brief.md", "", "# Brief v1")
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := store.saveWorkspaceDocument(ctx, src.ProjectID, "research/brief.md", doc1.SHA256, "# Brief v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveCitation(ctx, src.ProjectID, literature.Work{
		DOI: "10.1000/archive-ref", Title: "Archived Study", Authors: []string{"Bob"}, Year: 2024,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(createdAtLayout)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO workflow_runs (run_id, project_id, workflow_key, status, revision, checkpoints, created_at, project_title)
		VALUES ('run-arc-1', ?, 'experiment-v2', 'SUCCEEDED', 1, 1, ?, ?)`, src.ProjectID, now, src.Title); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, workflow_run_id, workflow_step, markdown, inputs_json)
		VALUES (?, 'task-wf-0', 'scope', 1, 'SUCCEEDED', ?, 'run-arc-1', 0, '# Scope Artifact', '[]')`, src.ProjectID, now); err != nil {
		t.Fatal(err)
	}

	// 1. Export single project ZIP archive.
	expReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/"+src.ProjectID+"/archive-export", nil)
	expRes := httptest.NewRecorder()
	handler.ServeHTTP(expRes, expReq)
	if expRes.Code != http.StatusOK {
		t.Fatalf("archive export failed: %d %s", expRes.Code, expRes.Body)
	}
	zipBytes := expRes.Body.Bytes()

	// 2. Preview archive before importing.
	prevReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/archive-import/preview", bytes.NewReader(zipBytes))
	prevReq.Header.Set("Content-Type", "application/zip")
	prevRes := httptest.NewRecorder()
	handler.ServeHTTP(prevRes, prevReq)
	if prevRes.Code != http.StatusOK {
		t.Fatalf("archive preview failed: %d %s", prevRes.Code, prevRes.Body)
	}
	var preview ProjectArchivePreview
	if err := json.Unmarshal(prevRes.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Title != src.Title || len(preview.Materials) != 2 || len(preview.WorkspaceFiles) != 2 ||
		len(preview.Citations) != 1 || len(preview.Artifacts) != 1 {
		t.Fatalf("unexpected archive preview: %+v", preview)
	}
	if preview.WorkspaceFiles[0].Latest || !preview.WorkspaceFiles[1].Latest {
		t.Fatalf("unexpected latest flags: %+v", preview.WorkspaceFiles)
	}

	// 3. Import archive as a new isolated project.
	impReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/archive-import", bytes.NewReader(zipBytes))
	impReq.Header.Set("Content-Type", "application/zip")
	impReq.Header.Set("X-Request-Id", "import-req-1")
	impRes := httptest.NewRecorder()
	handler.ServeHTTP(impRes, impReq)
	if impRes.Code != http.StatusCreated {
		t.Fatalf("archive import failed: %d %s", impRes.Code, impRes.Body)
	}
	var imported ProjectArchiveImportResult
	if err := json.Unmarshal(impRes.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Project.ProjectID == src.ProjectID || imported.ImportedMaterials != 2 ||
		imported.ImportedFiles != 2 || imported.ImportedCitations != 1 || imported.ArtifactCount != 1 {
		t.Fatalf("unexpected import result: %+v", imported)
	}

	// Verify imported materials keep original SHA-256 and workspace latest is doc2.
	impMats, err := store.listMaterials(ctx, imported.Project.ProjectID, "", 0, 10)
	if err != nil || len(impMats) != 2 {
		t.Fatalf("imported materials mismatch: %v %+v", err, impMats)
	}
	if impMats[0].SHA256 != m2.SHA256 || impMats[1].SHA256 != m1.SHA256 {
		t.Fatalf("imported material SHA-256 or order mismatch: %+v", impMats)
	}
	latestFile, err := store.workspaceFile(ctx, imported.Project.ProjectID, "research/brief.md")
	if err != nil || latestFile.SHA256 != doc2.SHA256 {
		t.Fatalf("imported workspace latest mismatch: %v %+v", err, latestFile)
	}

	// 4. Idempotent replay with same X-Request-Id reuses the imported project.
	replayReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/archive-import", bytes.NewReader(zipBytes))
	replayReq.Header.Set("Content-Type", "application/zip")
	replayReq.Header.Set("X-Request-Id", "import-req-1")
	replayRes := httptest.NewRecorder()
	handler.ServeHTTP(replayRes, replayReq)
	if replayRes.Code != http.StatusOK {
		t.Fatalf("idempotent replay expected 200, got %d %s", replayRes.Code, replayRes.Body)
	}
	var replayed ProjectArchiveImportResult
	if err := json.Unmarshal(replayRes.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Project.ProjectID != imported.Project.ProjectID {
		t.Fatalf("replay did not return same project: %+v", replayed)
	}

	// 5. Corrupted object digest must be rejected by both preview and import.
	corrupted := tamperArchiveObject(t, zipBytes, m1.SHA256, []byte("corrupted bytes"))
	badPrev := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/archive-import/preview", bytes.NewReader(corrupted))
	badPrev.Header.Set("Content-Type", "application/zip")
	badPrevRes := httptest.NewRecorder()
	handler.ServeHTTP(badPrevRes, badPrev)
	if badPrevRes.Code != http.StatusUnprocessableEntity {
		t.Fatalf("corrupted archive preview expected 422, got %d %s", badPrevRes.Code, badPrevRes.Body)
	}

	badImp := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectsPath+"/archive-import", bytes.NewReader(corrupted))
	badImp.Header.Set("Content-Type", "application/zip")
	badImp.Header.Set("X-Request-Id", "import-req-bad")
	badImpRes := httptest.NewRecorder()
	handler.ServeHTTP(badImpRes, badImp)
	if badImpRes.Code != http.StatusUnprocessableEntity {
		t.Fatalf("corrupted archive import expected 422, got %d %s", badImpRes.Code, badImpRes.Body)
	}
}

func tamperArchiveObject(t *testing.T, raw []byte, digest string, replacement []byte) []byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "objects/"+digest {
			data = replacement
		}
		entry, err := w.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestProjectMemorySummaryAndEvidenceChain(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "mem-1", Title: "研究记忆验证", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	// Save a research brief longer than 800 runes.
	longBrief := strings.Repeat("研究简报核心结论。", 120)
	briefDoc, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "research/brief.md", "", longBrief)
	if err != nil {
		t.Fatal(err)
	}
	manuscriptV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/main.md", "", "# 实验结果\n\n方案提升明显，见表1与文献。")
	if err != nil {
		t.Fatal(err)
	}
	csvV1, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "data/scores.csv", "", "baseline,variant\n1,3\n")
	if err != nil {
		t.Fatal(err)
	}
	pdfV1, _, err := store.saveMaterial(ctx, proj.ProjectID, "ref.pdf", strings.NewReader("%PDF-1.4\nv1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveCitation(ctx, proj.ProjectID, literature.Work{
		DOI: "10.1000/mem-doi", Title: "Memory Evidence Study", Authors: []string{"Chen"}, Year: 2025,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(createdAtLayout)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO workflow_runs (run_id, project_id, workflow_key, status, revision, checkpoints, created_at, project_title)
		VALUES ('run-mem-1', ?, 'paper-v1', 'SUCCEEDED', 1, 1, ?, ?)`, proj.ProjectID, now, proj.Title); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, workflow_run_id, workflow_step, inputs_json)
		VALUES (?, 'task-wf-m1', 'outline', 1, 'SUCCEEDED', ?, 'run-mem-1', 0, '[]')`, proj.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, revision, status, created_at, kind, inputs_json, result_json)
		VALUES (?, 'task-comp-mem', 'compare', 1, 'SUCCEEDED', ?, 'experiment-comparison', '[]', '{}')`, proj.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_comparison_links
		(link_id, project_id, manuscript_version_id, task_id, input_version_id, metric, value, start_byte, end_byte, selected_text)
		VALUES ('mem-comp-1', ?, ?, 'task-comp-mem', ?, 'meanDifference', '2', 0, 14, '# 实验结果')`,
		proj.ProjectID, manuscriptV1.VersionID, csvV1.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_pdf_links
		(link_id, project_id, manuscript_version_id, material_id, page, quote, start_byte, end_byte, selected_text)
		VALUES ('mem-pdf-1', ?, ?, ?, 4, 'prior finding', 0, 14, '# 实验结果')`,
		proj.ProjectID, manuscriptV1.VersionID, pdfV1.MaterialID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO manuscript_pdf_citations (link_id, project_id, doi)
		VALUES ('mem-pdf-1', ?, '10.1000/mem-doi')`, proj.ProjectID); err != nil {
		t.Fatal(err)
	}

	// Update manuscript, CSV, and PDF to newer versions so outdated/sourceChanged flags trigger.
	if _, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "paper/main.md", manuscriptV1.SHA256, "# 实验结果\n\n修订稿。"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, proj.ProjectID, "data/scores.csv", csvV1.SHA256, "baseline,variant\n1,4\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveMaterial(ctx, proj.ProjectID, "ref.pdf", strings.NewReader("%PDF-1.4\nv2")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+proj.ProjectID+"/memory-summary", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("memory-summary failed: %d %s", res.Code, res.Body)
	}
	var summary ProjectMemorySummary
	if err := json.Unmarshal(res.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ResearchBrief == nil || summary.ResearchBrief.SHA256 != briefDoc.SHA256 || !summary.ResearchBrief.Truncated || len([]rune(summary.ResearchBrief.Excerpt)) != 800 {
		t.Fatalf("unexpected research brief summary: %+v", summary.ResearchBrief)
	}
	if summary.VerifiedCitationCount != 1 || summary.CompletedComparisonCount != 1 {
		t.Fatalf("unexpected counts: citations=%d comparisons=%d", summary.VerifiedCitationCount, summary.CompletedComparisonCount)
	}
	if summary.LatestWorkflow == nil || summary.LatestWorkflow.RunID != "run-mem-1" || summary.LatestWorkflow.SucceededSteps != 1 {
		t.Fatalf("unexpected workflow summary: %+v", summary.LatestWorkflow)
	}
	if len(summary.EvidenceChain) != 2 {
		t.Fatalf("expected 2 evidence chain items, got %+v", summary.EvidenceChain)
	}
	var sawComp, sawPDF bool
	for _, item := range summary.EvidenceChain {
		if !item.ManuscriptOutdated || !item.SourceChanged {
			t.Fatalf("expected manuscriptOutdated & sourceChanged on %+v", item)
		}
		if item.Kind == "comparison" && item.Metric == "meanDifference" && item.Value == "2" {
			sawComp = true
		}
		if item.Kind == "pdf" && item.CitationDOI == "10.1000/mem-doi" && item.Page == 4 {
			sawPDF = true
		}
	}
	if !sawComp || !sawPDF {
		t.Fatalf("missing expected evidence chain types: %+v", summary.EvidenceChain)
	}
	_ = sha256.Sum256
	_ = hex.EncodeToString
}
