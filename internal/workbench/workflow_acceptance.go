package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const workflowAccept = "accept"

const workflowAcceptanceSchema = `
	ALTER TABLE tasks ADD COLUMN workflow_source_version_id TEXT REFERENCES workspace_versions(version_id);
	ALTER TABLE workflow_requests ADD COLUMN accepted_artifact_id TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 21;
`

type workflowArtifactSource struct {
	VersionID string `json:"versionId"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
}

type workflowAcceptInput struct {
	RequestID        string `json:"requestId"`
	RunID            string `json:"runId"`
	ExpectedRevision int    `json:"expectedRevision"`
	StepKey          string `json:"stepKey"`
	SourceVersionID  string `json:"sourceVersionId"`
}

type workflowAcceptance struct {
	WorkflowRun
	AcceptedArtifactID string `json:"acceptedArtifactId"`
}

type invalidWorkflowArtifact string

func (err invalidWorkflowArtifact) Error() string { return string(err) }

func acceptableWorkflowStep(run WorkflowRun, stages []workflowStage) string {
	switch run.Status {
	case workflowRangeCompleted, workflowPaused, taskFailed, taskInterrupted:
	default:
		return ""
	}
	stage := stages[run.step]
	if !stage.acceptMarkdown {
		return ""
	}
	for _, preceding := range run.Steps[:run.step] {
		if preceding.Status != taskSucceeded {
			return ""
		}
	}
	return stage.key
}

func (api workflows) accept(w http.ResponseWriter, r *http.Request) {
	var input workflowAcceptInput
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if !validRequestID(input.RequestID) || !validRequestID(input.RunID) || input.ExpectedRevision < 1 ||
		!validRequestID(input.StepKey) || !validRequestID(input.SourceVersionID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择要验收的步骤和已保存的文件版本。")
		return
	}
	result, err := api.store.acceptWorkflowArtifact(r.Context(), r.PathValue("id"), input)
	var invalid invalidWorkflowArtifact
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case errors.Is(err, errWorkflowState):
		writeError(w, http.StatusConflict, "WORKFLOW_STATE_CONFLICT", "当前步骤尚不能用已有成果验收。")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该工作流或文件版本不存在。")
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARTIFACT", invalid.Error())
	default:
		writeTaskError(w, "accept workflow artifact", err)
	}
}

func (s *Store) acceptWorkflowArtifact(ctx context.Context, projectID string, input workflowAcceptInput) (workflowAcceptance, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflowAcceptance{}, err
	}
	defer tx.Rollback()

	var runID, action, stepKey, artifactID, sourceVersionID string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT r.run_id, r.action, r.expected_revision, r.step_key, r.accepted_artifact_id,
		COALESCE(t.workflow_source_version_id, '') FROM workflow_requests r
		LEFT JOIN tasks t ON t.project_id = r.project_id AND t.task_id = r.accepted_artifact_id
		WHERE r.project_id = ? AND r.request_id = ?`, projectID, input.RequestID).
		Scan(&runID, &action, &revision, &stepKey, &artifactID, &sourceVersionID)
	if err == nil {
		if action != workflowAccept || runID != input.RunID || revision != input.ExpectedRevision ||
			stepKey != input.StepKey || sourceVersionID != input.SourceVersionID {
			return workflowAcceptance{}, errRequestConflict
		}
		run, err := readWorkflow(ctx, tx, projectID, runID)
		return workflowAcceptance{WorkflowRun: run, AcceptedArtifactID: artifactID}, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return workflowAcceptance{}, err
	}

	run, err := readWorkflow(ctx, tx, projectID, "")
	if err != nil {
		return workflowAcceptance{}, err
	}
	if run.RunID != input.RunID || run.Revision != input.ExpectedRevision {
		return workflowAcceptance{}, errTaskRevision
	}
	if run.AcceptStepKey != input.StepKey {
		return workflowAcceptance{}, errWorkflowState
	}
	file, err := lookupWorkspaceVersion(ctx, tx, projectID, input.SourceVersionID)
	if err != nil {
		return workflowAcceptance{}, err
	}
	markdown, err := s.acceptableWorkflowMarkdown(ctx, tx, run, file)
	if err != nil {
		return workflowAcceptance{}, err
	}

	artifactID = rand.Text()
	stage := workflowStages[run.WorkflowKey][run.step]
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
		(project_id, task_id, instruction, status, created_at, markdown, workflow_run_id, workflow_step, workflow_source_version_id)
		VALUES (?, ?, ?, 'SUCCEEDED', ?, ?, ?, ?, ?)`, projectID, artifactID, stage.title,
		time.Now().UTC().Format(createdAtLayout), markdown, run.RunID, run.step, file.VersionID); err != nil {
		return workflowAcceptance{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_requests
		(project_id, request_id, run_id, action, expected_revision, step_key, accepted_artifact_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, input.RequestID, run.RunID, workflowAccept, input.ExpectedRevision,
		input.StepKey, artifactID); err != nil {
		return workflowAcceptance{}, err
	}
	// Acceptance settles one step and hands the next decision back to the user.
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET range_start = step_no, range_end = step_no WHERE run_id = ?`, run.RunID); err != nil {
		return workflowAcceptance{}, err
	}
	if err := finishWorkflowStep(ctx, tx, projectID, Task{workflowRunID: run.RunID, workflowStep: run.step}, taskSucceeded); err != nil {
		return workflowAcceptance{}, err
	}
	updated, err := readWorkflow(ctx, tx, projectID, run.RunID)
	if err != nil {
		return workflowAcceptance{}, err
	}
	return workflowAcceptance{WorkflowRun: updated, AcceptedArtifactID: artifactID}, tx.Commit()
}

func (s *Store) acceptableWorkflowMarkdown(ctx context.Context, tx *sql.Tx, run WorkflowRun, file WorkspaceFile) (string, error) {
	ext := strings.ToLower(path.Ext(file.Path))
	if (ext != ".md" && ext != ".markdown") || file.Size == 0 || file.Size > planMaxSize {
		return "", invalidWorkflowArtifact("请选择 32 KiB 以内的非空 Markdown 文件。")
	}
	document, err := s.readWorkspaceDocument(file)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(document.Content) || strings.TrimSpace(document.Content) == "" || strings.ContainsRune(document.Content, 0) {
		return "", invalidWorkflowArtifact("该 Markdown 版本没有可验收的 UTF-8 正文。")
	}
	if run.WorkflowKey == paperWorkflow || run.WorkflowKey == reviewWorkflow || run.WorkflowKey == topicWorkflow {
		var evidence string
		if err := tx.QueryRowContext(ctx, `SELECT evidence_json FROM workflow_runs WHERE run_id = ?`, run.RunID).Scan(&evidence); err != nil {
			return "", err
		}
		var sources []reviewSource
		if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
			return "", err
		}
		if err := checkWorkflowCitations(document.Content, sources, run.WorkflowKey == reviewWorkflow); err != nil {
			return "", invalidWorkflowArtifact(err.Error())
		}
	}
	return document.Content, nil
}
