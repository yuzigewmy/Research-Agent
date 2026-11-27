package workbench

import (
	"errors"
	"net/http"
	"strings"
)

type scriptRuns struct {
	store  *Store
	python string
}

func (api scriptRuns) register(mux *http.ServeMux) {
	base := projectsPath + "/{id}/script-runs"
	mux.HandleFunc("GET "+base, api.handleList)
	mux.HandleFunc("POST "+base, api.handleCreate)
	mux.HandleFunc(base, methodNotAllowed("GET, HEAD, POST"))

	item := base + "/{runId}"
	mux.HandleFunc("GET "+item, api.handleGet)
	mux.HandleFunc(item, methodNotAllowed("GET, HEAD"))

	cancelPath := item + "/cancel"
	mux.HandleFunc("POST "+cancelPath, api.handleCancel)
	mux.HandleFunc(cancelPath, methodNotAllowed("POST"))

	publishPath := item + "/artifacts/publish"
	mux.HandleFunc("POST "+publishPath, api.handlePublishArtifact)
	mux.HandleFunc(publishPath, methodNotAllowed("POST"))
}

func (api scriptRuns) handleList(w http.ResponseWriter, r *http.Request) {
	items, err := api.store.listScriptRuns(r.Context(), r.PathValue("id"), scriptRunsListLimit)
	if err != nil {
		writeScriptRunError(w, "list script runs", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Configured bool            `json:"configured"`
		Limits     ScriptRunLimits `json:"limits"`
		Items      []ScriptRun     `json:"items"`
	}{
		Configured: api.python != "",
		Limits:     defaultScriptRunLimits(),
		Items:      items,
	})
}

func (api scriptRuns) handleCreate(w http.ResponseWriter, r *http.Request) {
	var input createScriptRunInput
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	if !validRequestID(input.RequestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号（requestId）。")
		return
	}
	if !input.Confirmed {
		writeError(w, http.StatusBadRequest, "CONFIRMATION_REQUIRED", "运行受限本机脚本前须先明确授权（confirmed: true）。")
		return
	}
	input.ScriptVersionID = strings.TrimSpace(input.ScriptVersionID)
	input.InputVersionID = strings.TrimSpace(input.InputVersionID)
	if input.ScriptVersionID == "" || len(input.ScriptVersionID) > 128 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择已保存的 .py 脚本版本。")
		return
	}
	if len(input.InputVersionID) > 128 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "输入文件版本编号无效。")
		return
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultScriptTimeoutSeconds
	}
	if input.TimeoutSeconds < minScriptTimeoutSeconds || input.TimeoutSeconds > maxScriptTimeoutSeconds {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "超时时间须为 1 至 30 秒之间的整数。")
		return
	}
	if api.python == "" {
		writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING", "服务端尚未配置受限本机脚本运行器（RESEARCH_SCRIPT_PYTHON）。")
		return
	}

	projectID := r.PathValue("id")
	run, created, err := api.store.createScriptRun(r.Context(), projectID, input)
	if err != nil {
		writeScriptRunError(w, "create script run", err)
		return
	}
	if created {
		api.store.startScriptRun(run, api.python)
		w.Header().Set("Location", projectsPath+"/"+projectID+"/script-runs/"+run.RunID)
		writeJSON(w, http.StatusCreated, run)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (api scriptRuns) handleGet(w http.ResponseWriter, r *http.Request) {
	run, err := api.store.getScriptRun(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeScriptRunError(w, "get script run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (api scriptRuns) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 || r.Header.Get("Content-Type") != "" {
		var body *struct{}
		if !readJSON(w, r, &body, 1024) {
			return
		}
		if body == nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交空 JSON 对象。")
			return
		}
	}
	run, err := api.store.cancelScriptRun(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeScriptRunError(w, "cancel script run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (api scriptRuns) handlePublishArtifact(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SHA256 = strings.ToLower(strings.TrimSpace(input.SHA256))
	if !validScriptArtifactName(input.Name) || !validObjectDigest(input.SHA256) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的产物文件名与 SHA-256 摘要。")
		return
	}
	file, err := api.store.publishScriptRunArtifact(r.Context(), r.PathValue("id"), r.PathValue("runId"), input.Name, input.SHA256)
	if err != nil {
		writeScriptRunError(w, "publish script artifact", err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func writeScriptRunError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, errScriptProjectNotFound), errors.Is(err, errScriptRunNotFound),
		errors.Is(err, errScriptVersionNotFound), errors.Is(err, errScriptInputNotFound),
		errors.Is(err, errScriptArtifactNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, errScriptRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", err.Error())
	case errors.Is(err, errScriptProjectBusy):
		writeError(w, http.StatusConflict, "SCRIPT_RUN_ACTIVE", err.Error())
	case errors.Is(err, errScriptRunNotSucceeded):
		writeError(w, http.StatusConflict, "SCRIPT_RUN_NOT_SUCCEEDED", err.Error())
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "目标工作区路径已存在不同内容，请核对后再发布。")
	case errors.Is(err, errScriptInvalidType):
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", err.Error())
	case errors.Is(err, errScriptTooLarge), errors.Is(err, errScriptInputTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "INPUT_TOO_LARGE", err.Error())
	case errors.Is(err, errScriptArtifactDigestMismatch):
		writeError(w, http.StatusUnprocessableEntity, "ARTIFACT_DIGEST_MISMATCH", err.Error())
	default:
		writeStoreError(w, operation, err)
	}
}
