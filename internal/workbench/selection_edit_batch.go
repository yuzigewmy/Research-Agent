package workbench

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"strings"

	"research-agent/internal/inference"
)

//go:embed prompts/batch_selection_edit.md
var batchSelectionEditPrompt string

const batchSelectionEditSchema = `
	CREATE TABLE batch_selection_edits (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		source_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		position INTEGER NOT NULL,
		start_byte INTEGER NOT NULL,
		end_byte INTEGER NOT NULL,
		original TEXT NOT NULL,
		applied_version_id TEXT REFERENCES workspace_versions(version_id),
		PRIMARY KEY (project_id, task_id, source_version_id),
		UNIQUE (project_id, task_id, position),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	PRAGMA user_version = 40;
`

func insertBatchSelectionEdits(ctx context.Context, tx *sql.Tx, projectID, taskID string, selections []manuscriptSelection) error {
	for position, selection := range selections {
		if _, err := tx.ExecContext(ctx, `INSERT INTO batch_selection_edits
			(project_id, task_id, source_version_id, position, start_byte, end_byte, original) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, taskID, selection.VersionID, position, selection.StartByte, selection.EndByte, selection.Text); err != nil {
			return err
		}
	}
	return nil
}

func readBatchSelectionEdit(ctx context.Context, db workspaceQuery, projectID, versionID, taskID string) (selectionEdit, error) {
	edit := selectionEdit{EditID: taskID, VersionID: versionID, batch: true}
	var output string
	err := db.QueryRowContext(ctx, `SELECT t.status, t.message, e.original, e.start_byte, e.end_byte,
		COALESCE(e.applied_version_id, ''), t.markdown
		FROM batch_selection_edits e JOIN tasks t ON t.project_id = e.project_id AND t.task_id = e.task_id
		WHERE e.project_id = ? AND e.task_id = ? AND e.source_version_id = ?`, projectID, taskID, versionID).
		Scan(&edit.Status, &edit.Message, &edit.Original, &edit.startByte, &edit.endByte, &edit.AppliedVersionID, &output)
	if err != nil || edit.Status != taskSucceeded {
		return edit, err
	}
	var suggestions map[string]EditSuggestion
	if err := json.Unmarshal([]byte(output), &suggestions); err != nil {
		return selectionEdit{}, err
	}
	suggestion := suggestions[versionID]
	edit.EditSuggestion = &suggestion
	edit.Unchanged = suggestion.Replacement == edit.Original
	return edit, nil
}

type batchSelectionRange struct {
	VersionID string `json:"versionId"`
	Path      string `json:"path"`
	selectionExcerpt
	documentSize int
}

func (s *Store) batchSelectionRanges(ctx context.Context, projectID, taskID string) ([]batchSelectionRange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.ordinal, v.path, v.version_id, v.sha256, v.size, v.created_at, e.start_byte, e.end_byte
		FROM batch_selection_edits e JOIN workspace_versions v ON v.project_id = e.project_id AND v.version_id = e.source_version_id
		WHERE e.project_id = ? AND e.task_id = ? ORDER BY e.position`, projectID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ranges []batchSelectionRange
	for rows.Next() {
		var file WorkspaceFile
		var start, end int
		if err := rows.Scan(&file.ordinal, &file.Path, &file.VersionID, &file.SHA256, &file.Size, &file.CreatedAt, &start, &end); err != nil {
			return nil, err
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, batchSelectionRange{
			VersionID: file.VersionID, Path: file.Path, selectionExcerpt: excerptSelection(doc.Content, start, end), documentSize: len(doc.Content),
		})
	}
	return ranges, rows.Err()
}

func (s *Store) generateBatchSelectionEdit(ctx context.Context, model *inference.Client, projectID string, task Task) (string, error) {
	ranges, err := s.batchSelectionRanges(ctx, projectID, task.TaskID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Instruction string                `json:"instruction"`
		Edits       []batchSelectionRange `json:"edits"`
	}{task.Instruction, ranges})
	if err != nil {
		return "", err
	}
	output, err := streamTaskText(ctx, model, batchSelectionEditPrompt, string(payload), 16384, 128<<10)
	if err != nil {
		return "", err
	}
	return encodeBatchSelectionSuggestions(output, ranges)
}

func encodeBatchSelectionSuggestions(output string, ranges []batchSelectionRange) (string, error) {
	var result struct {
		Edits []struct {
			VersionID   string  `json:"versionId"`
			Replacement *string `json:"replacement"`
			Reason      string  `json:"reason"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || len(result.Edits) != len(ranges) {
		return "", errSelectionOutput
	}
	scopes := make(map[string]batchSelectionRange, len(ranges))
	for _, scope := range ranges {
		scopes[scope.VersionID] = scope
	}
	suggestions := make(map[string]EditSuggestion, len(ranges))
	for _, item := range result.Edits {
		scope, exists := scopes[item.VersionID]
		if !exists || item.Replacement == nil {
			return "", errSelectionOutput
		}
		suggestion := EditSuggestion{Replacement: *item.Replacement, Reason: strings.TrimSpace(item.Reason)}
		if !validSelectionSuggestion(suggestion, scope.documentSize, len(scope.Selected)) {
			return "", errSelectionOutput
		}
		suggestions[item.VersionID] = suggestion
		delete(scopes, item.VersionID)
	}
	encoded, err := json.Marshal(suggestions)
	return string(encoded), err
}
