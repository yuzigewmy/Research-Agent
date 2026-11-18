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
		if task.workflowRunID != "" {
			deadline = 2 * time.Minute
		}
		ctx, stop := context.WithTimeout(context.Background(), deadline)
		worker.taskID, worker.stop = task.TaskID, stop
		workers.mu.Unlock()

		markdown, err := s.generateTaskMarkdown(ctx, model, projectID, task)
		status, message := planOutcome(ctx.Err(), err, task.TaskID)
		if task.workflowRunID != "" && status == taskFailed {
			message = "此步骤未生成成果。检查模型连接后，可从此步骤继续。"
			var invalid workflowOutputError
			if errors.As(err, &invalid) {
				message = invalid.Error() + "可以继续重试此步骤。"
			}
		}
		stop()
		saveCtx, stopSave := context.WithTimeout(context.Background(), taskDBLimit)
		err = s.finishTask(saveCtx, projectID, task, status, message, markdown)
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

func (s *Store) generateTaskMarkdown(ctx context.Context, model *inference.Client, projectID string, task Task) (string, error) {
	project, err := s.getProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	input := fmt.Sprintf("项目标题：%q\n项目类型：%q\n任务要求：\n%s", project.Title, project.TemplateID, task.Instruction)
	if task.workflowRunID != "" {
		return s.generateWorkflowMarkdown(ctx, model, projectID, task, input)
	}
	return streamTaskMarkdown(ctx, model, researchPlanPrompt, input)
}

func streamTaskMarkdown(ctx context.Context, model *inference.Client, prompt, input string) (string, error) {
	messages := []inference.Message{
		{Role: "system", Content: strings.TrimSpace(prompt)},
		{Role: "user", Content: input},
	}
	var output strings.Builder
	err := model.Stream(ctx, messages, func(delta string) error {
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
