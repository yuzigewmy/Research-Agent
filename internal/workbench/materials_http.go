package workbench

import (
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxUploadBytes = 64 << 20

func (s *Store) registerMaterials(mux *http.ServeMux) {
	path := projectsPath + "/{id}/materials"
	mux.HandleFunc("GET "+path, s.handleListMaterials)
	mux.HandleFunc("POST "+path, s.handleUploadMaterial)
	mux.HandleFunc("GET "+path+"/{materialId}/download", s.handleDownloadMaterial)
	mux.HandleFunc("GET "+path+"/{materialId}/preview", s.handlePreviewMaterial)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc(path+"/{materialId}/download", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(path+"/{materialId}/preview", methodNotAllowed("GET, HEAD"))
}

func (s *Store) handleListMaterials(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "资料列表游标无效。")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if !utf8.ValidString(search) || strings.ContainsRune(search, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "搜索词必须是有效文本，且不能包含空字符。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := s.getProject(ctx, projectID); err != nil {
		writeMaterialError(w, "read project for materials", err)
		return
	}
	const pageSize = 50
	items, err := s.listMaterials(ctx, projectID, search, cursor, pageSize+1)
	if err != nil {
		writeStoreError(w, "list materials", err)
		return
	}
	writeMaterialPage(w, items, pageSize)
}

func writeMaterialPage(w http.ResponseWriter, items []Material, pageSize int) {
	var next string
	if len(items) > pageSize {
		next = strconv.FormatInt(items[pageSize-1].ordinal, 10)
		items = items[:pageSize]
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []Material `json:"items"`
		NextCursor string     `json:"nextCursor,omitempty"`
	}{items, next})
}

func materialUpload(w http.ResponseWriter, r *http.Request) (string, io.Reader) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请以原始文件格式上传。")
		return "", nil
	}
	name := r.URL.Query().Get("name")
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") ||
		!utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.ContainsFunc(name, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供一个有效的文件名。")
		return "", nil
	}
	if r.ContentLength > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "单个资料不能超过 64 MiB。")
		return "", nil
	}
	return name, http.MaxBytesReader(w, r.Body, maxUploadBytes)
}

func writeMaterialTooLarge(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		return false
	}
	writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "单个资料不能超过 64 MiB。")
	return true
}

func (s *Store) handleUploadMaterial(w http.ResponseWriter, r *http.Request) {
	name, body := materialUpload(w, r)
	if body == nil {
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := s.getProject(ctx, projectID); err != nil {
		writeMaterialError(w, "read project for upload", err)
		return
	}
	material, created, err := s.saveMaterial(ctx, projectID, name, body)
	if writeMaterialTooLarge(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, "save material", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, material)
}

func (s *Store) handleDownloadMaterial(w http.ResponseWriter, r *http.Request) {
	material, err := s.getMaterial(r.Context(), r.PathValue("id"), r.PathValue("materialId"))
	if err != nil {
		writeMaterialError(w, "read material for download", err)
		return
	}
	s.serveMaterial(w, r, material)
}

func (s *Store) serveMaterial(w http.ResponseWriter, r *http.Request, material Material) {
	file, err := s.openObject(material.SHA256)
	if err != nil {
		writeStoreError(w, "open material object", err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": material.Name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, material.Name, time.Time{}, file)
}

func (s *Store) handlePreviewMaterial(w http.ResponseWriter, r *http.Request) {
	material, err := s.getMaterial(r.Context(), r.PathValue("id"), r.PathValue("materialId"))
	if err != nil {
		writeMaterialError(w, "read material for preview", err)
		return
	}
	excerpt, err := s.previewMaterial(r.Context(), material, materialPreviewBytes)
	if errors.Is(err, errMaterialNotText) {
		writeError(w, http.StatusUnprocessableEntity, "PREVIEW_UNAVAILABLE", "无法提取此资料的正文，可下载原件查看。")
		return
	}
	if err != nil {
		writeStoreError(w, "preview material", err)
		return
	}
	writeJSON(w, http.StatusOK, excerpt)
}

func writeMaterialError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目或资料不存在。")
		return
	}
	writeStoreError(w, operation, err)
}
