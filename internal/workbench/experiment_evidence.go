package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

const experimentEvidencePageSize = 50

var errExperimentEvidence = errors.New("comparison evidence does not match the recorded input")

func readExperimentReport(ctx context.Context, db workspaceQuery, projectID, taskID string) (experimentComparisonReport, error) {
	var report experimentComparisonReport
	var resultJSON, inputsJSON string
	if err := db.QueryRowContext(ctx, `SELECT result_json, inputs_json FROM tasks
		WHERE project_id = ? AND task_id = ? AND kind = ? AND status = 'SUCCEEDED' AND `+standaloneTask,
		projectID, taskID, experimentComparisonTask).Scan(&resultJSON, &inputsJSON); err != nil {
		return report, err
	}
	var inputs []taskInput
	if err := json.Unmarshal([]byte(resultJSON), &report); err != nil || report.Method != experimentComparisonMethod {
		return report, errExperimentEvidence
	}
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil || len(inputs) != 1 || report.Input != inputs[0] {
		return report, errExperimentEvidence
	}
	return report, nil
}

func (s *Store) readExperimentEvidence(ctx context.Context, projectID, taskID string) (experimentComparisonReport, string, error) {
	report, err := readExperimentReport(ctx, s.db, projectID, taskID)
	if err != nil {
		return report, "", err
	}
	source, err := s.readExperimentSource(ctx, projectID, report.Input)
	if err != nil {
		return report, "", errExperimentEvidence
	}
	return report, source, nil
}

func writeExperimentEvidenceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有可查看的配对计算证据。")
	case errors.Is(err, errExperimentEvidence):
		writeError(w, http.StatusUnprocessableEntity, "EVIDENCE_UNAVAILABLE", "暂时无法核对报告对应的 CSV 原件和计算记录。")
	default:
		writeStoreError(w, "read experiment evidence", err)
	}
}

func (api researchTasks) comparisonEvidence(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil || cursor >= maxExperimentRows {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "计算证据的分页游标无效。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	report, source, err := api.store.readExperimentEvidence(ctx, r.PathValue("id"), r.PathValue("taskId"))
	if err != nil {
		writeExperimentEvidenceError(w, err)
		return
	}
	page := struct {
		Input       taskInput       `json:"input"`
		Items       []experimentRow `json:"items"`
		PairedRows  int             `json:"pairedRows"`
		SkippedRows int             `json:"skippedRows"`
		NextCursor  string          `json:"nextCursor,omitempty"`
	}{Input: report.Input, Items: make([]experimentRow, 0, experimentEvidencePageSize)}
	err = scanExperimentRows(ctx, source, func(row experimentRow, _, _ *big.Rat) {
		index := page.PairedRows + page.SkippedRows
		if index >= int(cursor) && len(page.Items) < experimentEvidencePageSize {
			page.Items = append(page.Items, row)
		}
		if row.Missing == "" {
			page.PairedRows++
		} else {
			page.SkippedRows++
		}
	})
	if err != nil || page.PairedRows != report.Metrics.PairedRows || page.SkippedRows != report.Metrics.SkippedRows {
		writeExperimentEvidenceError(w, errExperimentEvidence)
		return
	}
	if next := int(cursor) + len(page.Items); next < page.PairedRows+page.SkippedRows {
		page.NextCursor = strconv.Itoa(next)
	}
	writeJSON(w, http.StatusOK, page)
}
