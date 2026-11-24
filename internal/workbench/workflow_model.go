package workbench

import (
	"errors"
	"strings"

	"research-agent/internal/inference"
)

const workflowModelSchema = `
	ALTER TABLE workflow_runs ADD COLUMN model_id TEXT NOT NULL DEFAULT 'default';
	ALTER TABLE workflow_runs ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN model_connection TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 44;
`

const (
	defaultWorkflowModelID   = "default"
	alternateWorkflowModelID = "workflow"
)

var (
	errWorkflowModelUnavailable = errors.New("requested workflow model is not configured")
	errWorkflowModelConnection  = errors.New("请恢复本轮工作流启动时的模型服务地址。")
)

type workflowModelOption struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	connection      string
}

func defaultWorkflowModel(client *inference.Client) workflowModelOption {
	if client == nil {
		return workflowModelOption{}
	}
	name, effort := client.ModelInfo()
	return workflowModelOption{ID: defaultWorkflowModelID, Model: name, ReasoningEffort: effort, connection: client.ConnectionFingerprint()}
}

func configuredWorkflowModels(client *inference.Client, name, effort string) []workflowModelOption {
	if client == nil {
		return nil
	}
	primary := defaultWorkflowModel(client)
	options := []workflowModelOption{primary}
	name, effort = strings.TrimSpace(name), strings.TrimSpace(effort)
	if name != "" && (name != primary.Model || effort != primary.ReasoningEffort) {
		options = append(options, workflowModelOption{ID: alternateWorkflowModelID, Model: name, ReasoningEffort: effort, connection: primary.connection})
	}
	return options
}
