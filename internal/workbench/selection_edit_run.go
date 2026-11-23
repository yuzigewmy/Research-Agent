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

type selectionExcerpt struct {
	Before   string `json:"before"`
	Selected string `json:"selected"`
	After    string `json:"after"`
}

func excerptSelection(content string, start, end int) selectionExcerpt {
	left, right := max(0, start-selectionContextBytes), min(len(content), end+selectionContextBytes)
	for !utf8.RuneStart(content[left]) {
		left++
	}
	for right < len(content) && !utf8.RuneStart(content[right]) {
		right--
	}
	return selectionExcerpt{Before: content[left:start], Selected: content[start:end], After: content[end:right]}
}

func validSelectionSuggestion(suggestion EditSuggestion, documentSize, selectionSize int) bool {
	return suggestion.Reason != "" && len(suggestion.Replacement) <= maxEditReplacementBytes &&
		!strings.ContainsRune(suggestion.Replacement, 0) && documentSize-selectionSize+len(suggestion.Replacement) <= maxWorkspaceText
}

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
	input := struct {
		Instruction string `json:"instruction"`
		selectionExcerpt
	}{task.Instruction, excerptSelection(doc.Content, start, end)}
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
	suggestion := EditSuggestion{Replacement: *result.Replacement, Reason: strings.TrimSpace(result.Reason)}
	if suggestion.Replacement == input.Selected || !validSelectionSuggestion(suggestion, len(doc.Content), end-start) {
		return "", errSelectionOutput
	}
	encoded, err := json.Marshal(suggestion)
	return string(encoded), err
}
