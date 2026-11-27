package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"research-agent/internal/survey"
)

const surveyPlanSchema = `
	CREATE TABLE survey_analysis_plans (
		ordinal INTEGER PRIMARY KEY,
		plan_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		version INTEGER NOT NULL,
		request_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		plan_json BLOB NOT NULL,
		UNIQUE (project_id, version),
		UNIQUE (project_id, request_id)
	);
	CREATE INDEX survey_plans_project_version ON survey_analysis_plans(project_id, version DESC);
	PRAGMA user_version = 64;
`

var errSurveyPlanVersionConflict = errors.New("survey analysis plan version conflict")

type surveyPlanCreateInput struct {
	RequestID        string                 `json:"requestId"`
	ExpectedVersion  *int                   `json:"expectedVersion"`
	Title            string                 `json:"title"`
	Objective        string                 `json:"objective"`
	TargetPopulation string                 `json:"targetPopulation"`
	SamplingMethod   string                 `json:"samplingMethod"`
	PlannedMethods   []string               `json:"plannedMethods"`
	MissingStrategy  string                 `json:"missingStrategy"`
	Notes            string                 `json:"notes"`
	SampleSizeInput  survey.SampleSizeInput `json:"sampleSizeInput"`
}

type surveyPlanRecord struct {
	PlanID    string `json:"planId"`
	ProjectID string `json:"projectId"`
	Version   int    `json:"version"`
	CreatedAt string `json:"createdAt"`
	survey.AnalysisPlanSpec
}

type surveyPlanList struct {
	LatestVersion int                `json:"latestVersion"`
	Items         []surveyPlanRecord `json:"items"`
}

func scanSurveyPlan(row interface{ Scan(...any) error }) (surveyPlanRecord, []byte, error) {
	var record surveyPlanRecord
	var planJSON []byte
	if err := row.Scan(&record.PlanID, &record.ProjectID, &record.Version, &record.CreatedAt, &planJSON); err != nil {
		return surveyPlanRecord{}, nil, err
	}
	if err := json.Unmarshal(planJSON, &record.AnalysisPlanSpec); err != nil {
		return surveyPlanRecord{}, nil, err
	}
	return record, planJSON, nil
}

func (s *Store) getSurveyPlan(ctx context.Context, projectID, planID string) (surveyPlanRecord, error) {
	record, _, err := scanSurveyPlan(s.db.QueryRowContext(ctx, `SELECT plan_id, project_id, version, created_at, plan_json
		FROM survey_analysis_plans WHERE project_id = ? AND plan_id = ?`, projectID, planID))
	return record, err
}

func (s *Store) listSurveyPlans(ctx context.Context, projectID string) (surveyPlanList, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		return surveyPlanList{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT plan_id, project_id, version, created_at, plan_json
		FROM survey_analysis_plans WHERE project_id = ? ORDER BY version DESC`, projectID)
	if err != nil {
		return surveyPlanList{}, err
	}
	defer rows.Close()
	items := make([]surveyPlanRecord, 0)
	for rows.Next() {
		record, _, err := scanSurveyPlan(rows)
		if err != nil {
			return surveyPlanList{}, err
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return surveyPlanList{}, err
	}
	latest := 0
	if len(items) > 0 {
		latest = items[0].Version
	}
	return surveyPlanList{LatestVersion: latest, Items: items}, nil
}

func (s *Store) createSurveyPlan(ctx context.Context, projectID, requestID string, expectedVersion int, spec survey.AnalysisPlanSpec) (surveyPlanRecord, bool, error) {
	planJSON, err := json.Marshal(spec)
	if err != nil {
		return surveyPlanRecord{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return surveyPlanRecord{}, false, err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE project_id = ?)`, projectID).Scan(&exists); err != nil {
		return surveyPlanRecord{}, false, err
	}
	if !exists {
		return surveyPlanRecord{}, false, sql.ErrNoRows
	}

	existing, existingJSON, err := scanSurveyPlan(tx.QueryRowContext(ctx, `SELECT plan_id, project_id, version, created_at, plan_json
		FROM survey_analysis_plans WHERE project_id = ? AND request_id = ?`, projectID, requestID))
	if err == nil {
		if existing.Version != expectedVersion+1 || !bytes.Equal(existingJSON, planJSON) {
			return surveyPlanRecord{}, false, errRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return surveyPlanRecord{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return surveyPlanRecord{}, false, err
	}

	var currentVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM survey_analysis_plans WHERE project_id = ?`, projectID).Scan(&currentVersion); err != nil {
		return surveyPlanRecord{}, false, err
	}
	if currentVersion != expectedVersion {
		return surveyPlanRecord{}, false, errSurveyPlanVersionConflict
	}

	record := surveyPlanRecord{
		PlanID:           rand.Text(),
		ProjectID:        projectID,
		Version:          currentVersion + 1,
		CreatedAt:        time.Now().UTC().Format(createdAtLayout),
		AnalysisPlanSpec: spec,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO survey_analysis_plans
		(plan_id, project_id, version, request_id, created_at, plan_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		record.PlanID, projectID, record.Version, requestID, record.CreatedAt, planJSON); err != nil {
		return surveyPlanRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return surveyPlanRecord{}, false, err
	}
	return record, true, nil
}

func planSourceFromRecord(record surveyPlanRecord) *survey.AnalysisPlanSource {
	methods := make([]string, len(record.PlannedMethods))
	copy(methods, record.PlannedMethods)
	return &survey.AnalysisPlanSource{
		PlanID:                   record.PlanID,
		Version:                  record.Version,
		Title:                    record.Title,
		Objective:                record.Objective,
		Method:                   record.SampleSizeResult.Method,
		ConfidenceLevel:          record.SampleSizeInput.ConfidenceLevel,
		UnadjustedSampleSize:     record.SampleSizeResult.UnadjustedSampleSize,
		FinitePopulationAdjusted: record.SampleSizeResult.FinitePopulationAdjusted,
		PlannedInvitations:       record.SampleSizeResult.PlannedInvitations,
		FormulaSummary:           record.SampleSizeResult.FormulaSummary,
		PlannedMethods:           methods,
		CreatedAt:                record.CreatedAt,
	}
}

func (s *Store) handleListSurveyPlans(w http.ResponseWriter, r *http.Request) {
	list, err := s.listSurveyPlans(r.Context(), r.PathValue("id"))
	if err != nil {
		writeSurveyPlanError(w, "list survey analysis plans", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Store) handleCreateSurveyPlan(w http.ResponseWriter, r *http.Request) {
	var input surveyPlanCreateInput
	if !readJSON(w, r, &input, 256<<10) {
		return
	}
	if !validRequestID(input.RequestID) || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号和预期版本号（首次保存为 0）。")
		return
	}
	spec, err := survey.BuildAnalysisPlanSpec(survey.AnalysisPlanSpec{
		Title:            input.Title,
		Objective:        input.Objective,
		TargetPopulation: input.TargetPopulation,
		SamplingMethod:   input.SamplingMethod,
		PlannedMethods:   input.PlannedMethods,
		MissingStrategy:  input.MissingStrategy,
		Notes:            input.Notes,
		SampleSizeInput:  input.SampleSizeInput,
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "SURVEY_PLAN_INVALID", err.Error())
		return
	}
	projectID := r.PathValue("id")
	record, created, err := s.createSurveyPlan(r.Context(), projectID, input.RequestID, *input.ExpectedVersion, spec)
	if err != nil {
		writeSurveyPlanError(w, "create survey analysis plan", err)
		return
	}
	if created {
		w.Header().Set("Location", projectsPath+"/"+projectID+"/survey-plans")
		writeJSON(w, http.StatusCreated, record)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func writeSurveyPlanError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目或分析计划不存在。")
	case errors.Is(err, errSurveyPlanVersionConflict):
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "分析计划已更新，请先刷新最新版本后再保存。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于不同的分析计划内容或版本。")
	default:
		writeStoreError(w, operation, err)
	}
}
