package workbench

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
)

const notificationInstruction = `CASE WHEN length(instruction) > 160 THEN substr(instruction, 1, 160) || '…' ELSE instruction END`

const notificationsSchema = `
	CREATE TABLE task_notifications (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		status TEXT NOT NULL,
		instruction TEXT NOT NULL,
		created_at TEXT NOT NULL,
		read INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (project_id, task_id) REFERENCES tasks (project_id, task_id) ON DELETE CASCADE
	);
	CREATE INDEX task_notifications_unread ON task_notifications (seq) WHERE read = 0;
	INSERT INTO task_notifications (project_id, task_id, status, instruction, created_at)
	SELECT project_id, task_id, status, ` + notificationInstruction + `, created_at
	FROM tasks WHERE status = 'PROPOSED' AND ` + taskCardFilter + ` ORDER BY ordinal;
	CREATE TRIGGER notify_task_status AFTER UPDATE OF status ON tasks
	WHEN OLD.status <> NEW.status AND NEW.status IN ('PROPOSED', 'FAILED', 'INTERRUPTED', 'SUCCEEDED')
	BEGIN
		INSERT INTO task_notifications (project_id, task_id, status, instruction, created_at)
		SELECT project_id, task_id, status, ` + notificationInstruction + `, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		FROM tasks WHERE ordinal = NEW.ordinal AND ` + taskCardFilter + `;
	END;
	PRAGMA user_version = 49;
`

const workflowNotificationsSchema = `
	CREATE TRIGGER notify_workflow_status AFTER UPDATE OF status ON workflow_runs
	WHEN OLD.status <> NEW.status AND NEW.status IN (
		'WAITING_CHECKPOINT', 'WAITING_TOPIC_CHOICE', 'RANGE_COMPLETED', 'COMPLETED', 'FAILED', 'INTERRUPTED'
	)
	BEGIN
		INSERT INTO task_notifications (project_id, task_id, status, instruction, created_at)
		SELECT NEW.project_id, task_id,
			CASE NEW.status
				WHEN 'WAITING_CHECKPOINT' THEN 'PROPOSED'
				WHEN 'WAITING_TOPIC_CHOICE' THEN 'PROPOSED'
				WHEN 'RANGE_COMPLETED' THEN 'SUCCEEDED'
				WHEN 'COMPLETED' THEN 'SUCCEEDED'
				ELSE NEW.status
			END,
			CASE NEW.status
				WHEN 'WAITING_CHECKPOINT' THEN '工作流到达检查点，请核对已完成的步骤。'
				WHEN 'WAITING_TOPIC_CHOICE' THEN '研究选题等待确认，请查看入围题目。'
				WHEN 'RANGE_COMPLETED' THEN '本次工作流范围已完成。'
				WHEN 'COMPLETED' THEN '本轮工作流已完成。'
				WHEN 'FAILED' THEN '工作流步骤失败，请查看原因。'
				WHEN 'INTERRUPTED' THEN '本轮工作流已中断，请查看运行记录。'
			END,
			strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		FROM tasks WHERE project_id = NEW.project_id AND workflow_run_id = NEW.run_id
		ORDER BY ordinal DESC LIMIT 1;
	END;
	PRAGMA user_version = 56;
`

// A proposal inserted and automatically queued in one transaction never needs approval.
func insertProposalNotification(ctx context.Context, tx *sql.Tx, projectID, taskID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_notifications (project_id, task_id, status, instruction, created_at)
		SELECT project_id, task_id, status, `+notificationInstruction+`, created_at
		FROM tasks WHERE project_id = ? AND task_id = ?`, projectID, taskID)
	return err
}

const notificationsPath = "/api/v1/notifications"

type taskNotification struct {
	ID            string `json:"notificationId"`
	ProjectID     string `json:"projectId"`
	ProjectName   string `json:"projectName"`
	TaskID        string `json:"taskId"`
	WorkflowRunID string `json:"workflowRunId,omitempty"`
	Instruction   string `json:"instruction"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	Read          bool   `json:"read"`
}

type notificationPage struct {
	Items       []taskNotification `json:"items"`
	UnreadCount int                `json:"unreadCount"`
	NextCursor  string             `json:"nextCursor,omitempty"`
}

func (s *Store) registerNotifications(mux *http.ServeMux) {
	mux.HandleFunc("GET "+notificationsPath, s.handleNotifications)
	mux.HandleFunc(notificationsPath, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("PATCH "+notificationsPath+"/{notificationId}", s.handleReadNotification)
	mux.HandleFunc(notificationsPath+"/{notificationId}", methodNotAllowed("PATCH"))
}

func (s *Store) handleNotifications(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "查询参数无效。")
		return
	}
	before, err := eventCursor(query.Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "通知游标无效。")
		return
	}
	if before == 0 {
		before = math.MaxInt64
	}
	limit := 30
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "limit 必须是 1 到 100 之间的整数。")
			return
		}
	}
	page, err := s.listNotifications(r.Context(), before, limit)
	if err != nil {
		writeStoreError(w, "list task notifications", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Store) listNotifications(ctx context.Context, before int64, limit int) (notificationPage, error) {
	page := notificationPage{Items: make([]taskNotification, 0, limit+1)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_notifications WHERE read = 0`).Scan(&page.UnreadCount); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.seq, n.project_id, COALESCE(NULLIF(p.display_name, ''), p.title),
		n.task_id, COALESCE(t.workflow_run_id, ''), n.instruction, n.status, n.created_at, n.read
		FROM task_notifications n JOIN projects p ON p.project_id = n.project_id
		JOIN tasks t ON t.project_id = n.project_id AND t.task_id = n.task_id
		WHERE n.seq < ? ORDER BY n.seq DESC LIMIT ?`, before, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item taskNotification
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.TaskID, &item.WorkflowRunID, &item.Instruction,
			&item.Status, &item.CreatedAt, &item.Read); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, rows.Err()
}

func (s *Store) handleReadNotification(w http.ResponseWriter, r *http.Request) {
	id, err := eventCursor(r.PathValue("notificationId"))
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "通知编号无效。")
		return
	}
	var input struct {
		Read bool `json:"read"`
	}
	if !readJSON(w, r, &input, 1024) {
		return
	}
	if !input.Read {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择标为已读。")
		return
	}
	err = s.db.QueryRowContext(r.Context(), `UPDATE task_notifications SET read = 1 WHERE seq = ? RETURNING seq`, id).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该通知不存在。")
	case err != nil:
		writeStoreError(w, "mark task notification read", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
