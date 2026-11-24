package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"research-agent/internal/survey"
)

const surveySchema = `
	CREATE TABLE survey_runs (
		ordinal INTEGER PRIMARY KEY,
		analysis_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		material_id TEXT NOT NULL REFERENCES materials(material_id),
		material_name TEXT NOT NULL,
		material_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL,
		total INTEGER NOT NULL,
		valid INTEGER NOT NULL,
		invalid INTEGER NOT NULL,
		pending INTEGER NOT NULL,
		demo INTEGER NOT NULL,
		options_json BLOB NOT NULL,
		report_json BLOB NOT NULL,
		UNIQUE (project_id, request_id)
	);
	CREATE INDEX surveys_latest ON survey_runs(project_id, ordinal DESC);
	PRAGMA user_version = 9;
`

const (
	surveyColumns  = `ordinal, analysis_id, material_id, material_name, material_sha256, created_at, total, valid, invalid, pending, demo`
	maxSurveyBytes = 8 << 20
)

var (
	errSurveyFormat           = errors.New("unsupported survey source format")
	errSurveyTooLarge         = errors.New("CSV source exceeds 8 MiB")
	errSurveyWorkbookTooLarge = errors.New("workbook exceeds 8 MiB")
)

type surveyAnalysisError struct{ cause error }

func (e *surveyAnalysisError) Error() string { return e.cause.Error() }
func (e *surveyAnalysisError) Unwrap() error { return e.cause }

type surveyRunInfo struct {
	AnalysisID     string `json:"analysisId"`
	MaterialID     string `json:"materialId"`
	MaterialName   string `json:"materialName"`
	MaterialSHA256 string `json:"materialSha256"`
	CreatedAt      string `json:"createdAt"`
	Total          int    `json:"total"`
	Valid          int    `json:"valid"`
	Invalid        int    `json:"invalid"`
	Pending        int    `json:"pending"`
	Demo           bool   `json:"demo"`
	ordinal        int64
}

type surveyRun struct {
	surveyRunInfo
	Options json.RawMessage `json:"options"`
	Report  json.RawMessage `json:"report"`
}

func (info *surveyRunInfo) scanFields() []any {
	return []any{&info.ordinal, &info.AnalysisID, &info.MaterialID, &info.MaterialName, &info.MaterialSHA256,
		&info.CreatedAt, &info.Total, &info.Valid, &info.Invalid, &info.Pending, &info.Demo}
}

func scanSurveyRun(row *sql.Row) (surveyRun, error) {
	var run surveyRun
	var options, report []byte
	if err := row.Scan(append(run.scanFields(), &options, &report)...); err != nil {
		return surveyRun{}, err
	}
	run.Options, run.Report = options, report
	return run, nil
}

func (s *Store) getSurveyRun(ctx context.Context, projectID, analysisID string) (surveyRun, error) {
	return scanSurveyRun(s.db.QueryRowContext(ctx, `SELECT `+surveyColumns+`, options_json, report_json
		FROM survey_runs WHERE project_id = ? AND analysis_id = ?`, projectID, analysisID))
}

func (s *Store) listSurveyRuns(ctx context.Context, projectID string, cursor int64, limit int) ([]surveyRunInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+surveyColumns+` FROM survey_runs
		WHERE project_id = ? AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, projectID, cursor, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]surveyRunInfo, 0, limit)
	for rows.Next() {
		var run surveyRunInfo
		if err := rows.Scan(run.scanFields()...); err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

func (s *Store) replaySurveyRun(ctx context.Context, projectID, requestID, materialID string, options []byte) (surveyRun, error) {
	run, err := scanSurveyRun(s.db.QueryRowContext(ctx, `SELECT `+surveyColumns+`, options_json, report_json
		FROM survey_runs WHERE project_id = ? AND request_id = ?`, projectID, requestID))
	if err == nil && (run.MaterialID != materialID || !bytes.Equal(run.Options, options)) {
		return surveyRun{}, errRequestConflict
	}
	return run, err
}

func (s *Store) createSurveyRun(ctx context.Context, projectID, requestID, materialID string, options survey.Options) (surveyRun, bool, error) {
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return surveyRun{}, false, err
	}
	run, err := s.replaySurveyRun(ctx, projectID, requestID, materialID, optionsJSON)
	if !errors.Is(err, sql.ErrNoRows) {
		return run, false, err
	}
	material, err := s.getMaterial(ctx, projectID, materialID)
	if err != nil {
		return surveyRun{}, false, err
	}
	report, err := s.analyzeSurveyMaterial(material, options)
	if err != nil {
		return surveyRun{}, false, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return surveyRun{}, false, err
	}
	run = surveyRun{
		surveyRunInfo: surveyRunInfo{
			AnalysisID: rand.Text(), MaterialID: material.MaterialID, MaterialName: material.Name, MaterialSHA256: material.SHA256,
			CreatedAt: time.Now().UTC().Format(createdAtLayout), Total: report.Total, Valid: report.Valid,
			Invalid: report.Invalid, Pending: report.Pending, Demo: report.Demo,
		},
		Options: optionsJSON, Report: reportJSON,
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO survey_runs
		(analysis_id, project_id, request_id, material_id, material_name, material_sha256, created_at,
		 total, valid, invalid, pending, demo, options_json, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, request_id) DO NOTHING`,
		run.AnalysisID, projectID, requestID, run.MaterialID, run.MaterialName, run.MaterialSHA256, run.CreatedAt,
		run.Total, run.Valid, run.Invalid, run.Pending, run.Demo, optionsJSON, reportJSON)
	if err != nil {
		return surveyRun{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return surveyRun{}, false, err
	}
	if inserted == 0 {
		// A concurrent request may have saved the run during analysis.
		run, err = s.replaySurveyRun(ctx, projectID, requestID, materialID, optionsJSON)
		return run, false, err
	}
	return run, true, nil
}

func (s *Store) openSurveyWorkbook(material Material) (*os.File, error) {
	if !strings.EqualFold(filepath.Ext(material.Name), ".xlsx") {
		return nil, errSurveyFormat
	}
	if material.Size > maxSurveyBytes {
		return nil, errSurveyWorkbookTooLarge
	}
	return os.Open(s.materialObject(material.SHA256))
}

func (s *Store) surveyWorkbookSheets(material Material) ([]string, error) {
	file, err := s.openSurveyWorkbook(material)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	sheets, err := survey.WorkbookSheets(file, material.Size)
	return sheets, asSurveyAnalysisError(err)
}

func (s *Store) analyzeSurveyMaterial(material Material, options survey.Options) (survey.Report, error) {
	switch strings.ToLower(filepath.Ext(material.Name)) {
	case ".xlsx":
		file, err := s.openSurveyWorkbook(material)
		if err != nil {
			return survey.Report{}, err
		}
		defer file.Close()
		report, err := survey.AnalyzeWorkbook(file, material.Size, options)
		return report, asSurveyAnalysisError(err)
	case ".csv":
		if material.Size > maxSurveyBytes {
			return survey.Report{}, errSurveyTooLarge
		}
		file, err := os.Open(s.materialObject(material.SHA256))
		if err != nil {
			return survey.Report{}, err
		}
		defer file.Close()
		input := &io.LimitedReader{R: file, N: maxSurveyBytes + 1}
		report, err := survey.Analyze(input, options)
		if input.N == 0 {
			return survey.Report{}, errSurveyTooLarge
		}
		return report, asSurveyAnalysisError(err)
	default:
		return survey.Report{}, errSurveyFormat
	}
}

func asSurveyAnalysisError(err error) error {
	var fileError *os.PathError
	if err == nil || errors.As(err, &fileError) {
		return err
	}
	return &surveyAnalysisError{cause: err}
}
