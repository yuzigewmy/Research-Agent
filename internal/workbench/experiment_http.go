package workbench

import (
	"database/sql"
	"errors"
	"net/http"

	"research-agent/internal/inference"
)

type workflows struct {
	store *Store
	model *inference.Client
}

func (api workflows) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/workflow"
	mux.HandleFunc("GET "+path, api.latest)
	mux.HandleFunc("POST "+path, api.change(workflowStart))
	mux.HandleFunc("POST "+path+"/continue", api.change(workflowContinue))
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifact", api.artifact)
	mux.HandleFunc("POST "+path+"/{runId}/manuscript", api.saveManuscript)
}

func (api workflows) latest(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := api.store.getProject(r.Context(), projectID); err != nil {
		writeTaskError(w, "look up workflow project", err)
		return
	}
	result := struct {
		Run             *WorkflowRun `json:"run"`
		ModelConfigured bool         `json:"modelConfigured"`
	}{ModelConfigured: api.model != nil}
	run, err := api.store.latestWorkflow(r.Context(), projectID)
	switch {
	case err == nil:
		result.Run = &run
	case !errors.Is(err, sql.ErrNoRows):
		writeStoreError(w, "read workflow run", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api workflows) change(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input workflowRequest
		if !readJSON(w, r, &input, 4<<10) {
			return
		}
		if !validRequestID(input.RequestID) || (action == workflowContinue && (input.RunID == "" || input.ExpectedRevision < 1)) {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交请求编号。继续时还需提供运行编号和当前修订。")
			return
		}
		projectID := r.PathValue("id")
		run, applied, err := api.store.applyWorkflow(r.Context(), projectID, action, input, api.model != nil)
		switch {
		case errors.Is(err, errWorkflowState):
			writeError(w, http.StatusConflict, "WORKFLOW_STATE_CONFLICT", "当前工作流状态不支持此操作。")
		case errors.Is(err, errReviewSources):
			writeError(w, http.StatusUnprocessableEntity, "SOURCE_REQUIRED", "请先在项目文献库核验并保存文献。")
		case errors.Is(err, errReviewTarget):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "参考文献数量上限须在 10 至 80 之间。")
		case err != nil:
			writeTaskError(w, "change workflow run", err)
		default:
			if run.Status == taskQueued && api.model != nil {
				api.store.kickTaskQueue(projectID, api.model)
			}
			status := http.StatusOK
			if applied {
				status = http.StatusAccepted
				if action == workflowStart {
					status = http.StatusCreated
				}
			}
			writeJSON(w, status, run)
		}
	}
}

func (api workflows) artifact(w http.ResponseWriter, r *http.Request) {
	name, markdown, err := api.store.workflowArtifact(r.Context(), r.PathValue("id"), r.PathValue("runId"), r.PathValue("key"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该步骤暂无可下载的成果。")
		return
	}
	if err != nil {
		writeStoreError(w, "read workflow artifact", err)
		return
	}
	writeMarkdown(w, r, name, markdown)
}
