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
	mux.HandleFunc("POST "+path+"/pause", api.change(workflowPause))
	mux.HandleFunc("POST "+path+"/cancel", api.change(workflowCancel))
	mux.HandleFunc("POST "+path+"/rerun", api.change(workflowRerun))
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifact", api.artifact)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifacts", api.artifacts)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifacts/{artifactId}", api.artifact)
	mux.HandleFunc("POST "+path+"/{runId}/manuscript", api.saveManuscript)
}

func (api workflows) latest(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	project, err := api.store.getProject(r.Context(), projectID)
	if err != nil {
		writeTaskError(w, "look up workflow project", err)
		return
	}
	type stepInfo struct {
		Key   string `json:"key"`
		Title string `json:"title"`
	}
	stages := workflowStages[projectWorkflows[project.TemplateID]]
	result := struct {
		Run             *WorkflowRun `json:"run"`
		Steps           []stepInfo   `json:"steps"`
		ModelConfigured bool         `json:"modelConfigured"`
	}{Steps: make([]stepInfo, len(stages)), ModelConfigured: api.model != nil}
	for index, stage := range stages {
		result.Steps[index] = stepInfo{Key: stage.key, Title: stage.title}
	}
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
		if !readJSON(w, r, &input, 6*maxPaperOutlineBytes+(4<<10)) {
			return
		}
		if !validRequestID(input.RequestID) || (action != workflowStart && (!validRequestID(input.RunID) || input.ExpectedRevision < 1)) {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的请求编号。操作已有工作流时还需提供运行编号和当前修订。")
			return
		}
		if action == workflowRerun && (input.StepKey == "" || input.ThroughStep != "") {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择要重跑的步骤。")
			return
		}
		projectID := r.PathValue("id")
		run, applied, err := api.store.applyWorkflow(r.Context(), projectID, action, input, api.model != nil)
		switch {
		case errors.Is(err, errWorkflowState):
			writeError(w, http.StatusConflict, "WORKFLOW_STATE_CONFLICT", "当前工作流状态不支持此操作。")
		case errors.Is(err, errWorkflowDependency):
			writeError(w, http.StatusConflict, "WORKFLOW_DEPENDENCY_REQUIRED", "请先完成或更新该步骤之前的步骤。")
		case errors.Is(err, errWorkflowStep):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "所选步骤不属于此工作流。")
		case errors.Is(err, errWorkflowRange):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择从本次起点可达的结束步骤。检查点或重试请沿用原范围。")
		case errors.Is(err, errReviewSources):
			writeError(w, http.StatusUnprocessableEntity, "SOURCE_REQUIRED", "请先在项目文献库核验并保存文献。")
		case errors.Is(err, errReviewTarget):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "参考文献数量上限须在 10 至 80 之间。")
		case errors.Is(err, errPaperOptions):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择有效的写作语言，大纲上限为 16 KiB。")
		case err != nil:
			writeTaskError(w, "change workflow run", err)
		default:
			if (action == workflowPause && run.Status == workflowPausing) || (action == workflowCancel && run.Status == taskCancelling) {
				api.store.stopTaskRun(projectID, run.stepTaskID)
			}
			if action != workflowPause && action != workflowCancel && run.Status == taskQueued && api.model != nil {
				api.store.kickTaskQueue(projectID, api.model)
			}
			status := http.StatusOK
			if applied {
				switch action {
				case workflowStart:
					status = http.StatusCreated
				case workflowContinue, workflowRerun:
					status = http.StatusAccepted
				}
			}
			writeJSON(w, status, run)
		}
	}
}
