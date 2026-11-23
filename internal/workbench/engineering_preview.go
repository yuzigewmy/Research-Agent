package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
)

const engineeringTemplate = "engineering_project"

const engineeringPreviewSchema = `
	CREATE TABLE engineering_previews (
		project_id TEXT PRIMARY KEY NOT NULL REFERENCES projects(project_id),
		preview_id TEXT NOT NULL UNIQUE,
		version_id TEXT NOT NULL REFERENCES workspace_versions(version_id)
	);
	PRAGMA user_version = 42;
`

const engineeringPreviewPolicy = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"

var (
	errEngineeringPreviewState  = errors.New("engineering preview state changed")
	errEngineeringPreviewFormat = errors.New("engineering preview requires an HTML document")
)

type engineeringPreview struct {
	PreviewID string        `json:"previewId"`
	File      WorkspaceFile `json:"file"`
}

type engineeringPreviewSnapshot struct {
	Preview *engineeringPreview `json:"preview"`
}

func engineeringProjectStatus(ctx context.Context, db workspaceQuery, projectID string) (string, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM projects WHERE project_id = ? AND template_id = ?`,
		projectID, engineeringTemplate).Scan(&status)
	return status, err
}

func readEngineeringPreview(ctx context.Context, db workspaceQuery, projectID, previewID string) (engineeringPreview, error) {
	var preview engineeringPreview
	file := &preview.File
	err := db.QueryRowContext(ctx, `SELECT e.preview_id, f.ordinal, f.path, f.version_id, f.sha256, f.size, f.created_at
		FROM engineering_previews e JOIN workspace_versions f ON f.version_id = e.version_id AND f.project_id = e.project_id
		JOIN projects p ON p.project_id = e.project_id
		WHERE e.project_id = ? AND (? = '' OR e.preview_id = ?) AND p.status = 'READY' AND p.template_id = ?`,
		projectID, previewID, previewID, engineeringTemplate).
		Scan(&preview.PreviewID, &file.ordinal, &file.Path, &file.VersionID, &file.SHA256, &file.Size, &file.CreatedAt)
	return preview, err
}

func (s *Store) startEngineeringPreview(ctx context.Context, projectID, versionID string) (engineeringPreview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return engineeringPreview{}, false, err
	}
	defer tx.Rollback()
	status, err := engineeringProjectStatus(ctx, tx, projectID)
	if err != nil {
		return engineeringPreview{}, false, err
	}
	if status != projectReady {
		return engineeringPreview{}, false, errEngineeringPreviewState
	}
	file, err := lookupWorkspaceVersion(ctx, tx, projectID, versionID)
	if err != nil {
		return engineeringPreview{}, false, err
	}
	extension := strings.ToLower(path.Ext(file.Path))
	if extension != ".html" && extension != ".htm" {
		return engineeringPreview{}, false, errEngineeringPreviewFormat
	}

	preview, err := readEngineeringPreview(ctx, tx, projectID, "")
	if err == nil && preview.File.VersionID == versionID {
		return preview, false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return engineeringPreview{}, false, err
	}
	preview = engineeringPreview{PreviewID: rand.Text(), File: file}
	if _, err := tx.ExecContext(ctx, `INSERT INTO engineering_previews(project_id, preview_id, version_id)
		VALUES (?, ?, ?) ON CONFLICT(project_id) DO UPDATE SET preview_id = excluded.preview_id, version_id = excluded.version_id`,
		projectID, preview.PreviewID, versionID); err != nil {
		return engineeringPreview{}, false, err
	}
	return preview, true, tx.Commit()
}

func (s *Store) stopEngineeringPreview(ctx context.Context, projectID, previewID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := engineeringProjectStatus(ctx, tx, projectID); err != nil {
		return err
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `SELECT preview_id FROM engineering_previews WHERE project_id = ?`, projectID).Scan(&activeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if activeID != previewID {
		return errEngineeringPreviewState
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM engineering_previews WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) registerEngineeringPreview(mux *http.ServeMux) {
	base := projectsPath + "/{id}/preview"
	mux.HandleFunc("GET "+base, s.handleEngineeringPreview)
	mux.HandleFunc("POST "+base, s.handleStartEngineeringPreview)
	mux.HandleFunc("DELETE "+base+"/{previewId}", s.handleStopEngineeringPreview)
	mux.HandleFunc("GET "+base+"/{previewId}/document", s.handleEngineeringPreviewDocument)
}

func (s *Store) handleEngineeringPreview(w http.ResponseWriter, r *http.Request) {
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := engineeringProjectStatus(ctx, s.db, projectID); err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	preview, err := readEngineeringPreview(ctx, s.db, projectID, "")
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, http.StatusOK, engineeringPreviewSnapshot{})
	case err != nil:
		writeEngineeringPreviewError(w, err)
	default:
		writeJSON(w, http.StatusOK, engineeringPreviewSnapshot{Preview: &preview})
	}
}

func (s *Store) handleStartEngineeringPreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		VersionID string `json:"versionId"`
	}
	if !readJSON(w, r, &input, 1<<10) {
		return
	}
	if !validRequestID(input.VersionID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择已保存的 HTML 文件版本。")
		return
	}
	preview, created, err := s.startEngineeringPreview(r.Context(), r.PathValue("id"), input.VersionID)
	if err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, engineeringPreviewSnapshot{Preview: &preview})
}

func (s *Store) handleStopEngineeringPreview(w http.ResponseWriter, r *http.Request) {
	if err := s.stopEngineeringPreview(r.Context(), r.PathValue("id"), r.PathValue("previewId")); err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, engineeringPreviewSnapshot{})
}

func (s *Store) handleEngineeringPreviewDocument(w http.ResponseWriter, r *http.Request) {
	preview, err := readEngineeringPreview(r.Context(), s.db, r.PathValue("id"), r.PathValue("previewId"))
	if err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	doc, err := s.readWorkspaceDocument(preview.File)
	if err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", engineeringPreviewPolicy)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.Content)))
	w.Header().Set("X-Source-SHA256", doc.SHA256)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, doc.Content)
	}
}

func writeEngineeringPreviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该工程项目、文件版本或预览不存在。")
	case errors.Is(err, errEngineeringPreviewState):
		writeError(w, http.StatusConflict, "PREVIEW_STATE_CONFLICT", "项目已归档或活动预览已更换，请刷新状态。")
	case errors.Is(err, errEngineeringPreviewFormat):
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "预览支持已保存的 HTML 或 HTM 文件。")
	default:
		writeStoreError(w, "engineering preview", err)
	}
}
