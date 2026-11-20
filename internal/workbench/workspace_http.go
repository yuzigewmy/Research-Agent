package workbench

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const maxWorkspaceText = 512 << 10

func (s *Store) registerWorkspace(mux *http.ServeMux) {
	path := projectsPath + "/{id}/files"
	mux.HandleFunc("GET "+path, s.handleListWorkspace(false))
	mux.HandleFunc("GET "+path+"/text", s.handleWorkspaceText)
	mux.HandleFunc("PUT "+path+"/text", s.handleSaveWorkspace)
	mux.HandleFunc("GET "+path+"/versions", s.handleListWorkspace(true))
	mux.HandleFunc("GET "+path+"/versions/{versionId}", s.handleWorkspaceVersion)
	mux.HandleFunc("GET "+path+"/versions/{versionId}/diagram.svg", s.handleDiagramSVG)
	mux.HandleFunc("POST "+path+"/versions/{versionId}/restore", s.handleRestoreWorkspace)
}

func validWorkspacePath(value string) bool {
	return len(value) <= 512 && value != "." && fs.ValidPath(value) &&
		!strings.ContainsAny(value, `\:`) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validWorkspaceBase(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *Store) handleListWorkspace(history bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var name string
		kind := r.URL.Query().Get("kind")
		if kind != "" && (history || kind != "diagram") {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择受支持的文件类型。")
			return
		}
		if history {
			name = r.URL.Query().Get("path")
			if !validWorkspacePath(name) {
				writeError(w, http.StatusBadRequest, "INVALID_PATH", "请输入项目内的相对路径，例如 paper/main.md。")
				return
			}
		}
		cursor, err := eventCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "文件列表游标无效。")
			return
		}
		ctx, projectID := r.Context(), r.PathValue("id")
		if _, err := s.getProject(ctx, projectID); err != nil {
			writeWorkspaceError(w, "look up workspace project", err)
			return
		}
		const limit = 50
		items, err := s.listWorkspaceFiles(ctx, projectID, name, kind == "diagram", cursor, limit+1)
		if err != nil {
			writeWorkspaceError(w, "list workspace files", err)
			return
		}
		var next string
		if len(items) > limit {
			next = strconv.FormatInt(items[limit-1].ordinal, 10)
			items = items[:limit]
		}
		writeJSON(w, http.StatusOK, struct {
			Items      []WorkspaceFile `json:"items"`
			NextCursor string          `json:"nextCursor,omitempty"`
		}{items, next})
	}
}

func (s *Store) handleWorkspaceText(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("path")
	if !validWorkspacePath(name) {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "请输入项目内的相对路径，例如 paper/main.md。")
		return
	}
	file, err := s.workspaceFile(r.Context(), r.PathValue("id"), name)
	s.writeWorkspaceText(w, file, err)
}

func (s *Store) handleWorkspaceVersion(w http.ResponseWriter, r *http.Request) {
	file, err := s.workspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
	s.writeWorkspaceText(w, file, err)
}

func (s *Store) writeWorkspaceText(w http.ResponseWriter, file WorkspaceFile, err error) {
	if err == nil {
		var doc WorkspaceDocument
		doc, err = s.readWorkspaceDocument(file)
		if err == nil {
			writeJSON(w, http.StatusOK, doc)
			return
		}
	}
	writeWorkspaceError(w, "read workspace document", err)
}

func (s *Store) handleSaveWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path       string  `json:"path"`
		Content    *string `json:"content"`
		BaseSHA256 string  `json:"baseSha256"`
	}
	if !readJSON(w, r, &input, 6*maxWorkspaceText+(8<<10)) {
		return
	}
	if !validWorkspacePath(input.Path) {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "请输入项目内的相对路径，例如 paper/main.md。")
		return
	}
	if input.Content == nil || len(*input.Content) > maxWorkspaceText || strings.ContainsRune(*input.Content, 0) || !validWorkspaceBase(input.BaseSHA256) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交 512 KiB 以内的正文和有效的基线摘要。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := s.getProject(ctx, projectID); err != nil {
		writeWorkspaceError(w, "look up workspace project", err)
		return
	}
	doc, err := s.saveWorkspaceDocument(ctx, projectID, input.Path, strings.ToLower(input.BaseSHA256), *input.Content)
	if err != nil {
		writeWorkspaceError(w, "save workspace document", err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Store) handleRestoreWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BaseSHA256 string `json:"baseSha256"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if input.BaseSHA256 == "" || !validWorkspaceBase(input.BaseSHA256) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "恢复版本时请提供当前文件摘要。")
		return
	}
	doc, err := s.restoreWorkspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"), strings.ToLower(input.BaseSHA256))
	if err != nil {
		writeWorkspaceError(w, "restore workspace document", err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func writeWorkspaceError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目、文件或版本不存在。")
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "文件已有新的版本。请查看最新版后合并改动。")
	case errors.Is(err, errInvalidDiagram):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DIAGRAM", "请核对流程图名称、节点位置、标签和连线端点。")
	default:
		writeStoreError(w, operation, err)
	}
}
