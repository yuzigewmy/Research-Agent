package workbench

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"research-agent/internal/inference"
)

//go:embed prompts/experiment_scope.md
var experimentScopePrompt string

//go:embed prompts/experiment_plan.md
var experimentPlanPrompt string

//go:embed prompts/experiment_checklist.md
var experimentChecklistPrompt string

var experimentStages = [...]struct {
	key, title, prompt string
	checkpoint         bool
}{
	{key: "scope", title: "界定研究范围", prompt: experimentScopePrompt},
	{key: "plan", title: "编制实验计划", prompt: experimentPlanPrompt, checkpoint: true},
	{key: "checklist", title: "生成验收清单", prompt: experimentChecklistPrompt},
}

func (s *Store) experimentContext(ctx context.Context, task Task) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT experiment_step, markdown FROM tasks
		WHERE experiment_run_id = ? AND experiment_step < ? AND status = 'SUCCEEDED'
		ORDER BY experiment_step`, task.experimentRunID, task.experimentStep)
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
		fmt.Fprintf(&previous, "\n\n--- %s ---\n%s", experimentStages[stage].title, markdown)
	}
	return previous.String(), rows.Err()
}

// ResumeQueuedExperiments submits persisted stages that had not begun at shutdown.
func (s *Store) ResumeQueuedExperiments(ctx context.Context, model *inference.Client) error {
	if model == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM experiment_runs WHERE status = 'QUEUED'`)
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
