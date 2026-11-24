package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const projectDraftSchema = `
	ALTER TABLE projects ADD COLUMN goal TEXT NOT NULL DEFAULT '';
	ALTER TABLE projects ADD COLUMN deliverables TEXT NOT NULL DEFAULT '';
	CREATE TABLE project_drafts (
		draft_id TEXT PRIMARY KEY NOT NULL,
		request_id TEXT NOT NULL UNIQUE,
		prompt TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		template_id TEXT NOT NULL DEFAULT 'freeform',
		goal TEXT NOT NULL DEFAULT '',
		deliverables TEXT NOT NULL DEFAULT '',
		clarification TEXT NOT NULL DEFAULT '',
		question TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT', 'PLANNING', 'CREATED')),
		revision INTEGER NOT NULL DEFAULT 1,
		updated_at TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		project_id TEXT REFERENCES projects(project_id)
	);
	CREATE INDEX project_drafts_updated ON project_drafts(updated_at DESC, draft_id DESC) WHERE status <> 'CREATED';
	CREATE TABLE project_draft_requests (
		draft_id TEXT NOT NULL REFERENCES project_drafts(draft_id),
		request_id TEXT NOT NULL,
		action TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		payload TEXT NOT NULL,
		PRIMARY KEY (draft_id, request_id)
	);
	PRAGMA user_version = 27;
`

const (
	draftOpen        = "DRAFT"
	draftPlanning    = "PLANNING"
	draftCreated     = "CREATED"
	draftSave        = "save"
	draftPlan        = "plan"
	draftFinalize    = "finalize"
	draftPromptLimit = 8 << 10
	draftFieldLimit  = 4 << 10
)

const projectDraftColumns = `draft_id, prompt, title, template_id, goal, deliverables, clarification,
	question, status, revision, updated_at, message, COALESCE(project_id, '')`

var (
	errDraftState      = errors.New("project draft is not editable")
	errDraftIncomplete = errors.New("project draft needs a title and goal")
)

type projectDraftFields struct {
	Title         string `json:"title"`
	TemplateID    string `json:"templateId"`
	Goal          string `json:"goal"`
	Deliverables  string `json:"deliverables"`
	Clarification string `json:"clarification"`
}

func (f *projectDraftFields) normalize() bool {
	f.Title = strings.TrimSpace(f.Title)
	f.Goal = strings.TrimSpace(f.Goal)
	f.Deliverables = strings.TrimSpace(f.Deliverables)
	f.Clarification = strings.TrimSpace(f.Clarification)
	if utf8.RuneCountInString(f.Title) > 200 || strings.ContainsRune(f.Title, 0) || !validProjectTemplate(f.TemplateID) {
		return false
	}
	for _, text := range []string{f.Goal, f.Deliverables, f.Clarification} {
		if len(text) > draftFieldLimit || strings.ContainsRune(text, 0) {
			return false
		}
	}
	return true
}

type projectDraft struct {
	DraftID string `json:"draftId"`
	Prompt  string `json:"prompt"`
	projectDraftFields
	Question  string `json:"question,omitempty"`
	Status    string `json:"status"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updatedAt"`
	Message   string `json:"message,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
}

type draftChangeInput struct {
	RequestID        string `json:"requestId"`
	ExpectedRevision int    `json:"expectedRevision"`
}

func scanProjectDraft(row interface{ Scan(...any) error }) (projectDraft, error) {
	var d projectDraft
	err := row.Scan(&d.DraftID, &d.Prompt, &d.Title, &d.TemplateID, &d.Goal, &d.Deliverables, &d.Clarification,
		&d.Question, &d.Status, &d.Revision, &d.UpdatedAt, &d.Message, &d.ProjectID)
	return d, err
}

func readProjectDraft(ctx context.Context, db workspaceQuery, draftID string) (projectDraft, error) {
	return scanProjectDraft(db.QueryRowContext(ctx, `SELECT `+projectDraftColumns+` FROM project_drafts WHERE draft_id = ?`, draftID))
}

func (s *Store) createProjectDraft(ctx context.Context, requestID, prompt string) (projectDraft, bool, error) {
	draft, err := scanProjectDraft(s.db.QueryRowContext(ctx, `INSERT INTO project_drafts(draft_id, request_id, prompt, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(request_id) DO NOTHING RETURNING `+projectDraftColumns,
		rand.Text(), requestID, prompt, time.Now().UTC().Format(createdAtLayout)))
	if !errors.Is(err, sql.ErrNoRows) {
		return draft, err == nil, err
	}
	draft, err = scanProjectDraft(s.db.QueryRowContext(ctx, `SELECT `+projectDraftColumns+` FROM project_drafts WHERE request_id = ?`, requestID))
	if err == nil && draft.Prompt != prompt {
		return projectDraft{}, false, errRequestConflict
	}
	return draft, false, err
}

func (s *Store) listProjectDrafts(ctx context.Context, cursor projectCursor, limit int) ([]projectDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectDraftColumns+` FROM project_drafts WHERE status <> 'CREATED'
		AND (? = '' OR (updated_at, draft_id) < (?, ?)) ORDER BY updated_at DESC, draft_id DESC LIMIT ?`,
		cursor.projectID, cursor.createdAt, cursor.projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]projectDraft, 0, limit)
	for rows.Next() {
		draft, err := scanProjectDraft(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, draft)
	}
	return items, rows.Err()
}

func prepareDraftMutation(ctx context.Context, tx *sql.Tx, id, action string, req draftChangeInput, payload string) (projectDraft, bool, error) {
	draft, err := readProjectDraft(ctx, tx, id)
	if err != nil {
		return projectDraft{}, false, err
	}
	var previousAction, previousPayload string
	var previousRevision int
	err = tx.QueryRowContext(ctx, `SELECT action, expected_revision, payload FROM project_draft_requests
		WHERE draft_id = ? AND request_id = ?`, id, req.RequestID).Scan(&previousAction, &previousRevision, &previousPayload)
	if err == nil {
		if previousAction != action || previousRevision != req.ExpectedRevision || previousPayload != payload {
			return projectDraft{}, false, errRequestConflict
		}
		return draft, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return projectDraft{}, false, err
	}
	if draft.Revision != req.ExpectedRevision {
		return projectDraft{}, false, errProjectRevision
	}
	if draft.Status != draftOpen {
		return projectDraft{}, false, errDraftState
	}
	return draft, false, nil
}

func saveDraftReceipt(ctx context.Context, tx *sql.Tx, id, action string, req draftChangeInput, payload string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_draft_requests (draft_id, request_id, action, expected_revision, payload)
		VALUES (?, ?, ?, ?, ?)`, id, req.RequestID, action, req.ExpectedRevision, payload)
	return err
}

func (s *Store) changeProjectDraft(ctx context.Context, id, action string, req draftChangeInput, fields projectDraftFields, modelReady bool) (projectDraft, bool, error) {
	payload := ""
	if action == draftSave {
		encoded, err := json.Marshal(fields)
		if err != nil {
			return projectDraft{}, false, err
		}
		payload = string(encoded)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectDraft{}, false, err
	}
	defer tx.Rollback()
	draft, replayed, err := prepareDraftMutation(ctx, tx, id, action, req, payload)
	if err != nil || replayed {
		return draft, false, err
	}
	stamp := time.Now().UTC().Format(createdAtLayout)
	switch action {
	case draftSave:
		draft.projectDraftFields = fields
	case draftPlan:
		if !modelReady {
			return projectDraft{}, false, errModelUnavailable
		}
		draft.Status = draftPlanning
	case draftFinalize:
		if draft.Title == "" || draft.Goal == "" {
			return projectDraft{}, false, errDraftIncomplete
		}
		draft.ProjectID = rand.Text()
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects
			(project_id, request_id, title, created_title, template_id, goal, deliverables, status, revision, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'READY', 1, ?)`, draft.ProjectID, rand.Text(), draft.Title, draft.Title, draft.TemplateID, draft.Goal, draft.Deliverables, stamp); err != nil {
			return projectDraft{}, false, err
		}
		if err := publishDraftMaterials(ctx, tx, draft.DraftID, draft.ProjectID); err != nil {
			return projectDraft{}, false, err
		}
		draft.Status = draftCreated
	}
	updated, err := scanProjectDraft(tx.QueryRowContext(ctx, `UPDATE project_drafts SET title = ?, template_id = ?, goal = ?, deliverables = ?,
		clarification = ?, status = ?, revision = revision + 1, updated_at = ?, message = '', project_id = NULLIF(?, '')
		WHERE draft_id = ? RETURNING `+projectDraftColumns, draft.Title, draft.TemplateID, draft.Goal, draft.Deliverables,
		draft.Clarification, draft.Status, stamp, draft.ProjectID, id))
	if err != nil {
		return projectDraft{}, false, err
	}
	if err := saveDraftReceipt(ctx, tx, id, action, req, payload); err != nil {
		return projectDraft{}, false, err
	}
	return updated, true, tx.Commit()
}

func (s *Store) finishDraftPlan(ctx context.Context, draft projectDraft, message string) (projectDraft, error) {
	return scanProjectDraft(s.db.QueryRowContext(ctx, `UPDATE project_drafts SET title = ?, template_id = ?, goal = ?, deliverables = ?,
		question = ?, status = 'DRAFT', revision = revision + 1, updated_at = ?, message = ?
		WHERE draft_id = ? AND status = 'PLANNING' AND revision = ? RETURNING `+projectDraftColumns,
		draft.Title, draft.TemplateID, draft.Goal, draft.Deliverables, draft.Question,
		time.Now().UTC().Format(createdAtLayout), message, draft.DraftID, draft.Revision))
}

func recoverDraftPlans(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE project_drafts SET status = 'DRAFT', revision = revision + 1, updated_at = ?,
		message = '服务重启中断了整理。可以继续编辑草稿或重新整理。' WHERE status = 'PLANNING'`, time.Now().UTC().Format(createdAtLayout))
	return err
}
