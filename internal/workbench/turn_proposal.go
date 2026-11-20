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
	RequestID    string   `json:"requestId"`
	Text         string   `json:"text"`
	ReferenceIDs []string `json:"referenceIds"`
	Mode         string   `json:"mode"`
}

var researchTaskTool = inference.Tool{
	Name:        "propose_research_task",
	Description: "当用户明确要求拟定研究计划时，提出一张待用户确认的任务卡。任务产出为 Markdown 计划。写明研究范围、拟使用的资料和预期成果。",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"instruction":{"type":"string","description":"研究任务说明，包含范围、输入和交付物"}},"required":["instruction"],"additionalProperties":false}`),
}

func researchTaskInstruction(arguments json.RawMessage) (string, error) {
	var proposal struct {
		Instruction string `json:"instruction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return "", errInvalidTaskProposal
	}
	instruction := strings.TrimSpace(proposal.Instruction)
	if !validTaskInstruction(instruction) {
		return "", errInvalidTaskProposal
	}
	return instruction, nil
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
