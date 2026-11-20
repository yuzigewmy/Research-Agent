package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const taskColumns = `ordinal, task_id, instruction, revision, status, created_at, message,
	COALESCE(workflow_run_id, ''), COALESCE(workflow_step, 0), COALESCE(review_version_id, ''), selection_edit, finding_recheck,
	inputs_json, COALESCE(source_turn_id, '')`

const standaloneTask = `workflow_run_id IS NULL AND review_version_id IS NULL AND selection_edit = 0 AND finding_recheck = 0`

const nextTaskQueuePosition = `(SELECT COALESCE(MAX(queued_seq), 0) + 1 FROM tasks
	WHERE project_id = ? AND status = 'QUEUED')`

const (
	taskProposed    = "PROPOSED"
	taskQueued      = "QUEUED"
	taskRunning     = "RUNNING"
	taskCancelling  = "CANCELLING"
	taskCancelled   = "CANCELLED"
	taskSucceeded   = "SUCCEEDED"
	taskFailed      = "FAILED"
	taskInterrupted = "INTERRUPTED"
)

const (
	proposeTask = "propose"
	editTask    = "edit"
	confirmTask = "confirm"
	cancelTask  = "cancel"
)

var (
	errTaskRevision = errors.New("task revision changed")
	errTaskState    = errors.New("task cannot be confirmed in its current state")
)

// Task is the public record of a research plan proposal and its execution.
type Task struct {
	ordinal         int64
	workflowRunID   string
	workflowStep    int
	reviewVersionID string
	selectionEdit   bool
	findingRecheck  bool
	inputsJSON      string
	TaskID          string      `json:"taskId"`
	Instruction     string      `json:"instruction"`
	Revision        int         `json:"revision"`
	Status          string      `json:"status"`
	CreatedAt       string      `json:"createdAt"`
	Message         string      `json:"message,omitempty"`
	Inputs          []taskInput `json:"inputs,omitempty"`
	SourceTurnID    string      `json:"sourceTurnId,omitempty"`
}

type taskRequest struct {
	ProjectID, TaskID, Action, RequestID, Instruction string
	ExpectedRevision                                  int
	InputVersionIDs                                   []string
	SourceTurnID                                      string
}

const tasksSchema = `
	CREATE TABLE tasks (
		ordinal INTEGER PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		task_id TEXT NOT NULL,
		instruction TEXT NOT NULL,
		revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
		status TEXT NOT NULL CHECK (status IN ('PROPOSED', 'QUEUED', 'RUNNING', 'CANCELLING', 'CANCELLED', 'SUCCEEDED', 'FAILED', 'INTERRUPTED')),
		created_at TEXT NOT NULL,
		queued_seq INTEGER,
		message TEXT NOT NULL DEFAULT '',
		markdown TEXT NOT NULL DEFAULT '',
		UNIQUE (project_id, task_id)
	);
	CREATE INDEX tasks_created ON tasks (project_id, ordinal DESC);
	CREATE INDEX tasks_queue ON tasks (project_id, queued_seq) WHERE status = 'QUEUED';
	CREATE UNIQUE INDEX tasks_active ON tasks (project_id) WHERE status IN ('RUNNING', 'CANCELLING');
	CREATE TABLE task_requests (
		seq INTEGER PRIMARY KEY,
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		request_id TEXT NOT NULL,
		action TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		UNIQUE (project_id, request_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks (project_id, task_id)
	);
	PRAGMA user_version = 3;
`

const taskEditSchema = `
	ALTER TABLE task_requests ADD COLUMN instruction TEXT NOT NULL DEFAULT '';
	UPDATE task_requests SET instruction = (
		SELECT instruction FROM tasks WHERE tasks.project_id = task_requests.project_id AND tasks.task_id = task_requests.task_id
	) WHERE action = 'propose';
	PRAGMA user_version = 18;
`

func recoverInterruptedTasks(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE tasks SET revision = revision + 1,
		status = CASE WHEN status = 'CANCELLING' THEN 'CANCELLED' ELSE 'INTERRUPTED' END,
		message = CASE
			WHEN status = 'CANCELLING' THEN ''
			WHEN finding_recheck = 1 THEN '服务重启中断了复查，可重试。'
			WHEN selection_edit = 1 THEN '服务重启中断了建议生成，可重新提交。'
			WHEN review_version_id IS NOT NULL THEN '服务重启中断了评审，可以按原稿版本重试。'
			WHEN workflow_run_id IS NOT NULL THEN '服务重启中断了此步骤。可以从这里继续。'
			ELSE '服务重启中断了已确认的任务，可以提议一项新任务。'
		END
		WHERE status IN ('RUNNING', 'CANCELLING') OR (status = 'QUEUED' AND ` + standaloneTask + `)`)
	return err
}

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	if err := row.Scan(&t.ordinal, &t.TaskID, &t.Instruction, &t.Revision, &t.Status, &t.CreatedAt, &t.Message,
		&t.workflowRunID, &t.workflowStep, &t.reviewVersionID, &t.selectionEdit, &t.findingRecheck, &t.inputsJSON, &t.SourceTurnID); err != nil {
		return Task{}, err
	}
	if err := json.Unmarshal([]byte(t.inputsJSON), &t.Inputs); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) listTasks(ctx context.Context, projectID, sourceTurnID string, before int64, limit int) ([]Task, error) {
	query := `SELECT ` + taskColumns + ` FROM tasks
		WHERE project_id = ? AND ` + standaloneTask + ` AND (? = 0 OR ordinal < ?)`
	args := []any{projectID, before, before}
	if sourceTurnID != "" {
		query += ` AND source_turn_id = ?`
		args = append(args, sourceTurnID)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY ordinal DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Task, 0, limit)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

// Receipts share the same transaction as the state change. A replay returns
// the current task before checking its revision or model availability.
func (s *Store) applyTaskRequest(ctx context.Context, req taskRequest, modelConfigured bool) (Task, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()

	if len(req.InputVersionIDs) > maxTaskInputs {
		return Task{}, false, errInvalidTaskInput
	}
	var inputIDs string
	if len(req.InputVersionIDs) > 0 || (req.Action == editTask && req.InputVersionIDs != nil) {
		encoded, err := json.Marshal(req.InputVersionIDs)
		if err != nil {
			return Task{}, false, err
		}
		inputIDs = string(encoded)
	}
	var taskID, action, instruction, recordedInputs string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT task_id, action, expected_revision, instruction, input_version_ids FROM task_requests
		WHERE project_id = ? AND request_id = ?`, req.ProjectID, req.RequestID).Scan(&taskID, &action, &revision, &instruction, &recordedInputs)
	if err == nil {
		if action != req.Action || revision != req.ExpectedRevision || instruction != req.Instruction ||
			recordedInputs != inputIDs || (action != proposeTask && taskID != req.TaskID) {
			return Task{}, false, errRequestConflict
		}
		task, err := lookupTask(ctx, tx, req.ProjectID, taskID)
		if err == nil && action == proposeTask && task.SourceTurnID != req.SourceTurnID {
			return Task{}, false, errRequestConflict
		}
		return task, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, err
	}

	var task Task
	var nextStatus string
	if req.Action == proposeTask {
		if err := checkTaskSource(ctx, tx, req.ProjectID, req.SourceTurnID); err != nil {
			return Task{}, false, err
		}
		inputs, err := resolveTaskInputs(ctx, tx, req.ProjectID, req.InputVersionIDs)
		if err != nil {
			return Task{}, false, err
		}
		task, err = scanTask(tx.QueryRowContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, inputs_json, source_turn_id)
			SELECT project_id, ?, ?, 'PROPOSED', ?, ?, NULLIF(?, '') FROM projects WHERE project_id = ? RETURNING `+taskColumns,
			rand.Text(), req.Instruction, time.Now().UTC().Format(createdAtLayout), inputs, req.SourceTurnID, req.ProjectID))
		if err != nil {
			return Task{}, false, err
		}
	} else {
		task, err = lookupTask(ctx, tx, req.ProjectID, req.TaskID)
		if err != nil {
			return Task{}, false, err
		}
		nextStatus, err = nextTaskStatus(task, req, modelConfigured)
		if err != nil {
			return Task{}, false, err
		}
	}

	if nextStatus != "" {
		instruction, inputs, message := task.Instruction, task.inputsJSON, task.Message
		switch req.Action {
		case editTask:
			instruction = req.Instruction
			if req.InputVersionIDs != nil {
				inputs, err = resolveTaskInputs(ctx, tx, req.ProjectID, req.InputVersionIDs)
				if err != nil {
					return Task{}, false, err
				}
				message = ""
			}
		case confirmTask:
			if len(task.Inputs) != 0 {
				var changed bool
				if err := tx.QueryRowContext(ctx, `SELECT `+changedTaskInputs+` FROM tasks WHERE ordinal = ?`, task.ordinal).Scan(&changed); err != nil {
					return Task{}, false, err
				}
				if changed {
					return Task{}, false, errTaskInputChanged
				}
			}
			message = ""
		}
		task, err = scanTask(tx.QueryRowContext(ctx, `UPDATE tasks SET status = ?, instruction = ?, inputs_json = ?, message = ?, revision = revision + 1,
			queued_seq = CASE WHEN ? = 'QUEUED' THEN `+nextTaskQueuePosition+` ELSE queued_seq END
			WHERE project_id = ? AND task_id = ? AND revision = ? RETURNING `+taskColumns,
			nextStatus, instruction, inputs, message, nextStatus, req.ProjectID, req.ProjectID, task.TaskID, req.ExpectedRevision))
		if err != nil {
			return Task{}, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_requests(project_id, task_id, request_id, action, expected_revision, instruction, input_version_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, req.ProjectID, task.TaskID, req.RequestID, req.Action, req.ExpectedRevision, req.Instruction, inputIDs)
	if err != nil {
		return Task{}, false, err
	}
	return task, true, tx.Commit()
}

func lookupTask(ctx context.Context, tx *sql.Tx, projectID, taskID string) (Task, error) {
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE project_id = ? AND task_id = ? AND `+standaloneTask, projectID, taskID))
}

func nextTaskStatus(task Task, req taskRequest, modelConfigured bool) (string, error) {
	if task.Revision != req.ExpectedRevision {
		return "", errTaskRevision
	}
	if req.Action == editTask {
		if task.Status != taskProposed {
			return "", errTaskState
		}
		return taskProposed, nil
	}
	if req.Action == confirmTask {
		if task.Status != taskProposed {
			return "", errTaskState
		}
		if !modelConfigured {
			return "", errModelUnavailable
		}
		return taskQueued, nil
	}
	switch task.Status {
	case taskProposed, taskQueued:
		return taskCancelled, nil
	case taskRunning:
		return taskCancelling, nil
	default:
		return "", nil
	}
}

func (s *Store) claimQueuedTask(ctx context.Context, projectID string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'PROPOSED', revision = revision + 1, queued_seq = NULL, message = ?
		WHERE project_id = ? AND status = 'QUEUED' AND `+changedTaskInputs, taskInputsChangedMessage, projectID); err != nil {
		return Task{}, err
	}
	task, err := scanTask(tx.QueryRowContext(ctx, `UPDATE tasks SET status = 'RUNNING', revision = revision + 1
		WHERE project_id = ? AND status = 'QUEUED' AND ordinal = (
			SELECT ordinal FROM tasks WHERE project_id = ? AND status = 'QUEUED' ORDER BY queued_seq, ordinal LIMIT 1
		) AND NOT EXISTS (SELECT 1 FROM tasks WHERE project_id = ? AND status IN ('RUNNING', 'CANCELLING'))
		RETURNING `+taskColumns, projectID, projectID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return Task{}, commitErr
		}
		return Task{}, err
	}
	if err != nil {
		return Task{}, err
	}
	if task.workflowRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status = 'RUNNING', revision = revision + 1
			WHERE run_id = ?`, task.workflowRunID); err != nil {
			return Task{}, err
		}
	}
	return task, tx.Commit()
}

// The committed cancellation wins against success or failure. A completed
// plan is visible in the same write that marks the task SUCCEEDED.
func (s *Store) finishTask(ctx context.Context, projectID string, task Task, status, message string, result taskResult) error {
	findings := "[]"
	if len(result.findings) != 0 {
		encoded, err := json.Marshal(result.findings)
		if err != nil {
			return err
		}
		findings = string(encoded)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `UPDATE tasks SET revision = revision + 1,
		status = CASE WHEN status = 'CANCELLING' THEN 'CANCELLED' ELSE ? END,
		message = CASE WHEN status = 'RUNNING' THEN ? ELSE '' END,
		markdown = CASE WHEN status = 'RUNNING' AND ? = 'SUCCEEDED' THEN ? ELSE '' END,
		review_findings = CASE WHEN status = 'RUNNING' AND ? = 'SUCCEEDED' THEN ? ELSE '[]' END,
		workflow_topic_data = CASE WHEN status = 'RUNNING' AND ? = 'SUCCEEDED' THEN ? ELSE '' END
		WHERE project_id = ? AND task_id = ? AND status IN ('RUNNING', 'CANCELLING')
		RETURNING status`, status, message, status, result.markdown, status, findings, status, result.topicData,
		projectID, task.TaskID).Scan(&status)
	if err != nil {
		return err
	}
	if task.workflowRunID != "" {
		if err := finishWorkflowStep(ctx, tx, projectID, task, status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) taskMarkdown(ctx context.Context, projectID, taskID string) (string, error) {
	var markdown string
	err := s.db.QueryRowContext(ctx, `SELECT markdown FROM tasks WHERE project_id = ? AND task_id = ?
		AND `+standaloneTask+` AND status = 'SUCCEEDED'`,
		projectID, taskID).Scan(&markdown)
	return markdown, err
}
