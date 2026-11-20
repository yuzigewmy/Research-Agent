package workbench

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

type workflowRunSummary struct {
	RunID     string `json:"runId"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	ordinal   int64
}

func (api workflows) listRuns(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	cursor, err := eventCursor(query.Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "运行历史游标无效。")
		return
	}
	limit := 20
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "每页可查询 1 至 100 条运行记录。")
			return
		}
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := api.store.getProject(ctx, projectID); err != nil {
		writeTaskError(w, "lookup project for workflow history", err)
		return
	}
	rows, err := api.store.db.QueryContext(ctx, `SELECT ordinal, run_id, status, created_at FROM workflow_runs
		WHERE project_id = ? AND ordinal < (SELECT MAX(ordinal) FROM workflow_runs WHERE project_id = ?)
		AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, projectID, projectID, cursor, cursor, limit+1)
	if err != nil {
		writeStoreError(w, "list workflow history", err)
		return
	}
	defer rows.Close()
	items := make([]workflowRunSummary, 0, limit+1)
	for rows.Next() {
		var item workflowRunSummary
		if err := rows.Scan(&item.ordinal, &item.RunID, &item.Status, &item.CreatedAt); err != nil {
			writeStoreError(w, "read workflow history item", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeStoreError(w, "read workflow history", err)
		return
	}
	var next string
	if len(items) > limit {
		next = strconv.FormatInt(items[limit-1].ordinal, 10)
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []workflowRunSummary `json:"items"`
		NextCursor string               `json:"nextCursor,omitempty"`
	}{items, next})
}

func (api workflows) readRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := api.store.db.BeginTx(ctx, nil)
	if err != nil {
		writeStoreError(w, "open workflow detail snapshot", err)
		return
	}
	defer tx.Rollback()
	run, err := readWorkflow(ctx, tx, r.PathValue("id"), r.PathValue("runId"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该运行记录不存在。")
	case err != nil:
		writeStoreError(w, "read workflow history detail", err)
	default:
		writeJSON(w, http.StatusOK, run)
	}
}
