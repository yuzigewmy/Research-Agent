package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"research-agent/internal/survey"
)

func (s *Store) handleMapSurveySettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID        string             `json:"requestId"`
		TargetAnalysisID string             `json:"targetAnalysisId"`
		Fields           []survey.FieldPair `json:"fields"`
	}
	if !readJSON(w, r, &input, 512<<10) {
		return
	}
	sourceID := r.PathValue("analysisId")
	if !validRequestID(sourceID) || !validRequestID(input.RequestID) || !validRequestID(input.TargetAnalysisID) || input.Fields == nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供请求编号、来源报告、目标报告和字段对应关系。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	source, err := s.getSurveyRun(ctx, projectID, sourceID)
	if err != nil {
		writeSurveyError(w, "read source survey", err)
		return
	}
	target, err := s.getSurveyRun(ctx, projectID, input.TargetAnalysisID)
	if err != nil {
		writeSurveyError(w, "read target survey", err)
		return
	}
	if source.MaterialID == target.MaterialID || source.MaterialSHA256 == target.MaterialSHA256 {
		writeError(w, http.StatusUnprocessableEntity, "SURVEY_INVALID", "请选择另一份资料版本作为目标。")
		return
	}
	options, err := mapStoredSurveySettings(source, target, input.Fields)
	if err != nil {
		writeSurveyError(w, "map survey settings", err)
		return
	}
	s.respondToSurveyCreation(w, r, input.RequestID, target.MaterialID, options)
}

func mapStoredSurveySettings(source, target surveyRun, fields []survey.FieldPair) (survey.Options, error) {
	var sourceOptions, targetOptions survey.Options
	var sourceReport, targetReport struct{ Columns []string }
	for _, item := range []struct {
		data json.RawMessage
		out  any
	}{
		{source.Options, &sourceOptions}, {target.Options, &targetOptions},
		{source.Report, &sourceReport}, {target.Report, &targetReport},
	} {
		if err := json.Unmarshal(item.data, item.out); err != nil {
			return survey.Options{}, err
		}
	}
	options, confirmed, err := survey.MapSettings(sourceOptions, sourceReport.Columns, targetReport.Columns, fields, targetOptions.Sheet)
	if err != nil {
		return survey.Options{}, asSurveyAnalysisError(err)
	}
	options.SettingsSource = &survey.SettingsSource{AnalysisID: source.AnalysisID, Fields: confirmed}
	return options, nil
}

func (s *Store) verifySurveySettingsSource(ctx context.Context, projectID, materialID string, options survey.Options) error {
	if options.SettingsSource == nil {
		return nil
	}
	provenance, err := json.Marshal(options.SettingsSource)
	if err != nil {
		return err
	}
	var exists bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM survey_runs
		WHERE project_id = ? AND material_id = ? AND COALESCE(json_extract(options_json, '$.sheet'), '') = ?
		AND json_extract(options_json, '$.settingsSource') = ?)`, projectID, materialID, options.Sheet, string(provenance)).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return asSurveyAnalysisError(errors.New("找不到此资料和工作表已确认的字段映射。"))
	}
	return nil
}
