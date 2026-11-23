package workbench

import (
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const focusedResearchTask = "focused-research"

//go:embed prompts/focused_research.md
var focusedResearchPrompt string

const focusedResearchSchema = `
	ALTER TABLE tasks RENAME COLUMN kind TO previous_kind;
	ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK (kind IN ('', 'manuscript-check', 'focused-research'));
	UPDATE tasks SET kind = previous_kind WHERE previous_kind <> '';
	ALTER TABLE tasks DROP COLUMN previous_kind;
	PRAGMA user_version = 38;
`

var errInvalidFocusInput = errors.New("focused research requires one to four saved Markdown versions")

func selectFocusInputs(ids []string, manuscripts []taskInput) ([]taskInput, error) {
	if len(ids) == 0 || len(ids) > maxTaskInputs {
		return nil, errInvalidTaskProposal
	}
	inputs := make([]taskInput, 0, len(ids))
	for _, id := range ids {
		index := slices.IndexFunc(manuscripts, func(file taskInput) bool { return file.VersionID == id })
		if index < 0 {
			return nil, errInvalidTaskProposal
		}
		file := manuscripts[index]
		if slices.ContainsFunc(inputs, func(input taskInput) bool { return input.Path == file.Path }) {
			return nil, errInvalidTaskProposal
		}
		inputs = append(inputs, file)
	}
	return inputs, nil
}

func focusedResearchReport(analysis string, inputs []taskInput) (taskResult, error) {
	manifest, err := json.MarshalIndent(inputs, "", "  ")
	if err != nil {
		return taskResult{}, err
	}
	return taskResult{markdown: "# 聚焦研究报告\n\n" + strings.TrimSpace(analysis) +
		"\n\n## 固定输入\n\n    " + strings.ReplaceAll(string(manifest), "\n", "\n    ") + "\n"}, nil
}
