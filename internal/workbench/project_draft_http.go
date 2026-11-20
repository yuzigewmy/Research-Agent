package workbench

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"research-agent/internal/inference"
)

//go:embed prompts/project_draft.md
var projectDraftPrompt string

const projectDraftsPath = "/api/v1/project-drafts"

type projectDrafts struct {
	store *Store
	model *inference.Client
}

func (api projectDrafts) register(mux *http.ServeMux) {
	item := projectDraftsPath + "/{draftId}"
	mux.HandleFunc("GET "+projectDraftsPath, api.list)
	mux.HandleFunc("POST "+projectDraftsPath, api.create)
	mux.HandleFunc("GET "+item, api.get)
	mux.HandleFunc("PATCH "+item, api.change(draftSave))
	mux.HandleFunc("POST "+item+"/plan", api.change(draftPlan))
	mux.HandleFunc("POST "+item+"/finalize", api.change(draftFinalize))
}

func (api projectDrafts) list(w http.ResponseWriter, r *http.Request) {
	cursor, ok := decodeProjectCursor(r.URL.Query().Get("cursor"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "草稿列表游标无效。")
		return
	}
	const limit = 30
	items, err := api.store.listProjectDrafts(r.Context(), cursor, limit+1)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	var next string
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.UpdatedAt + "/" + last.DraftID))
	}
	writeJSON(w, http.StatusOK, struct {
		Items           []projectDraft `json:"items"`
		ModelConfigured bool           `json:"modelConfigured"`
		NextCursor      string         `json:"nextCursor,omitempty"`
	}{items, api.model != nil, next})
}

func (api projectDrafts) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string `json:"requestId"`
		Prompt    string `json:"prompt"`
	}
	if !readJSON(w, r, &input, 6*draftPromptLimit+2048) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if !validRequestID(input.RequestID) || input.Prompt == "" || len(input.Prompt) > draftPromptLimit || strings.ContainsRune(input.Prompt, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的请求编号和 8 KiB 以内的研究描述。")
		return
	}
	draft, created, err := api.store.createProjectDraft(r.Context(), input.RequestID, input.Prompt)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	if created {
		w.Header().Set("Location", projectDraftsPath+"/"+draft.DraftID)
		writeJSON(w, http.StatusCreated, draft)
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (api projectDrafts) get(w http.ResponseWriter, r *http.Request) {
	draft, err := readProjectDraft(r.Context(), api.store.db, r.PathValue("draftId"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (api projectDrafts) change(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input draftChangeInput
		var fields projectDraftFields
		if action == draftSave {
			var body struct {
				draftChangeInput
				projectDraftFields
			}
			if !readJSON(w, r, &body, 6*3*draftFieldLimit+(4<<10)) {
				return
			}
			input, fields = body.draftChangeInput, body.projectDraftFields
			if !fields.normalize() {
				writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请核对项目题名、模板与草稿字段的长度。")
				return
			}
		} else if !readJSON(w, r, &input, 4<<10) {
			return
		}
		if !validRequestID(input.RequestID) || input.ExpectedRevision < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号和草稿修订。")
			return
		}
		draft, applied, err := api.store.changeProjectDraft(r.Context(), r.PathValue("draftId"), action, input, fields, api.model != nil)
		if err != nil {
			writeDraftError(w, err)
			return
		}
		if action == draftFinalize {
			project, err := api.store.getProject(r.Context(), draft.ProjectID)
			if err != nil {
				writeDraftError(w, err)
				return
			}
			if applied {
				w.Header().Set("Location", projectsPath+"/"+project.ProjectID)
				writeJSON(w, http.StatusCreated, project)
				return
			}
			writeJSON(w, http.StatusOK, project)
			return
		}
		if action == draftPlan && applied {
			draft, err = api.plan(context.WithoutCancel(r.Context()), draft)
			if err != nil {
				writeDraftError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, draft)
	}
}

func (api projectDrafts) plan(ctx context.Context, draft projectDraft) (projectDraft, error) {
	input, err := json.Marshal(struct {
		Prompt   string             `json:"prompt"`
		Fields   projectDraftFields `json:"currentDraft"`
		Question string             `json:"previousQuestion,omitempty"`
	}{draft.Prompt, draft.projectDraftFields, draft.Question})
	if err != nil {
		return projectDraft{}, err
	}
	modelCtx, stop := context.WithTimeout(ctx, 90*time.Second)
	output, err := streamTaskMarkdown(modelCtx, api.model, projectDraftPrompt, string(input))
	stop()
	var suggestion struct {
		projectDraftFields
		Question string `json:"question"`
	}
	message := ""
	if err == nil {
		err = json.Unmarshal([]byte(output), &suggestion)
	}
	suggestion.Clarification = draft.Clarification
	suggestion.Question = strings.TrimSpace(suggestion.Question)
	if err != nil || !suggestion.normalize() || utf8.RuneCountInString(suggestion.Question) > 300 ||
		strings.ContainsRune(suggestion.Question, 0) ||
		(suggestion.Question == "" && (suggestion.Title == "" || suggestion.Goal == "")) {
		message = "草稿整理未完成。已保留原内容，可以编辑或重新整理。"
	} else {
		draft.projectDraftFields = suggestion.projectDraftFields
		draft.Question = suggestion.Question
	}
	writeCtx, cancel := context.WithTimeout(ctx, taskDBLimit)
	defer cancel()
	return api.store.finishDraftPlan(writeCtx, draft, message)
}

func writeDraftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目草稿不存在。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于其他草稿操作。")
	case errors.Is(err, errProjectRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "草稿已更新，请读取最新版本。")
	case errors.Is(err, errDraftState):
		writeError(w, http.StatusConflict, "DRAFT_STATE_CONFLICT", "请等待草稿整理完成，或打开已经创建的项目。")
	case errors.Is(err, errDraftIncomplete):
		writeError(w, http.StatusUnprocessableEntity, "DRAFT_INCOMPLETE", "请先填写项目题名和研究目标。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先配置研究模型，也可以直接编辑草稿。")
	default:
		writeStoreError(w, "change project draft", err)
	}
}
