package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"time"
)

const comparisonTimeout = 5 * time.Second

const experimentAttemptsSchema = `
	CREATE TABLE experiment_attempts (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		revision INTEGER NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('RUNNING', 'CANCELLING', 'CANCELLED', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT '',
		input_version_id TEXT NOT NULL,
		input_path TEXT NOT NULL,
		input_sha256 TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		result_sha256 TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (project_id, task_id, revision),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks (project_id, task_id) ON DELETE CASCADE
	);
	CREATE TRIGGER cancel_experiment_attempt AFTER UPDATE OF status ON tasks
	WHEN OLD.status = 'RUNNING' AND NEW.status = 'CANCELLING' AND NEW.kind = 'experiment-comparison'
	BEGIN
		UPDATE experiment_attempts SET status = 'CANCELLING'
		WHERE project_id = OLD.project_id AND task_id = OLD.task_id AND revision = OLD.revision;
	END;
	PRAGMA user_version = 60;
`

type experimentAttempt struct {
	Revision     int       `json:"revision"`
	Status       string    `json:"status"`
	StartedAt    string    `json:"startedAt"`
	FinishedAt   string    `json:"finishedAt,omitempty"`
	Input        taskInput `json:"input"`
	Message      string    `json:"message,omitempty"`
	ResultSHA256 string    `json:"resultSha256,omitempty"`
}

func recordExperimentClaim(ctx context.Context, tx *sql.Tx, projectID string, task Task) error {
	input := task.Inputs[0]
	_, err := tx.ExecContext(ctx, `INSERT INTO experiment_attempts
		(project_id, task_id, revision, status, started_at, input_version_id, input_path, input_sha256)
		VALUES (?, ?, ?, 'RUNNING', ?, ?, ?, ?)`, projectID, task.TaskID, task.Revision,
		time.Now().UTC().Format(createdAtLayout), input.VersionID, input.Path, input.SHA256)
	return err
}

func finishExperimentAttempt(ctx context.Context, tx *sql.Tx, projectID string, task Task, status, message, result string) error {
	digest := ""
	if status == taskSucceeded {
		checksum := sha256.Sum256([]byte(result))
		digest = hex.EncodeToString(checksum[:])
	} else if status == taskCancelled {
		message = ""
	}
	_, err := tx.ExecContext(ctx, `UPDATE experiment_attempts SET status = ?, finished_at = ?, message = ?, result_sha256 = ?
		WHERE project_id = ? AND task_id = ? AND revision = ?`, status, time.Now().UTC().Format(createdAtLayout),
		message, digest, projectID, task.TaskID, task.Revision)
	return err
}

func recoverExperimentAttempts(tx *sql.Tx) error {
	// Task recovery has already mapped RUNNING to INTERRUPTED and CANCELLING to CANCELLED.
	_, err := tx.Exec(`UPDATE experiment_attempts AS a SET (status, message) = (
			SELECT status, message FROM tasks WHERE project_id = a.project_id AND task_id = a.task_id
		), finished_at = ? WHERE a.status IN ('RUNNING', 'CANCELLING')`, time.Now().UTC().Format(createdAtLayout))
	return err
}

func (api researchTasks) comparisonAttempts(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, taskID := r.Context(), r.PathValue("id"), r.PathValue("taskId")
	task, err := lookupTask(ctx, api.store.db, projectID, taskID)
	if err == nil && task.Kind != experimentComparisonTask {
		err = sql.ErrNoRows
	}
	if err != nil {
		writeTaskError(w, "read comparison attempts", err)
		return
	}
	rows, err := api.store.db.QueryContext(ctx, `SELECT revision, status, started_at, finished_at,
		input_version_id, input_path, input_sha256, message, result_sha256 FROM experiment_attempts
		WHERE project_id = ? AND task_id = ? ORDER BY revision DESC LIMIT 20`, projectID, taskID)
	if err != nil {
		writeStoreError(w, "read comparison attempts", err)
		return
	}
	defer rows.Close()
	items := make([]experimentAttempt, 0)
	for rows.Next() {
		var item experimentAttempt
		if err := rows.Scan(&item.Revision, &item.Status, &item.StartedAt, &item.FinishedAt,
			&item.Input.VersionID, &item.Input.Path, &item.Input.SHA256, &item.Message, &item.ResultSHA256); err != nil {
			writeStoreError(w, "scan comparison attempt", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeStoreError(w, "read comparison attempts", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Items          []experimentAttempt `json:"items"`
		TimeoutSeconds int                 `json:"timeoutSeconds"`
	}{items, int(comparisonTimeout / time.Second)})
}
