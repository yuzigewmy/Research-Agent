package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const paperOptionsSchema = `
	ALTER TABLE workflow_runs ADD COLUMN options_json TEXT NOT NULL DEFAULT '{}';
	PRAGMA user_version = 13;
`

const (
	maxPaperOutlineBytes = 16 << 10
	defaultPaperLanguage = "zh"
)

var errPaperOptions = errors.New("invalid paper language or outline")

type paperOptions struct {
	Language string `json:"language"`
	Outline  string `json:"outline,omitempty"`
}

func workflowStartSettings(ctx context.Context, tx *sql.Tx, projectID, key, action string, req workflowRequest) (string, int, error) {
	if isPatentWorkflow(key) && action == workflowStart {
		if req.Paper != nil || req.Topic != nil || req.Proposal != nil || req.Course != nil || req.Software != nil || req.TargetPaperCount != 0 {
			return "", 0, errPatentOptions
		}
		options, err := freezePatentSettings(ctx, tx, projectID, req.Patent)
		if err != nil {
			return "", 0, err
		}
		options, err = applyWorkflowRunConfig(options, key, action, req)
		return options, 0, err
	}
	if req.Patent != nil {
		return "", 0, errPatentOptions
	}
	if key == softwareRegistrationWorkflow && action == workflowStart {
		if req.Paper != nil || req.Topic != nil || req.Proposal != nil || req.Course != nil || req.TargetPaperCount != 0 {
			return "", 0, errSoftwareOptions
		}
		options, err := freezeSoftwareSettings(ctx, tx, projectID, req.Software)
		if err != nil {
			return "", 0, err
		}
		options, err = applyWorkflowRunConfig(options, key, action, req)
		return options, 0, err
	}
	if req.Software != nil {
		return "", 0, errSoftwareOptions
	}
	if isCourseWorkflow(key) && action == workflowStart {
		if req.Paper != nil || req.Topic != nil || req.Proposal != nil || req.TargetPaperCount != 0 {
			return "", 0, errCourseOptions
		}
		options, err := freezeCourseSettings(ctx, tx, projectID, key, req.Course)
		if err != nil {
			return "", 0, err
		}
		options, err = applyWorkflowRunConfig(options, key, action, req)
		return options, 0, err
	}
	if req.Course != nil {
		return "", 0, errCourseOptions
	}
	if isProposalWorkflow(key) && action == workflowStart {
		if req.Paper != nil || req.Topic != nil {
			return "", 0, errProposalOptions
		}
		options, err := normalizeProposalOptions(req.Proposal)
		if err != nil {
			return "", 0, err
		}
		body, err := json.Marshal(options)
		if err != nil {
			return "", 0, err
		}
		merged, err := applyWorkflowRunConfig(string(body), key, action, req)
		return merged, options.TargetSources, err
	}
	if req.Proposal != nil {
		return "", 0, errProposalOptions
	}
	target, err := reviewTarget(key, action, req.TargetPaperCount)
	if err != nil {
		return "", 0, err
	}
	options, err := workflowOptions(key, action, req.Paper, req.Topic)
	if err != nil {
		return "", 0, err
	}
	options, err = applyWorkflowRunConfig(options, key, action, req)
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
	options := paperOptions{Language: defaultPaperLanguage}
	if input != nil {
		options = *input
		if options.Language == "" {
			options.Language = defaultPaperLanguage
		}
	}
	if (options.Language != "zh" && options.Language != "en") ||
		len(options.Outline) > maxPaperOutlineBytes || strings.ContainsRune(options.Outline, 0) {
		return "", errPaperOptions
	}
	body, err := json.Marshal(options)
	return string(body), err
}
