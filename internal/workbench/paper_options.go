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

func workflowStartSettings(key, action string, req workflowRequest) (string, int, error) {
	if key == proposalWorkflow && action == workflowStart {
		if req.Paper != nil || req.Topic != nil {
			return "", 0, errProposalOptions
		}
		options, err := normalizeProposalOptions(req.Proposal)
		if err != nil {
			return "", 0, err
		}
		body, err := json.Marshal(options)
		return string(body), options.TargetSources, err
	}
	if req.Proposal != nil {
		return "", 0, errProposalOptions
	}
	target, err := reviewTarget(key, action, req.TargetPaperCount)
	if err != nil {
		return "", 0, err
	}
	options, err := workflowOptions(key, action, req.Paper, req.Topic)
	return options, target, err
}

func workflowOptions(key, action string, input *paperOptions, topic *topicOptions) (string, error) {
	if topic != nil {
		if key != topicWorkflow || action != workflowStart || input != nil {
			return "", errTopicOptions
		}
		requirements := strings.TrimSpace(topic.Requirements)
		if len(requirements) > 4<<10 || strings.ContainsRune(requirements, 0) {
			return "", errTopicOptions
		}
		body, err := json.Marshal(topicOptions{Requirements: requirements})
		return string(body), err
	}
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
