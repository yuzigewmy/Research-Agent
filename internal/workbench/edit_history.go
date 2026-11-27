package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const editHistoryStatusApplied = "APPLIED"

var errEditNotApplied = errors.New("edit has not been applied")

type EditHistoryItem struct {
	EditID           string `json:"editId"`
	Kind             string `json:"kind"`
	Path             string `json:"path"`
	Status           string `json:"status"`
	Instruction      string `json:"instruction"`
	Original         string `json:"original"`
	Replacement      string `json:"replacement,omitempty"`
	Reason           string `json:"reason,omitempty"`
	SourceVersionID  string `json:"sourceVersionId"`
	SourceSHA256     string `json:"sourceSha256"`
	AppliedVersionID string `json:"appliedVersionId,omitempty"`
	AppliedSHA256    string `json:"appliedSha256,omitempty"`
	ReviewID         string `json:"reviewId,omitempty"`
	FindingNumber    int    `json:"findingNumber,omitempty"`
	CreatedAt        string `json:"createdAt"`
	CanUndo          bool   `json:"canUndo"`
}

type EditHistoryPage struct {
	Items   []EditHistoryItem `json:"items"`
	HasMore bool              `json:"hasMore"`
}

type EditHistoryUndoResult struct {
	EditID          string        `json:"editId"`
	Path            string        `json:"path"`
	SourceVersionID string        `json:"sourceVersionId"`
	BeforeSnapshot  WorkspaceFile `json:"beforeSnapshot"`
	RestoredVersion WorkspaceFile `json:"restoredVersion"`
}

func (s *Store) registerEditHistory(mux *http.ServeMux) {
	base := projectsPath + "/{id}/edit-history"
	mux.HandleFunc("GET "+base, s.handleListEditHistory)
	mux.HandleFunc(base, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("POST "+base+"/{editId}/undo", s.handleUndoEditHistory)
	mux.HandleFunc(base+"/{editId}/undo", methodNotAllowed("POST"))
}

func validEditHistoryStatusFilter(status string) bool {
	if status == "" {
		return true
	}
	switch strings.ToUpper(status) {
	case editHistoryStatusApplied, taskQueued, taskRunning, taskSucceeded, taskFailed, taskCancelled, taskInterrupted:
		return true
	default:
		return false
	}
}

func (s *Store) listEditHistory(ctx context.Context, projectID, pathFilter, statusFilter string, limit, offset int) (EditHistoryPage, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		return EditHistoryPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH all_edits AS (
			SELECT
				t.ordinal AS sort_ordinal,
				0 AS sort_pos,
				e.task_id AS edit_id,
				CASE WHEN e.review_id IS NOT NULL AND e.review_id <> '' THEN 'review_finding' ELSE 'selection' END AS kind,
				sv.path AS path,
				CASE WHEN e.applied_version_id IS NOT NULL AND e.applied_version_id <> '' THEN 'APPLIED' ELSE t.status END AS status,
				t.instruction AS instruction,
				e.original AS original,
				t.markdown AS task_markdown,
				t.status AS task_status,
				e.source_version_id AS source_version_id,
				sv.sha256 AS source_sha256,
				COALESCE(e.applied_version_id, '') AS applied_version_id,
				COALESCE(av.sha256, '') AS applied_sha256,
				t.created_at AS created_at,
				COALESCE(e.review_id, '') AS review_id,
				COALESCE(e.finding_number, 0) AS finding_number
			FROM selection_edits e
			JOIN tasks t ON t.project_id = e.project_id AND t.task_id = e.task_id
			JOIN workspace_versions sv ON sv.project_id = e.project_id AND sv.version_id = e.source_version_id
			LEFT JOIN workspace_versions av ON av.project_id = e.project_id AND av.version_id = e.applied_version_id
			WHERE e.project_id = ?

			UNION ALL

			SELECT
				t.ordinal AS sort_ordinal,
				b.position AS sort_pos,
				b.task_id || ':' || b.source_version_id AS edit_id,
				'batch' AS kind,
				sv.path AS path,
				CASE WHEN b.applied_version_id IS NOT NULL AND b.applied_version_id <> '' THEN 'APPLIED' ELSE t.status END AS status,
				t.instruction AS instruction,
				b.original AS original,
				t.markdown AS task_markdown,
				t.status AS task_status,
				b.source_version_id AS source_version_id,
				sv.sha256 AS source_sha256,
				COALESCE(b.applied_version_id, '') AS applied_version_id,
				COALESCE(av.sha256, '') AS applied_sha256,
				t.created_at AS created_at,
				'' AS review_id,
				0 AS finding_number
			FROM batch_selection_edits b
			JOIN tasks t ON t.project_id = b.project_id AND t.task_id = b.task_id
			JOIN workspace_versions sv ON sv.project_id = b.project_id AND sv.version_id = b.source_version_id
			LEFT JOIN workspace_versions av ON av.project_id = b.project_id AND av.version_id = b.applied_version_id
			WHERE b.project_id = ?
		)
		SELECT edit_id, kind, path, status, instruction, original, task_markdown, task_status,
			source_version_id, source_sha256, applied_version_id, applied_sha256, created_at, review_id, finding_number
		FROM all_edits
		WHERE (? = '' OR instr(lower(path), lower(?)) > 0)
			AND (? = '' OR upper(status) = upper(?))
		ORDER BY sort_ordinal DESC, sort_pos ASC
		LIMIT ? OFFSET ?`,
		projectID, projectID, pathFilter, pathFilter, statusFilter, statusFilter, limit+1, offset)
	if err != nil {
		return EditHistoryPage{}, err
	}
	defer rows.Close()

	items := make([]EditHistoryItem, 0, limit)
	hasMore := false
	for rows.Next() {
		var item EditHistoryItem
		var taskMarkdown, taskStatus string
		if err := rows.Scan(
			&item.EditID, &item.Kind, &item.Path, &item.Status, &item.Instruction, &item.Original,
			&taskMarkdown, &taskStatus, &item.SourceVersionID, &item.SourceSHA256,
			&item.AppliedVersionID, &item.AppliedSHA256, &item.CreatedAt, &item.ReviewID, &item.FindingNumber,
		); err != nil {
			return EditHistoryPage{}, err
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		if taskStatus == taskSucceeded && taskMarkdown != "" {
			if item.Kind == "batch" {
				var suggestions map[string]EditSuggestion
				if json.Unmarshal([]byte(taskMarkdown), &suggestions) == nil {
					if suggestion, ok := suggestions[item.SourceVersionID]; ok {
						item.Replacement = suggestion.Replacement
						item.Reason = suggestion.Reason
					}
				}
			} else {
				var suggestion EditSuggestion
				if json.Unmarshal([]byte(taskMarkdown), &suggestion) == nil {
					item.Replacement = suggestion.Replacement
					item.Reason = suggestion.Reason
				}
			}
		}
		item.CanUndo = item.Status == editHistoryStatusApplied
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return EditHistoryPage{}, err
	}
	return EditHistoryPage{Items: items, HasMore: hasMore}, nil
}

func (s *Store) undoEditHistoryItem(ctx context.Context, projectID, editID, expectedCurrentSHA256 string, force bool) (EditHistoryUndoResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EditHistoryUndoResult{}, err
	}
	defer tx.Rollback()

	if _, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID)); err != nil {
		return EditHistoryUndoResult{}, err
	}

	var sourceVersionID, appliedVersionID string
	if taskID, srcID, isComposite := strings.Cut(editID, ":"); isComposite {
		err = tx.QueryRowContext(ctx, `SELECT source_version_id, COALESCE(applied_version_id, '')
			FROM batch_selection_edits WHERE project_id = ? AND task_id = ? AND source_version_id = ?`,
			projectID, taskID, srcID).Scan(&sourceVersionID, &appliedVersionID)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT source_version_id, COALESCE(applied_version_id, '')
			FROM selection_edits WHERE project_id = ? AND task_id = ?`,
			projectID, editID).Scan(&sourceVersionID, &appliedVersionID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT source_version_id, COALESCE(applied_version_id, '')
				FROM batch_selection_edits WHERE project_id = ? AND task_id = ? ORDER BY position ASC LIMIT 1`,
				projectID, editID).Scan(&sourceVersionID, &appliedVersionID)
		}
	}
	if err != nil {
		return EditHistoryUndoResult{}, err
	}
	if appliedVersionID == "" {
		return EditHistoryUndoResult{}, errEditNotApplied
	}

	sourceFile, err := lookupWorkspaceVersion(ctx, tx, projectID, sourceVersionID)
	if err != nil {
		return EditHistoryUndoResult{}, err
	}
	currentFile, err := currentWorkspaceFile(ctx, tx, projectID, sourceFile.Path)
	if err != nil {
		return EditHistoryUndoResult{}, err
	}
	if expectedCurrentSHA256 != "" {
		if currentFile.SHA256 != strings.ToLower(expectedCurrentSHA256) {
			return EditHistoryUndoResult{}, errWorkspaceConflict
		}
	} else if !force && currentFile.VersionID != appliedVersionID {
		return EditHistoryUndoResult{}, errWorkspaceConflict
	}

	restoredFile := currentFile
	if currentFile.SHA256 != sourceFile.SHA256 {
		restoredFile, err = s.insertWorkspaceVersion(ctx, tx, projectID, sourceFile, nil)
		if err != nil {
			return EditHistoryUndoResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EditHistoryUndoResult{}, err
	}
	return EditHistoryUndoResult{
		EditID:          editID,
		Path:            sourceFile.Path,
		SourceVersionID: sourceFile.VersionID,
		BeforeSnapshot:  currentFile,
		RestoredVersion: restoredFile,
	}, nil
}

func (s *Store) handleListEditHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	pathFilter := strings.TrimSpace(query.Get("path"))
	if len(pathFilter) > 512 || !utf8.ValidString(pathFilter) || strings.ContainsFunc(pathFilter, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "路径筛选词须为 512 字节以内的有效文本。")
		return
	}
	statusFilter := strings.TrimSpace(query.Get("status"))
	if !validEditHistoryStatusFilter(statusFilter) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择受支持的编辑历史状态筛选值。")
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "limit 须为 1 至 100 之间的整数。")
			return
		}
		limit = parsed
	}
	offset := 0
	if rawOffset := strings.TrimSpace(query.Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "offset 须为不小于 0 的整数。")
			return
		}
		offset = parsed
	}
	page, err := s.listEditHistory(r.Context(), r.PathValue("id"), pathFilter, statusFilter, limit, offset)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, page)
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	default:
		writeStoreError(w, "list edit history", err)
	}
}

func (s *Store) handleUndoEditHistory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedCurrentSHA256 string `json:"expectedCurrentSha256"`
		Force                 bool   `json:"force"`
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "无法读取请求正文。")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的 JSON 参数。")
				return
			}
		}
	}
	if !validWorkspaceBase(input.ExpectedCurrentSHA256) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的当前文件基线摘要。")
		return
	}
	editID := r.PathValue("editId")
	if !validRequestID(editID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的编辑记录编号。")
		return
	}
	result, err := s.undoEditHistoryItem(r.Context(), r.PathValue("id"), editID, input.ExpectedCurrentSHA256, input.Force)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目或编辑记录不存在。")
	case errors.Is(err, errEditNotApplied):
		writeError(w, http.StatusConflict, "EDIT_NOT_APPLIED", "仅支持撤销状态为已应用（APPLIED）的编辑记录。")
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "文件在本次改动后已有新版本，请核对最新版或显式确认恢复应用前原稿。")
	default:
		writeStoreError(w, "undo edit history", err)
	}
}
