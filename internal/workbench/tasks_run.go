package workbench

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"research-agent/internal/inference"
)

//go:embed prompts/research_plan.md
var researchPlanPrompt string

const (
	planTimeout = 45 * time.Second
	planMaxSize = 32 << 10
	taskDBLimit = 5 * time.Second
)

type taskWorkers struct {
	mu      sync.Mutex
	active  map[string]*taskWorker
	closing bool
	wg      sync.WaitGroup
}

type taskWorker struct {
	taskID string
	stop   context.CancelFunc
}

func (s *Store) kickTaskQueue(projectID string, model *inference.Client) {
	workers := &s.tasks
	workers.mu.Lock()
	defer workers.mu.Unlock()
	if workers.closing || workers.active[projectID] != nil {
		return
	}
	if workers.active == nil {
		workers.active = make(map[string]*taskWorker)
	}
	worker := &taskWorker{}
	workers.active[projectID] = worker
	workers.wg.Add(1)
	go s.drainTaskQueue(projectID, worker, model)
}

func (s *Store) drainTaskQueue(projectID string, worker *taskWorker, model *inference.Client) {
	workers := &s.tasks
	defer workers.wg.Done()
	for {
		workers.mu.Lock()
		if workers.closing {
			delete(workers.active, projectID)
			workers.mu.Unlock()
			return
		}
		// Hold the lock through the claim so cancellation can always find
		// the new context and a concurrent confirmation cannot lose its wakeup.
		claimCtx, stopClaim := context.WithTimeout(context.Background(), taskDBLimit)
		task, err := s.claimQueuedTask(claimCtx, projectID)
		stopClaim()
		if err != nil {
			delete(workers.active, projectID)
			workers.mu.Unlock()
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Error("claim research task", "project_id", projectID, "error", err)
			}
			return
		}
		deadline := planTimeout
		if task.workflowRunID != "" || task.reviewVersionID != "" || task.selectionEdit || task.findingRecheck {
			deadline = 2 * time.Minute
		}
		ctx, stop := context.WithTimeout(context.Background(), deadline)
		worker.taskID, worker.stop = task.TaskID, stop
		workers.mu.Unlock()

		result, err := s.generateTaskResult(ctx, model, projectID, task)
		status, message := planOutcome(ctx.Err(), err, task.TaskID)
		if task.workflowRunID != "" && status == taskFailed {
			message = "此步骤未生成成果。检查模型连接后，可从此步骤继续。"
			var invalid workflowOutputError
			if errors.As(err, &invalid) {
				message = invalid.Error() + "可以继续重试此步骤。"
			}
		}
		if task.reviewVersionID != "" && status == taskFailed {
			message = "评审未完成。检查模型连接后，可按原稿版本重试。"
			if errors.Is(err, errManuscriptReviewOutput) {
				message = "评审结果的评分、格式或原文定位有误，可以重试。"
			}
		}
		if task.selectionEdit && status == taskFailed {
			message = "生成修改建议失败，可重新提交。"
			if errors.Is(err, errSelectionOutput) {
				message = "模型没有返回可用的替换建议，可调整要求后重新提交。"
			}
		}
		if task.findingRecheck && (status == taskFailed || status == taskInterrupted) {
			message = "复查未完成，可按已保存的应用版本重试。"
			if errors.Is(err, errFindingRecheckOutput) {
				message = "模型未返回有效的复查结论，可重试。"
			}
		}
		stop()
		saveCtx, stopSave := context.WithTimeout(context.Background(), taskDBLimit)
		err = s.finishTask(saveCtx, projectID, task, status, message, result)
		stopSave()
		if err != nil {
			slog.Error("finish research task", "task_id", task.TaskID, "error", err)
		}
	}
}

func (s *Store) stopTaskRun(projectID, taskID string) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if worker := s.tasks.active[projectID]; worker != nil && worker.taskID == taskID {
		worker.stop()
	}
}

func (workers *taskWorkers) close() {
	workers.mu.Lock()
	workers.closing = true
	for _, worker := range workers.active {
		if worker.stop != nil {
			worker.stop()
		}
	}
	workers.mu.Unlock()
	workers.wg.Wait()
}

type taskResult struct {
	markdown  string
	findings  []reviewFinding
	topicData string
}

func (s *Store) generateTaskResult(ctx context.Context, model *inference.Client, projectID string, task Task) (taskResult, error) {
	switch {
	case task.reviewVersionID != "":
		return s.generateManuscriptReview(ctx, model, projectID, task)
	case task.findingRecheck:
		markdown, err := s.generateFindingRecheck(ctx, model, projectID, task)
		return taskResult{markdown: markdown}, err
	case task.selectionEdit:
		markdown, err := s.generateSelectionEdit(ctx, model, projectID, task)
		return taskResult{markdown: markdown}, err
	}
	project, err := s.getProject(ctx, projectID)
	if err != nil {
		return taskResult{}, err
	}
	input := fmt.Sprintf("项目标题：%q\n项目类型：%q", project.Title, project.TemplateID)
	if project.Goal != "" {
		input += fmt.Sprintf("\n研究目标：%q", project.Goal)
	}
	if project.Deliverables != "" {
		input += fmt.Sprintf("\n预期交付物：%q", project.Deliverables)
	}
	input += "\n任务要求：\n" + task.Instruction
	if task.workflowRunID != "" {
		return s.generateWorkflowResult(ctx, model, project, task, input)
	}
	if task.SourceTurnID != "" {
		source, err := s.taskTurnContext(ctx, projectID, task.SourceTurnID)
		if err != nil {
			return taskResult{}, err
		}
		input += source
	}
	if len(task.Inputs) > 0 {
		excerpts, err := s.taskInputContext(task.Inputs)
		if err != nil {
			return taskResult{}, err
		}
		input += excerpts
	}
	markdown, err := streamTaskMarkdown(ctx, model, researchPlanPrompt, input)
	return taskResult{markdown: markdown}, err
}

func streamTaskMarkdown(ctx context.Context, model *inference.Client, prompt, input string) (string, error) {
	return streamTaskMarkdownWithBudget(ctx, model, prompt, input, 0)
}

func streamTaskMarkdownWithBudget(ctx context.Context, model *inference.Client, prompt, input string, tokens int) (string, error) {
	messages := []inference.Message{
		{Role: "system", Content: strings.TrimSpace(prompt)},
		{Role: "user", Content: input},
	}
	var output strings.Builder
	err := model.StreamWithBudget(ctx, messages, tokens, func(delta string) error {
		if len(delta) > planMaxSize-output.Len() {
			return errors.New("research plan exceeds output limit")
		}
		output.WriteString(delta)
		return nil
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output.String()) == "" {
		return "", errors.New("research plan is empty")
	}
	return output.String(), nil
}

func planOutcome(contextErr, generationErr error, taskID string) (status, message string) {
	switch {
	case errors.Is(contextErr, context.Canceled):
		// A persisted cancellation takes precedence when the result is saved.
		return taskInterrupted, "服务关闭中断了此任务。"
	case errors.Is(contextErr, context.DeadlineExceeded):
		slog.Warn("research plan timed out", "task_id", taskID)
		return taskFailed, "生成研究计划超时，可以提议一项新任务。"
	case generationErr != nil:
		// Provider payloads are untrusted and may include sensitive data.
		slog.Warn("research plan generation failed", "task_id", taskID)
		return taskFailed, "未能生成研究计划。请检查模型连接，或提议一项新任务。"
	default:
		return taskSucceeded, ""
	}
}
