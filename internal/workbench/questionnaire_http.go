package workbench

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
)

func (s *Store) registerQuestionnaires(mux *http.ServeMux) {
	admin := projectsPath + "/{id}/questionnaire"
	mux.HandleFunc("GET "+admin, s.handleQuestionnaire)
	mux.HandleFunc("PUT "+admin, s.handleSaveQuestionnaire)
	mux.HandleFunc("POST "+admin+"/publish", s.handleQuestionnaireStatus(questionnairePublished))
	mux.HandleFunc("POST "+admin+"/close", s.handleQuestionnaireStatus(questionnaireClosed))
	mux.HandleFunc("GET "+admin+"/responses.csv", s.handleQuestionnaireCSV(false))
	mux.HandleFunc("POST "+admin+"/responses/material", s.handleQuestionnaireCSV(true))
	public := "/api/v1/questionnaires/{publicId}"
	mux.HandleFunc("GET "+public, s.handlePublicQuestionnaire)
	mux.HandleFunc("POST "+public+"/responses", s.handleQuestionnaireResponse)
}

func (s *Store) handleQuestionnaire(w http.ResponseWriter, r *http.Request) {
	q, err := readQuestionnaire(r.Context(), s.db, r.PathValue("id"))
	if err != nil {
		writeQuestionnaireError(w, "read questionnaire", err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *Store) handleSaveQuestionnaire(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedRevision *int `json:"expectedRevision"`
		questionnaireDefinition
	}
	if !readJSON(w, r, &input, 192<<10) || !validQuestionnaireRevision(w, input.ExpectedRevision) {
		return
	}
	if err := normalizeQuestionnaire(&input.questionnaireDefinition); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	q, err := s.changeQuestionnaire(r.Context(), r.PathValue("id"), *input.ExpectedRevision, &input.questionnaireDefinition, questionnaireDraft)
	if err != nil {
		writeQuestionnaireError(w, "save questionnaire", err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *Store) handleQuestionnaireStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision *int `json:"expectedRevision"`
		}
		if !readJSON(w, r, &input, 1<<10) || !validQuestionnaireRevision(w, input.ExpectedRevision) {
			return
		}
		q, err := s.changeQuestionnaire(r.Context(), r.PathValue("id"), *input.ExpectedRevision, nil, status)
		if err != nil {
			writeQuestionnaireError(w, "change questionnaire state", err)
			return
		}
		writeJSON(w, http.StatusOK, q)
	}
}

func validQuestionnaireRevision(w http.ResponseWriter, revision *int) bool {
	if revision == nil || *revision < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供当前问卷修订号。")
		return false
	}
	return true
}

func (s *Store) handlePublicQuestionnaire(w http.ResponseWriter, r *http.Request) {
	q, err := readPublishedQuestionnaire(r.Context(), s.db, r.PathValue("publicId"))
	if err != nil {
		writeQuestionnaireError(w, "read published questionnaire", err)
		return
	}
	if q.Status != questionnairePublished || q.projectStatus != projectReady {
		writeQuestionnaireError(w, "read published questionnaire", errQuestionnaireClosed)
		return
	}
	writeJSON(w, http.StatusOK, q.questionnaireDefinition)
}

func (s *Store) handleQuestionnaireResponse(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string            `json:"requestId"`
		Answers   map[string]string `json:"answers"`
	}
	if !readJSON(w, r, &input, 48<<10) {
		return
	}
	if !validRequestID(input.RequestID) || input.Answers == nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交填写内容和提交编号。")
		return
	}
	receipt, created, err := s.submitQuestionnaire(r.Context(), r.PathValue("publicId"), input.RequestID, input.Answers)
	if err != nil {
		writeQuestionnaireError(w, "record questionnaire answers", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, receipt)
}

func (s *Store) handleQuestionnaireCSV(save bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, projectID := r.Context(), r.PathValue("id")
		body, err := s.questionnaireCSV(ctx, projectID)
		if err != nil {
			writeQuestionnaireError(w, "collect questionnaire export", err)
			return
		}
		if !save {
			writeTextAttachment(w, r, questionnaireCSVName, "text/csv; charset=utf-8", string(body))
			return
		}
		material, created, err := s.saveMaterial(ctx, projectID, questionnaireCSVName, bytes.NewReader(body))
		if err != nil {
			writeQuestionnaireError(w, "save questionnaire export", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, material)
	}
}

func writeQuestionnaireError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "未找到该问卷。")
	case errors.Is(err, errQuestionnaireState):
		writeError(w, http.StatusConflict, "QUESTIONNAIRE_STATE_CONFLICT", "当前状态不支持此操作。请确认是否已保存、发布或关闭。")
	case errors.Is(err, errQuestionnaireRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "问卷已更新，请读取最新修订。")
	case errors.Is(err, errQuestionnaireClosed):
		writeError(w, http.StatusGone, "QUESTIONNAIRE_CLOSED", "本问卷已停止回收。")
	case errors.Is(err, errQuestionnaireFull):
		writeError(w, http.StatusConflict, "QUESTIONNAIRE_FULL", "本问卷的答卷已满。")
	case errors.Is(err, errQuestionnaireAnswers):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ANSWERS", "请核对可见必答题、单选选项和文本回答长度。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该提交编号已有不同的答卷。")
	default:
		writeStoreError(w, operation, err)
	}
}
