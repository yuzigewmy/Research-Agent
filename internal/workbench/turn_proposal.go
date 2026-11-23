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

const turnProposalSchema = `
	ALTER TABLE turns ADD COLUMN mode TEXT NOT NULL DEFAULT 'CHAT' CHECK (mode IN ('CHAT', 'CONFIRM'));
	ALTER TABLE turns ADD COLUMN proposed_task_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE turn_events ADD COLUMN proposed_task_id TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 29;
`

type sendTurnInput struct {
	RequestID           string                    `json:"requestId"`
	Text                string                    `json:"text"`
	ReferenceIDs        []string                  `json:"referenceIds"`
	PDFQuotes           map[string]pdfQuote       `json:"pdfQuotes"`
	Mode                string                    `json:"mode"`
	ManuscriptSelection *manuscriptSelectionInput `json:"manuscriptSelection"`
}

type turnProposal struct {
	instruction   string
	workflow      *workflowProposalRecord
	checkInput    *taskInput
	editSelection *manuscriptSelection
}

func proposalTool(workflow *workflowProposalRecord, manuscripts []taskInput, selection *manuscriptSelection) (inference.Tool, error) {
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
	if selection != nil {
		properties["editSelection"] = map[string]any{
			"type": "boolean", "enum": []bool{true}, "description": "用户明确要求改写本轮附带的稿件选区时填写 true。只生成该选区的替换建议。此项与区间运行及结构检查互斥。",
		}
	}
	parameters, err := json.Marshal(map[string]any{
		"type": "object", "required": []string{"instruction"}, "additionalProperties": false, "properties": properties,
	})
	return inference.Tool{
		Name:        "propose_research_task",
		Description: "为用户明确要求的研究计划、工作流区间、稿件结构检查或选区改稿提交一项建议。工作台按本轮协作方式处理。",
		Parameters:  parameters,
	}, err
}

func parseTurnProposal(arguments json.RawMessage, workflow *workflowProposalRecord, manuscripts []taskInput, selection *manuscriptSelection) (turnProposal, error) {
	var input struct {
		Instruction    string          `json:"instruction"`
		ThroughStep    json.RawMessage `json:"throughStep"`
		CheckVersionID json.RawMessage `json:"checkVersionId"`
		EditSelection  json.RawMessage `json:"editSelection"`
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
	if len(input.EditSelection) != 0 {
		var edit bool
		if selection == nil || len(input.ThroughStep) != 0 || len(input.CheckVersionID) != 0 ||
			len(instruction) > maxEditInstructionBytes || json.Unmarshal(input.EditSelection, &edit) != nil || !edit {
			return turnProposal{}, errInvalidTaskProposal
		}
		return turnProposal{instruction: instruction, editSelection: selection}, nil
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

func insertTurnProposal(ctx context.Context, tx *sql.Tx, turnID string, proposal turnProposal) (string, bool, error) {
	kind, inputs := "", "[]"
	source := proposal.checkInput
	selection := proposal.editSelection
	if source != nil {
		kind = manuscriptCheckTask
	}
	if selection != nil {
		source = &taskInput{VersionID: selection.VersionID, Path: selection.Path, SHA256: selection.SHA256}
	}
	if source != nil {
		body, err := json.Marshal([]taskInput{*source})
		if err != nil {
			return "", false, err
		}
		inputs = string(body)
	}
	var projectID, mode string
	if err := tx.QueryRowContext(ctx, `SELECT project_id, mode FROM turns
		WHERE turn_id = ? AND status = 'RUNNING' AND mode <> 'CHAT'`, turnID).Scan(&projectID, &mode); err != nil {
		return "", false, err
	}
	taskID := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
		(project_id, task_id, instruction, status, created_at, source_turn_id, kind, inputs_json, selection_edit)
		VALUES (?, ?, ?, 'PROPOSED', ?, ?, ?, ?, ?)`,
		projectID, taskID, proposal.instruction, time.Now().UTC().Format(createdAtLayout), turnID, kind, inputs, selection != nil); err != nil {
		return "", false, err
	}
	if selection != nil {
		if err := insertSelectionEdit(ctx, tx, projectID, taskID, taskID, selection.manuscriptSelectionInput, selection.Text, reviewFindingRef{}); err != nil {
			return "", false, err
		}
	}
	if mode != turnModeConfirmSteps {
		return taskID, false, nil
	}
	queued, err := queueTurnTask(ctx, tx, projectID, taskID)
	return taskID, queued, err
}
