package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const (
	engineeringTemplate         = "engineering_project"
	maxEngineeringPreviewAssets = 24
)

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

type engineeringPreviewAsset struct {
	RelativePath string `json:"relativePath"`
	WorkspaceFile
}

type engineeringPreview struct {
	PreviewID string                    `json:"previewId"`
	File      WorkspaceFile             `json:"file"`
	Assets    []engineeringPreviewAsset `json:"assets,omitempty"`
}

type engineeringPreviewSnapshot struct {
	Preview *engineeringPreview `json:"preview"`
}

type previewQuery interface {
	workspaceQuery
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func engineeringProjectStatus(ctx context.Context, db workspaceQuery, projectID string) (string, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM projects WHERE project_id = ? AND template_id = ?`,
		projectID, engineeringTemplate).Scan(&status)
	return status, err
}

func previewAssetContentType(name string) (string, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".css":
		return "text/css; charset=utf-8", true
	case ".js":
		return "application/javascript; charset=utf-8", true
	case ".svg":
		return "image/svg+xml", true
	case ".json":
		return "application/json; charset=utf-8", true
	case ".txt":
		return "text/plain; charset=utf-8", true
	default:
		return "", false
	}
}

func previewAssetRelativePath(entryPath, candidatePath string) (string, bool) {
	if _, ok := previewAssetContentType(candidatePath); !ok {
		return "", false
	}
	entryDir := path.Dir(entryPath)
	if entryDir == "." {
		if !validWorkspacePath(candidatePath) {
			return "", false
		}
		return candidatePath, true
	}
	rel, ok := strings.CutPrefix(candidatePath, entryDir+"/")
	if !ok || !validWorkspacePath(rel) {
		return "", false
	}
	return rel, true
}

func previewSnapshotOrdinal(previewID string, fallback int64) int64 {
	if idx := strings.LastIndexByte(previewID, '-'); idx > 0 {
		if ord, err := strconv.ParseInt(previewID[idx+1:], 10, 64); err == nil && ord > 0 {
			return ord
		}
	}
	return fallback
}

func readEngineeringPreviewAssets(ctx context.Context, db previewQuery, projectID, entryPath string, snapshotOrdinal int64) ([]engineeringPreviewAsset, error) {
	if snapshotOrdinal <= 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+workspaceFileColumns+`
		FROM workspace_versions
		WHERE project_id = ? AND ordinal IN (
			SELECT MAX(ordinal) FROM workspace_versions
			WHERE project_id = ? AND ordinal <= ?
			GROUP BY path
		)
		ORDER BY path ASC`,
		projectID, projectID, snapshotOrdinal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []engineeringPreviewAsset
	for rows.Next() {
		file, err := scanWorkspaceFile(rows)
		if err != nil {
			return nil, err
		}
		rel, ok := previewAssetRelativePath(entryPath, file.Path)
		if !ok {
			continue
		}
		assets = append(assets, engineeringPreviewAsset{
			RelativePath:  rel,
			WorkspaceFile: file,
		})
		if len(assets) >= maxEngineeringPreviewAssets {
			break
		}
	}
	return assets, rows.Err()
}

func readEngineeringPreview(ctx context.Context, db previewQuery, projectID, previewID string) (engineeringPreview, error) {
	var preview engineeringPreview
	file := &preview.File
	err := db.QueryRowContext(ctx, `SELECT e.preview_id, f.ordinal, f.path, f.version_id, f.sha256, f.size, f.created_at
		FROM engineering_previews e JOIN workspace_versions f ON f.version_id = e.version_id AND f.project_id = e.project_id
		JOIN projects p ON p.project_id = e.project_id
		WHERE e.project_id = ? AND (? = '' OR e.preview_id = ?) AND p.status = 'READY' AND p.template_id = ?`,
		projectID, previewID, previewID, engineeringTemplate).
		Scan(&preview.PreviewID, &file.ordinal, &file.Path, &file.VersionID, &file.SHA256, &file.Size, &file.CreatedAt)
	if err != nil {
		return engineeringPreview{}, err
	}
	assets, err := readEngineeringPreviewAssets(ctx, db, projectID, file.Path, previewSnapshotOrdinal(preview.PreviewID, file.ordinal))
	if err != nil {
		return engineeringPreview{}, err
	}
	preview.Assets = assets
	return preview, nil
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
	if !htmlWorkspacePath(file.Path) {
		return engineeringPreview{}, false, errEngineeringPreviewFormat
	}

	preview, err := readEngineeringPreview(ctx, tx, projectID, "")
	if err == nil && preview.File.VersionID == versionID {
		return preview, false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return engineeringPreview{}, false, err
	}
	var snapshotOrdinal int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) FROM workspace_versions WHERE project_id = ?`, projectID).Scan(&snapshotOrdinal); err != nil {
		return engineeringPreview{}, false, err
	}
	if snapshotOrdinal < file.ordinal {
		snapshotOrdinal = file.ordinal
	}
	previewID := fmt.Sprintf("%s-%d", rand.Text(), snapshotOrdinal)
	assets, err := readEngineeringPreviewAssets(ctx, tx, projectID, file.Path, snapshotOrdinal)
	if err != nil {
		return engineeringPreview{}, false, err
	}
	preview = engineeringPreview{PreviewID: previewID, File: file, Assets: assets}
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
	mux.HandleFunc("GET "+base+"/{previewId}/{assetPath...}", s.handleEngineeringPreviewAsset)
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

func isSafeCSPHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == ':', r == '-', r == '[', r == ']':
		default:
			return false
		}
	}
	return true
}

func engineeringPreviewCSP(r *http.Request, projectID, previewID string) string {
	host := strings.TrimSpace(r.Host)
	if !isSafeCSPHost(host) {
		return engineeringPreviewPolicy
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	prefix := fmt.Sprintf("%s://%s%s/%s/preview/%s/",
		scheme, host, projectsPath, url.PathEscape(projectID), url.PathEscape(previewID))
	return fmt.Sprintf(
		"default-src 'none'; script-src 'unsafe-inline' %s; style-src 'unsafe-inline' %s; img-src data: %s; connect-src %s; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts",
		prefix, prefix, prefix, prefix,
	)
}

func (s *Store) handleEngineeringPreviewDocument(w http.ResponseWriter, r *http.Request) {
	projectID, previewID := r.PathValue("id"), r.PathValue("previewId")
	preview, err := readEngineeringPreview(r.Context(), s.db, projectID, previewID)
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
	w.Header().Set("Content-Security-Policy", engineeringPreviewCSP(r, projectID, previewID))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.Content)))
	w.Header().Set("X-Source-SHA256", doc.SHA256)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, doc.Content)
	}
}

func hasRawPathTraversal(raw string) bool {
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "..") || strings.Contains(lower, "%2e") ||
		strings.Contains(lower, "%5c") || strings.Contains(lower, `\`) || strings.Contains(lower, "%00")
}

func (s *Store) handleEngineeringPreviewAsset(w http.ResponseWriter, r *http.Request) {
	if hasRawPathTraversal(r.URL.RawPath) || hasRawPathTraversal(r.URL.Path) {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "静态资源路径无效。")
		return
	}
	rel := r.PathValue("assetPath")
	if !validWorkspacePath(rel) {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "静态资源路径无效。")
		return
	}
	contentType, ok := previewAssetContentType(rel)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该静态预览资源不存在。")
		return
	}
	projectID, previewID := r.PathValue("id"), r.PathValue("previewId")
	preview, err := readEngineeringPreview(r.Context(), s.db, projectID, previewID)
	if err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	var matched *engineeringPreviewAsset
	for i := range preview.Assets {
		if preview.Assets[i].RelativePath == rel {
			matched = &preview.Assets[i]
			break
		}
	}
	if matched == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该静态预览资源不存在或未冻结。")
		return
	}
	doc, err := s.readWorkspaceDocument(matched.WorkspaceFile)
	if err != nil {
		writeEngineeringPreviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
