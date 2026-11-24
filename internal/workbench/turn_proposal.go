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
	RequestID            string                     `json:"requestId"`
	Text                 string                     `json:"text"`
	ReferenceIDs         []string                   `json:"referenceIds"`
	PDFQuotes            map[string]pdfQuote        `json:"pdfQuotes"`
	Mode                 string                     `json:"mode"`
	ModelID              string                     `json:"modelId"`
	ManuscriptSelection  *manuscriptSelectionInput  `json:"manuscriptSelection"`
	ManuscriptSelections []manuscriptSelectionInput `json:"manuscriptSelections"`
}

type turnProposal struct {
	instruction    string
	workflow       *workflowProposalRecord
	checkInput     *taskInput
	focusInputs    []taskInput
	editSelections []manuscriptSelection
}

func proposalTool(workflow *workflowProposalRecord, manuscripts []taskInput, canEdit bool) (inference.Tool, error) {
	properties := map[string]any{
		"instruction": map[string]string{"type": "string", "description": "任务范围、输入和交付要求"},
	}
	if steps := workflow.runnableSteps(); len(steps) != 0 {
		ends := make([]string, 0, len(steps))
		for _, step := range steps {
			ends = append(ends, step.Key)
		}
		properties["throughStep"] = map[string]any{
			"type": "string", "enum": ends, "description": "运行工作流时选择区间终点。",
		}
	}
	if len(manuscripts) != 0 {
		ids := make([]string, 0, len(manuscripts))
		for _, manuscript := range manuscripts {
			ids = append(ids, manuscript.VersionID)
		}
		properties["checkVersionId"] = map[string]any{
			"type": "string", "enum": ids, "description": "明确要求检查稿件结构时，从 manuscriptVersions 选择对应稿件的 versionId。",
		}
		properties["focusVersionIds"] = map[string]any{
			"type": "array", "items": map[string]any{"type": "string", "enum": ids},
			"minItems": 1, "maxItems": maxTaskInputs, "uniqueItems": true,
			"description": "围绕具体问题分析或对照稿件时，选取一至四个不同的版本。用户确认后读取节选。",
		}
	}
	if canEdit {
		properties["editSelection"] = map[string]any{
			"type": "boolean", "enum": []bool{true}, "description": "用户明确要求改写本轮附带的稿件选区时填写 true。按引用范围逐份生成替换建议。",
		}
	}
	parameters, err := json.Marshal(map[string]any{
		"type": "object", "required": []string{"instruction"}, "additionalProperties": false, "properties": properties,
	})
	return inference.Tool{
		Name:        "propose_research_task",
		Description: "为用户明确要求的研究计划、工作流区间、稿件结构检查、聚焦研究或选区改稿提交一项建议。范围参数最多填写一项。工作台按本轮协作方式处理。",
		Parameters:  parameters,
	}, err
}

func parseTurnProposal(arguments json.RawMessage, workflow *workflowProposalRecord, manuscripts []taskInput, selections []manuscriptSelection) (turnProposal, error) {
	var input struct {
		Instruction     string          `json:"instruction"`
		ThroughStep     json.RawMessage `json:"throughStep"`
		CheckVersionID  json.RawMessage `json:"checkVersionId"`
		FocusVersionIDs json.RawMessage `json:"focusVersionIds"`
		EditSelection   json.RawMessage `json:"editSelection"`
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
	var scopeCount int
	for _, scope := range []json.RawMessage{input.ThroughStep, input.CheckVersionID, input.FocusVersionIDs, input.EditSelection} {
		if len(scope) != 0 {
			scopeCount++
		}
	}
	if scopeCount > 1 {
		return turnProposal{}, errInvalidTaskProposal
	}
	switch {
	case len(input.EditSelection) != 0:
		var edit bool
		if len(selections) == 0 || len(instruction) > maxEditInstructionBytes || json.Unmarshal(input.EditSelection, &edit) != nil || !edit {
			return turnProposal{}, errInvalidTaskProposal
		}
		return turnProposal{instruction: instruction, editSelections: selections}, nil
	case len(input.CheckVersionID) != 0:
		var id string
		if json.Unmarshal(input.CheckVersionID, &id) != nil {
			return turnProposal{}, errInvalidTaskProposal
		}
		for _, source := range manuscripts {
			if source.VersionID == id {
				return turnProposal{instruction: instruction, checkInput: &source}, nil
			}
		}
		return turnProposal{}, errInvalidTaskProposal
	case len(input.FocusVersionIDs) != 0:
		var ids []string
		if json.Unmarshal(input.FocusVersionIDs, &ids) != nil {
			return turnProposal{}, errInvalidTaskProposal
		}
		files, err := selectFocusInputs(ids, manuscripts)
		return turnProposal{instruction: instruction, focusInputs: files}, err
	case len(input.ThroughStep) != 0:
		var end string
		if json.Unmarshal(input.ThroughStep, &end) != nil || end == "" {
			return turnProposal{}, errInvalidTaskProposal
		}
		if err := workflow.selectScope(instruction, end); err != nil {
			return turnProposal{}, err
		}
		return turnProposal{workflow: workflow}, nil
	default:
		return turnProposal{instruction: instruction}, nil
	}
}

var errInvalidTaskProposal = errors.New("model proposed an invalid research task")

func insertTurnProposal(ctx context.Context, tx *sql.Tx, turnID string, proposal turnProposal, editModel modelSnapshot) (string, bool, error) {
	kind, inputs := "", "[]"
	selections := proposal.editSelections
	var sources []taskInput
	switch {
	case proposal.checkInput != nil:
		kind, sources = manuscriptCheckTask, []taskInput{*proposal.checkInput}
	case proposal.focusInputs != nil:
		kind, sources = focusedResearchTask, proposal.focusInputs
	case len(selections) != 0:
		sources = make([]taskInput, 0, len(selections))
		for _, selection := range selections {
			sources = append(sources, taskInput{VersionID: selection.VersionID, Path: selection.Path, SHA256: selection.SHA256})
		}
	}
	if len(sources) != 0 {
		body, err := json.Marshal(sources)
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
		projectID, taskID, proposal.instruction, time.Now().UTC().Format(createdAtLayout), turnID, kind, inputs, len(selections) != 0); err != nil {
		return "", false, err
	}
	switch len(selections) {
	case 0:
	case 1:
		selection := selections[0]
		if err := insertSelectionEdit(ctx, tx, projectID, taskID, taskID, selection.manuscriptSelectionInput, selection.Text, reviewFindingRef{}); err != nil {
			return "", false, err
		}
	default:
		if err := insertBatchSelectionEdits(ctx, tx, projectID, taskID, selections); err != nil {
			return "", false, err
		}
	}
	// The conversation has seen only version metadata. Reading these excerpts needs approval.
	if kind != focusedResearchTask && (mode == turnModeConfirmSteps || mode == turnModeAuto) {
		var model modelSnapshot
		if len(selections) != 0 {
			model = editModel
		}
		queued, err := queueTurnTask(ctx, tx, projectID, taskID, model)
		if err != nil || queued {
			return taskID, queued, err
		}
	}
	return taskID, false, insertProposalNotification(ctx, tx, projectID, taskID)
}
