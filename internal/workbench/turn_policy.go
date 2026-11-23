package workbench

import (
	"context"
	"database/sql"
)

const (
	turnModeChat         = "CHAT"
	turnModeConfirm      = "CONFIRM"
	turnModeConfirmSteps = "CONFIRM_STEPS"
)

// Replace the constrained columns without rebuilding the tables referenced by tasks and receipts.
const turnPolicySchema = `
	ALTER TABLE projects RENAME COLUMN default_turn_mode TO previous_turn_mode;
	ALTER TABLE projects ADD COLUMN default_turn_mode TEXT NOT NULL DEFAULT 'CHAT'
		CHECK (default_turn_mode IN ('CHAT', 'CONFIRM', 'CONFIRM_STEPS'));
	UPDATE projects SET default_turn_mode = previous_turn_mode WHERE previous_turn_mode <> 'CHAT';
	ALTER TABLE projects DROP COLUMN previous_turn_mode;

	ALTER TABLE project_patch_requests RENAME COLUMN default_turn_mode TO previous_turn_mode;
	ALTER TABLE project_patch_requests ADD COLUMN default_turn_mode TEXT
		CHECK (default_turn_mode IN ('CHAT', 'CONFIRM', 'CONFIRM_STEPS'));
	UPDATE project_patch_requests SET default_turn_mode = previous_turn_mode WHERE previous_turn_mode IS NOT NULL;
	ALTER TABLE project_patch_requests DROP COLUMN previous_turn_mode;

	ALTER TABLE turns RENAME COLUMN mode TO previous_turn_mode;
	ALTER TABLE turns ADD COLUMN mode TEXT NOT NULL DEFAULT 'CHAT'
		CHECK (mode IN ('CHAT', 'CONFIRM', 'CONFIRM_STEPS'));
	UPDATE turns SET mode = previous_turn_mode WHERE previous_turn_mode <> 'CHAT';
	ALTER TABLE turns DROP COLUMN previous_turn_mode;
	PRAGMA user_version = 36;
`

func validTurnMode(mode string) bool {
	switch mode {
	case turnModeChat, turnModeConfirm, turnModeConfirmSteps:
		return true
	default:
		return false
	}
}

func queueTurnTask(ctx context.Context, tx *sql.Tx, projectID, taskID string) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'QUEUED', queued_seq = `+nextTaskQueuePosition+`
		WHERE project_id = ? AND task_id = ? AND NOT `+changedTaskInputs, projectID, projectID, taskID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count > 0 {
		return count > 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET message = CASE WHEN selection_edit = 1 THEN ? ELSE ? END
		WHERE project_id = ? AND task_id = ?`, selectionTaskChangedMessage, taskInputsChangedMessage, projectID, taskID)
	return false, err
}
