package workbench

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"

	"research-agent/internal/inference"
)

type selectionEdits struct {
	store *Store
	model *inference.Client
}

func (api selectionEdits) register(mux *http.ServeMux) {
	base := projectsPath + "/{id}/files/versions/{versionId}/edits"
	mux.HandleFunc("POST "+base, api.create)
	mux.HandleFunc("GET "+base+"/{editId}", api.read)
	mux.HandleFunc("POST "+base+"/{editId}/apply", api.apply)
}

func (api selectionEdits) create(w http.ResponseWriter, r *http.Request) {
	var input selectionEditInput
	if !readJSON(w, r, &input, 6*maxEditInstructionBytes+(4<<10)) {
		return
	}
	input.Instruction = strings.TrimSpace(input.Instruction)
	if !validRequestID(input.RequestID) || input.Instruction == "" || len(input.Instruction) > maxEditInstructionBytes || strings.ContainsRune(input.Instruction, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供请求编号和 2 KiB 以内的修改要求。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	file, err := api.store.workspaceVersion(ctx, projectID, r.PathValue("versionId"))
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	switch strings.ToLower(path.Ext(file.Path)) {
	case ".md", ".markdown":
	default:
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "选区改稿支持已保存的 Markdown 稿件。")
		return
	}
	doc, err := api.store.readWorkspaceDocument(file)
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	edit, created, err := api.store.queueSelectionEdit(ctx, projectID, doc, input, api.model != nil)
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	if edit.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, edit)
}

func (api selectionEdits) read(w http.ResponseWriter, r *http.Request) {
	edit, err := readSelectionEdit(r.Context(), api.store.db, r.PathValue("id"), r.PathValue("versionId"), r.PathValue("editId"))
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, edit)
}

func (api selectionEdits) apply(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	doc, err := api.store.applySelectionEdit(r.Context(), projectID, r.PathValue("versionId"), r.PathValue("editId"))
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	if api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	writeJSON(w, http.StatusOK, doc)
}

func writeSelectionEditError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "稿件版本或修改建议不存在。")
	case errors.Is(err, errSelectionRange):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SELECTION", "请从已保存的稿件中选择不超过 8 KiB 的完整文字。")
	case errors.Is(err, errSelectionState):
		writeError(w, http.StatusConflict, "EDIT_STATE_CONFLICT", "修改建议尚未生成，请查看任务状态。")
	case errors.Is(err, errSelectionUnchanged):
		writeError(w, http.StatusConflict, "EDIT_NO_CHANGE", "此选区无需改动。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已关联另一处修改建议。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先配置模型。")
	default:
		writeWorkspaceError(w, "selection edit", err)
	}
}
