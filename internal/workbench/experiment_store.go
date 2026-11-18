package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	workflowStart    = "start"
	workflowContinue = "continue"
	workflowDone     = "COMPLETED"
	workflowWaiting  = "WAITING_CHECKPOINT"
)

var (
	errWorkflowState = errors.New("workflow action is invalid in the current state")
	errReviewSources = errors.New("literature review needs at least one verified work")
	errReviewTarget  = errors.New("invalid literature source target")
)

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

const workflowSchema = `
	ALTER TABLE experiment_runs RENAME TO workflow_runs;
	ALTER TABLE experiment_requests RENAME TO workflow_requests;
	ALTER TABLE tasks RENAME COLUMN experiment_run_id TO workflow_run_id;
	ALTER TABLE tasks RENAME COLUMN experiment_step TO workflow_step;
	ALTER TABLE workflow_runs ADD COLUMN workflow_key TEXT NOT NULL DEFAULT 'experiment-v1';
	ALTER TABLE workflow_runs ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE workflow_runs ADD COLUMN target_paper_count INTEGER NOT NULL DEFAULT 0;
	PRAGMA user_version = 11;
`

type WorkflowRun struct {
	RunID            string         `json:"runId"`
	WorkflowKey      string         `json:"workflowKey"`
	Revision         int            `json:"revision"`
	Status           string         `json:"status"`
	Checkpoints      bool           `json:"checkpoints"`
	CreatedAt        string         `json:"createdAt"`
	TargetPaperCount int            `json:"targetPaperCount,omitempty"`
	SourceCount      int            `json:"sourceCount,omitempty"`
	Steps            []WorkflowStep `json:"steps"`
	step             int
}

type WorkflowStep struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type workflowRequest struct {
	RequestID        string `json:"requestId"`
	RunID            string `json:"runId"`
	ExpectedRevision int    `json:"expectedRevision"`
	Checkpoints      bool   `json:"checkpoints"`
	TargetPaperCount int    `json:"targetPaperCount"`
}

func (s *Store) latestWorkflow(ctx context.Context, projectID string) (WorkflowRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRun{}, err
	}
	defer tx.Rollback()
	return readWorkflow(ctx, tx, projectID, "")
}

func readWorkflow(ctx context.Context, tx *sql.Tx, projectID, runID string) (WorkflowRun, error) {
	var run WorkflowRun
	err := tx.QueryRowContext(ctx, `SELECT run_id, workflow_key, revision, status, checkpoints, created_at, step_no,
		target_paper_count, json_array_length(evidence_json) FROM workflow_runs
		WHERE project_id = ? AND (? = '' OR run_id = ?) ORDER BY ordinal DESC LIMIT 1`, projectID, runID, runID).
		Scan(&run.RunID, &run.WorkflowKey, &run.Revision, &run.Status, &run.Checkpoints, &run.CreatedAt, &run.step, &run.TargetPaperCount, &run.SourceCount)
	if err != nil {
		return WorkflowRun{}, err
	}
	stages := workflowStages[run.WorkflowKey]
	run.Steps = make([]WorkflowStep, len(stages))
	for i, stage := range stages {
		run.Steps[i] = WorkflowStep{Key: stage.key, Title: stage.title, Status: "PENDING"}
	}
	rows, err := tx.QueryContext(ctx, `SELECT workflow_step, status, message FROM tasks
		WHERE ordinal IN (SELECT MAX(ordinal) FROM tasks WHERE workflow_run_id = ? GROUP BY workflow_step)`, run.RunID)
	if err != nil {
		return WorkflowRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var status, message string
		if err := rows.Scan(&index, &status, &message); err != nil {
			return WorkflowRun{}, err
		}
		run.Steps[index].Status, run.Steps[index].Message = status, message
	}
	return run, rows.Err()
}

func (s *Store) applyWorkflow(ctx context.Context, projectID, action string, req workflowRequest, modelReady bool) (WorkflowRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	defer tx.Rollback()

	var template string
	if err := tx.QueryRowContext(ctx, `SELECT template_id FROM projects WHERE project_id = ?`, projectID).Scan(&template); err != nil {
		return WorkflowRun{}, false, err
	}
	key := projectWorkflows[template]
	if key == "" {
		return WorkflowRun{}, false, errWorkflowState
	}
	target, err := reviewTarget(key, action, req.TargetPaperCount)
	if err != nil {
		return WorkflowRun{}, false, err
	}

	var runID, recordedAction string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT run_id, action, expected_revision FROM workflow_requests
		WHERE project_id = ? AND request_id = ?`, projectID, req.RequestID).Scan(&runID, &recordedAction, &revision)
	if err == nil {
		if recordedAction != action || revision != req.ExpectedRevision || (action == workflowContinue && runID != req.RunID) {
			return WorkflowRun{}, false, errRequestConflict
		}
		run, err := readWorkflow(ctx, tx, projectID, runID)
		if err == nil && action == workflowStart && (req.Checkpoints != run.Checkpoints || target != run.TargetPaperCount) {
			return WorkflowRun{}, false, errRequestConflict
		}
		return run, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, false, err
	}
	if !modelReady {
		return WorkflowRun{}, false, errModelUnavailable
	}

	current, err := readWorkflow(ctx, tx, projectID, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, false, err
	}
	if action == workflowStart {
		if current.RunID != "" && current.Status != workflowDone {
			return WorkflowRun{}, false, errWorkflowState
		}
		evidence := "[]"
		if key == reviewWorkflow {
			evidence, err = freezeReviewSources(ctx, tx, projectID, target)
			if err != nil {
				return WorkflowRun{}, false, err
			}
		}
		current = WorkflowRun{RunID: rand.Text(), WorkflowKey: key}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs
			(run_id, project_id, status, checkpoints, created_at, workflow_key, evidence_json, target_paper_count)
			VALUES (?, ?, 'QUEUED', ?, ?, ?, ?, ?)`, current.RunID, projectID, req.Checkpoints, time.Now().UTC().Format(createdAtLayout), key, evidence, target)
	} else {
		if current.RunID != req.RunID || current.Revision != req.ExpectedRevision {
			return WorkflowRun{}, false, errTaskRevision
		}
		if current.Status != workflowWaiting && current.Status != taskFailed && current.Status != taskInterrupted {
			return WorkflowRun{}, false, errWorkflowState
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status = 'QUEUED', revision = revision + 1 WHERE run_id = ?`, current.RunID)
	}
	if err != nil {
		return WorkflowRun{}, false, err
	}
	if err := enqueueWorkflowStep(ctx, tx, projectID, current.RunID, current.step, workflowStages[current.WorkflowKey][current.step]); err != nil {
		return WorkflowRun{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_requests(project_id, request_id, run_id, action, expected_revision)
		VALUES (?, ?, ?, ?, ?)`, projectID, req.RequestID, current.RunID, action, req.ExpectedRevision); err != nil {
		return WorkflowRun{}, false, err
	}
	run, err := readWorkflow(ctx, tx, projectID, current.RunID)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	return run, true, tx.Commit()
}

func enqueueWorkflowStep(ctx context.Context, tx *sql.Tx, projectID, runID string, step int, stage workflowStage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, workflow_run_id, workflow_step)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, ?, ?)`,
		projectID, rand.Text(), stage.title, time.Now().UTC().Format(createdAtLayout), projectID, runID, step)
	return err
}

func finishWorkflowStep(ctx context.Context, tx *sql.Tx, projectID string, task Task, status string) error {
	step := task.workflowStep
	var stages []workflowStage
	if status == taskSucceeded {
		var key string
		var checkpoints bool
		if err := tx.QueryRowContext(ctx, `SELECT workflow_key, checkpoints FROM workflow_runs WHERE run_id = ?`, task.workflowRunID).
			Scan(&key, &checkpoints); err != nil {
			return err
		}
		stages = workflowStages[key]
		switch {
		case step+1 == len(stages):
			status = workflowDone
		case checkpoints && stages[step].checkpoint:
			step++
			status = workflowWaiting
		default:
			step++
			status = taskQueued
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, step_no = ?, revision = revision + 1 WHERE run_id = ?`,
		status, step, task.workflowRunID); err != nil {
		return err
	}
	if status == taskQueued {
		return enqueueWorkflowStep(ctx, tx, projectID, task.workflowRunID, step, stages[step])
	}
	return nil
}

func recoverInterruptedWorkflows(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE workflow_runs SET revision = revision + 1, status = 'INTERRUPTED' WHERE status = 'RUNNING'`)
	return err
}

func (s *Store) workflowArtifact(ctx context.Context, projectID, runID, key string) (string, string, error) {
	var workflow string
	if err := s.db.QueryRowContext(ctx, `SELECT workflow_key FROM workflow_runs WHERE project_id = ? AND run_id = ?`, projectID, runID).Scan(&workflow); err != nil {
		return "", "", err
	}
	for i, stage := range workflowStages[workflow] {
		if stage.key == key {
			var markdown string
			err := s.db.QueryRowContext(ctx, `SELECT markdown FROM tasks WHERE project_id = ? AND workflow_run_id = ?
				AND workflow_step = ? AND status = 'SUCCEEDED' ORDER BY ordinal DESC LIMIT 1`, projectID, runID, i).Scan(&markdown)
			return strings.TrimSuffix(workflow, "-v1") + "-" + key + ".md", markdown, err
		}
	}
	return "", "", sql.ErrNoRows
}
