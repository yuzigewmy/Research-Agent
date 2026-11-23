package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
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
	errSelectionRange     = errors.New("invalid saved selection")
	errSelectionState     = errors.New("selection edit is not ready to apply")
	errSelectionUnchanged = errors.New("selection edit does not change the manuscript")
)

type selectionEditInput struct {
	RequestID   string `json:"requestId"`
	StartByte   int    `json:"startByte"`
	EndByte     int    `json:"endByte"`
	Instruction string `json:"instruction"`
}

type EditSuggestion struct {
	Replacement string `json:"replacement"`
	Reason      string `json:"reason"`
}

type selectionEdit struct {
	EditID           string `json:"editId"`
	VersionID        string `json:"versionId"`
	Status           string `json:"status"`
	Original         string `json:"original"`
	Message          string `json:"message,omitempty"`
	AppliedVersionID string `json:"appliedVersionId,omitempty"`
	Unchanged        bool   `json:"unchanged,omitempty"`
	*EditSuggestion
	batch       bool
	finding     reviewFindingRef
	instruction string
	startByte   int
	endByte     int
}

const selectEdit = `SELECT t.task_id, t.status, e.original, t.message, COALESCE(e.applied_version_id, ''),
	e.source_version_id, t.instruction, e.start_byte, e.end_byte, t.markdown,
	COALESCE(e.review_id, ''), COALESCE(e.finding_number, 0)
	FROM selection_edits e JOIN tasks t ON t.project_id = e.project_id AND t.task_id = e.task_id
	WHERE e.project_id = ?`

func scanSelectionEdit(row interface{ Scan(...any) error }) (selectionEdit, error) {
	var edit selectionEdit
	var output string
	if err := row.Scan(&edit.EditID, &edit.Status, &edit.Original, &edit.Message, &edit.AppliedVersionID,
		&edit.VersionID, &edit.instruction, &edit.startByte, &edit.endByte, &output, &edit.finding.reviewID, &edit.finding.number); err != nil {
		return selectionEdit{}, err
	}
	if edit.Status == taskSucceeded {
		edit.EditSuggestion = new(EditSuggestion)
		if err := json.Unmarshal([]byte(output), edit.EditSuggestion); err != nil {
			return selectionEdit{}, err
		}
	}
	return edit, nil
}

func readSelectionEdit(ctx context.Context, db workspaceQuery, projectID, versionID, editID string) (selectionEdit, error) {
	edit, err := scanSelectionEdit(db.QueryRowContext(ctx, selectEdit+` AND e.source_version_id = ? AND e.task_id = ?`, projectID, versionID, editID))
	if errors.Is(err, sql.ErrNoRows) {
		return readBatchSelectionEdit(ctx, db, projectID, versionID, editID)
	}
	return edit, err
}

func (s *Store) queueSelectionEdit(ctx context.Context, projectID string, doc WorkspaceDocument, req selectionEditInput, modelReady bool) (selectionEdit, bool, error) {
	return s.queueSelectionEditWithFinding(ctx, projectID, doc, req, reviewFindingRef{}, modelReady)
}

func (s *Store) queueSelectionEditWithFinding(ctx context.Context, projectID string, doc WorkspaceDocument, req selectionEditInput, finding reviewFindingRef, modelReady bool) (selectionEdit, bool, error) {
	start, end := req.StartByte, req.EndByte
	if !validSelectionRange(doc.Content, start, end) {
		return selectionEdit{}, false, errSelectionRange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return selectionEdit{}, false, err
	}
	defer tx.Rollback()

	edit, err := scanSelectionEdit(tx.QueryRowContext(ctx, selectEdit+` AND e.request_id = ?`, projectID, req.RequestID))
	if err == nil {
		if edit.VersionID != doc.VersionID || edit.startByte != start || edit.endByte != end || edit.instruction != req.Instruction || edit.finding != finding {
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
	if finding.reviewID != "" {
		current, err := currentWorkspaceFile(ctx, tx, projectID, doc.Path)
		if err != nil {
			return selectionEdit{}, false, err
		}
		if current.VersionID != doc.VersionID {
			return selectionEdit{}, false, errWorkspaceConflict
		}
	}
	id := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, selection_edit)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, 1)`, projectID, id, req.Instruction,
		time.Now().UTC().Format(createdAtLayout), projectID); err != nil {
		return selectionEdit{}, false, err
	}
	original := doc.Content[start:end]
	if err := insertSelectionEdit(ctx, tx, projectID, id, req.RequestID,
		manuscriptSelectionInput{VersionID: doc.VersionID, StartByte: start, EndByte: end}, original, finding); err != nil {
		return selectionEdit{}, false, err
	}
	return selectionEdit{EditID: id, VersionID: doc.VersionID, Status: taskQueued, Original: original}, true, tx.Commit()
}

func insertSelectionEdit(ctx context.Context, tx *sql.Tx, projectID, taskID, requestID string, source manuscriptSelectionInput, original string, finding reviewFindingRef) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO selection_edits
		(project_id, task_id, request_id, source_version_id, start_byte, end_byte, original, review_id, finding_number)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, 0))`,
		projectID, taskID, requestID, source.VersionID, source.StartByte, source.EndByte, original, finding.reviewID, finding.number)
	return err
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
	if edit.Unchanged {
		return WorkspaceDocument{}, errSelectionUnchanged
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
	receiptSQL := `UPDATE selection_edits SET applied_version_id = ? WHERE project_id = ? AND task_id = ? AND source_version_id = ?`
	if edit.batch {
		receiptSQL = `UPDATE batch_selection_edits SET applied_version_id = ? WHERE project_id = ? AND task_id = ? AND source_version_id = ?`
	}
	if _, err := tx.ExecContext(ctx, receiptSQL, file.VersionID, projectID, editID, versionID); err != nil {
		return WorkspaceDocument{}, err
	}
	if edit.finding.reviewID != "" {
		if _, err := insertFindingRecheck(ctx, tx, projectID, edit, file); err != nil {
			return WorkspaceDocument{}, err
		}
	}
	return WorkspaceDocument{WorkspaceFile: file, Content: content}, tx.Commit()
}
