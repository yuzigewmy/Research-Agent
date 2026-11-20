package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const taskTurnSchema = `
	ALTER TABLE tasks ADD COLUMN source_turn_id TEXT REFERENCES turns(turn_id);
	CREATE INDEX tasks_by_source_turn ON tasks(project_id, source_turn_id, ordinal DESC) WHERE source_turn_id IS NOT NULL;
	PRAGMA user_version = 25;
`

var errInvalidTaskSource = errors.New("task source must be a completed turn in the same project")

func checkTaskSource(ctx context.Context, tx *sql.Tx, projectID, turnID string) error {
	if turnID == "" {
		return nil
	}
	if !validRequestID(turnID) {
		return errInvalidTaskSource
	}
	var completed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns
		WHERE project_id = ? AND turn_id = ? AND status = 'COMPLETED')`, projectID, turnID).Scan(&completed); err != nil {
		return err
	}
	if !completed {
		return errInvalidTaskSource
	}
	return nil
}

func (s *Store) taskTurnContext(ctx context.Context, projectID, turnID string) (string, error) {
	turn, err := s.getTurn(ctx, projectID, turnID)
	if err != nil {
		return "", err
	}
	materials, err := s.turnMaterialExcerpts(turn.References)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		TurnID        string                `json:"turnId"`
		Request       string                `json:"request"`
		AnswerExcerpt string                `json:"answerExcerpt"`
		Materials     []turnMaterialExcerpt `json:"materials,omitempty"`
	}{turn.TurnID, turn.Text, excerptRunes(turn.Answer, 2000), materials})
	return "\n\n来源对话及当时引用的材料节选（JSON）\n" + string(payload), err
}
