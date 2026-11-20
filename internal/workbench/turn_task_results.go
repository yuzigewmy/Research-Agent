package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
)

const turnTaskResultsSchema = `
	ALTER TABLE turns ADD COLUMN task_results_json TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE tasks ADD COLUMN result_synced_turn_id TEXT REFERENCES turns(turn_id);
	CREATE INDEX tasks_pending_turn_sync ON tasks(project_id, ordinal)
		WHERE source_turn_id IS NOT NULL AND status = 'SUCCEEDED' AND result_synced_turn_id IS NULL;
	PRAGMA user_version = 26;
`

const maxTurnTaskResults = 4
const taskResultExcerptRunes = 1200

type turnTaskResult struct {
	TaskID       string `json:"taskId"`
	SourceTurnID string `json:"sourceTurnId"`
	Instruction  string `json:"instruction"`
}

func pendingTurnTaskResults(ctx context.Context, tx *sql.Tx, projectID string) ([]turnTaskResult, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id, source_turn_id, instruction FROM tasks
		WHERE project_id = ? AND source_turn_id IS NOT NULL AND status = 'SUCCEEDED' AND result_synced_turn_id IS NULL
		ORDER BY ordinal LIMIT ?`, projectID, maxTurnTaskResults)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]turnTaskResult, 0, maxTurnTaskResults)
	for rows.Next() {
		var result turnTaskResult
		if err := rows.Scan(&result.TaskID, &result.SourceTurnID, &result.Instruction); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func syncTurnTaskResults(ctx context.Context, tx *sql.Tx, turnID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET result_synced_turn_id = turns.turn_id FROM turns
		WHERE turns.turn_id = ? AND tasks.project_id = turns.project_id AND tasks.result_synced_turn_id IS NULL
		AND tasks.task_id IN (SELECT json_extract(value, '$.taskId') FROM json_each(turns.task_results_json))`, turnID)
	return err
}

func (s *Store) turnTaskResultsContext(ctx context.Context, projectID string, results []turnTaskResult) (string, error) {
	type resultExcerpt struct {
		turnTaskResult
		Excerpt string `json:"excerpt"`
	}
	excerpts := make([]resultExcerpt, 0, len(results))
	for _, result := range results {
		markdown, err := s.taskMarkdown(ctx, projectID, result.TaskID)
		if err != nil {
			return "", err
		}
		excerpts = append(excerpts, resultExcerpt{turnTaskResult: result, Excerpt: excerptRunes(markdown, taskResultExcerptRunes)})
	}
	payload, err := json.Marshal(excerpts)
	return "以下 JSON 是项目任务的已保存成果节选。\n" + string(payload), err
}
