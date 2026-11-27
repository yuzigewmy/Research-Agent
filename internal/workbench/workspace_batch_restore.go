package workbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const workspaceBatchRestoreLimit = 24

var errBatchRestorePathMismatch = errors.New("target version does not belong to the requested path")

type workspaceRestorePreviewInputItem struct {
	Path            string `json:"path"`
	TargetVersionID string `json:"targetVersionId"`
}

type workspaceRestorePreviewItem struct {
	Path             string `json:"path"`
	CurrentVersionID string `json:"currentVersionId"`
	CurrentSHA256    string `json:"currentSha256"`
	TargetVersionID  string `json:"targetVersionId"`
	TargetSHA256     string `json:"targetSha256"`
	Changed          bool   `json:"changed"`
	Summary          string `json:"summary"`
}

type workspaceRestoreBatchInputItem struct {
	Path                  string `json:"path"`
	TargetVersionID       string `json:"targetVersionId"`
	ExpectedCurrentSHA256 string `json:"expectedCurrentSha256"`
}

type workspaceRestoreBatchResult struct {
	BeforeSnapshots []WorkspaceFile `json:"beforeSnapshots"`
	RestoredFiles   []WorkspaceFile `json:"restoredFiles"`
}

func (s *Store) registerWorkspaceBatchRestore(mux *http.ServeMux) {
	base := projectsPath + "/{id}/workspace"
	mux.HandleFunc("POST "+base+"/restore-preview", s.handleWorkspaceRestorePreview)
	mux.HandleFunc(base+"/restore-preview", methodNotAllowed("POST"))
	mux.HandleFunc("POST "+base+"/restore-batch", s.handleWorkspaceRestoreBatch)
	mux.HandleFunc(base+"/restore-batch", methodNotAllowed("POST"))
}

func countTextLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func formatRestoreDiffSummary(currentText, targetText string, changed bool) string {
	curLines, tgtLines := countTextLines(currentText), countTextLines(targetText)
	curBytes, tgtBytes := len(currentText), len(targetText)
	if !changed {
		return fmt.Sprintf("内容与当前版本一致（%d 行，%d B），无需变更", curLines, curBytes)
	}
	return fmt.Sprintf("当前 %d 行（%d B）→ 目标 %d 行（%d B），行数 %+d，字节 %+d B",
		curLines, curBytes, tgtLines, tgtBytes, tgtLines-curLines, tgtBytes-curBytes)
}

func (s *Store) previewWorkspaceBatchRestore(ctx context.Context, projectID string, items []workspaceRestorePreviewInputItem) ([]workspaceRestorePreviewItem, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		return nil, err
	}
	results := make([]workspaceRestorePreviewItem, 0, len(items))
	for _, item := range items {
		target, err := s.workspaceVersion(ctx, projectID, item.TargetVersionID)
		if err != nil {
			return nil, err
		}
		if target.Path != item.Path {
			return nil, errBatchRestorePathMismatch
		}
		current, err := s.workspaceFile(ctx, projectID, item.Path)
		if err != nil {
			return nil, err
		}
		currentDoc, err := s.readWorkspaceDocument(current)
		if err != nil {
			return nil, err
		}
		targetDoc, err := s.readWorkspaceDocument(target)
		if err != nil {
			return nil, err
		}
		changed := current.SHA256 != target.SHA256
		results = append(results, workspaceRestorePreviewItem{
			Path:             item.Path,
			CurrentVersionID: current.VersionID,
			CurrentSHA256:    current.SHA256,
			TargetVersionID:  target.VersionID,
			TargetSHA256:     target.SHA256,
			Changed:          changed,
			Summary:          formatRestoreDiffSummary(currentDoc.Content, targetDoc.Content, changed),
		})
	}
	return results, nil
}

func (s *Store) executeWorkspaceBatchRestore(ctx context.Context, projectID string, items []workspaceRestoreBatchInputItem) (workspaceRestoreBatchResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspaceRestoreBatchResult{}, err
	}
	defer tx.Rollback()

	if _, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID)); err != nil {
		return workspaceRestoreBatchResult{}, err
	}

	result := workspaceRestoreBatchResult{
		BeforeSnapshots: make([]WorkspaceFile, 0, len(items)),
		RestoredFiles:   make([]WorkspaceFile, 0, len(items)),
	}
	for _, item := range items {
		target, err := lookupWorkspaceVersion(ctx, tx, projectID, item.TargetVersionID)
		if err != nil {
			return workspaceRestoreBatchResult{}, err
		}
		if target.Path != item.Path {
			return workspaceRestoreBatchResult{}, errBatchRestorePathMismatch
		}
		targetDoc, err := s.readWorkspaceDocument(target)
		if err != nil {
			return workspaceRestoreBatchResult{}, err
		}
		if err := validateDiagramDocument(target.Path, targetDoc.Content); err != nil {
			return workspaceRestoreBatchResult{}, err
		}
		current, err := currentWorkspaceFile(ctx, tx, projectID, item.Path)
		if err != nil {
			return workspaceRestoreBatchResult{}, err
		}
		if current.SHA256 != strings.ToLower(item.ExpectedCurrentSHA256) {
			return workspaceRestoreBatchResult{}, errWorkspaceConflict
		}
		result.BeforeSnapshots = append(result.BeforeSnapshots, current)
		if current.SHA256 == target.SHA256 {
			result.RestoredFiles = append(result.RestoredFiles, current)
			continue
		}
		restored, err := s.insertWorkspaceVersion(ctx, tx, projectID, target, nil)
		if err != nil {
			return workspaceRestoreBatchResult{}, err
		}
		result.RestoredFiles = append(result.RestoredFiles, restored)
	}
	if err := tx.Commit(); err != nil {
		return workspaceRestoreBatchResult{}, err
	}
	return result, nil
}

func (s *Store) handleWorkspaceRestorePreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Items []workspaceRestorePreviewInputItem `json:"items"`
	}
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	if len(input.Items) == 0 || len(input.Items) > workspaceBatchRestoreLimit {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择 1 至 24 项需要预览恢复的文件版本。")
		return
	}
	seen := make(map[string]bool, len(input.Items))
	for _, item := range input.Items {
		if !validWorkspacePath(item.Path) || !validRequestID(item.TargetVersionID) || seen[item.Path] {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效且不重复的文件路径与目标版本编号。")
			return
		}
		seen[item.Path] = true
	}
	items, err := s.previewWorkspaceBatchRestore(r.Context(), r.PathValue("id"), input.Items)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, struct {
			Items []workspaceRestorePreviewItem `json:"items"`
		}{Items: items})
	case errors.Is(err, errBatchRestorePathMismatch):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_INPUT", "目标版本与指定文件路径不匹配。")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目、文件或目标版本不存在。")
	default:
		writeStoreError(w, "preview workspace batch restore", err)
	}
}

func (s *Store) handleWorkspaceRestoreBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Items []workspaceRestoreBatchInputItem `json:"items"`
	}
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	if len(input.Items) == 0 || len(input.Items) > workspaceBatchRestoreLimit {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择 1 至 24 项需要批量恢复的文件版本。")
		return
	}
	seen := make(map[string]bool, len(input.Items))
	for _, item := range input.Items {
		if !validWorkspacePath(item.Path) || !validRequestID(item.TargetVersionID) ||
			item.ExpectedCurrentSHA256 == "" || !validWorkspaceBase(item.ExpectedCurrentSHA256) || seen[item.Path] {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效且不重复的文件路径、目标版本编号及当前文件基线摘要。")
			return
		}
		seen[item.Path] = true
	}
	result, err := s.executeWorkspaceBatchRestore(r.Context(), r.PathValue("id"), input.Items)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "部分文件已有新版本，请重新预览影响后再执行整段恢复。")
	case errors.Is(err, errBatchRestorePathMismatch):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_INPUT", "目标版本与指定文件路径不匹配。")
	case errors.Is(err, errInvalidDiagram):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DIAGRAM", "目标流程图版本结构无效，无法恢复。")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目、文件或目标版本不存在。")
	default:
		writeStoreError(w, "execute workspace batch restore", err)
	}
}
