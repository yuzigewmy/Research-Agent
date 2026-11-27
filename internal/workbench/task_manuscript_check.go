package workbench

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

const manuscriptCheckTask = "manuscript-check"
const maxManuscriptChoices = 20

const taskManuscriptCheckSchema = `
	ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK (kind IN ('', 'manuscript-check'));
	PRAGMA user_version = 34;
`

var errInvalidCheckInput = errors.New("manuscript check requires one saved Markdown version")

func markdownPath(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

func (s *Store) manuscriptCheckChoices(ctx context.Context, projectID string) ([]taskInput, error) {
	files, err := s.listWorkspaceFiles(ctx, projectID, "", "markdown", "", 0, maxManuscriptChoices)
	if err != nil {
		return nil, err
	}
	choices := make([]taskInput, 0, len(files))
	for _, file := range files {
		choices = append(choices, taskInput{VersionID: file.VersionID, Path: file.Path, SHA256: file.SHA256})
	}
	return choices, nil
}

func (s *Store) runManuscriptCheck(ctx context.Context, projectID string, input taskInput) (taskResult, error) {
	file, err := s.workspaceVersion(ctx, projectID, input.VersionID)
	if err != nil {
		return taskResult{}, err
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		return taskResult{}, err
	}
	if workspaceTextFile(file.Path, doc.Content).SHA256 != input.SHA256 {
		return taskResult{}, errors.New("manuscript object digest does not match the approved input")
	}
	report := paperCheck(doc.Content)
	return taskResult{markdown: report + fmt.Sprintf("\n稿件版本 `%s`\n", doc.VersionID)}, nil
}
