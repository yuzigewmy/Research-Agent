package workbench

import (
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"strconv"

	"research-agent/internal/survey"
)

type surveyCreateInput struct {
	RequestID  string `json:"requestId"`
	MaterialID string `json:"materialId"`
	Options    struct {
		RequiredColumns  []string                `json:"requiredColumns"`
		DuplicateColumns []string                `json:"duplicateColumns"`
		CrossTab         *survey.CrossTabOptions `json:"crossTab"`
		Reviews          []struct {
			Row    int    `json:"row"`
			Accept *bool  `json:"accept"`
			Reason string `json:"reason"`
		} `json:"reviews"`
	} `json:"options"`
}

func (s *Store) registerSurveys(mux *http.ServeMux) {
	path := projectsPath + "/{id}/surveys"
	mux.HandleFunc("GET "+path, s.handleListSurveys)
	mux.HandleFunc("POST "+path, s.handleCreateSurvey)
	mux.HandleFunc("GET "+path+"/{analysisId}", s.handleGetSurvey(false))
	mux.HandleFunc("GET "+path+"/{analysisId}/download", s.handleGetSurvey(true))
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc(path+"/{analysisId}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(path+"/{analysisId}/download", methodNotAllowed("GET, HEAD"))
}

func (s *Store) handleCreateSurvey(w http.ResponseWriter, r *http.Request) {
	var input surveyCreateInput
	if !readJSON(w, r, &input, 4<<20) {
		return
	}
	if !validRequestID(input.RequestID) || !validRequestID(input.MaterialID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号和资料编号。")
		return
	}
	options := survey.Options{
		RequiredColumns: input.Options.RequiredColumns, DuplicateColumns: input.Options.DuplicateColumns,
		Reviews: make([]survey.Review, 0, len(input.Options.Reviews)), CrossTab: input.Options.CrossTab,
	}
	for _, review := range input.Options.Reviews {
		if review.Accept == nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请为每条复核记录明确选择接受或拒绝。")
			return
		}
		options.Reviews = append(options.Reviews, survey.Review{Row: review.Row, Accept: *review.Accept, Reason: review.Reason})
	}
	projectID := r.PathValue("id")
	run, created, err := s.createSurveyRun(r.Context(), projectID, input.RequestID, input.MaterialID, options)
	if err != nil {
		writeSurveyError(w, "create survey run", err)
		return
	}
	if created {
		w.Header().Set("Location", projectsPath+"/"+projectID+"/surveys/"+run.AnalysisID)
		writeJSON(w, http.StatusCreated, run)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Store) handleListSurveys(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "问卷分析列表游标无效。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := s.getProject(ctx, projectID); err != nil {
		writeSurveyError(w, "read project for survey list", err)
		return
	}
	const pageSize = 50
	items, err := s.listSurveyRuns(ctx, projectID, cursor, pageSize+1)
	if err != nil {
		writeStoreError(w, "list survey runs", err)
		return
	}
	var next string
	if len(items) > pageSize {
		next = strconv.FormatInt(items[pageSize-1].ordinal, 10)
		items = items[:pageSize]
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []surveyRunInfo `json:"items"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}{Items: items, NextCursor: next})
}

func (s *Store) handleGetSurvey(download bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, err := s.getSurveyRun(r.Context(), r.PathValue("id"), r.PathValue("analysisId"))
		if err != nil {
			writeSurveyError(w, "read survey run", err)
			return
		}
		if download {
			filename := "survey-" + run.AnalysisID + ".json"
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		}
		writeJSON(w, http.StatusOK, run)
	}
}

func writeSurveyError(w http.ResponseWriter, operation string, err error) {
	var inputError *surveyAnalysisError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目、资料或问卷分析不存在。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于不同的问卷分析设置。")
	case errors.Is(err, errSurveyFormat):
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "请选择扩展名为 .csv 的问卷资料。")
	case errors.Is(err, errSurveyTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "CSV_TOO_LARGE", "问卷资料不能超过 8 MiB。")
	case errors.As(err, &inputError):
		writeError(w, http.StatusUnprocessableEntity, "SURVEY_INVALID", "无法分析该问卷。请检查 CSV 编码和格式、所选列及复核设置。"+inputError.Error())
	default:
		writeStoreError(w, operation, err)
	}
}
