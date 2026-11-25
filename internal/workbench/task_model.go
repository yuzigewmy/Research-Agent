package workbench

import (
	"errors"
	"strings"

	"research-agent/internal/inference"
)

const taskModelSchema = `
	ALTER TABLE tasks ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE tasks ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
	ALTER TABLE tasks ADD COLUMN model_connection TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 45;
`

var (
	errReviewModelConnection = errors.New("请恢复提交本次审稿时的模型服务地址，再重试。")
	errEditModelConnection   = errors.New("改稿任务使用的模型服务地址已变更。请核对当前连接后重新生成建议。")
)

const defaultModelID = "default"

type modelOption struct {
	ID           string `json:"id"`
	ImageQuality string `json:"imageQuality,omitempty"`
	modelSnapshot
}

type modelSnapshot struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	connection      string
}

func snapshotModel(client *inference.Client, name, effort string) modelSnapshot {
	if client == nil {
		return modelSnapshot{}
	}
	name, effort = strings.TrimSpace(name), strings.TrimSpace(effort)
	if name == "" {
		name, effort = client.ModelInfo()
	}
	return modelSnapshot{Model: name, ReasoningEffort: effort, connection: client.ConnectionFingerprint()}
}

func configuredModelOptions(client *inference.Client, alternateID, name, effort string) []modelOption {
	if client == nil {
		return nil
	}
	primary := snapshotModel(client, "", "")
	options := []modelOption{{ID: defaultModelID, modelSnapshot: primary}}
	alternate := snapshotModel(client, name, effort)
	if alternate != primary {
		options = append(options, modelOption{ID: alternateID, modelSnapshot: alternate})
	}
	return options
}

func (saved modelSnapshot) checkReviewRetry(current modelSnapshot) error {
	if current.Model == "" {
		return errModelUnavailable
	}
	if saved.connection != "" && saved.connection != current.connection {
		return errReviewModelConnection
	}
	return nil
}
