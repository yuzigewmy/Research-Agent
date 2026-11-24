package workbench

import (
	"errors"
	"slices"
	"strings"
)

const workflowModelSchema = `
	ALTER TABLE workflow_runs ADD COLUMN model_id TEXT NOT NULL DEFAULT 'default';
	ALTER TABLE workflow_runs ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN model_connection TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 44;
`

const workflowStepModelSchema = `
	ALTER TABLE workflow_runs ADD COLUMN step_models_json TEXT NOT NULL DEFAULT '{}';
	PRAGMA user_version = 47;
`

const workflowModelRequestSchema = `
	ALTER TABLE workflow_requests ADD COLUMN requested_model_id TEXT;
	PRAGMA user_version = 48;
`

const alternateWorkflowModelID = "workflow"

var (
	errWorkflowModelUnavailable = errors.New("requested workflow model is not configured")
	errWorkflowModelConnection  = errors.New("请恢复本轮工作流启动时的模型服务地址。")
	errWorkflowStepModel        = errors.New("invalid workflow step model selection")
)

type workflowStepModels map[string]modelOption

func (models workflowStepModels) matches(requested map[string]string) bool {
	if len(models) != len(requested) {
		return false
	}
	for key, model := range models {
		if requested[key] != model.ID {
			return false
		}
	}
	return true
}

func freezeWorkflowStepModels(stages []workflowStage, requested map[string]string, options []modelOption) (workflowStepModels, error) {
	models := make(workflowStepModels, len(requested))
	for key, id := range requested {
		if (id != defaultModelID && id != alternateWorkflowModelID) || !slices.ContainsFunc(stages, func(stage workflowStage) bool {
			return stage.key == key && stage.prompt != ""
		}) {
			return nil, errWorkflowStepModel
		}
		index := slices.IndexFunc(options, func(option modelOption) bool { return option.ID == id })
		if index < 0 {
			return nil, errWorkflowModelUnavailable
		}
		models[key] = options[index]
	}
	return models, nil
}

type workflowModels struct {
	options   []modelOption
	templates map[string]bool
}

func configureWorkflowModels(options []modelOption, templates string) workflowModels {
	preferred := make(map[string]bool)
	for template := range strings.SplitSeq(templates, ",") {
		if template = strings.TrimSpace(template); projectWorkflowKey(template) != "" {
			preferred[template] = true
		}
	}
	return workflowModels{options: options, templates: preferred}
}

func (models workflowModels) defaultID(template string) string {
	if len(models.options) > 1 && models.templates[template] {
		return alternateWorkflowModelID
	}
	return defaultModelID
}

func (models workflowModels) optionsFor(template string) []modelOption {
	if models.defaultID(template) == alternateWorkflowModelID {
		return []modelOption{models.options[1], models.options[0]}
	}
	return models.options
}

func (models workflowModels) option(id string) modelOption {
	for _, option := range models.options {
		if option.ID == id {
			return option
		}
	}
	return modelOption{}
}
