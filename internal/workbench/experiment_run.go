package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"research-agent/internal/inference"
)

func (s *Store) workflowContext(ctx context.Context, task Task, stages []workflowStage, first int) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_step, markdown FROM tasks
		WHERE workflow_run_id = ? AND workflow_step >= ? AND workflow_step < ?
		AND status = 'SUCCEEDED' AND workflow_superseded = 0
		ORDER BY workflow_step`, task.workflowRunID, first, task.workflowStep)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var previous strings.Builder
	for rows.Next() {
		var stage int
		var markdown string
		if err := rows.Scan(&stage, &markdown); err != nil {
			return "", err
		}
		fmt.Fprintf(&previous, "\n\n--- %s ---\n%s", stages[stage].title, markdown)
	}
	return previous.String(), rows.Err()
}

type workflowOutputError struct{ err error }

func (e workflowOutputError) Error() string { return e.err.Error() }

func (s *Store) generateWorkflowMarkdown(ctx context.Context, model *inference.Client, projectID string, task Task, input string) (string, error) {
	var key, evidence, options string
	if err := s.db.QueryRowContext(ctx, `SELECT workflow_key, evidence_json, options_json FROM workflow_runs WHERE run_id = ?`,
		task.workflowRunID).Scan(&key, &evidence, &options); err != nil {
		return "", err
	}
	stages := workflowStages[key]
	stage := stages[task.workflowStep]
	var sources []reviewSource
	first := 0
	if key == paperWorkflow {
		if stage.key == "structure" {
			_, manuscript, err := s.workflowArtifact(ctx, projectID, task.workflowRunID, "manuscript")
			if err != nil {
				return "", err
			}
			return paperCheck(manuscript), nil
		}
		input += "\n\n本轮写作设置（JSON）：\n" + options
	}
	if key == reviewWorkflow || key == paperWorkflow {
		if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
			return "", err
		}
		if stage.key == "sources" {
			return reviewSourcesMarkdown(sources), nil
		}
		input += "\n\n本轮固定的文献元数据（JSON）：\n" + evidence
		if key == reviewWorkflow {
			first = 1
		}
	}
	previous, err := s.workflowContext(ctx, task, stages, first)
	if err != nil {
		return "", err
	}
	markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input+previous)
	if err == nil && (key == reviewWorkflow || key == paperWorkflow) {
		if invalid := checkWorkflowCitations(markdown, sources, key == reviewWorkflow); invalid != nil {
			return "", workflowOutputError{invalid}
		}
	}
	return markdown, err
}

// ResumeQueuedTasks starts the persisted work that had not begun at shutdown.
func (s *Store) ResumeQueuedTasks(ctx context.Context, model *inference.Client) error {
	if model == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM tasks WHERE status = 'QUEUED'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var projects []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return err
		}
		projects = append(projects, projectID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, projectID := range projects {
		s.kickTaskQueue(projectID, model)
	}
	return nil
}
