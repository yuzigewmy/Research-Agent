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
		RequiredColumns    []string                          `json:"requiredColumns"`
		DuplicateColumns   []string                          `json:"duplicateColumns"`
		CrossTab           *survey.CrossTabOptions           `json:"crossTab"`
		Reliability        *survey.ReliabilityOptions        `json:"reliability"`
		Regression         *survey.RegressionOptions         `json:"regression"`
		MultipleRegression *survey.MultipleRegressionOptions `json:"multipleRegression"`
		FactorAnalysis     *survey.FactorAnalysisOptions     `json:"factorAnalysis"`
		Kano               *survey.KanoOptions               `json:"kano"`
		PSM                *survey.PSMOptions                `json:"psm"`
		Conjoint           *survey.ConjointOptions           `json:"conjoint"`
		CFA                *survey.CFAOptions                `json:"cfa"`
		SEM                *survey.SEMOptions                `json:"sem"`
		Sheet              string                            `json:"sheet"`
		WeightColumn       string                            `json:"weightColumn"`
		CategoryMappings   []survey.CategoryMapping          `json:"categoryMappings"`
		SettingsSource     *survey.SettingsSource            `json:"settingsSource"`
		PlanID             string                            `json:"planId"`
		Reviews            []struct {
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
	mux.HandleFunc("POST "+path+"/{analysisId}/map-settings", s.handleMapSurveySettings)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc(path+"/{analysisId}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(path+"/{analysisId}/download", methodNotAllowed("GET, HEAD"))
	sheetsPath := projectsPath + "/{id}/materials/{materialId}/survey-sheets"
	mux.HandleFunc("GET "+sheetsPath, s.handleSurveySheets)
	mux.HandleFunc(sheetsPath, methodNotAllowed("GET, HEAD"))
	plansPath := projectsPath + "/{id}/survey-plans"
	mux.HandleFunc("GET "+plansPath, s.handleListSurveyPlans)
	mux.HandleFunc("POST "+plansPath, s.handleCreateSurveyPlan)
	mux.HandleFunc(plansPath, methodNotAllowed("GET, HEAD, POST"))
}

func (s *Store) handleSurveySheets(w http.ResponseWriter, r *http.Request) {
	material, err := s.getMaterial(r.Context(), r.PathValue("id"), r.PathValue("materialId"))
	if err != nil {
		writeSurveyError(w, "read survey workbook material", err)
		return
	}
	sheets, err := s.surveyWorkbookSheets(material)
	if err != nil {
		writeSurveyError(w, "list survey workbook sheets", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sheets []string `json:"sheets"`
	}{Sheets: sheets})
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
	if input.Options.PlanID != "" && !validRequestID(input.Options.PlanID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的分析计划编号。")
		return
	}
	options := survey.Options{
		RequiredColumns: input.Options.RequiredColumns, DuplicateColumns: input.Options.DuplicateColumns,
		Reviews:            make([]survey.Review, 0, len(input.Options.Reviews)),
		CrossTab:           input.Options.CrossTab,
		Reliability:        input.Options.Reliability,
		Regression:         input.Options.Regression,
		MultipleRegression: input.Options.MultipleRegression,
		FactorAnalysis:     input.Options.FactorAnalysis,
		Kano:               input.Options.Kano,
		PSM:                input.Options.PSM,
		Conjoint:           input.Options.Conjoint,
		CFA:                input.Options.CFA,
		SEM:                input.Options.SEM,
		Sheet:              input.Options.Sheet,
		WeightColumn:       input.Options.WeightColumn,
		CategoryMappings:   input.Options.CategoryMappings,
		PlanID:             input.Options.PlanID,
	}
	for _, review := range input.Options.Reviews {
		if review.Accept == nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请为每条复核记录明确选择接受或拒绝。")
			return
		}
		options.Reviews = append(options.Reviews, survey.Review{Row: review.Row, Accept: *review.Accept, Reason: review.Reason})
	}
	options.SettingsSource = input.Options.SettingsSource
	if err := s.verifySurveySettingsSource(r.Context(), r.PathValue("id"), input.MaterialID, options); err != nil {
		writeSurveyError(w, "check survey settings source", err)
		return
	}
	s.respondToSurveyCreation(w, r, input.RequestID, input.MaterialID, options)
}

func (s *Store) respondToSurveyCreation(w http.ResponseWriter, r *http.Request, requestID, materialID string, options survey.Options) {
	projectID := r.PathValue("id")
	run, created, err := s.createSurveyRun(r.Context(), projectID, requestID, materialID, options)
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
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "问卷分析支持 CSV 和 XLSX。读取工作表时请选择 XLSX 资料。")
	case errors.Is(err, errSurveyTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "CSV_TOO_LARGE", "问卷资料不能超过 8 MiB。")
	case errors.Is(err, errSurveyWorkbookTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "WORKBOOK_TOO_LARGE", "问卷工作簿不能超过 8 MiB。")
	case errors.As(err, &inputError):
		writeError(w, http.StatusUnprocessableEntity, "SURVEY_INVALID", "无法读取或分析问卷资料。"+inputError.Error())
	default:
		writeStoreError(w, operation, err)
	}
}
