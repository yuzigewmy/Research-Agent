package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	workflowStart          = "start"
	workflowContinue       = "continue"
	workflowRerun          = "rerun"
	workflowSkip           = "skip"
	workflowPause          = "pause"
	workflowCancel         = "cancel"
	workflowPausing        = "PAUSING"
	workflowPaused         = "PAUSED"
	workflowDone           = "COMPLETED"
	workflowWaiting        = "WAITING_CHECKPOINT"
	workflowRangeCompleted = "RANGE_COMPLETED"
	workflowStepStale      = "STALE"
	workflowThroughLast    = -1
)

var (
	errWorkflowState      = errors.New("workflow action is invalid in the current state")
	errWorkflowRange      = errors.New("invalid workflow range")
	errWorkflowStep       = errors.New("unknown workflow step")
	errWorkflowDependency = errors.New("a preceding workflow step is not complete")
	errReviewSources      = errors.New("literature review needs at least one verified work")
	errReviewTarget       = errors.New("invalid literature source target")
)

const experimentSchema = `
	CREATE TABLE experiment_runs (
		ordinal INTEGER PRIMARY KEY,
		run_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		revision INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL,
		step_no INTEGER NOT NULL DEFAULT 0,
		checkpoints INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX experiment_latest ON experiment_runs(project_id, ordinal DESC);
	CREATE TABLE experiment_requests (
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		request_id TEXT NOT NULL,
		run_id TEXT NOT NULL REFERENCES experiment_runs(run_id),
		action TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		PRIMARY KEY (project_id, request_id)
	);
	ALTER TABLE tasks ADD COLUMN experiment_run_id TEXT REFERENCES experiment_runs(run_id);
	ALTER TABLE tasks ADD COLUMN experiment_step INTEGER;
	CREATE INDEX experiment_tasks ON tasks(experiment_run_id, experiment_step, ordinal DESC);
	PRAGMA user_version = 4;
`

const workflowSchema = `
	ALTER TABLE experiment_runs RENAME TO workflow_runs;
	ALTER TABLE experiment_requests RENAME TO workflow_requests;
	ALTER TABLE tasks RENAME COLUMN experiment_run_id TO workflow_run_id;
	ALTER TABLE tasks RENAME COLUMN experiment_step TO workflow_step;
	ALTER TABLE workflow_runs ADD COLUMN workflow_key TEXT NOT NULL DEFAULT 'experiment-v1';
	ALTER TABLE workflow_runs ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE workflow_runs ADD COLUMN target_paper_count INTEGER NOT NULL DEFAULT 0;
	PRAGMA user_version = 11;
`

const workflowRangeSchema = `
	ALTER TABLE workflow_runs ADD COLUMN range_start INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE workflow_runs ADD COLUMN range_end INTEGER NOT NULL DEFAULT -1;
	ALTER TABLE workflow_requests ADD COLUMN through_step TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 19;
`

const workflowRerunSchema = `
	ALTER TABLE tasks ADD COLUMN workflow_superseded INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE workflow_requests ADD COLUMN step_key TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 20;
`

type WorkflowScope struct {
	StartStep string `json:"startStep"`
	EndStep   string `json:"endStep"`
}

type WorkflowRun struct {
	RunID            string         `json:"runId"`
	WorkflowKey      string         `json:"workflowKey"`
	Revision         int            `json:"revision"`
	Status           string         `json:"status"`
	Checkpoints      bool           `json:"checkpoints"`
	CreatedAt        string         `json:"createdAt"`
	TargetPaperCount int            `json:"targetPaperCount,omitempty"`
	SourceCount      int            `json:"sourceCount,omitempty"`
	Language         string         `json:"language,omitempty"`
	Scope            WorkflowScope  `json:"scope"`
	Steps            []WorkflowStep `json:"steps"`
	AcceptStepKey    string         `json:"acceptStepKey,omitempty"`
	SkipStepKey      string         `json:"skipStepKey,omitempty"`
	TopicChoice      *topicChoice   `json:"topicChoice,omitempty"`
	step             int
	rangeStart       int
	rangeEnd         int
	stepTaskID       string
	selectedTopic    int
}

type WorkflowStep struct {
	Key           string                  `json:"key"`
	Title         string                  `json:"title"`
	Status        string                  `json:"status"`
	Message       string                  `json:"message,omitempty"`
	ArtifactID    string                  `json:"artifactId,omitempty"`
	ArtifactCount int                     `json:"artifactCount,omitempty"`
	Source        *workflowArtifactSource `json:"source,omitempty"`
}

type workflowRequest struct {
	RequestID        string        `json:"requestId"`
	RunID            string        `json:"runId"`
	ExpectedRevision int           `json:"expectedRevision"`
	Checkpoints      bool          `json:"checkpoints"`
	ThroughStep      string        `json:"throughStep"`
	StepKey          string        `json:"stepKey"`
	Reason           string        `json:"reason"`
	CandidateNumber  int           `json:"candidateNumber"`
	TargetPaperCount int           `json:"targetPaperCount"`
	Paper            *paperOptions `json:"paper"`
	Topic            *topicOptions `json:"topic"`
}

func (s *Store) latestWorkflow(ctx context.Context, projectID string) (WorkflowRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRun{}, err
	}
	defer tx.Rollback()
	return readWorkflow(ctx, tx, projectID, "")
}

func readWorkflow(ctx context.Context, tx *sql.Tx, projectID, runID string) (WorkflowRun, error) {
	var run WorkflowRun
	var skipReason string
	err := tx.QueryRowContext(ctx, `SELECT w.run_id, w.workflow_key, w.revision, w.status, w.checkpoints, w.created_at, w.step_no,
		w.target_paper_count, json_array_length(w.evidence_json), COALESCE(json_extract(w.options_json, '$.language'), ''),
		w.range_start, w.range_end, COALESCE(r.skip_reason, ''), w.selected_topic_number
		FROM workflow_runs w LEFT JOIN workflow_requests r ON r.project_id = w.project_id AND r.request_id = w.skipped_request_id
		WHERE w.project_id = ? AND (? = '' OR w.run_id = ?) ORDER BY w.ordinal DESC LIMIT 1`, projectID, runID, runID).
		Scan(&run.RunID, &run.WorkflowKey, &run.Revision, &run.Status, &run.Checkpoints, &run.CreatedAt, &run.step,
			&run.TargetPaperCount, &run.SourceCount, &run.Language, &run.rangeStart, &run.rangeEnd, &skipReason, &run.selectedTopic)
	if err != nil {
		return WorkflowRun{}, err
	}
	stages := workflowStages[run.WorkflowKey]
	last := run.rangeEnd
	if last == workflowThroughLast {
		last = len(stages) - 1
	}
	if run.rangeStart < 0 || last < run.rangeStart || last >= len(stages) {
		return WorkflowRun{}, errors.New("stored workflow range is invalid")
	}
	run.Scope = WorkflowScope{StartStep: stages[run.rangeStart].key, EndStep: stages[last].key}
	run.Steps = make([]WorkflowStep, len(stages))
	for i, stage := range stages {
		run.Steps[i] = WorkflowStep{Key: stage.key, Title: stage.title, Status: "PENDING"}
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.workflow_step, t.task_id, t.status, t.message, t.workflow_superseded, history.artifacts,
		COALESCE(v.version_id, ''), COALESCE(v.path, ''), COALESCE(v.sha256, ''), t.workflow_topic_data
		FROM tasks t JOIN (
			SELECT MAX(ordinal) AS latest, COUNT(CASE WHEN status = 'SUCCEEDED' THEN 1 END) AS artifacts
			FROM tasks WHERE workflow_run_id = ? GROUP BY workflow_step
		) history ON t.ordinal = history.latest
		LEFT JOIN workspace_versions v ON v.version_id = t.workflow_source_version_id AND v.project_id = t.project_id`, run.RunID)
	if err != nil {
		return WorkflowRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var index, count int
		var superseded bool
		var id, status, message, topicData string
		var source workflowArtifactSource
		if err := rows.Scan(&index, &id, &status, &message, &superseded, &count,
			&source.VersionID, &source.Path, &source.SHA256, &topicData); err != nil {
			return WorkflowRun{}, err
		}
		step := &run.Steps[index]
		step.ArtifactCount = count
		if superseded {
			if count > 0 {
				step.Status = workflowStepStale
			}
			continue
		}
		if index == run.step {
			run.stepTaskID = id
		}
		step.Status, step.Message = status, message
		if status == taskSucceeded {
			step.ArtifactID = id
			if source.VersionID != "" {
				step.Source = &source
			}
			if run.WorkflowKey == topicWorkflow && index == topicShortlistStep {
				var choice topicChoice
				if err := json.Unmarshal([]byte(topicData), &choice); err != nil {
					return WorkflowRun{}, err
				}
				choice.SelectedNumber = run.selectedTopic
				run.TopicChoice = &choice
			}
		}
	}
	if skipReason != "" {
		step := &run.Steps[run.step]
		step.Status, step.Message = workflowStepSkipped, skipReason
	}
	run.AcceptStepKey = acceptableWorkflowStep(run, stages)
	run.SkipStepKey = skippableWorkflowStep(run, stages)
	return run, rows.Err()
}

func (s *Store) applyWorkflow(ctx context.Context, projectID, action string, req workflowRequest, modelReady bool) (WorkflowRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	defer tx.Rollback()

	var template string
	if err := tx.QueryRowContext(ctx, `SELECT template_id FROM projects WHERE project_id = ?`, projectID).Scan(&template); err != nil {
		return WorkflowRun{}, false, err
	}
	key := projectWorkflows[template]
	if key == "" {
		return WorkflowRun{}, false, errWorkflowState
	}
	target, err := reviewTarget(key, action, req.TargetPaperCount)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	options, err := workflowOptions(key, action, req.Paper, req.Topic)
	if err != nil {
		return WorkflowRun{}, false, err
	}

	var runID, recordedAction, recordedEnd, recordedStep, recordedReason string
	var revision, recordedCandidate int
	err = tx.QueryRowContext(ctx, `SELECT run_id, action, expected_revision, through_step, step_key, skip_reason, topic_number
		FROM workflow_requests WHERE project_id = ? AND request_id = ?`, projectID, req.RequestID).
		Scan(&runID, &recordedAction, &revision, &recordedEnd, &recordedStep, &recordedReason, &recordedCandidate)
	if err == nil {
		if recordedAction != action || revision != req.ExpectedRevision || recordedEnd != req.ThroughStep ||
			recordedStep != req.StepKey || recordedReason != req.Reason || recordedCandidate != req.CandidateNumber ||
			(action != workflowStart && runID != req.RunID) {
			return WorkflowRun{}, false, errRequestConflict
		}
		run, err := readWorkflow(ctx, tx, projectID, runID)
		if err == nil && action == workflowStart {
			if req.Checkpoints != run.Checkpoints || target != run.TargetPaperCount {
				return WorkflowRun{}, false, errRequestConflict
			}
			if run.WorkflowKey == paperWorkflow || run.WorkflowKey == topicWorkflow {
				var saved string
				if err := tx.QueryRowContext(ctx, `SELECT options_json FROM workflow_runs WHERE run_id = ?`, runID).Scan(&saved); err != nil {
					return WorkflowRun{}, false, err
				}
				if saved != options {
					return WorkflowRun{}, false, errRequestConflict
				}
			}
		}
		return run, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, false, err
	}
	current, err := readWorkflow(ctx, tx, projectID, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, false, err
	}
	if action != workflowStart {
		if current.RunID != req.RunID || current.Revision != req.ExpectedRevision {
			return WorkflowRun{}, false, errTaskRevision
		}
		key = current.WorkflowKey
	}
	end, err := workflowEndIndex(workflowStages[key], req.ThroughStep)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	queuesWork := action == workflowStart || action == workflowContinue || action == workflowRerun
	if !queuesWork && req.ThroughStep != "" {
		return WorkflowRun{}, false, errWorkflowRange
	}
	switch action {
	case workflowStart:
		if current.RunID != "" && current.Status != workflowDone && current.Status != taskCancelled {
			return WorkflowRun{}, false, errWorkflowState
		}
		current = WorkflowRun{RunID: rand.Text(), WorkflowKey: key, rangeEnd: end}
	case workflowPause:
		if current.Status != taskQueued && current.Status != taskRunning {
			return WorkflowRun{}, false, errWorkflowState
		}
	case workflowCancel:
		switch current.Status {
		case taskQueued, taskRunning, workflowPaused, workflowWaiting, workflowWaitingTopic, workflowRangeCompleted, taskFailed, taskInterrupted:
		default:
			return WorkflowRun{}, false, errWorkflowState
		}
	case workflowRerun:
		switch current.Status {
		case taskQueued, taskRunning, workflowPausing, taskCancelling, taskCancelled:
			return WorkflowRun{}, false, errWorkflowState
		}
		step, err := rerunnableStep(current.Steps, req.StepKey)
		if err != nil {
			return WorkflowRun{}, false, err
		}
		if key == topicWorkflow {
			if step == topicContractStep && current.selectedTopic == 0 {
				return WorkflowRun{}, false, errWorkflowState
			}
			if step <= topicShortlistStep {
				current.selectedTopic = 0
			}
		}
		current.step, current.rangeStart, current.rangeEnd = step, step, step
	case workflowSelectTopic:
		if current.Status != workflowWaitingTopic || current.TopicChoice == nil {
			return WorkflowRun{}, false, errWorkflowState
		}
		if _, ok := current.TopicChoice.candidate(req.CandidateNumber); !ok {
			return WorkflowRun{}, false, errTopicCandidate
		}
		current.selectedTopic = req.CandidateNumber
		queuesWork = current.rangeEnd != topicShortlistStep
	case workflowSkip:
		if current.SkipStepKey == "" || current.SkipStepKey != req.StepKey {
			return WorkflowRun{}, false, errWorkflowState
		}
	case workflowContinue:
		switch current.Status {
		case workflowRangeCompleted:
			if end != workflowThroughLast && end < current.step {
				return WorkflowRun{}, false, errWorkflowRange
			}
			current.rangeStart, current.rangeEnd = current.step, end
		case workflowWaiting, workflowPaused, taskFailed, taskInterrupted:
			if req.ThroughStep != "" {
				return WorkflowRun{}, false, errWorkflowRange
			}
		default:
			return WorkflowRun{}, false, errWorkflowState
		}
	}
	if queuesWork && !modelReady {
		return WorkflowRun{}, false, errModelUnavailable
	}

	switch action {
	case workflowStart:
		evidence := "[]"
		if key == reviewWorkflow || key == paperWorkflow || key == topicWorkflow {
			limit := target
			if key != reviewWorkflow {
				limit = defaultReviewTarget
			}
			sources, err := freezeLiteratureSources(ctx, tx, projectID, limit)
			if err != nil {
				return WorkflowRun{}, false, err
			}
			if key == reviewWorkflow && len(sources) == 0 {
				return WorkflowRun{}, false, errReviewSources
			}
			snapshot, err := json.Marshal(sources)
			if err != nil {
				return WorkflowRun{}, false, err
			}
			evidence = string(snapshot)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs
			(run_id, project_id, status, checkpoints, created_at, workflow_key, evidence_json, target_paper_count, options_json, range_end)
			VALUES (?, ?, 'QUEUED', ?, ?, ?, ?, ?, ?, ?)`, current.RunID, projectID, req.Checkpoints,
			time.Now().UTC().Format(createdAtLayout), key, evidence, target, options, current.rangeEnd)
	case workflowPause, workflowCancel:
		runStatus := taskCancelled
		if action == workflowPause {
			runStatus = workflowPaused
		}
		if current.Status == taskQueued || current.Status == taskRunning {
			stepStatus := taskCancelled
			if current.Status == taskRunning {
				stepStatus, runStatus = taskCancelling, taskCancelling
				if action == workflowPause {
					runStatus = workflowPausing
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, revision = revision + 1
				WHERE project_id = ? AND task_id = ?`, stepStatus, projectID, current.stepTaskID); err != nil {
				return WorkflowRun{}, false, err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, revision = revision + 1 WHERE run_id = ?`, runStatus, current.RunID)
	case workflowSkip:
		_, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status = 'COMPLETED', revision = revision + 1,
			range_start = step_no, range_end = step_no, skipped_request_id = ? WHERE run_id = ?`, req.RequestID, current.RunID)
	case workflowRerun:
		// Retain prior attempts for downloads, but remove them from dependency resolution.
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET workflow_superseded = 1
			WHERE workflow_run_id = ? AND workflow_step >= ? AND workflow_superseded = 0`, current.RunID, current.step); err != nil {
			return WorkflowRun{}, false, err
		}
		fallthrough
	case workflowContinue, workflowSelectTopic:
		status := taskQueued
		if !queuesWork {
			status = workflowRangeCompleted
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, revision = revision + 1,
			step_no = ?, range_start = ?, range_end = ?, skipped_request_id = NULL, selected_topic_number = ? WHERE run_id = ?`,
			status, current.step, current.rangeStart, current.rangeEnd, current.selectedTopic, current.RunID)
	}
	if err != nil {
		return WorkflowRun{}, false, err
	}
	if queuesWork {
		if err := enqueueWorkflowStep(ctx, tx, projectID, current.RunID, current.step, workflowStages[current.WorkflowKey][current.step]); err != nil {
			return WorkflowRun{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_requests
		(project_id, request_id, run_id, action, expected_revision, through_step, step_key, skip_reason, topic_number)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, req.RequestID, current.RunID, action, req.ExpectedRevision,
		req.ThroughStep, req.StepKey, req.Reason, req.CandidateNumber); err != nil {
		return WorkflowRun{}, false, err
	}
	run, err := readWorkflow(ctx, tx, projectID, current.RunID)
	if err != nil {
		return WorkflowRun{}, false, err
	}
	return run, true, tx.Commit()
}

func rerunnableStep(steps []WorkflowStep, key string) (int, error) {
	for i, step := range steps {
		if step.Key != key {
			continue
		}
		switch step.Status {
		case taskSucceeded, workflowStepStale, workflowStepSkipped:
		default:
			return 0, errWorkflowState
		}
		for _, preceding := range steps[:i] {
			if preceding.Status != taskSucceeded {
				return 0, errWorkflowDependency
			}
		}
		return i, nil
	}
	return 0, errWorkflowStep
}

func workflowEndIndex(stages []workflowStage, throughStep string) (int, error) {
	if throughStep == "" {
		return workflowThroughLast, nil
	}
	for i, stage := range stages {
		if stage.key == throughStep {
			return i, nil
		}
	}
	return 0, errWorkflowRange
}

func enqueueWorkflowStep(ctx context.Context, tx *sql.Tx, projectID, runID string, step int, stage workflowStage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, workflow_run_id, workflow_step)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, ?, ?)`,
		projectID, rand.Text(), stage.title, time.Now().UTC().Format(createdAtLayout), projectID, runID, step)
	return err
}

func finishWorkflowStep(ctx context.Context, tx *sql.Tx, projectID string, task Task, status string) error {
	step := task.workflowStep
	var stages []workflowStage
	if status == taskSucceeded {
		var key string
		var checkpoints bool
		var end int
		if err := tx.QueryRowContext(ctx, `SELECT workflow_key, checkpoints, range_end FROM workflow_runs WHERE run_id = ?`, task.workflowRunID).
			Scan(&key, &checkpoints, &end); err != nil {
			return err
		}
		stages = workflowStages[key]
		if key == topicWorkflow && step == topicShortlistStep && !checkpoints {
			choice, err := readTopicStep[topicChoice](ctx, tx, task.workflowRunID, step)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET selected_topic_number = ? WHERE run_id = ?`,
				choice.RecommendedNumber, task.workflowRunID); err != nil {
				return err
			}
		}
		if step+1 == len(stages) {
			status = workflowDone
		} else {
			switch {
			case checkpoints && key == topicWorkflow && step == topicShortlistStep:
				status = workflowWaitingTopic
			case step == end:
				status = workflowRangeCompleted
			case checkpoints && stages[step].checkpoint:
				status = workflowWaiting
			default:
				status = taskQueued
			}
			step++
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET
		status = CASE WHEN ? = 'CANCELLED' AND status = 'PAUSING' THEN 'PAUSED' ELSE ? END,
		step_no = ?, revision = revision + 1 WHERE run_id = ?`, status, status, step, task.workflowRunID); err != nil {
		return err
	}
	if status == taskQueued {
		return enqueueWorkflowStep(ctx, tx, projectID, task.workflowRunID, step, stages[step])
	}
	return nil
}

func recoverInterruptedWorkflows(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE workflow_runs SET revision = revision + 1,
		status = CASE status WHEN 'PAUSING' THEN 'PAUSED' WHEN 'CANCELLING' THEN 'CANCELLED' ELSE 'INTERRUPTED' END
		WHERE status IN ('RUNNING', 'PAUSING', 'CANCELLING')`)
	return err
}
