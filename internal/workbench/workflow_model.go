package workbench

import (
	"errors"
	"slices"

	"research-agent/internal/inference"
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

func defaultWorkflowModel(client *inference.Client) modelOption {
	return modelOption{ID: defaultModelID, modelSnapshot: snapshotModel(client, "", "")}
}
