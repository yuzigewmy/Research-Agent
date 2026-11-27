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

func (s *Store) generateWorkflowResult(ctx context.Context, model *inference.Client, project Project, task Task) (taskResult, error) {
	projectID := project.ProjectID
	var key, evidence, options, sourceTurnID string
	var modelName, reasoningEffort, connection, stepModelsJSON string
	var selectedNumber int
	if err := s.db.QueryRowContext(ctx, `SELECT workflow_key, evidence_json, options_json, selected_topic_number,
		COALESCE(range_source_turn_id, ''), model_name, reasoning_effort, model_connection, step_models_json, project_title
		FROM workflow_runs WHERE run_id = ?`, task.workflowRunID).
		Scan(&key, &evidence, &options, &selectedNumber, &sourceTurnID, &modelName, &reasoningEffort, &connection, &stepModelsJSON, &project.Title); err != nil {
		return taskResult{}, err
	}
	input := researchTaskInput(project, task.Instruction)
	stages := workflowStagesForRun(key, options)
	stage := stages[task.workflowStep]
	var stepModels workflowStepModels
	if err := json.Unmarshal([]byte(stepModelsJSON), &stepModels); err != nil {
		return taskResult{}, err
	}
	if selected, ok := stepModels[stage.key]; ok {
		modelName, reasoningEffort = selected.Model, selected.ReasoningEffort
	}
	if modelName != "" {
		if connection != model.ConnectionFingerprint() {
			return taskResult{}, workflowOutputError{errWorkflowModelConnection}
		}
		model = model.ForModel(modelName, reasoningEffort)
	}
	if project.MaxOutputTokens > 0 {
		model = model.WithMaxOutputTokens(project.MaxOutputTokens)
	}
	if sourceTurnID != "" {
		source, err := s.taskTurnContext(ctx, projectID, sourceTurnID)
		if err != nil {
			return taskResult{}, err
		}
		input += source
	}
	if key == topicWorkflow {
		return s.generateTopicStep(ctx, model, task, input, evidence, options, selectedNumber)
	}
	if isCourseWorkflow(key) {
		return s.generateCourseStep(ctx, model, project, task, key, input, evidence, options)
	}
	if isSoftwareRegistrationWorkflow(key) {
		return s.generateSoftwareRegistrationStep(ctx, model, project, task, stage, input, options)
	}
	if isPatentWorkflow(key) {
		return s.generatePatentStep(ctx, model, project, task, key, input, options)
	}
	if isExpandedWorkflow(key) {
		return s.generateExpandedWorkflowStep(ctx, model, project, task, key, input, evidence, options)
	}
	var sources []reviewSource
	first := 0
	usesLiterature := key == reviewWorkflow || key == paperWorkflow || isProposalWorkflow(key)
	if key == paperWorkflow {
		if stage.key == "structure" {
			_, manuscript, err := s.workflowArtifact(ctx, projectID, task.workflowRunID, "manuscript")
			if err != nil {
				return taskResult{}, err
			}
			return taskResult{markdown: paperCheck(manuscript)}, nil
		}
		input += "\n\n本轮写作设置（JSON）：\n" + options
	}
	if usesLiterature {
		if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
			return taskResult{}, err
		}
		if stage.key == "sources" {
			return taskResult{markdown: reviewSourcesMarkdown(sources)}, nil
		}
		input += "\n\n本轮固定的文献元数据（JSON）：\n" + evidence
		if key != paperWorkflow {
			first = 1
		}
	}
	if isProposalWorkflow(key) {
		input += "\n\n本轮开题设置（JSON）：\n" + options
		if stage.key == "check" || (key == proposalWorkflow && stage.key != "sources" && stage.key != "outline") {
			var goals proposalTargets
			if err := json.Unmarshal([]byte(options), &goals); err != nil {
				return taskResult{}, err
			}
			if stage.key == "check" {
				return s.checkProposal(ctx, projectID, task.workflowRunID, goals, sources)
			}
			return s.sectionedProposalResult(ctx, model, project, task, stage, input, goals, sources)
		}
	}
	previous, err := s.workflowContext(ctx, task, stages, first)
	if err != nil {
		return taskResult{}, err
	}
	markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input+previous)
	if err == nil && usesLiterature {
		if invalid := checkWorkflowCitations(markdown, sources, key == reviewWorkflow); invalid != nil {
			return taskResult{}, workflowOutputError{invalid}
		}
	}
	return taskResult{markdown: markdown}, err
}

// ResumeQueuedTasks starts the persisted work that had not begun at shutdown.
func (s *Store) ResumeQueuedTasks(ctx context.Context, model *inference.Client) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM tasks WHERE status = 'QUEUED' AND (? OR `+tasksWithoutModel+`)`, model != nil)
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
