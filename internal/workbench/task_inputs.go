package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const taskInputSchema = `
	ALTER TABLE tasks ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE task_requests ADD COLUMN input_version_ids TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 24;
`

const maxTaskInputs = 4

const changedTaskInputs = `EXISTS (
	SELECT 1 FROM json_each(tasks.inputs_json) AS input
	WHERE json_extract(input.value, '$.versionId') IS NOT (
		SELECT version_id FROM workspace_versions
		WHERE project_id = tasks.project_id AND path = json_extract(input.value, '$.path')
		ORDER BY ordinal DESC LIMIT 1
	)
)`

const taskInputsChangedMessage = "任务所用文件已有新版本。请核对并更新输入后重新确认。"

var (
	errInvalidTaskInput = errors.New("task input must be a saved file from this project")
	errTaskInputChanged = errors.New("task input has a newer version")
)

type taskInput struct {
	VersionID string `json:"versionId"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
}

func resolveTaskInputs(ctx context.Context, tx *sql.Tx, projectID, kind string, ids []string) (string, error) {
	if kind == manuscriptCheckTask && len(ids) != 1 {
		return "", errInvalidCheckInput
	}
	inputs := make([]taskInput, 0, len(ids))
	paths := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validRequestID(id) {
			return "", errInvalidTaskInput
		}
		file, err := lookupWorkspaceVersion(ctx, tx, projectID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errInvalidTaskInput
		}
		if err != nil {
			return "", err
		}
		if kind == manuscriptCheckTask && !markdownPath(file.Path) {
			return "", errInvalidCheckInput
		}
		if paths[file.Path] {
			return "", errInvalidTaskInput
		}
		paths[file.Path] = true
		inputs = append(inputs, taskInput{VersionID: id, Path: file.Path, SHA256: file.SHA256})
	}
	for _, input := range inputs {
		current, err := currentWorkspaceFile(ctx, tx, projectID, input.Path)
		if err != nil {
			return "", err
		}
		if current.VersionID != input.VersionID {
			return "", errTaskInputChanged
		}
	}
	body, err := json.Marshal(inputs)
	return string(body), err
}

func (s *Store) taskInputContext(inputs []taskInput) (string, error) {
	type taskExcerpt struct {
		taskInput
		MaterialPreview
	}
	excerpts := make([]taskExcerpt, 0, len(inputs))
	for _, input := range inputs {
		preview, err := s.previewObject(input.SHA256, referenceExcerptBytes)
		if err != nil {
			return "", err
		}
		excerpts = append(excerpts, taskExcerpt{input, preview})
	}
	body, err := json.Marshal(excerpts)
	return "\n\n用户确认的工作区文件节选（JSON）\n" + string(body), err
}
