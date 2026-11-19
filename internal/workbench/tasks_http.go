package workbench

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"research-agent/internal/inference"
)

const taskInstructionLimit = 8 << 10

type researchTasks struct {
	store *Store
	model *inference.Client
}

type taskPage struct {
	Items           []Task `json:"items"`
	NextCursor      string `json:"nextCursor,omitempty"`
	ModelConfigured bool   `json:"modelConfigured"`
}

func (api researchTasks) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/tasks"
	mux.HandleFunc("GET "+path, api.list)
	mux.HandleFunc("POST "+path, api.propose)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc("PATCH "+path+"/{taskId}", api.edit)
	mux.HandleFunc(path+"/{taskId}", methodNotAllowed("PATCH"))
	for _, action := range []string{confirmTask, cancelTask} {
		mutation := path + "/{taskId}/" + action
		mux.HandleFunc("POST "+mutation, api.change(action))
		mux.HandleFunc(mutation, methodNotAllowed("POST"))
	}
	mux.HandleFunc("GET "+path+"/{taskId}/artifact", api.artifact)
	mux.HandleFunc(path+"/{taskId}/artifact", methodNotAllowed("GET, HEAD"))
}

func (api researchTasks) list(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "查询参数无效。")
		return
	}
	limit := 50
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "limit 必须是 1 到 100 之间的整数。")
			return
		}
	}
	before, err := eventCursor(query.Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "任务列表游标无效。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := api.store.getProject(ctx, projectID); err != nil {
		writeTaskError(w, "lookup project for tasks", err)
		return
	}
	items, err := api.store.listTasks(ctx, projectID, before, limit+1)
	if err != nil {
		writeTaskError(w, "list tasks", err)
		return
	}
	page := taskPage{Items: items, ModelConfigured: api.model != nil}
	if len(items) > limit {
		page.NextCursor = strconv.FormatInt(items[limit-1].ordinal, 10)
		page.Items = items[:limit]
	}
	writeJSON(w, http.StatusOK, page)
}

func (api researchTasks) propose(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID   string `json:"requestId"`
		Instruction string `json:"instruction"`
	}
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	input.Instruction = strings.TrimSpace(input.Instruction)
	if !validRequestID(input.RequestID) || !validTaskInstruction(input.Instruction) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的请求编号和 8 KiB 以内的非空任务说明。")
		return
	}
	api.apply(w, r, taskRequest{ProjectID: r.PathValue("id"), Action: proposeTask, RequestID: input.RequestID, Instruction: input.Instruction})
}

func validTaskInstruction(instruction string) bool {
	return instruction != "" && len(instruction) <= taskInstructionLimit && !strings.ContainsRune(instruction, 0)
}

func (api researchTasks) edit(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID        string `json:"requestId"`
		ExpectedRevision int    `json:"expectedRevision"`
		Instruction      string `json:"instruction"`
	}
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	instruction := strings.TrimSpace(input.Instruction)
	if !validRequestID(input.RequestID) || input.ExpectedRevision < 1 || !validTaskInstruction(instruction) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供请求编号、当前修订和 8 KiB 以内的非空任务说明。")
		return
	}
	api.apply(w, r, taskRequest{ProjectID: r.PathValue("id"), TaskID: r.PathValue("taskId"), Action: editTask,
		RequestID: input.RequestID, ExpectedRevision: input.ExpectedRevision, Instruction: instruction})
}

func (api researchTasks) change(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RequestID        string `json:"requestId"`
			ExpectedRevision int    `json:"expectedRevision"`
		}
		if !readJSON(w, r, &input, 4<<10) {
			return
		}
		if !validRequestID(input.RequestID) || input.ExpectedRevision < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的请求编号和正整数修订版本。")
			return
		}
		api.apply(w, r, taskRequest{ProjectID: r.PathValue("id"), TaskID: r.PathValue("taskId"), Action: action,
			RequestID: input.RequestID, ExpectedRevision: input.ExpectedRevision})
	}
}

func (api researchTasks) apply(w http.ResponseWriter, r *http.Request, req taskRequest) {
	task, applied, err := api.store.applyTaskRequest(r.Context(), req, api.model != nil)
	if req.Action == editTask && errors.Is(err, errTaskState) {
		writeError(w, http.StatusConflict, "TASK_STATE_CONFLICT", "只有待确认的任务可以修改说明。")
		return
	}
	if err != nil {
		writeTaskError(w, "apply task request", err)
		return
	}
	status := http.StatusOK
	if applied {
		switch req.Action {
		case proposeTask:
			status = http.StatusCreated
		case confirmTask:
			api.store.kickTaskQueue(req.ProjectID, api.model)
			status = http.StatusAccepted
		case cancelTask:
			if task.Status == taskCancelling {
				api.store.stopTaskRun(req.ProjectID, task.TaskID)
			}
		}
	}
	writeJSON(w, status, task)
}

func (api researchTasks) artifact(w http.ResponseWriter, r *http.Request) {
	markdown, err := api.store.taskMarkdown(r.Context(), r.PathValue("id"), r.PathValue("taskId"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有可下载的研究计划。")
		return
	}
	if err != nil {
		writeStoreError(w, "read research plan", err)
		return
	}
	writeMarkdown(w, r, "research-plan.md", markdown)
}

func writeMarkdown(w http.ResponseWriter, r *http.Request, filename, markdown string) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(markdown)))
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, markdown)
	}
}

func writeTaskError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目或任务不存在。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于其他任务操作。")
	case errors.Is(err, errTaskRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "任务状态已更新，请查看最新版本。")
	case errors.Is(err, errTaskState):
		writeError(w, http.StatusConflict, "TASK_STATE_CONFLICT", "只有提议中的任务可以确认。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先配置模型。")
	default:
		writeStoreError(w, operation, err)
	}
}
