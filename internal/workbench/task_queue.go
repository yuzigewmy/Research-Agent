package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

const taskQueueStopSchema = `
	CREATE TABLE task_queue_stop_requests (
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		task_id TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		stopping_count INTEGER NOT NULL,
		cancelled_count INTEGER NOT NULL,
		PRIMARY KEY (project_id, request_id)
	);
	PRAGMA user_version = 33;
`

var errTaskQueueChanged = errors.New("the current project task has changed")

type queueTask struct {
	TaskID   string `json:"taskId"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Revision int    `json:"revision"`
}

type taskQueueSnapshot struct {
	Current     *queueTask `json:"current"`
	QueuedCount int        `json:"queuedCount"`
}

type taskQueueStopInput struct {
	RequestID        string `json:"requestId"`
	Scope            string `json:"scope"`
	TaskID           string `json:"taskId"`
	ExpectedRevision int    `json:"expectedRevision"`
}

type taskQueueStopReceipt struct {
	Scope          string `json:"scope"`
	StoppingCount  int    `json:"stoppingCount"`
	CancelledCount int    `json:"cancelledCount"`
}

func (s *Store) registerTaskQueue(mux *http.ServeMux) {
	path := projectsPath + "/{id}/task-queue"
	mux.HandleFunc("GET "+path, s.handleTaskQueue)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("POST "+path+"/stop", s.handleStopTaskQueue)
	mux.HandleFunc(path+"/stop", methodNotAllowed("POST"))
}

func readTaskQueue(ctx context.Context, db workspaceQuery, projectID string) (taskQueueSnapshot, error) {
	var current queueTask
	var snapshot taskQueueSnapshot
	err := db.QueryRowContext(ctx, `SELECT COALESCE(t.task_id, ''),
		CASE WHEN t.finding_recheck = 1 THEN '稿件意见复查' ELSE COALESCE(t.instruction, '') END,
		CASE WHEN t.workflow_run_id IS NOT NULL THEN 'workflow'
			WHEN t.automatic_review = 1 THEN 'automatic-review'
			WHEN t.review_version_id IS NOT NULL THEN 'review'
			WHEN t.selection_edit = 1 THEN 'edit'
			WHEN t.finding_recheck = 1 THEN 'recheck'
			WHEN t.kind = 'manuscript-check' THEN t.kind
			ELSE 'plan' END,
		COALESCE(t.status, ''), COALESCE(t.revision, 0),
		(SELECT COUNT(*) FROM tasks WHERE project_id = p.project_id AND status = 'QUEUED')
		FROM projects p LEFT JOIN tasks t ON t.project_id = p.project_id AND t.status IN ('RUNNING', 'CANCELLING')
		WHERE p.project_id = ?`, projectID).
		Scan(&current.TaskID, &current.Title, &current.Kind, &current.Status, &current.Revision, &snapshot.QueuedCount)
	if current.TaskID != "" {
		snapshot.Current = &current
	}
	return snapshot, err
}

func (s *Store) handleTaskQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := readTaskQueue(r.Context(), s.db, r.PathValue("id"))
	if err != nil {
		writeTaskError(w, "read project task queue", err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (s *Store) handleStopTaskQueue(w http.ResponseWriter, r *http.Request) {
	var input taskQueueStopInput
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	validTarget := input.Scope == "current" && validRequestID(input.TaskID) && input.ExpectedRevision > 0
	all := input.Scope == "all" && input.TaskID == "" && input.ExpectedRevision == 0
	if !validRequestID(input.RequestID) || (!validTarget && !all) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的停止范围。停止当前任务时需要任务编号和修订。")
		return
	}
	receipt, err := s.stopProjectTaskQueue(r.Context(), r.PathValue("id"), input)
	switch {
	case errors.Is(err, errTaskQueueChanged):
		writeError(w, http.StatusConflict, "TASK_QUEUE_CHANGED", "当前任务已更新，请查看队列后重试。")
	case err != nil:
		writeTaskError(w, "stop project task queue", err)
	default:
		writeJSON(w, http.StatusOK, receipt)
	}
}

func (s *Store) stopProjectTaskQueue(ctx context.Context, projectID string, input taskQueueStopInput) (taskQueueStopReceipt, error) {
	// Use the claim lock until the cancellation signal is sent. A retry of a
	// stopped review can reuse its task ID but will receive a new worker context.
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return taskQueueStopReceipt{}, err
	}
	defer tx.Rollback()

	var receipt taskQueueStopReceipt
	var target string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT scope, task_id, expected_revision, stopping_count, cancelled_count
		FROM task_queue_stop_requests WHERE project_id = ? AND request_id = ?`, projectID, input.RequestID).
		Scan(&receipt.Scope, &target, &revision, &receipt.StoppingCount, &receipt.CancelledCount)
	if err == nil {
		if receipt.Scope != input.Scope || target != input.TaskID || revision != input.ExpectedRevision {
			return taskQueueStopReceipt{}, errRequestConflict
		}
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return taskQueueStopReceipt{}, err
	}

	queue, err := readTaskQueue(ctx, tx, projectID)
	if err != nil {
		return taskQueueStopReceipt{}, err
	}
	current := queue.Current
	if input.Scope == "current" && (current == nil || current.Status != taskRunning ||
		current.TaskID != input.TaskID || current.Revision != input.ExpectedRevision) {
		return taskQueueStopReceipt{}, errTaskQueueChanged
	}

	receipt.Scope = input.Scope
	if input.Scope == "all" && queue.QueuedCount > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status = 'CANCELLED', revision = revision + 1
			WHERE project_id = ? AND status = 'QUEUED'`, projectID); err != nil {
			return taskQueueStopReceipt{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'CANCELLED', revision = revision + 1,
			queued_seq = NULL, message = '' WHERE project_id = ? AND status = 'QUEUED'`, projectID); err != nil {
			return taskQueueStopReceipt{}, err
		}
		receipt.CancelledCount = queue.QueuedCount
	}
	if current != nil {
		if current.Status == taskRunning {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'CANCELLING', revision = revision + 1
				WHERE project_id = ? AND task_id = ?`, projectID, current.TaskID); err != nil {
				return taskQueueStopReceipt{}, err
			}
			receipt.StoppingCount = 1
		}
		if current.Kind == "workflow" {
			status := taskCancelling
			if input.Scope == "current" {
				status = workflowPausing
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, revision = revision + 1
				WHERE run_id = (SELECT workflow_run_id FROM tasks WHERE project_id = ? AND task_id = ?) AND status <> ?`,
				status, projectID, current.TaskID, status); err != nil {
				return taskQueueStopReceipt{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_queue_stop_requests
		(project_id, request_id, scope, task_id, expected_revision, stopping_count, cancelled_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, input.RequestID, input.Scope, input.TaskID, input.ExpectedRevision,
		receipt.StoppingCount, receipt.CancelledCount); err != nil {
		return taskQueueStopReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return taskQueueStopReceipt{}, err
	}
	if worker := s.tasks.active[projectID]; current != nil && worker != nil && worker.taskID == current.TaskID {
		worker.stop()
	}
	return receipt, nil
}
