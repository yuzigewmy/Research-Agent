package workbench

import (
	"encoding/json"
	"errors"
	"strings"
)

const paperOptionsSchema = `
	ALTER TABLE workflow_runs ADD COLUMN options_json TEXT NOT NULL DEFAULT '{}';
	PRAGMA user_version = 13;
`

const maxPaperOutlineBytes = 16 << 10

var errPaperOptions = errors.New("invalid paper language or outline")

type paperOptions struct {
	Language string `json:"language"`
	Outline  string `json:"outline,omitempty"`
}

func workflowOptions(key, action string, input *paperOptions) (string, error) {
	if key != paperWorkflow || action != workflowStart {
		if input != nil {
			return "", errPaperOptions
		}
		return "{}", nil
	}
	options := paperOptions{Language: "zh"}
	if input != nil {
		options = *input
		if options.Language == "" {
			options.Language = "zh"
		}
	}
	if (options.Language != "zh" && options.Language != "en") ||
		len(options.Outline) > maxPaperOutlineBytes || strings.ContainsRune(options.Outline, 0) {
		return "", errPaperOptions
	}
	body, err := json.Marshal(options)
	return string(body), err
}
