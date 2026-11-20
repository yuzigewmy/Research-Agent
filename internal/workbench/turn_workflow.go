package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

const turnWorkflowSchema = `
	ALTER TABLE turns ADD COLUMN workflow_proposal_json TEXT NOT NULL DEFAULT 'null';
	ALTER TABLE turn_events ADD COLUMN workflow_proposal_json TEXT NOT NULL DEFAULT 'null';
	ALTER TABLE workflow_runs ADD COLUMN range_source_turn_id TEXT REFERENCES turns(turn_id);
	PRAGMA user_version = 31;
`

const (
	workflowProposed  = "PROPOSED"
	workflowConfirmed = "CONFIRMED"
)

var errWorkflowProposalStale = errors.New("workflow changed since the proposal was made")

type workflowStepDefinition struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type turnWorkflowProposal struct {
	Action      string                   `json:"action"`
	Scope       WorkflowScope            `json:"scope"`
	Steps       []workflowStepDefinition `json:"steps"`
	Instruction string                   `json:"instruction"`
	Checkpoints bool                     `json:"checkpoints"`
	Status      string                   `json:"status"`
	RunID       string                   `json:"runId,omitempty"`
}

// The same snapshot supplies the model's allowed steps and the confirmation fence.
type workflowProposalRecord struct {
	turnWorkflowProposal
	WorkflowKey  string `json:"workflowKey"`
	BaseRunID    string `json:"baseRunId"`
	BaseRevision int    `json:"baseRevision"`
}

func (s *Store) workflowProposalOptions(ctx context.Context, project Project) (*workflowProposalRecord, error) {
	key := projectWorkflowKey(project.TemplateID)
	if key == "" {
		return nil, nil
	}
	run, err := s.latestWorkflow(ctx, project.ProjectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	start := 0
	action, checkpoints := workflowStart, true
	switch run.Status {
	case "", workflowDone, taskCancelled:
	case workflowRangeCompleted:
		key, start = run.WorkflowKey, run.step
		action, checkpoints = workflowContinue, run.Checkpoints
	default:
		return nil, nil
	}
	stages := workflowStages[key]
	steps := make([]workflowStepDefinition, 0, len(stages)-start)
	for _, stage := range stages[start:] {
		steps = append(steps, workflowStepDefinition{Key: stage.key, Title: stage.title})
	}
	return &workflowProposalRecord{
		turnWorkflowProposal: turnWorkflowProposal{Action: action, Steps: steps, Checkpoints: checkpoints, Status: workflowProposed},
		WorkflowKey:          key, BaseRunID: run.RunID, BaseRevision: run.Revision,
	}, nil
}

func (p *workflowProposalRecord) runnableSteps() []workflowStepDefinition {
	if p == nil {
		return nil
	}
	return p.Steps
}

func (p *workflowProposalRecord) selectScope(instruction, end string) error {
	if p != nil {
		for i, step := range p.Steps {
			if step.Key == end {
				p.Scope = WorkflowScope{StartStep: p.Steps[0].Key, EndStep: end}
				p.Steps, p.Instruction = p.Steps[:i+1], instruction
				return nil
			}
		}
	}
	return errInvalidTaskProposal
}

func (c conversation) changeWorkflowProposal(confirm bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, turnID := r.PathValue("id"), r.PathValue("turnId")
		proposal, queued, err := c.store.applyWorkflowProposal(r.Context(), projectID, turnID, confirm, c.model != nil)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目下没有这项工作流建议。")
		case errors.Is(err, errWorkflowProposalStale):
			writeError(w, http.StatusConflict, "WORKFLOW_PROPOSAL_STALE", "工作流已有更新。请根据当前步骤重新提出建议。")
		case errors.Is(err, errReviewSources):
			writeError(w, http.StatusUnprocessableEntity, "SOURCE_REQUIRED", "请先在项目文献库核验并保存文献。")
		case err != nil:
			writeTaskError(w, "apply workflow proposal", err)
		default:
			if queued {
				c.store.kickTaskQueue(projectID, c.model)
			}
			writeJSON(w, http.StatusOK, proposal)
		}
	}
}

func (s *Store) applyWorkflowProposal(ctx context.Context, projectID, turnID string, confirm, modelReady bool) (turnWorkflowProposal, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return turnWorkflowProposal{}, false, err
	}
	defer tx.Rollback()
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT workflow_proposal_json FROM turns WHERE project_id = ? AND turn_id = ? AND status = 'COMPLETED'`,
		projectID, turnID).Scan(&body); err != nil {
		return turnWorkflowProposal{}, false, err
	}
	var record *workflowProposalRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return turnWorkflowProposal{}, false, err
	}
	if record == nil {
		return turnWorkflowProposal{}, false, sql.ErrNoRows
	}
	proposal := record.turnWorkflowProposal
	if proposal.Status != workflowProposed {
		return proposal, false, nil
	}
	if confirm {
		latest, err := readWorkflow(ctx, tx, projectID, "")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return turnWorkflowProposal{}, false, err
		}
		if latest.RunID != record.BaseRunID || latest.Revision != record.BaseRevision {
			return turnWorkflowProposal{}, false, errWorkflowProposalStale
		}
		req := workflowRequest{RequestID: rand.Text(), ThroughStep: proposal.Scope.EndStep, sourceTurnID: turnID, workflowKey: record.WorkflowKey}
		if proposal.Action == workflowStart {
			req.Checkpoints = proposal.Checkpoints
		} else {
			req.RunID, req.ExpectedRevision = record.BaseRunID, record.BaseRevision
		}
		run, _, err := applyWorkflowInTx(ctx, tx, projectID, proposal.Action, req, modelReady)
		if err != nil {
			return turnWorkflowProposal{}, false, err
		}
		proposal.Status, proposal.RunID = workflowConfirmed, run.RunID
	} else {
		proposal.Status = taskCancelled
	}
	record.turnWorkflowProposal = proposal
	body, err = json.Marshal(record)
	if err != nil {
		return turnWorkflowProposal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET workflow_proposal_json = ? WHERE turn_id = ?`, string(body), turnID); err != nil {
		return turnWorkflowProposal{}, false, err
	}
	return proposal, confirm, tx.Commit()
}
