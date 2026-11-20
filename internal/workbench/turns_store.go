package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const turnColumns = `ordinal, turn_id, request_id, user_text, answer, status, message, event_seq, created_at,
	references_json, task_results_json, mode, proposed_task_id, workflow_proposal_json`

var (
	errTurnBusy         = errors.New("project already has a running turn")
	errModelUnavailable = errors.New("conversation model is not configured")
)

type Turn struct {
	ordinal          int64
	TurnID           string                `json:"turnId"`
	RequestID        string                `json:"requestId"`
	Text             string                `json:"text"`
	Answer           string                `json:"answer"`
	Status           string                `json:"status"`
	Message          string                `json:"message,omitempty"`
	AfterSeq         int64                 `json:"afterSeq"`
	CreatedAt        string                `json:"createdAt"`
	References       []TurnReference       `json:"references,omitempty"`
	TaskResults      []turnTaskResult      `json:"taskResults,omitempty"`
	Mode             string                `json:"mode"`
	ProposedTaskID   string                `json:"proposedTaskId,omitempty"`
	WorkflowProposal *turnWorkflowProposal `json:"workflowProposal,omitempty"`
}

type TurnEvent struct {
	Seq              int64                 `json:"seq"`
	Kind             string                `json:"kind"`
	Text             string                `json:"text,omitempty"`
	Status           string                `json:"status,omitempty"`
	Message          string                `json:"message,omitempty"`
	ProposedTaskID   string                `json:"proposedTaskId,omitempty"`
	WorkflowProposal *turnWorkflowProposal `json:"workflowProposal,omitempty"`
	proposal         turnProposal
}

const turnsSchema = `
	CREATE TABLE turns (
		ordinal INTEGER PRIMARY KEY,
		turn_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		user_text TEXT NOT NULL,
		answer TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		event_seq INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		UNIQUE(project_id, request_id)
	);
	CREATE INDEX turns_by_project ON turns(project_id, ordinal DESC);
	CREATE TABLE turn_events (
		seq INTEGER PRIMARY KEY,
		turn_id TEXT NOT NULL REFERENCES turns(turn_id),
		kind TEXT NOT NULL,
		text TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX turn_events_by_turn ON turn_events(turn_id, seq);
	PRAGMA user_version = 2;
`

// A stopped process loses its connection to the provider. Preserve the partial
// answer and its receipt so a restart does not dispatch a second paid request.
func recoverInterruptedTurns(tx *sql.Tx) error {
	const message = "服务重启打断了这轮回复，可以重新发送。"
	if _, err := tx.Exec(`INSERT INTO turn_events(turn_id, kind, status, message)
		SELECT turn_id, 'done', 'INTERRUPTED', ? FROM turns WHERE status = 'RUNNING'`, message); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE turns SET status = 'INTERRUPTED', message = ?,
		event_seq = (SELECT MAX(seq) FROM turn_events WHERE turn_id = turns.turn_id)
		WHERE status = 'RUNNING'`, message)
	return err
}

func scanTurn(row interface{ Scan(...any) error }) (Turn, error) {
	var t Turn
	var references, taskResults, workflowProposal []byte
	if err := row.Scan(&t.ordinal, &t.TurnID, &t.RequestID, &t.Text, &t.Answer, &t.Status, &t.Message,
		&t.AfterSeq, &t.CreatedAt, &references, &taskResults, &t.Mode, &t.ProposedTaskID, &workflowProposal); err != nil {
		return Turn{}, err
	}
	if err := json.Unmarshal(references, &t.References); err != nil {
		return Turn{}, err
	}
	if err := json.Unmarshal(taskResults, &t.TaskResults); err != nil {
		return Turn{}, err
	}
	if err := json.Unmarshal(workflowProposal, &t.WorkflowProposal); err != nil {
		return Turn{}, err
	}
	return t, nil
}

func (s *Store) beginTurn(ctx context.Context, projectID string, input sendTurnInput, modelReady bool) (Turn, Project, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	defer tx.Rollback()

	turn, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE project_id = ? AND request_id = ?`, projectID, input.RequestID))
	if err == nil {
		// An omitted mode reuses the original choice on replay, even after settings change.
		if turn.Text != input.Text || (input.Mode != "" && turn.Mode != input.Mode) || !slices.EqualFunc(turn.References, input.ReferenceIDs, func(ref TurnReference, id string) bool {
			return ref.MaterialID == id
		}) {
			return Turn{}, Project{}, false, errRequestConflict
		}
		return turn, Project{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Turn{}, Project{}, false, err
	}
	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID))
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	if !modelReady {
		return Turn{}, Project{}, false, errModelUnavailable
	}
	var running bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE project_id = ? AND status = 'RUNNING')`, projectID).Scan(&running); err != nil {
		return Turn{}, Project{}, false, err
	}
	if running {
		return Turn{}, Project{}, false, errTurnBusy
	}
	references, err := resolveTurnReferences(ctx, tx, projectID, input.ReferenceIDs)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	referenceJSON, err := json.Marshal(references)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	results, err := pendingTurnTaskResults(ctx, tx, projectID)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	mode := input.Mode
	if mode == "" {
		mode = project.DefaultTurnMode
	}
	turn = Turn{TurnID: rand.Text(), RequestID: input.RequestID, Text: input.Text, References: references, TaskResults: results,
		Mode: mode, Status: "RUNNING", CreatedAt: time.Now().UTC().Format(createdAtLayout)}
	err = tx.QueryRowContext(ctx, `INSERT INTO turns(turn_id, project_id, request_id, user_text, status, created_at, references_json, task_results_json, mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING ordinal`, turn.TurnID, projectID, input.RequestID, turn.Text,
		turn.Status, turn.CreatedAt, string(referenceJSON), string(resultsJSON), turn.Mode).Scan(&turn.ordinal)
	if err != nil {
		return Turn{}, Project{}, false, err
	}
	return turn, project, true, tx.Commit()
}

func (s *Store) getTurn(ctx context.Context, projectID, turnID string) (Turn, error) {
	return readTurn(ctx, s.db, projectID, turnID)
}

func readTurn(ctx context.Context, db workspaceQuery, projectID, turnID string) (Turn, error) {
	return scanTurn(db.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE project_id = ? AND turn_id = ?`, projectID, turnID))
}

func (s *Store) listTurns(ctx context.Context, projectID string, before int64, limit int) ([]Turn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns
		WHERE project_id = ? AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, projectID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	turns := make([]Turn, 0, limit)
	for rows.Next() {
		turn, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

func (s *Store) appendTurnEvent(ctx context.Context, turnID string, e TurnEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := recordTurnEvent(ctx, tx, turnID, e); err != nil {
		return err
	}
	return tx.Commit()
}

func recordTurnEvent(ctx context.Context, tx *sql.Tx, turnID string, e TurnEvent) (int64, error) {
	completed := e.Kind == "done" && e.Status == "COMPLETED"
	workflowJSON := "null"
	if completed {
		switch {
		case e.proposal.workflow != nil:
			body, err := json.Marshal(e.proposal.workflow)
			if err != nil {
				return 0, err
			}
			workflowJSON = string(body)
		case e.proposal.instruction != "":
			taskID, err := insertTurnProposal(ctx, tx, turnID, e.proposal.instruction)
			if err != nil {
				return 0, err
			}
			e.ProposedTaskID = taskID
		}
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO turn_events(turn_id, kind, text, status, message, proposed_task_id, workflow_proposal_json)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING seq`, turnID, e.Kind, e.Text, e.Status, e.Message, e.ProposedTaskID, workflowJSON).Scan(&seq); err != nil {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `UPDATE turns SET answer = answer || ?,
		status = COALESCE(NULLIF(?, ''), status), message = ?, event_seq = ?, proposed_task_id = ?, workflow_proposal_json = ?
		WHERE turn_id = ? AND status = 'RUNNING' RETURNING event_seq`, e.Text, e.Status, e.Message, seq, e.ProposedTaskID,
		workflowJSON, turnID).Scan(&seq); err != nil {
		return 0, err
	}
	if completed {
		if err := syncTurnTaskResults(ctx, tx, turnID); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

const streamBatch = 128

func (s *Store) readTurnEvents(ctx context.Context, turnID string, after int64) ([]TurnEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, kind, text, status, message, proposed_task_id, workflow_proposal_json FROM turn_events
		WHERE turn_id = ? AND seq > ? ORDER BY seq LIMIT ?`, turnID, after, streamBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TurnEvent
	for rows.Next() {
		var e TurnEvent
		var proposal []byte
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Text, &e.Status, &e.Message, &e.ProposedTaskID, &proposal); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(proposal, &e.WorkflowProposal); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
