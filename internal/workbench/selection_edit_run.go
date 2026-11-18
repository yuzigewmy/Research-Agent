package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"research-agent/internal/inference"
)

//go:embed prompts/selection_edit.md
var selectionEditPrompt string

const selectionContextBytes = 512
const maxEditReplacementBytes = 16 << 10

var errSelectionOutput = errors.New("model returned an invalid selection edit")

func (s *Store) generateSelectionEdit(ctx context.Context, model *inference.Client, projectID string, task Task) (string, error) {
	var versionID string
	var start, end int
	if err := s.db.QueryRowContext(ctx, `SELECT source_version_id, start_byte, end_byte FROM selection_edits
		WHERE project_id = ? AND task_id = ?`, projectID, task.TaskID).Scan(&versionID, &start, &end); err != nil {
		return "", err
	}
	file, err := s.workspaceVersion(ctx, projectID, versionID)
	if err != nil {
		return "", err
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		return "", err
	}
	left, right := max(0, start-selectionContextBytes), min(len(doc.Content), end+selectionContextBytes)
	for !utf8.RuneStart(doc.Content[left]) {
		left++
	}
	for right < len(doc.Content) && !utf8.RuneStart(doc.Content[right]) {
		right--
	}
	input := struct {
		Instruction string `json:"instruction"`
		Before      string `json:"before"`
		Selected    string `json:"selected"`
		After       string `json:"after"`
	}{task.Instruction, doc.Content[left:start], doc.Content[start:end], doc.Content[end:right]}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	output, err := streamTaskMarkdown(ctx, model, selectionEditPrompt, string(payload))
	if err != nil {
		return "", err
	}
	var result struct {
		Replacement *string `json:"replacement"`
		Reason      string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.Replacement == nil {
		return "", errSelectionOutput
	}
	replacement := *result.Replacement
	reason := strings.TrimSpace(result.Reason)
	if len(replacement) > maxEditReplacementBytes || strings.ContainsRune(replacement, 0) ||
		len(doc.Content)-(end-start)+len(replacement) > maxWorkspaceText || reason == "" || replacement == input.Selected {
		return "", errSelectionOutput
	}
	encoded, err := json.Marshal(editSuggestion{Replacement: replacement, Reason: reason})
	return string(encoded), err
}
