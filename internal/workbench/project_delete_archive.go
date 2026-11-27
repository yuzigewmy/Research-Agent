package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/literature"
)

const (
	projectArchiveFormatVersion    = 1
	projectArchiveManifestName     = "manifest.json"
	maxProjectArchiveBytes         = 64 << 20
	maxProjectArchiveManifestBytes = 8 << 20
	maxProjectArchiveExpandedBytes = 256 << 20
	maxProjectArchiveEntries       = 2000
)

var (
	errProjectBusy             = errors.New("project has active tasks")
	errProjectConfirmMismatch  = errors.New("confirm title does not match project title")
	errInvalidProjectArchive   = errors.New("invalid project archive")
	errProjectArchiveTooLarge  = errors.New("project archive exceeds size limit")
)

type ProjectDeletePreview struct {
	ProjectID        string `json:"projectId"`
	Title            string `json:"title"`
	Revision         int    `json:"revision"`
	MaterialCount    int    `json:"materialCount"`
	FileVersionCount int    `json:"fileVersionCount"`
	LiteratureCount  int    `json:"literatureCount"`
	WorkflowRunCount int    `json:"workflowRunCount"`
	ActiveTaskCount  int    `json:"activeTaskCount"`
	TotalBytes       int64  `json:"totalBytes"`
	CanDelete        bool   `json:"canDelete"`
}

type deleteProjectInput struct {
	ExpectedRevision int    `json:"expectedRevision"`
	ConfirmTitle     string `json:"confirmTitle"`
}

type projectArchiveManifest struct {
	Version        int                       `json:"version"`
	ExportedAt     string                    `json:"exportedAt"`
	Project        projectArchiveProjectMeta `json:"project"`
	Materials      []projectArchiveMaterial  `json:"materials"`
	WorkspaceFiles []projectArchiveFile      `json:"workspaceFiles"`
	Citations      []projectArchiveCitation  `json:"citations"`
	Artifacts      []projectArchiveArtifact  `json:"artifacts"`
}

type projectArchiveProjectMeta struct {
	SourceProjectID string `json:"sourceProjectId"`
	Title           string `json:"title"`
	DisplayName     string `json:"displayName,omitempty"`
	TemplateID      string `json:"templateId"`
	Status          string `json:"status"`
	Goal            string `json:"goal,omitempty"`
	Deliverables    string `json:"deliverables,omitempty"`
	DefaultTurnMode string `json:"defaultTurnMode,omitempty"`
}

type projectArchiveMaterial struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
}

type projectArchiveFile struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
	CreatedAt string `json:"createdAt"`
	Latest    bool   `json:"latest"`
}

type projectArchiveCitation struct {
	DOI        string          `json:"doi"`
	Metadata   json.RawMessage `json:"metadata"`
	VerifiedAt string          `json:"verifiedAt"`
}

type projectArchiveCitationSummary struct {
	DOI        string `json:"doi"`
	Title      string `json:"title,omitempty"`
	Year       int    `json:"year,omitempty"`
	VerifiedAt string `json:"verifiedAt,omitempty"`
}

type projectArchiveArtifact struct {
	RunID     string `json:"runId"`
	StepKey   string `json:"stepKey"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}

type ProjectArchivePreview struct {
	Title          string                          `json:"title"`
	DisplayName    string                          `json:"displayName,omitempty"`
	TemplateID     string                          `json:"templateId"`
	Goal           string                          `json:"goal,omitempty"`
	Deliverables   string                          `json:"deliverables,omitempty"`
	ExportedAt     string                          `json:"exportedAt,omitempty"`
	Materials      []projectArchiveMaterial        `json:"materials"`
	WorkspaceFiles []projectArchiveFile            `json:"workspaceFiles"`
	Citations      []projectArchiveCitationSummary `json:"citations"`
	Artifacts      []projectArchiveArtifact        `json:"artifacts"`
	TotalBytes     int64                           `json:"totalBytes"`
}

type ProjectArchiveImportResult struct {
	Project           Project `json:"project"`
	ImportedMaterials int     `json:"importedMaterials"`
	ImportedFiles     int     `json:"importedFiles"`
	ImportedCitations int     `json:"importedCitations"`
	ArtifactCount     int     `json:"artifactCount"`
	Replayed          bool    `json:"replayed,omitempty"`
}

func (s *Store) registerProjectArchiveAndDelete(mux *http.ServeMux) {
	mux.HandleFunc("GET "+projectsPath+"/{id}/delete-preview", s.handleProjectDeletePreview)
	mux.HandleFunc(projectsPath+"/{id}/delete-preview", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("POST "+projectsPath+"/{id}/archive-export", s.handleProjectArchiveExport)
	mux.HandleFunc(projectsPath+"/{id}/archive-export", methodNotAllowed("POST"))
	mux.HandleFunc("POST "+projectsPath+"/archive-import/preview", s.handleProjectArchiveImportPreview)
	mux.HandleFunc("POST "+projectsPath+"/archive-import", s.handleProjectArchiveImport)
}

func countProjectActiveTasks(ctx context.Context, db workspaceQuery, projectID string) (int, error) {
	var taskActive, turnActive, workflowActive, scriptActive int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks
		WHERE project_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`, projectID).Scan(&taskActive); err != nil {
		return 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns
		WHERE project_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`, projectID).Scan(&turnActive); err != nil {
		return 0, err
	}
	if taskActive == 0 {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs
			WHERE project_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`, projectID).Scan(&workflowActive); err != nil {
			return 0, err
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM script_runs
		WHERE project_id = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING')`, projectID).Scan(&scriptActive); err != nil {
		return 0, err
	}
	return taskActive + turnActive + workflowActive + scriptActive, nil
}

func (s *Store) projectDeletePreview(ctx context.Context, projectID string) (ProjectDeletePreview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	defer tx.Rollback()

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID))
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	preview := ProjectDeletePreview{
		ProjectID: project.ProjectID,
		Title:     project.Title,
		Revision:  project.Revision,
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM materials WHERE project_id = ?`, projectID).Scan(&preview.MaterialCount); err != nil {
		return ProjectDeletePreview{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_versions WHERE project_id = ?`, projectID).Scan(&preview.FileVersionCount); err != nil {
		return ProjectDeletePreview{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM literature_citations WHERE project_id = ?`, projectID).Scan(&preview.LiteratureCount); err != nil {
		return ProjectDeletePreview{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE project_id = ?`, projectID).Scan(&preview.WorkflowRunCount); err != nil {
		return ProjectDeletePreview{}, err
	}
	active, err := countProjectActiveTasks(ctx, tx, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	preview.ActiveTaskCount = active
	preview.CanDelete = active == 0
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM (
		SELECT sha256, MAX(size) AS size FROM (
			SELECT sha256, size FROM materials WHERE project_id = ?
			UNION ALL
			SELECT sha256, size FROM workspace_versions WHERE project_id = ?
		) GROUP BY sha256
	)`, projectID, projectID).Scan(&preview.TotalBytes); err != nil {
		return ProjectDeletePreview{}, err
	}
	return preview, nil
}

func (s *Store) handleProjectDeletePreview(w http.ResponseWriter, r *http.Request) {
	preview, err := s.projectDeletePreview(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	case err != nil:
		writeStoreError(w, "project delete preview", err)
	default:
		writeJSON(w, http.StatusOK, preview)
	}
}

func (s *Store) deleteProject(ctx context.Context, projectID string, input deleteProjectInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID))
	if err != nil {
		return err
	}
	if project.Revision != input.ExpectedRevision {
		return errProjectRevision
	}
	if strings.TrimSpace(input.ConfirmTitle) != project.Title {
		return errProjectConfirmMismatch
	}
	active, err := countProjectActiveTasks(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if active > 0 {
		return errProjectBusy
	}

	statements := []string{
		`DELETE FROM manuscript_pdf_citations WHERE project_id = ?`,
		`DELETE FROM manuscript_pdf_links WHERE project_id = ?`,
		`DELETE FROM manuscript_comparison_links WHERE project_id = ?`,
		`DELETE FROM image_generation_sources WHERE project_id = ?`,
		`DELETE FROM image_generations WHERE project_id = ?`,
		`DELETE FROM image_reviews WHERE project_id = ?`,
		`DELETE FROM finding_rechecks WHERE project_id = ?`,
		`DELETE FROM batch_selection_edits WHERE project_id = ?`,
		`DELETE FROM selection_edits WHERE project_id = ?`,
		`DELETE FROM automatic_reviews WHERE project_id = ?`,
		`DELETE FROM remote_comparisons WHERE project_id = ?`,
		`DELETE FROM experiment_attempts WHERE project_id = ?`,
		`DELETE FROM task_notifications WHERE project_id = ?`,
		`DELETE FROM task_requests WHERE project_id = ?`,
		`DELETE FROM task_queue_stop_requests WHERE project_id = ?`,
		`DELETE FROM tasks WHERE project_id = ?`,
		`DELETE FROM workflow_requests WHERE project_id = ?`,
		`DELETE FROM workflow_runs WHERE project_id = ?`,
		`DELETE FROM turn_events WHERE turn_id IN (SELECT turn_id FROM turns WHERE project_id = ?)`,
		`DELETE FROM turns WHERE project_id = ?`,
		`DELETE FROM script_run_artifacts WHERE run_id IN (SELECT run_id FROM script_runs WHERE project_id = ?)`,
		`DELETE FROM script_runs WHERE project_id = ?`,
		`DELETE FROM survey_analysis_plans WHERE project_id = ?`,
		`DELETE FROM survey_runs WHERE project_id = ?`,
		`DELETE FROM questionnaire_responses WHERE project_id = ?`,
		`DELETE FROM questionnaires WHERE project_id = ?`,
		`DELETE FROM engineering_previews WHERE project_id = ?`,
		`DELETE FROM word_exports WHERE version_id IN (SELECT version_id FROM workspace_versions WHERE project_id = ?)`,
		`DELETE FROM literature_citations WHERE project_id = ?`,
		`DELETE FROM materials WHERE project_id = ?`,
		`DELETE FROM workspace_versions WHERE project_id = ?`,
		`DELETE FROM project_patch_requests WHERE project_id = ?`,
		`UPDATE project_drafts SET project_id = NULL WHERE project_id = ?`,
		`DELETE FROM projects WHERE project_id = ?`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt, projectID); err != nil {
			return fmt.Errorf("delete project statement %q: %w", stmt, err)
		}
	}
	return tx.Commit()
}

func (s *Store) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	var input deleteProjectInput
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	if input.ExpectedRevision < 1 || strings.TrimSpace(input.ConfirmTitle) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供正整数修订版本和确认的研究题目。")
		return
	}
	projectID := r.PathValue("id")
	err := s.deleteProject(r.Context(), projectID, input)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "projectId": projectID})
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	case errors.Is(err, errProjectRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "项目已更新，请刷新并核对最新版本后再删除。")
	case errors.Is(err, errProjectBusy):
		writeError(w, http.StatusConflict, "PROJECT_BUSY", "项目存在排队或运行中的任务，请先停止后再删除。")
	case errors.Is(err, errProjectConfirmMismatch):
		writeError(w, http.StatusBadRequest, "CONFIRM_TITLE_MISMATCH", "输入的研究题目与当前项目不一致，未执行删除。")
	default:
		writeStoreError(w, "delete project", err)
	}
}

func (s *Store) buildProjectArchive(ctx context.Context, projectID string, out io.Writer) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID))
	if err != nil {
		return "", err
	}

	manifest := projectArchiveManifest{
		Version:    projectArchiveFormatVersion,
		ExportedAt: time.Now().UTC().Format(createdAtLayout),
		Project: projectArchiveProjectMeta{
			SourceProjectID: project.ProjectID,
			Title:           project.Title,
			DisplayName:     project.DisplayName,
			TemplateID:      project.TemplateID,
			Status:          project.Status,
			Goal:            project.Goal,
			Deliverables:    project.Deliverables,
			DefaultTurnMode: project.DefaultTurnMode,
		},
		Materials:      make([]projectArchiveMaterial, 0),
		WorkspaceFiles: make([]projectArchiveFile, 0),
		Citations:      make([]projectArchiveCitation, 0),
		Artifacts:      make([]projectArchiveArtifact, 0),
	}

	objectDigests := make([]string, 0)
	seenObjects := make(map[string]bool)
	inlineObjects := make(map[string][]byte)
	recordObject := func(digest string) {
		if !seenObjects[digest] {
			seenObjects[digest] = true
			objectDigests = append(objectDigests, digest)
		}
	}

	matRows, err := tx.QueryContext(ctx, `SELECT name, sha256, size, kind, created_at FROM materials
		WHERE project_id = ? ORDER BY ordinal ASC`, projectID)
	if err != nil {
		return "", err
	}
	for matRows.Next() {
		var m projectArchiveMaterial
		if err := matRows.Scan(&m.Name, &m.SHA256, &m.Size, &m.Kind, &m.CreatedAt); err != nil {
			matRows.Close()
			return "", err
		}
		manifest.Materials = append(manifest.Materials, m)
		recordObject(m.SHA256)
	}
	if err := matRows.Err(); err != nil {
		matRows.Close()
		return "", err
	}
	matRows.Close()

	fileRows, err := tx.QueryContext(ctx, `SELECT path, sha256, size, created_at FROM workspace_versions
		WHERE project_id = ? ORDER BY ordinal ASC`, projectID)
	if err != nil {
		return "", err
	}
	latestIndexByPath := make(map[string]int)
	for fileRows.Next() {
		var f projectArchiveFile
		if err := fileRows.Scan(&f.Path, &f.SHA256, &f.Size, &f.CreatedAt); err != nil {
			fileRows.Close()
			return "", err
		}
		latestIndexByPath[f.Path] = len(manifest.WorkspaceFiles)
		manifest.WorkspaceFiles = append(manifest.WorkspaceFiles, f)
		recordObject(f.SHA256)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return "", err
	}
	fileRows.Close()
	for _, idx := range latestIndexByPath {
		manifest.WorkspaceFiles[idx].Latest = true
	}

	citRows, err := tx.QueryContext(ctx, `SELECT doi, metadata_json, verified_at FROM literature_citations
		WHERE project_id = ? ORDER BY ordinal ASC`, projectID)
	if err != nil {
		return "", err
	}
	for citRows.Next() {
		var c projectArchiveCitation
		var raw []byte
		if err := citRows.Scan(&c.DOI, &raw, &c.VerifiedAt); err != nil {
			citRows.Close()
			return "", err
		}
		c.Metadata = append([]byte(nil), raw...)
		manifest.Citations = append(manifest.Citations, c)
	}
	if err := citRows.Err(); err != nil {
		citRows.Close()
		return "", err
	}
	citRows.Close()

	artRows, err := tx.QueryContext(ctx, `SELECT t.workflow_run_id, stage.value ->> 'key', stage.value ->> 'name', t.markdown, t.created_at
		FROM tasks t
		JOIN workflow_runs w ON w.run_id = t.workflow_run_id AND w.project_id = t.project_id
		JOIN json_each(?, '$.' || json_quote(w.workflow_key)) stage ON stage.key = t.workflow_step
		WHERE t.project_id = ? AND t.status = 'SUCCEEDED'
		ORDER BY t.ordinal ASC`, artifactStageNames, projectID)
	if err != nil {
		return "", err
	}
	for artRows.Next() {
		var a projectArchiveArtifact
		var markdown string
		if err := artRows.Scan(&a.RunID, &a.StepKey, &a.Name, &markdown, &a.CreatedAt); err != nil {
			artRows.Close()
			return "", err
		}
		sum := sha256.Sum256([]byte(markdown))
		a.SHA256 = hex.EncodeToString(sum[:])
		a.Size = int64(len(markdown))
		manifest.Artifacts = append(manifest.Artifacts, a)
		if !seenObjects[a.SHA256] {
			seenObjects[a.SHA256] = true
			objectDigests = append(objectDigests, a.SHA256)
			inlineObjects[a.SHA256] = []byte(markdown)
		}
	}
	if err := artRows.Err(); err != nil {
		artRows.Close()
		return "", err
	}
	artRows.Close()
	_ = tx.Rollback()

	slices.Sort(objectDigests)

	archive := zip.NewWriter(out)
	for _, digest := range objectDigests {
		if data, ok := inlineObjects[digest]; ok {
			entry, err := archive.CreateHeader(&zip.FileHeader{Name: "objects/" + digest, Method: zip.Deflate})
			if err != nil {
				return "", err
			}
			if _, err := entry.Write(data); err != nil {
				return "", err
			}
			continue
		}
		source, err := s.openObject(digest)
		if err != nil {
			return "", fmt.Errorf("open archive object %s: %w", digest, err)
		}
		saved, err := zipBackupFile(ctx, archive, "objects/"+digest, source)
		if err != nil {
			return "", err
		}
		if saved.SHA256 != digest {
			return "", fmt.Errorf("archive object %s digest mismatch", digest)
		}
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if len(manifestBytes) > maxProjectArchiveManifestBytes {
		return "", errors.New("项目归档清单超过上限。")
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: projectArchiveManifestName, Method: zip.Deflate})
	if err != nil {
		return "", err
	}
	if _, err := entry.Write(manifestBytes); err != nil {
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	filename := "project-" + sanitizeArchiveSlug(project.ProjectID) + "-" + time.Now().UTC().Format("20060102-150405Z") + ".zip"
	return filename, nil
}

func sanitizeArchiveSlug(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	if b.Len() == 0 {
		return "export"
	}
	return b.String()
}

func (s *Store) handleProjectArchiveExport(w http.ResponseWriter, r *http.Request) {
	stage, err := os.CreateTemp("", "research-project-archive-*.zip")
	if err != nil {
		writeStoreError(w, "create project archive temp", err)
		return
	}
	defer os.Remove(stage.Name())
	defer stage.Close()

	filename, err := s.buildProjectArchive(r.Context(), r.PathValue("id"), stage)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
		return
	}
	if err != nil {
		log.Printf("project archive export: %v", err)
		writeError(w, http.StatusInternalServerError, "ARCHIVE_EXPORT_FAILED", "无法生成项目归档，请重试。")
		return
	}
	length, err := stage.Seek(0, io.SeekCurrent)
	if err != nil {
		writeStoreError(w, "measure project archive", err)
		return
	}
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		writeStoreError(w, "rewind project archive", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, stage)
}

type parsedProjectArchive struct {
	manifest     projectArchiveManifest
	objectSizes  map[string]int64
	zipReader    *zip.Reader
	objectFiles  map[string]*zip.File
	totalBytes   int64
}

func readProjectArchiveUpload(w http.ResponseWriter, r *http.Request) (*os.File, int64, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/zip" && mediaType != "application/octet-stream") {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请上传项目导出的 ZIP 归档文件。")
		return nil, 0, false
	}
	if r.ContentLength > maxProjectArchiveBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "项目归档不能超过 64 MiB。")
		return nil, 0, false
	}
	upload, err := os.CreateTemp("", "research-project-import-*.zip")
	if err != nil {
		writeStoreError(w, "stage project archive upload", err)
		return nil, 0, false
	}
	body := http.MaxBytesReader(w, r.Body, maxProjectArchiveBytes)
	size, err := io.Copy(upload, body)
	if err != nil {
		_ = upload.Close()
		_ = os.Remove(upload.Name())
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "项目归档不能超过 64 MiB。")
			return nil, 0, false
		}
		writeError(w, http.StatusBadRequest, "INVALID_ARCHIVE", "读取上传归档失败，请重试。")
		return nil, 0, false
	}
	return upload, size, true
}

func validArchiveMaterialName(name string) bool {
	return strings.TrimSpace(name) != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\") && utf8.ValidString(name) &&
		utf8.RuneCountInString(name) <= 255 && !strings.ContainsFunc(name, unicode.IsControl)
}

func parseAndVerifyProjectArchive(file *os.File, size int64) (*parsedProjectArchive, error) {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidProjectArchive, err)
	}
	if len(reader.File) == 0 || len(reader.File) > maxProjectArchiveEntries {
		return nil, fmt.Errorf("%w: entry count out of range", errInvalidProjectArchive)
	}

	var manifestFile *zip.File
	objectFiles := make(map[string]*zip.File)
	for _, entry := range reader.File {
		switch {
		case entry.Name == projectArchiveManifestName:
			if manifestFile != nil {
				return nil, fmt.Errorf("%w: duplicate manifest", errInvalidProjectArchive)
			}
			manifestFile = entry
		case strings.HasPrefix(entry.Name, "objects/"):
			digest := strings.TrimPrefix(entry.Name, "objects/")
			if !validObjectDigest(digest) || strings.Contains(digest, "/") {
				return nil, fmt.Errorf("%w: invalid object entry %q", errInvalidProjectArchive, entry.Name)
			}
			if _, dup := objectFiles[digest]; dup {
				return nil, fmt.Errorf("%w: duplicate object %s", errInvalidProjectArchive, digest)
			}
			objectFiles[digest] = entry
		default:
			return nil, fmt.Errorf("%w: unexpected entry %q", errInvalidProjectArchive, entry.Name)
		}
	}
	if manifestFile == nil {
		return nil, fmt.Errorf("%w: missing manifest.json", errInvalidProjectArchive)
	}

	rc, err := manifestFile.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open manifest: %v", errInvalidProjectArchive, err)
	}
	manifestData, err := io.ReadAll(io.LimitReader(rc, maxProjectArchiveManifestBytes+1))
	_ = rc.Close()
	if err != nil || len(manifestData) > maxProjectArchiveManifestBytes {
		return nil, fmt.Errorf("%w: invalid manifest size", errInvalidProjectArchive)
	}
	var manifest projectArchiveManifest
	dec := json.NewDecoder(bytes.NewReader(manifestData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("%w: decode manifest: %v", errInvalidProjectArchive, err)
	}
	if manifest.Version != projectArchiveFormatVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", errInvalidProjectArchive, manifest.Version)
	}
	manifest.Project.Title = strings.TrimSpace(manifest.Project.Title)
	manifest.Project.DisplayName = strings.TrimSpace(manifest.Project.DisplayName)
	if !validProjectTitle(manifest.Project.Title) || !validProjectTemplate(manifest.Project.TemplateID) {
		return nil, fmt.Errorf("%w: invalid project metadata", errInvalidProjectArchive)
	}
	if utf8.RuneCountInString(manifest.Project.DisplayName) > 200 || strings.ContainsRune(manifest.Project.DisplayName, 0) {
		return nil, fmt.Errorf("%w: invalid display name", errInvalidProjectArchive)
	}
	if manifest.Project.DefaultTurnMode == "" {
		manifest.Project.DefaultTurnMode = turnModeChat
	} else if !validTurnMode(manifest.Project.DefaultTurnMode) {
		return nil, fmt.Errorf("%w: invalid default turn mode", errInvalidProjectArchive)
	}
	if manifest.Project.Status == "" {
		manifest.Project.Status = projectReady
	} else if manifest.Project.Status != projectReady && manifest.Project.Status != projectArchived {
		return nil, fmt.Errorf("%w: invalid project status", errInvalidProjectArchive)
	}

	expectedSizes := make(map[string]int64)
	registerExpected := func(digest string, sz int64) error {
		if !validObjectDigest(digest) || sz < 0 {
			return fmt.Errorf("%w: invalid digest or size", errInvalidProjectArchive)
		}
		if prev, ok := expectedSizes[digest]; ok && prev != sz {
			return fmt.Errorf("%w: conflicting size for %s", errInvalidProjectArchive, digest)
		}
		expectedSizes[digest] = sz
		return nil
	}

	for _, m := range manifest.Materials {
		if !validArchiveMaterialName(m.Name) || (m.Kind != "text" && m.Kind != "file") {
			return nil, fmt.Errorf("%w: invalid material entry", errInvalidProjectArchive)
		}
		if err := registerExpected(m.SHA256, m.Size); err != nil {
			return nil, err
		}
	}
	for _, f := range manifest.WorkspaceFiles {
		if !validWorkspacePath(f.Path) || f.Size < 0 || f.Size > maxWorkspaceText {
			return nil, fmt.Errorf("%w: invalid workspace file entry", errInvalidProjectArchive)
		}
		if err := registerExpected(f.SHA256, int64(f.Size)); err != nil {
			return nil, err
		}
	}
	for _, a := range manifest.Artifacts {
		if strings.TrimSpace(a.Name) == "" || strings.ContainsAny(a.Name, "/\\") {
			return nil, fmt.Errorf("%w: invalid artifact entry", errInvalidProjectArchive)
		}
		if err := registerExpected(a.SHA256, a.Size); err != nil {
			return nil, err
		}
	}
	for _, c := range manifest.Citations {
		if _, err := literature.NormalizeDOI(c.DOI); err != nil {
			return nil, fmt.Errorf("%w: invalid citation doi", errInvalidProjectArchive)
		}
		var work literature.Work
		if err := json.Unmarshal(c.Metadata, &work); err != nil {
			return nil, fmt.Errorf("%w: invalid citation metadata", errInvalidProjectArchive)
		}
	}

	// Verify every expected object exists in ZIP and matches its declared SHA-256 and size.
	var totalExpanded int64
	for digest, wantSize := range expectedSizes {
		zf, ok := objectFiles[digest]
		if !ok {
			return nil, fmt.Errorf("%w: missing object %s", errInvalidProjectArchive, digest)
		}
		orc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: open object %s", errInvalidProjectArchive, digest)
		}
		hash := sha256.New()
		limit := maxProjectArchiveExpandedBytes - totalExpanded
		if limit < 0 {
			limit = 0
		}
		n, copyErr := io.Copy(hash, io.LimitReader(orc, limit+1))
		_ = orc.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("%w: read object %s", errInvalidProjectArchive, digest)
		}
		totalExpanded += n
		if totalExpanded > maxProjectArchiveExpandedBytes {
			return nil, errProjectArchiveTooLarge
		}
		if n != wantSize || hex.EncodeToString(hash.Sum(nil)) != digest {
			return nil, fmt.Errorf("%w: object %s digest or size mismatch", errInvalidProjectArchive, digest)
		}
	}

	return &parsedProjectArchive{
		manifest:    manifest,
		objectSizes: expectedSizes,
		zipReader:   reader,
		objectFiles: objectFiles,
		totalBytes:  totalExpanded,
	}, nil
}

func buildProjectArchivePreview(parsed *parsedProjectArchive) ProjectArchivePreview {
	citations := make([]projectArchiveCitationSummary, 0, len(parsed.manifest.Citations))
	for _, c := range parsed.manifest.Citations {
		var work literature.Work
		_ = json.Unmarshal(c.Metadata, &work)
		citations = append(citations, projectArchiveCitationSummary{
			DOI:        c.DOI,
			Title:      work.Title,
			Year:       work.Year,
			VerifiedAt: c.VerifiedAt,
		})
	}
	return ProjectArchivePreview{
		Title:          parsed.manifest.Project.Title,
		DisplayName:    parsed.manifest.Project.DisplayName,
		TemplateID:     parsed.manifest.Project.TemplateID,
		Goal:           parsed.manifest.Project.Goal,
		Deliverables:   parsed.manifest.Project.Deliverables,
		ExportedAt:     parsed.manifest.ExportedAt,
		Materials:      parsed.manifest.Materials,
		WorkspaceFiles: parsed.manifest.WorkspaceFiles,
		Citations:      citations,
		Artifacts:      parsed.manifest.Artifacts,
		TotalBytes:     parsed.totalBytes,
	}
}

func (s *Store) handleProjectArchiveImportPreview(w http.ResponseWriter, r *http.Request) {
	upload, size, ok := readProjectArchiveUpload(w, r)
	if !ok {
		return
	}
	defer os.Remove(upload.Name())
	defer upload.Close()

	parsed, err := parseAndVerifyProjectArchive(upload, size)
	switch {
	case errors.Is(err, errProjectArchiveTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "项目归档解压后体积超过上限。")
	case errors.Is(err, errInvalidProjectArchive):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARCHIVE", "项目归档校验失败，请确认 ZIP 完整且对象摘要未被篡改。")
	case err != nil:
		writeStoreError(w, "preview project archive", err)
	default:
		writeJSON(w, http.StatusOK, buildProjectArchivePreview(parsed))
	}
}

func (s *Store) importProjectArchive(ctx context.Context, requestID string, parsed *parsedProjectArchive) (ProjectArchiveImportResult, error) {
	importRequestID := "archive-import:" + requestID
	// Check idempotent replay first.
	if existing, err := scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE request_id = ?`, importRequestID)); err == nil {
		return ProjectArchiveImportResult{
			Project:           existing,
			ImportedMaterials: len(parsed.manifest.Materials),
			ImportedFiles:     len(parsed.manifest.WorkspaceFiles),
			ImportedCitations: len(parsed.manifest.Citations),
			ArtifactCount:     len(parsed.manifest.Artifacts),
			Replayed:          true,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectArchiveImportResult{}, err
	}

	// Publish all verified objects to content-addressed storage before DB transaction.
	for digest := range parsed.objectSizes {
		if _, err := os.Stat(s.materialObject(digest)); err == nil {
			continue
		}
		zf := parsed.objectFiles[digest]
		orc, err := zf.Open()
		if err != nil {
			return ProjectArchiveImportResult{}, err
		}
		staged, err := os.CreateTemp(s.objectsDir, ".import-")
		if err != nil {
			_ = orc.Close()
			return ProjectArchiveImportResult{}, err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(staged, hash), orc)
		_ = orc.Close()
		if copyErr != nil || n != parsed.objectSizes[digest] || hex.EncodeToString(hash.Sum(nil)) != digest {
			discardMaterialStage(staged)
			if copyErr != nil {
				return ProjectArchiveImportResult{}, copyErr
			}
			return ProjectArchiveImportResult{}, errInvalidProjectArchive
		}
		if err := s.publishObject(staged, digest); err != nil {
			_ = os.Remove(staged.Name())
			return ProjectArchiveImportResult{}, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectArchiveImportResult{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(createdAtLayout)
	newProjectID := rand.Text()
	meta := parsed.manifest.Project
	res, err := tx.ExecContext(ctx, `
		INSERT INTO projects (project_id, title, created_title, display_name, template_id, status, revision, created_at, request_id, goal, deliverables, default_turn_mode)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?) ON CONFLICT (request_id) DO NOTHING
	`, newProjectID, meta.Title, meta.Title, meta.DisplayName, meta.TemplateID, projectReady, now, importRequestID, meta.Goal, meta.Deliverables, meta.DefaultTurnMode)
	if err != nil {
		return ProjectArchiveImportResult{}, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return ProjectArchiveImportResult{}, err
	}
	if inserted == 0 {
		existing, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE request_id = ?`, importRequestID))
		if err != nil {
			return ProjectArchiveImportResult{}, err
		}
		return ProjectArchiveImportResult{
			Project:           existing,
			ImportedMaterials: len(parsed.manifest.Materials),
			ImportedFiles:     len(parsed.manifest.WorkspaceFiles),
			ImportedCitations: len(parsed.manifest.Citations),
			ArtifactCount:     len(parsed.manifest.Artifacts),
			Replayed:          true,
		}, nil
	}

	importedMaterials := 0
	for _, m := range parsed.manifest.Materials {
		createdAt := m.CreatedAt
		if _, err := time.Parse(createdAtLayout, createdAt); err != nil {
			createdAt = now
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO materials (material_id, project_id, name, sha256, size, kind, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, name, sha256) DO NOTHING`,
			rand.Text(), newProjectID, m.Name, m.SHA256, m.Size, m.Kind, createdAt)
		if err != nil {
			return ProjectArchiveImportResult{}, err
		}
		if rows, _ := r.RowsAffected(); rows > 0 {
			importedMaterials++
		}
	}

	importedFiles := 0
	for _, f := range parsed.manifest.WorkspaceFiles {
		createdAt := f.CreatedAt
		if _, err := time.Parse(createdAtLayout, createdAt); err != nil {
			createdAt = now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_versions (version_id, project_id, path, sha256, size, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			rand.Text(), newProjectID, path.Clean(f.Path), f.SHA256, f.Size, createdAt); err != nil {
			return ProjectArchiveImportResult{}, err
		}
		importedFiles++
	}

	importedCitations := 0
	for _, c := range parsed.manifest.Citations {
		doi, err := literature.NormalizeDOI(c.DOI)
		if err != nil {
			return ProjectArchiveImportResult{}, errInvalidProjectArchive
		}
		verifiedAt := c.VerifiedAt
		if _, err := time.Parse(createdAtLayout, verifiedAt); err != nil {
			verifiedAt = now
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO literature_citations (project_id, doi, metadata_json, verified_at)
			VALUES (?, ?, ?, ?) ON CONFLICT (project_id, doi) DO NOTHING`,
			newProjectID, doi, string(c.Metadata), verifiedAt)
		if err != nil {
			return ProjectArchiveImportResult{}, err
		}
		if rows, _ := r.RowsAffected(); rows > 0 {
			importedCitations++
		}
	}

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, newProjectID))
	if err != nil {
		return ProjectArchiveImportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectArchiveImportResult{}, err
	}
	return ProjectArchiveImportResult{
		Project:           project,
		ImportedMaterials: importedMaterials,
		ImportedFiles:     importedFiles,
		ImportedCitations: importedCitations,
		ArtifactCount:     len(parsed.manifest.Artifacts),
		Replayed:          false,
	}, nil
}

func (s *Store) handleProjectArchiveImport(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if !validRequestID(requestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请在 X-Request-Id 请求头中提供有效的幂等请求编号。")
		return
	}
	upload, size, ok := readProjectArchiveUpload(w, r)
	if !ok {
		return
	}
	defer os.Remove(upload.Name())
	defer upload.Close()

	parsed, err := parseAndVerifyProjectArchive(upload, size)
	switch {
	case errors.Is(err, errProjectArchiveTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "项目归档解压后体积超过上限。")
		return
	case errors.Is(err, errInvalidProjectArchive):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARCHIVE", "项目归档校验失败，请确认 ZIP 完整且对象摘要未被篡改。")
		return
	case err != nil:
		writeStoreError(w, "verify project archive for import", err)
		return
	}

	result, err := s.importProjectArchive(r.Context(), requestID, parsed)
	if errors.Is(err, errInvalidProjectArchive) {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARCHIVE", "项目归档校验失败，请确认 ZIP 完整且对象摘要未被篡改。")
		return
	}
	if err != nil {
		writeStoreError(w, "import project archive", err)
		return
	}
	if result.Replayed {
		writeJSON(w, http.StatusOK, result)
		return
	}
	w.Header().Set("Location", projectsPath+"/"+result.Project.ProjectID)
	writeJSON(w, http.StatusCreated, result)
}
