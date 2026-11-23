package workbench

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"research-agent/internal/inference"
)

type workflows struct {
	store           *Store
	model           *inference.Client
	softwarePDFFont string
}

func (api workflows) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/workflow"
	mux.HandleFunc("GET "+path, api.latest)
	mux.HandleFunc("POST "+path, api.change(workflowStart))
	mux.HandleFunc("POST "+path+"/continue", api.change(workflowContinue))
	mux.HandleFunc("POST "+path+"/pause", api.change(workflowPause))
	mux.HandleFunc("POST "+path+"/cancel", api.change(workflowCancel))
	mux.HandleFunc("POST "+path+"/rerun", api.change(workflowRerun))
	mux.HandleFunc("POST "+path+"/accept", api.accept)
	mux.HandleFunc("POST "+path+"/skip", api.change(workflowSkip))
	mux.HandleFunc("POST "+path+"/select-topic", api.change(workflowSelectTopic))
	mux.HandleFunc("GET "+path+"/runs", api.listRuns)
	mux.HandleFunc("GET "+path+"/runs/{runId}", api.readRun)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifact", api.artifact)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifacts", api.artifacts)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifacts/{artifactId}", api.artifact)
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifacts/{artifactId}/pdf", api.softwareIdentificationPDF)
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
	key := projectWorkflowKey(project.TemplateID)
	stages := workflowStages[key]
	result := struct {
		Run                   *WorkflowRun `json:"run"`
		Steps                 []stepInfo   `json:"steps"`
		ModelConfigured       bool         `json:"modelConfigured"`
		SoftwarePDFConfigured *bool        `json:"softwarePdfConfigured,omitempty"`
	}{Steps: make([]stepInfo, len(stages)), ModelConfigured: api.model != nil}
	if isSoftwareRegistrationWorkflow(key) {
		result.SoftwarePDFConfigured = new(api.softwarePDFFont != "")
	}
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
		if action == workflowSkip {
			input.Reason = strings.TrimSpace(input.Reason)
			if !validRequestID(input.StepKey) || input.ThroughStep != "" || input.Reason == "" ||
				utf8.RuneCountInString(input.Reason) > 300 || strings.ContainsRune(input.Reason, 0) {
				writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择要跳过的步骤，并填写 300 个字符以内的理由。")
				return
			}
		}
		if action == workflowSelectTopic && (input.CandidateNumber < 1 || input.ThroughStep != "") {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择一个入围题目。")
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
		case errors.Is(err, errCourseOptions):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请核对课程写作设置。目标为 1000 至 30000 字，补充要求最多 4 KiB，可选最多四份资料。")
		case errors.Is(err, errSoftwareOptions):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请核对版本号和软件说明。版本号最多 64 字节，软件说明最多 4 KiB，可选最多四份资料。")
		case errors.Is(err, errSoftwareSources):
			writeError(w, http.StatusUnprocessableEntity, "SOURCE_TOO_LARGE", "本轮选定的源码文本合计不得超过 64 KiB。请缩小材料范围。")
		case errors.Is(err, errInvalidSoftwareMaterial):
			writeError(w, http.StatusUnprocessableEntity, "INVALID_REFERENCE", "请选择本项目中的 UTF-8 文本、Word、ZIP 或 TAR.GZ 资料。")
		case errors.Is(err, errInvalidTurnReference):
			writeError(w, http.StatusUnprocessableEntity, "INVALID_REFERENCE", "请选择当前项目的文本或 Word 资料。")
		case errors.Is(err, errTopicOptions):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "研究选题的补充要求上限为 4 KiB。")
		case errors.Is(err, errProposalOptions):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请核对学位设置和月份。正文目标为 500 至 30000 字，文献目标为 1 至 80 条，补充要求上限为 4 KiB。")
		case errors.Is(err, errTopicCandidate):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择本轮的入围题目。")
		case err != nil:
			writeTaskError(w, "change workflow run", err)
		default:
			if (action == workflowPause && run.Status == workflowPausing) || (action == workflowCancel && run.Status == taskCancelling) {
				api.store.stopTaskRun(projectID, run.stepTaskID)
			}
			if run.Status == taskQueued && api.model != nil {
				switch action {
				case workflowStart, workflowContinue, workflowRerun, workflowSelectTopic:
					api.store.kickTaskQueue(projectID, api.model)
				}
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
