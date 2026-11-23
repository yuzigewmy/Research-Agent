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

var (
	errWorkflowProposalStale = errors.New("workflow changed since the proposal was made")
	errWorkflowCheckpoint    = errors.New("the preceding template checkpoint needs confirmation")
)

type workflowStepDefinition struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type turnWorkflowProposal struct {
	Action          string                   `json:"action"`
	Scope           WorkflowScope            `json:"scope"`
	Steps           []workflowStepDefinition `json:"steps"`
	Instruction     string                   `json:"instruction"`
	Checkpoints     bool                     `json:"checkpoints"`
	ConfirmEachStep bool                     `json:"confirmEachStep,omitempty"`
	Automatic       bool                     `json:"automatic,omitempty"`
	Message         string                   `json:"message,omitempty"`
	Status          string                   `json:"status"`
	RunID           string                   `json:"runId,omitempty"`
}

// The same snapshot supplies the model's allowed steps and the confirmation fence.
type workflowProposalRecord struct {
	turnWorkflowProposal
	WorkflowKey  string `json:"workflowKey"`
	BaseRunID    string `json:"baseRunId"`
	BaseRevision int    `json:"baseRevision"`
}

func (s *Store) workflowProposalOptions(ctx context.Context, project Project, mode string) (*workflowProposalRecord, error) {
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
		action = workflowContinue
		checkpoints = workflowCheckpoints(run.templateCheckpoints, mode)
	default:
		return nil, nil
	}
	stages := workflowStages[key]
	steps := make([]workflowStepDefinition, 0, len(stages)-start)
	for _, stage := range stages[start:] {
		steps = append(steps, workflowStepDefinition{Key: stage.key, Title: stage.title})
	}
	return &workflowProposalRecord{
		turnWorkflowProposal: turnWorkflowProposal{Action: action, Steps: steps, Checkpoints: checkpoints,
			ConfirmEachStep: mode == turnModeConfirmAll, Status: workflowProposed},
		WorkflowKey: key, BaseRunID: run.RunID, BaseRevision: run.Revision,
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
	if record.Status != workflowProposed {
		return record.turnWorkflowProposal, false, nil
	}
	queued := false
	if confirm {
		queued, err = confirmWorkflowInTx(ctx, tx, projectID, turnID, record, modelReady, false)
		if err != nil {
			return turnWorkflowProposal{}, false, err
		}
	} else {
		record.Status, record.Message = taskCancelled, ""
	}
	body, err = json.Marshal(record)
	if err != nil {
		return turnWorkflowProposal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET workflow_proposal_json = ? WHERE turn_id = ?`, string(body), turnID); err != nil {
		return turnWorkflowProposal{}, false, err
	}
	return record.turnWorkflowProposal, queued, tx.Commit()
}

func confirmWorkflowInTx(ctx context.Context, tx *sql.Tx, projectID, turnID string, record *workflowProposalRecord, modelReady, automatic bool) (bool, error) {
	latest, err := readWorkflow(ctx, tx, projectID, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if latest.RunID != record.BaseRunID || latest.Revision != record.BaseRevision {
		return false, errWorkflowProposalStale
	}
	if automatic && record.Action == workflowContinue && latest.step > 0 &&
		workflowStages[latest.WorkflowKey][latest.step-1].checkpoint {
		return false, errWorkflowCheckpoint
	}
	req := workflowRequest{RequestID: rand.Text(), ThroughStep: record.Scope.EndStep, Checkpoints: new(record.Checkpoints),
		sourceTurnID: turnID, workflowKey: record.WorkflowKey}
	if record.Action == workflowContinue {
		req.RunID, req.ExpectedRevision = record.BaseRunID, record.BaseRevision
	}
	run, applied, err := applyWorkflowInTx(ctx, tx, projectID, record.Action, req, modelReady)
	if err != nil {
		return false, err
	}
	record.Status, record.RunID, record.Automatic, record.Message = workflowConfirmed, run.RunID, automatic, ""
	return applied && run.Status == taskQueued, nil
}

func dispatchTurnWorkflow(ctx context.Context, tx *sql.Tx, turnID string, record *workflowProposalRecord) (bool, error) {
	var projectID, mode string
	if err := tx.QueryRowContext(ctx, `SELECT project_id, mode FROM turns
		WHERE turn_id = ? AND status = 'RUNNING' AND mode <> 'CHAT'`, turnID).Scan(&projectID, &mode); err != nil {
		return false, err
	}
	if mode != turnModeAuto {
		return false, nil
	}
	queued, err := confirmWorkflowInTx(ctx, tx, projectID, turnID, record, true, true)
	// These conditions are checked before the workflow writes its run and queue receipt.
	switch {
	case errors.Is(err, errReviewSources):
		record.Message = "请先在项目文献库核验并保存文献，再确认运行。"
	case errors.Is(err, errWorkflowCheckpoint):
		record.Message = "请核对上一步成果，再确认从当前检查点继续。"
	case errors.Is(err, errWorkflowProposalStale):
		record.Message = "工作流已有更新，请根据当前步骤重新提出建议。"
	default:
		return queued, err
	}
	return false, nil
}
