package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"research-agent/internal/wordexport"
)

const wordExportSchema = `
	CREATE TABLE word_exports (
		version_id TEXT NOT NULL PRIMARY KEY REFERENCES workspace_versions(version_id),
		sha256 TEXT NOT NULL,
		docx BLOB NOT NULL,
		created_at TEXT NOT NULL
	);
	PRAGMA user_version = 8;
`

const wordContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type wordExport struct {
	Filename     string `json:"filename"`
	SourceSHA256 string `json:"sourceSha256"`
	SHA256       string `json:"sha256"`
	Size         int    `json:"size"`
	CreatedAt    string `json:"createdAt"`
}

func wordFilename(name string) string {
	name = path.Base(name)
	return strings.TrimSuffix(name, path.Ext(name)) + ".docx"
}

func (s *Store) registerWord(mux *http.ServeMux, python string) {
	route := projectsPath + "/{id}/files/versions/{versionId}/word"
	mux.HandleFunc("GET "+route, s.handleWord(python, false))
	mux.HandleFunc("POST "+route, s.handleWord(python, true))
	mux.HandleFunc("GET "+route+"/download", s.handleWordDownload)
	mux.HandleFunc(route, methodNotAllowed("GET, HEAD, POST"))
}

func (s *Store) wordReceipt(ctx context.Context, source WorkspaceFile) (wordExport, error) {
	result := wordExport{Filename: wordFilename(source.Path), SourceSHA256: source.SHA256}
	err := s.db.QueryRowContext(ctx, `SELECT sha256, length(docx), created_at FROM word_exports WHERE version_id = ?`,
		source.VersionID).Scan(&result.SHA256, &result.Size, &result.CreatedAt)
	return result, err
}

func (s *Store) handleWord(python string, generate bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		file, err := s.workspaceVersion(ctx, r.PathValue("id"), r.PathValue("versionId"))
		if err != nil {
			writeWorkspaceError(w, "read Word export source", err)
			return
		}
		switch strings.ToLower(path.Ext(file.Path)) {
		case ".md", ".markdown":
		default:
			writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "Word 导出支持已保存的 Markdown 稿件。")
			return
		}
		receipt, err := s.wordReceipt(ctx, file)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			writeStoreError(w, "read Word export receipt", err)
			return
		}
		if !generate {
			status := struct {
				Configured bool        `json:"configured"`
				File       *wordExport `json:"file"`
			}{Configured: python != ""}
			if !missing {
				status.File = &receipt
			}
			writeJSON(w, http.StatusOK, status)
			return
		}
		if !missing {
			writeJSON(w, http.StatusOK, receipt)
			return
		}
		if python == "" {
			writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING", "请先启用 Word 导出组件。")
			return
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			writeStoreError(w, "read Markdown for Word export", err)
			return
		}
		if strings.TrimSpace(doc.Content) == "" {
			writeError(w, http.StatusUnprocessableEntity, "EMPTY_DOCUMENT", "请先保存稿件正文。")
			return
		}
		renderCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		body, err := wordexport.Export(renderCtx, python, doc.Content)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "EXPORT_FAILED", "Word 生成失败。请检查排版组件和稿件格式。")
			return
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err := s.db.ExecContext(ctx, `INSERT INTO word_exports(version_id, sha256, docx, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(version_id) DO NOTHING`, file.VersionID, digest, body, time.Now().UTC().Format(createdAtLayout)); err != nil {
			writeStoreError(w, "publish Word export", err)
			return
		}
		receipt, err = s.wordReceipt(ctx, file)
		if err != nil {
			writeStoreError(w, "read published Word receipt", err)
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	}
}

func (s *Store) handleWordDownload(w http.ResponseWriter, r *http.Request) {
	var source string
	var body []byte
	err := s.db.QueryRowContext(r.Context(), `SELECT v.path, e.docx FROM word_exports e
		JOIN workspace_versions v ON v.version_id = e.version_id WHERE v.project_id = ? AND v.version_id = ?`,
		r.PathValue("id"), r.PathValue("versionId")).Scan(&source, &body)
	if err != nil {
		writeWorkspaceError(w, "download Word export", err)
		return
	}
	w.Header().Set("Content-Type", wordContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": wordFilename(source)}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
