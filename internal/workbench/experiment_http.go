package workbench

import (
	"database/sql"
	"errors"
	"net/http"

	"research-agent/internal/inference"
)

type experiments struct {
	store *Store
	model *inference.Client
}

func (api experiments) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/workflow"
	mux.HandleFunc("GET "+path, api.latest)
	mux.HandleFunc("POST "+path, api.change(experimentStart))
	mux.HandleFunc("POST "+path+"/continue", api.change(experimentContinue))
	mux.HandleFunc("GET "+path+"/{runId}/steps/{key}/artifact", api.artifact)
}

func (api experiments) latest(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := api.store.getProject(r.Context(), projectID); err != nil {
		writeTaskError(w, "look up experiment project", err)
		return
	}
	result := struct {
		Run             *ExperimentRun `json:"run"`
		ModelConfigured bool           `json:"modelConfigured"`
	}{ModelConfigured: api.model != nil}
	run, err := api.store.latestExperiment(r.Context(), projectID)
	switch {
	case err == nil:
		result.Run = &run
	case !errors.Is(err, sql.ErrNoRows):
		writeStoreError(w, "read experiment run", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api experiments) change(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input experimentRequest
		if !readJSON(w, r, &input, 4<<10) {
			return
		}
		if !validRequestID(input.RequestID) || (action == experimentContinue && (input.RunID == "" || input.ExpectedRevision < 1)) {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交请求编号。继续时还需提供运行编号和当前修订。")
			return
		}
		projectID := r.PathValue("id")
		run, applied, err := api.store.applyExperiment(r.Context(), projectID, action, input, api.model != nil)
		if errors.Is(err, errExperimentState) {
			writeError(w, http.StatusConflict, "WORKFLOW_STATE_CONFLICT", "当前工作流状态不支持此操作。")
			return
		}
		if err != nil {
			writeTaskError(w, "change experiment run", err)
			return
		}
		if run.Status == taskQueued && api.model != nil {
			api.store.kickTaskQueue(projectID, api.model)
		}
		status := http.StatusOK
		if applied {
			status = http.StatusAccepted
			if action == experimentStart {
				status = http.StatusCreated
			}
		}
		writeJSON(w, status, run)
	}
}

func (api experiments) artifact(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	markdown, err := api.store.experimentArtifact(r.Context(), r.PathValue("id"), r.PathValue("runId"), key)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该步骤暂无可下载的成果。")
		return
	}
	if err != nil {
		writeStoreError(w, "read experiment artifact", err)
		return
	}
	writeMarkdown(w, r, "experiment-"+key+".md", markdown)
}
