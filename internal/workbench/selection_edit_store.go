package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

const selectionEditSchema = `
	ALTER TABLE tasks ADD COLUMN selection_edit INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE selection_edits (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		request_id TEXT NOT NULL,
		source_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		start_byte INTEGER NOT NULL,
		end_byte INTEGER NOT NULL,
		original TEXT NOT NULL,
		applied_version_id TEXT REFERENCES workspace_versions(version_id),
		PRIMARY KEY (project_id, task_id),
		UNIQUE (project_id, request_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	PRAGMA user_version = 15;
`

const maxSelectionBytes = 8 << 10
const maxEditInstructionBytes = 2 << 10

var (
	errSelectionRange = errors.New("invalid saved selection")
	errSelectionState = errors.New("selection edit is not ready to apply")
)

type selectionEditInput struct {
	RequestID   string `json:"requestId"`
	StartByte   int    `json:"startByte"`
	EndByte     int    `json:"endByte"`
	Instruction string `json:"instruction"`
}

type editSuggestion struct {
	Replacement string `json:"replacement"`
	Reason      string `json:"reason"`
}

type selectionEdit struct {
	EditID           string `json:"editId"`
	Status           string `json:"status"`
	Original         string `json:"original"`
	Message          string `json:"message,omitempty"`
	AppliedVersionID string `json:"appliedVersionId,omitempty"`
	*editSuggestion
	versionID   string
	instruction string
	startByte   int
	endByte     int
}

const selectEdit = `SELECT t.task_id, t.status, e.original, t.message, COALESCE(e.applied_version_id, ''),
	e.source_version_id, t.instruction, e.start_byte, e.end_byte, t.markdown
	FROM selection_edits e JOIN tasks t ON t.project_id = e.project_id AND t.task_id = e.task_id
	WHERE e.project_id = ?`

func scanSelectionEdit(row *sql.Row) (selectionEdit, error) {
	var edit selectionEdit
	var output string
	if err := row.Scan(&edit.EditID, &edit.Status, &edit.Original, &edit.Message, &edit.AppliedVersionID,
		&edit.versionID, &edit.instruction, &edit.startByte, &edit.endByte, &output); err != nil {
		return selectionEdit{}, err
	}
	if edit.Status == taskSucceeded {
		edit.editSuggestion = new(editSuggestion)
		if err := json.Unmarshal([]byte(output), edit.editSuggestion); err != nil {
			return selectionEdit{}, err
		}
	}
	return edit, nil
}

func readSelectionEdit(ctx context.Context, db workspaceQuery, projectID, versionID, editID string) (selectionEdit, error) {
	return scanSelectionEdit(db.QueryRowContext(ctx, selectEdit+` AND e.source_version_id = ? AND e.task_id = ?`, projectID, versionID, editID))
}

func (s *Store) queueSelectionEdit(ctx context.Context, projectID string, doc WorkspaceDocument, req selectionEditInput, modelReady bool) (selectionEdit, bool, error) {
	start, end := req.StartByte, req.EndByte
	if start < 0 || end <= start || end > len(doc.Content) || end-start > maxSelectionBytes ||
		!utf8.RuneStart(doc.Content[start]) || (end < len(doc.Content) && !utf8.RuneStart(doc.Content[end])) {
		return selectionEdit{}, false, errSelectionRange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return selectionEdit{}, false, err
	}
	defer tx.Rollback()

	edit, err := scanSelectionEdit(tx.QueryRowContext(ctx, selectEdit+` AND e.request_id = ?`, projectID, req.RequestID))
	if err == nil {
		if edit.versionID != doc.VersionID || edit.startByte != start || edit.endByte != end || edit.instruction != req.Instruction {
			return selectionEdit{}, false, errRequestConflict
		}
		return edit, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return selectionEdit{}, false, err
	}
	if !modelReady {
		return selectionEdit{}, false, errModelUnavailable
	}
	id := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, selection_edit)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, 1)`, projectID, id, req.Instruction,
		time.Now().UTC().Format(createdAtLayout), projectID); err != nil {
		return selectionEdit{}, false, err
	}
	original := doc.Content[start:end]
	if _, err := tx.ExecContext(ctx, `INSERT INTO selection_edits
		(project_id, task_id, request_id, source_version_id, start_byte, end_byte, original) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, id, req.RequestID, doc.VersionID, start, end, original); err != nil {
		return selectionEdit{}, false, err
	}
	return selectionEdit{EditID: id, Status: taskQueued, Original: original}, true, tx.Commit()
}

func (s *Store) applySelectionEdit(ctx context.Context, projectID, versionID, editID string) (WorkspaceDocument, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	defer tx.Rollback()

	edit, err := readSelectionEdit(ctx, tx, projectID, versionID, editID)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	if edit.AppliedVersionID != "" {
		file, err := lookupWorkspaceVersion(ctx, tx, projectID, edit.AppliedVersionID)
		if err != nil {
			return WorkspaceDocument{}, err
		}
		return s.readWorkspaceDocument(file)
	}
	if edit.Status != taskSucceeded {
		return WorkspaceDocument{}, errSelectionState
	}
	base, err := lookupWorkspaceVersion(ctx, tx, projectID, versionID)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	current, err := currentWorkspaceFile(ctx, tx, projectID, base.Path)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	if base.VersionID != current.VersionID {
		return WorkspaceDocument{}, errWorkspaceConflict
	}
	doc, err := s.readWorkspaceDocument(base)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	content := doc.Content[:edit.startByte] + edit.Replacement + doc.Content[edit.endByte:]
	file, err := s.insertWorkspaceVersion(ctx, tx, projectID, workspaceTextFile(base.Path, content), &content)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE selection_edits SET applied_version_id = ? WHERE project_id = ? AND task_id = ?`,
		file.VersionID, projectID, editID); err != nil {
		return WorkspaceDocument{}, err
	}
	return WorkspaceDocument{WorkspaceFile: file, Content: content}, tx.Commit()
}
