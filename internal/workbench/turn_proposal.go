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

var researchTaskTool = inference.Tool{
	Name:        "propose_research_task",
	Description: "当用户明确要求拟定研究计划时，提出一张待用户确认的任务卡。任务产出为 Markdown 计划。写明研究范围、拟使用的资料和预期成果。",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"instruction":{"type":"string","description":"研究任务说明，包含范围、输入和交付物"}},"required":["instruction"],"additionalProperties":false}`),
}

type turnProposal struct {
	instruction string
	workflow    *workflowProposalRecord
}

func proposalTool(options *workflowProposalRecord) (inference.Tool, error) {
	if options == nil {
		return researchTaskTool, nil
	}
	ends := make([]string, 0, len(options.Steps))
	for _, step := range options.Steps {
		ends = append(ends, step.Key)
	}
	parameters, err := json.Marshal(map[string]any{
		"type": "object", "required": []string{"instruction"}, "additionalProperties": false,
		"properties": map[string]any{
			"instruction": map[string]string{"type": "string", "description": "任务范围、输入和交付要求"},
			"throughStep": map[string]any{"type": "string", "enum": ends, "description": "仅在用户明确要求运行该工作流时，从当前可运行起点运行到此步骤。"},
		},
	})
	tool := researchTaskTool
	tool.Description = "为用户明确要求的行动提出一项待确认建议。研究计划使用 instruction。启动或继续本项目工作流还须选择 throughStep。"
	tool.Parameters = parameters
	return tool, err
}

func parseTurnProposal(arguments json.RawMessage, workflow *workflowProposalRecord) (turnProposal, error) {
	var input struct {
		Instruction string          `json:"instruction"`
		ThroughStep json.RawMessage `json:"throughStep"`
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

func insertTurnProposal(ctx context.Context, tx *sql.Tx, turnID, instruction string) (string, error) {
	var taskID string
	err := tx.QueryRowContext(ctx, `INSERT INTO tasks (project_id, task_id, instruction, status, created_at, source_turn_id)
		SELECT project_id, ?, ?, 'PROPOSED', ?, turn_id FROM turns
		WHERE turn_id = ? AND status = 'RUNNING' AND mode = 'CONFIRM' RETURNING task_id`,
		rand.Text(), instruction, time.Now().UTC().Format(createdAtLayout), turnID).Scan(&taskID)
	return taskID, err
}
