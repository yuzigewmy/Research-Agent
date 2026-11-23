package workbench

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

const turnManuscriptSelectionSchema = `
	ALTER TABLE turns ADD COLUMN manuscript_selection_json TEXT NOT NULL DEFAULT 'null';
	PRAGMA user_version = 35;
`

const turnManuscriptSelectionsSchema = `
	ALTER TABLE turns ADD COLUMN manuscript_selections_json TEXT NOT NULL DEFAULT 'null';
	PRAGMA user_version = 39;
`

const maxManuscriptSelections = 4

var errInvalidManuscriptSelection = errors.New("invalid selection in a saved manuscript")

type manuscriptSelectionInput struct {
	VersionID string `json:"versionId"`
	StartByte int    `json:"startByte"`
	EndByte   int    `json:"endByte"`
}

type manuscriptSelection struct {
	manuscriptSelectionInput
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}

func (selection *manuscriptSelection) matches(input *manuscriptSelectionInput) bool {
	if selection == nil || input == nil {
		return selection == nil && input == nil
	}
	return selection.manuscriptSelectionInput == *input
}

func validSelectionRange(content string, start, end int) bool {
	return start >= 0 && end > start && end <= len(content) && end-start <= maxSelectionBytes &&
		utf8.RuneStart(content[start]) && (end == len(content) || utf8.RuneStart(content[end]))
}

func (s *Store) resolveManuscriptSelection(ctx context.Context, tx *sql.Tx, projectID string, input *manuscriptSelectionInput) (*manuscriptSelection, error) {
	if input == nil {
		return nil, nil
	}
	if !validRequestID(input.VersionID) {
		return nil, errInvalidManuscriptSelection
	}
	file, err := lookupWorkspaceVersion(ctx, tx, projectID, input.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errInvalidManuscriptSelection
	}
	if err != nil {
		return nil, err
	}
	if !markdownPath(file.Path) {
		return nil, errInvalidManuscriptSelection
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		return nil, err
	}
	if !validSelectionRange(doc.Content, input.StartByte, input.EndByte) {
		return nil, errInvalidManuscriptSelection
	}
	text := doc.Content[input.StartByte:input.EndByte]
	if strings.TrimSpace(text) == "" {
		return nil, errInvalidManuscriptSelection
	}
	return &manuscriptSelection{manuscriptSelectionInput: *input, Path: file.Path, SHA256: file.SHA256, Text: text}, nil
}

func (s *Store) resolveManuscriptSelections(ctx context.Context, tx *sql.Tx, projectID string, inputs []manuscriptSelectionInput) ([]manuscriptSelection, error) {
	var selections []manuscriptSelection
	paths := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		selection, err := s.resolveManuscriptSelection(ctx, tx, projectID, &input)
		if err != nil {
			return nil, err
		}
		if paths[selection.Path] {
			return nil, errInvalidManuscriptSelection
		}
		paths[selection.Path] = true
		selections = append(selections, *selection)
	}
	return selections, nil
}
