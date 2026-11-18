package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

const (
	experimentStart    = "start"
	experimentContinue = "continue"
	experimentDone     = "COMPLETED"
	experimentWaiting  = "WAITING_CHECKPOINT"
)

var errExperimentState = errors.New("experiment cannot be started or continued in its current state")

const experimentSchema = `
	CREATE TABLE experiment_runs (
		ordinal INTEGER PRIMARY KEY,
		run_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		revision INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL,
		step_no INTEGER NOT NULL DEFAULT 0,
		checkpoints INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX experiment_latest ON experiment_runs(project_id, ordinal DESC);
	CREATE TABLE experiment_requests (
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		run_id TEXT NOT NULL REFERENCES experiment_runs(run_id),
		action TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		PRIMARY KEY (project_id, request_id)
	);
	ALTER TABLE tasks ADD COLUMN experiment_run_id TEXT REFERENCES experiment_runs(run_id);
	ALTER TABLE tasks ADD COLUMN experiment_step INTEGER;
	CREATE INDEX experiment_tasks ON tasks(experiment_run_id, experiment_step, ordinal DESC);
	PRAGMA user_version = 4;
`

type ExperimentRun struct {
	RunID       string           `json:"runId"`
	Revision    int              `json:"revision"`
	Status      string           `json:"status"`
	Checkpoints bool             `json:"checkpoints"`
	CreatedAt   string           `json:"createdAt"`
	Steps       []ExperimentStep `json:"steps"`
	step        int
}

type ExperimentStep struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type experimentRequest struct {
	RequestID        string `json:"requestId"`
	RunID            string `json:"runId"`
	ExpectedRevision int    `json:"expectedRevision"`
	Checkpoints      bool   `json:"checkpoints"`
}

func (s *Store) latestExperiment(ctx context.Context, projectID string) (ExperimentRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentRun{}, err
	}
	defer tx.Rollback()
	return readExperiment(ctx, tx, projectID, "")
}

func readExperiment(ctx context.Context, tx *sql.Tx, projectID, runID string) (ExperimentRun, error) {
	var run ExperimentRun
	if err := tx.QueryRowContext(ctx, `SELECT run_id, revision, status, checkpoints, created_at, step_no FROM experiment_runs
		WHERE project_id = ? AND (? = '' OR run_id = ?) ORDER BY ordinal DESC LIMIT 1`, projectID, runID, runID).
		Scan(&run.RunID, &run.Revision, &run.Status, &run.Checkpoints, &run.CreatedAt, &run.step); err != nil {
		return run, err
	}
	run.Steps = make([]ExperimentStep, len(experimentStages))
	for i, definition := range experimentStages {
		run.Steps[i] = ExperimentStep{Key: definition.key, Title: definition.title, Status: "PENDING"}
	}
	rows, err := tx.QueryContext(ctx, `SELECT experiment_step, status, message FROM tasks
		WHERE ordinal IN (SELECT MAX(ordinal) FROM tasks WHERE experiment_run_id = ? GROUP BY experiment_step)`, run.RunID)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var status, message string
		if err := rows.Scan(&index, &status, &message); err != nil {
			return run, err
		}
		run.Steps[index].Status, run.Steps[index].Message = status, message
	}
	return run, rows.Err()
}

func (s *Store) applyExperiment(ctx context.Context, projectID, action string, req experimentRequest, modelReady bool) (ExperimentRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentRun{}, false, err
	}
	defer tx.Rollback()

	var runID, recordedAction string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT run_id, action, expected_revision FROM experiment_requests
		WHERE project_id = ? AND request_id = ?`, projectID, req.RequestID).Scan(&runID, &recordedAction, &revision)
	if err == nil {
		if recordedAction != action || revision != req.ExpectedRevision || (action == experimentContinue && runID != req.RunID) {
			return ExperimentRun{}, false, errRequestConflict
		}
		run, err := readExperiment(ctx, tx, projectID, runID)
		if err == nil && action == experimentStart && req.Checkpoints != run.Checkpoints {
			return ExperimentRun{}, false, errRequestConflict
		}
		return run, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ExperimentRun{}, false, err
	}
	var template string
	if err := tx.QueryRowContext(ctx, `SELECT template_id FROM projects WHERE project_id = ?`, projectID).Scan(&template); err != nil {
		return ExperimentRun{}, false, err
	}
	if template != "experiment" {
		return ExperimentRun{}, false, errExperimentState
	}
	if !modelReady {
		return ExperimentRun{}, false, errModelUnavailable
	}

	current, err := readExperiment(ctx, tx, projectID, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ExperimentRun{}, false, err
	}
	if action == experimentStart {
		if current.RunID != "" && current.Status != experimentDone {
			return ExperimentRun{}, false, errExperimentState
		}
		current = ExperimentRun{RunID: rand.Text()}
		_, err = tx.ExecContext(ctx, `INSERT INTO experiment_runs(run_id, project_id, status, checkpoints, created_at)
			VALUES (?, ?, 'QUEUED', ?, ?)`, current.RunID, projectID, req.Checkpoints, time.Now().UTC().Format(createdAtLayout))
	} else {
		if current.RunID != req.RunID || current.Revision != req.ExpectedRevision {
			return ExperimentRun{}, false, errTaskRevision
		}
		if current.Status != experimentWaiting && current.Status != taskFailed && current.Status != taskInterrupted {
			return ExperimentRun{}, false, errExperimentState
		}
		_, err = tx.ExecContext(ctx, `UPDATE experiment_runs SET status = 'QUEUED', revision = revision + 1 WHERE run_id = ?`, current.RunID)
	}
	if err != nil {
		return ExperimentRun{}, false, err
	}
	if err := enqueueExperimentStep(ctx, tx, projectID, current.RunID, current.step); err != nil {
		return ExperimentRun{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_requests(project_id, request_id, run_id, action, expected_revision)
		VALUES (?, ?, ?, ?, ?)`, projectID, req.RequestID, current.RunID, action, req.ExpectedRevision); err != nil {
		return ExperimentRun{}, false, err
	}
	run, err := readExperiment(ctx, tx, projectID, current.RunID)
	if err != nil {
		return ExperimentRun{}, false, err
	}
	return run, true, tx.Commit()
}

func enqueueExperimentStep(ctx context.Context, tx *sql.Tx, projectID, runID string, step int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, experiment_run_id, experiment_step)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, ?, ?)`,
		projectID, rand.Text(), experimentStages[step].title, time.Now().UTC().Format(createdAtLayout), projectID, runID, step)
	return err
}

func finishExperimentStep(ctx context.Context, tx *sql.Tx, projectID string, task Task, status string) error {
	step := task.experimentStep
	if status == taskSucceeded {
		if step+1 == len(experimentStages) {
			status = experimentDone
		} else {
			step++
			status = taskQueued
			if experimentStages[task.experimentStep].checkpoint {
				var checkpoints bool
				if err := tx.QueryRowContext(ctx, `SELECT checkpoints FROM experiment_runs WHERE run_id = ?`, task.experimentRunID).Scan(&checkpoints); err != nil {
					return err
				}
				if checkpoints {
					status = experimentWaiting
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE experiment_runs SET status = ?, step_no = ?, revision = revision + 1 WHERE run_id = ?`,
		status, step, task.experimentRunID); err != nil {
		return err
	}
	if status == taskQueued {
		return enqueueExperimentStep(ctx, tx, projectID, task.experimentRunID, step)
	}
	return nil
}

func recoverInterruptedExperiments(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE experiment_runs SET revision = revision + 1, status = 'INTERRUPTED' WHERE status = 'RUNNING'`)
	return err
}

func (s *Store) experimentArtifact(ctx context.Context, projectID, runID, key string) (string, error) {
	for i, stage := range experimentStages {
		if stage.key == key {
			var markdown string
			err := s.db.QueryRowContext(ctx, `SELECT markdown FROM tasks WHERE project_id = ? AND experiment_run_id = ?
				AND experiment_step = ? AND status = 'SUCCEEDED' ORDER BY ordinal DESC LIMIT 1`, projectID, runID, i).Scan(&markdown)
			return markdown, err
		}
	}
	return "", sql.ErrNoRows
}
