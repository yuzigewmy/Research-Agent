package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"research-agent/internal/inference"
)

const (
	turnModeChat    = "CHAT"
	turnModeConfirm = "CONFIRM"
)

func validTurnMode(mode string) bool {
	return mode == turnModeChat || mode == turnModeConfirm
}

const turnProposalSchema = `
	ALTER TABLE turns ADD COLUMN mode TEXT NOT NULL DEFAULT 'CHAT' CHECK (mode IN ('CHAT', 'CONFIRM'));
	ALTER TABLE turns ADD COLUMN proposed_task_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE turn_events ADD COLUMN proposed_task_id TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 29;
`

type sendTurnInput struct {
	RequestID    string              `json:"requestId"`
	Text         string              `json:"text"`
	ReferenceIDs []string            `json:"referenceIds"`
	PDFQuotes    map[string]pdfQuote `json:"pdfQuotes"`
	Mode         string              `json:"mode"`
}

type turnProposal struct {
	instruction string
	workflow    *workflowProposalRecord
	checkInput  *taskInput
}

func proposalTool(workflow *workflowProposalRecord, manuscripts []taskInput) (inference.Tool, error) {
	properties := map[string]any{
		"instruction": map[string]string{"type": "string", "description": "任务范围、输入和交付要求"},
	}
	if steps := workflow.runnableSteps(); len(steps) != 0 {
		ends := make([]string, 0, len(steps))
		for _, step := range steps {
			ends = append(ends, step.Key)
		}
		properties["throughStep"] = map[string]any{
			"type": "string", "enum": ends, "description": "运行工作流时选择区间终点。与 checkVersionId 互斥。",
		}
	}
	if len(manuscripts) != 0 {
		ids := make([]string, 0, len(manuscripts))
		for _, manuscript := range manuscripts {
			ids = append(ids, manuscript.VersionID)
		}
		properties["checkVersionId"] = map[string]any{
			"type": "string", "enum": ids, "description": "明确要求检查稿件结构时，从 manuscriptVersions 选择对应稿件的 versionId。与 throughStep 互斥。",
		}
	}
	parameters, err := json.Marshal(map[string]any{
		"type": "object", "required": []string{"instruction"}, "additionalProperties": false, "properties": properties,
	})
	return inference.Tool{
		Name:        "propose_research_task",
		Description: "为用户明确要求的研究计划、工作流区间或稿件结构检查提出一项待确认建议。",
		Parameters:  parameters,
	}, err
}

func parseTurnProposal(arguments json.RawMessage, workflow *workflowProposalRecord, manuscripts []taskInput) (turnProposal, error) {
	var input struct {
		Instruction    string          `json:"instruction"`
		ThroughStep    json.RawMessage `json:"throughStep"`
		CheckVersionID json.RawMessage `json:"checkVersionId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return turnProposal{}, errInvalidTaskProposal
	}
	instruction := strings.TrimSpace(input.Instruction)
	if !validTaskInstruction(instruction) {
		return turnProposal{}, errInvalidTaskProposal
	}
	if len(input.CheckVersionID) != 0 {
		var id string
		if len(input.ThroughStep) != 0 || json.Unmarshal(input.CheckVersionID, &id) != nil {
			return turnProposal{}, errInvalidTaskProposal
		}
		for _, source := range manuscripts {
			if source.VersionID == id {
				return turnProposal{instruction: instruction, checkInput: &source}, nil
			}
		}
		return turnProposal{}, errInvalidTaskProposal
	}
	if len(input.ThroughStep) == 0 {
		return turnProposal{instruction: instruction}, nil
	}
	var end string
	if err := json.Unmarshal(input.ThroughStep, &end); err != nil || end == "" {
		return turnProposal{}, errInvalidTaskProposal
	}
	if err := workflow.selectScope(instruction, end); err != nil {
		return turnProposal{}, err
	}
	return turnProposal{workflow: workflow}, nil
}

var errInvalidTaskProposal = errors.New("model proposed an invalid research task")

func insertTurnProposal(ctx context.Context, tx *sql.Tx, turnID string, proposal turnProposal) (string, error) {
	kind, inputs := "", "[]"
	if proposal.checkInput != nil {
		body, err := json.Marshal([]taskInput{*proposal.checkInput})
		if err != nil {
			return "", err
		}
		kind, inputs = manuscriptCheckTask, string(body)
	}
	var taskID string
	err := tx.QueryRowContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, status, created_at, source_turn_id, kind, inputs_json)
		SELECT project_id, ?, ?, 'PROPOSED', ?, turn_id, ?, ? FROM turns
		WHERE turn_id = ? AND status = 'RUNNING' AND mode = 'CONFIRM' RETURNING task_id`,
		rand.Text(), proposal.instruction, time.Now().UTC().Format(createdAtLayout), kind, inputs, turnID).Scan(&taskID)
	return taskID, err
}
