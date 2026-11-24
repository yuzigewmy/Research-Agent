package workbench

import (
	"errors"

	"research-agent/internal/inference"
)

const workflowModelSchema = `
	ALTER TABLE workflow_runs ADD COLUMN model_id TEXT NOT NULL DEFAULT 'default';
	ALTER TABLE workflow_runs ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN model_connection TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 44;
`

const alternateWorkflowModelID = "workflow"

var (
	errWorkflowModelUnavailable = errors.New("requested workflow model is not configured")
	errWorkflowModelConnection  = errors.New("请恢复本轮工作流启动时的模型服务地址。")
)

func defaultWorkflowModel(client *inference.Client) modelOption {
	return modelOption{ID: defaultModelID, modelSnapshot: snapshotModel(client, "", "")}
}
