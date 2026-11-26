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

func (api researchTasks) comparisonEvidence(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil || cursor >= maxExperimentRows {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "计算证据的分页游标无效。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	projectID, taskID := r.PathValue("id"), r.PathValue("taskId")
	artifact, err := api.store.readTaskArtifact(ctx, projectID, taskID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && artifact.kind != experimentComparisonTask {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有可查看的配对计算证据。")
		return
	}
	if err != nil {
		writeStoreError(w, "read experiment report for evidence", err)
		return
	}
	unavailable := func() {
		writeError(w, http.StatusUnprocessableEntity, "EVIDENCE_UNAVAILABLE", "暂时无法核对报告对应的 CSV 原件和计算记录。")
	}
	task, err := lookupTask(ctx, api.store.db, projectID, taskID)
	if err != nil {
		writeTaskError(w, "read task for experiment evidence", err)
		return
	}
	var report experimentComparisonReport
	if err := json.Unmarshal([]byte(artifact.jsonArtifact), &report); err != nil ||
		report.Method != experimentComparisonMethod || len(task.Inputs) != 1 || report.Input != task.Inputs[0] {
		unavailable()
		return
	}
	source, err := api.store.readExperimentSource(ctx, projectID, report.Input)
	if err != nil {
		unavailable()
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
		unavailable()
		return
	}
	if next := int(cursor) + len(page.Items); next < page.PairedRows+page.SkippedRows {
		page.NextCursor = strconv.Itoa(next)
	}
	writeJSON(w, http.StatusOK, page)
}
