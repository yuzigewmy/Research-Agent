package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	projectReady    = "READY"
	projectArchived = "ARCHIVED"
)

const projectLifecycleSchema = `
	ALTER TABLE projects ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
	DROP INDEX projects_created;
	CREATE INDEX projects_created ON projects (status, created_at DESC, project_id DESC);
	CREATE TABLE project_patch_requests (
		project_id TEXT NOT NULL REFERENCES projects (project_id),
		request_id TEXT NOT NULL,
		expected_revision INTEGER NOT NULL,
		display_name TEXT,
		status TEXT CHECK (status IN ('READY', 'ARCHIVED')),
		PRIMARY KEY (project_id, request_id)
	);
	PRAGMA user_version = 12;
`

const projectTurnModeSchema = `
	ALTER TABLE projects ADD COLUMN default_turn_mode TEXT NOT NULL DEFAULT 'CHAT'
		CHECK (default_turn_mode IN ('CHAT', 'CONFIRM'));
	ALTER TABLE project_patch_requests ADD COLUMN default_turn_mode TEXT
		CHECK (default_turn_mode IN ('CHAT', 'CONFIRM'));
	PRAGMA user_version = 30;
`

const projectTitleSchema = `
	ALTER TABLE projects ADD COLUMN created_title TEXT NOT NULL DEFAULT '';
	UPDATE projects SET created_title = title;
	ALTER TABLE project_patch_requests ADD COLUMN title TEXT;
	ALTER TABLE workflow_runs ADD COLUMN project_title TEXT NOT NULL DEFAULT '';
	UPDATE workflow_runs SET project_title = (SELECT title FROM projects WHERE projects.project_id = workflow_runs.project_id);
	PRAGMA user_version = 50;
`

const projectTechnicalLimitsSchema = `
	ALTER TABLE projects ADD COLUMN max_model_runs INTEGER NOT NULL DEFAULT 0
		CHECK (max_model_runs >= 0 AND max_model_runs <= 1000);
	ALTER TABLE projects ADD COLUMN max_output_tokens INTEGER NOT NULL DEFAULT 0
		CHECK (max_output_tokens = 0 OR (max_output_tokens >= 256 AND max_output_tokens <= 32768));
	ALTER TABLE project_patch_requests ADD COLUMN max_model_runs INTEGER
		CHECK (max_model_runs IS NULL OR (max_model_runs >= 0 AND max_model_runs <= 1000));
	ALTER TABLE project_patch_requests ADD COLUMN max_output_tokens INTEGER
		CHECK (max_output_tokens IS NULL OR max_output_tokens = 0 OR (max_output_tokens >= 256 AND max_output_tokens <= 32768));
	PRAGMA user_version = 64;
`

const (
	maxProjectModelRunsLimit    = 1000
	minProjectOutputTokensLimit = 256
	maxProjectOutputTokensLimit = 32768
)

var (
	errProjectRevision        = errors.New("project revision changed")
	errModelRunBudgetExceeded = errors.New("project model run budget exceeded")
)

type projectBudgetQuery interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func projectModelRunsUsed(ctx context.Context, db projectBudgetQuery, projectID string) (int, error) {
	var turnsCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE project_id = ?`, projectID).Scan(&turnsCount); err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT w.workflow_key, t.workflow_step
		FROM tasks t JOIN workflow_runs w ON w.run_id = t.workflow_run_id
		WHERE t.project_id = ? AND t.workflow_source_version_id IS NULL`, projectID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	workflowModelSteps := 0
	for rows.Next() {
		var key string
		var step int
		if err := rows.Scan(&key, &step); err != nil {
			return 0, err
		}
		stages := workflowStages[key]
		if step >= 0 && step < len(stages) && stages[step].prompt != "" {
			workflowModelSteps++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return turnsCount + workflowModelSteps, nil
}

func checkProjectModelRunBudget(ctx context.Context, db projectBudgetQuery, projectID string) error {
	var maxRuns int
	if err := db.QueryRowContext(ctx, `SELECT max_model_runs FROM projects WHERE project_id = ?`, projectID).Scan(&maxRuns); err != nil {
		return err
	}
	if maxRuns <= 0 {
		return nil
	}
	used, err := projectModelRunsUsed(ctx, db, projectID)
	if err != nil {
		return err
	}
	if used >= maxRuns {
		return errModelRunBudgetExceeded
	}
	return nil
}

func validProjectTitle(title string) bool {
	return title != "" && utf8.RuneCountInString(title) <= 200 && !strings.ContainsRune(title, 0)
}

// An omitted field binds as SQL NULL. An explicit empty string remains a value.
type projectPatchField struct{ sql.NullString }

func (field *projectPatchField) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("project fields must be strings")
	}
	field.NullString = sql.NullString{String: *value, Valid: true}
	return nil
}

type projectPatchIntField struct{ sql.NullInt64 }

func (field *projectPatchIntField) UnmarshalJSON(data []byte) error {
	var value *int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("project limit fields must be integers")
	}
	field.NullInt64 = sql.NullInt64{Int64: *value, Valid: true}
	return nil
}

type patchProjectInput struct {
	RequestID        string               `json:"requestId"`
	ExpectedRevision int                  `json:"expectedRevision"`
	Title            projectPatchField    `json:"title"`
	DisplayName      projectPatchField    `json:"displayName"`
	Status           projectPatchField    `json:"status"`
	DefaultTurnMode  projectPatchField    `json:"defaultTurnMode"`
	MaxModelRuns     projectPatchIntField `json:"maxModelRuns"`
	MaxOutputTokens  projectPatchIntField `json:"maxOutputTokens"`
}

func (s *Store) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	var input patchProjectInput
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	if !validRequestID(input.RequestID) || input.ExpectedRevision < 1 ||
		(!input.Title.Valid && !input.DisplayName.Valid && !input.Status.Valid && !input.DefaultTurnMode.Valid &&
			!input.MaxModelRuns.Valid && !input.MaxOutputTokens.Valid) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交有效的请求编号、正整数修订版本和要修改的项目设置。")
		return
	}
	input.Title.String = strings.TrimSpace(input.Title.String)
	if input.Title.Valid && !validProjectTitle(input.Title.String) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "研究题目须为 1 至 200 个字符，且不能包含空字符。")
		return
	}
	input.DisplayName.String = strings.TrimSpace(input.DisplayName.String)
	if utf8.RuneCountInString(input.DisplayName.String) > 200 || strings.ContainsRune(input.DisplayName.String, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "显示名最多 200 个字符，且不能包含空字符。")
		return
	}
	if input.Status.Valid && input.Status.String != projectReady && input.Status.String != projectArchived {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "项目状态必须是 READY 或 ARCHIVED。")
		return
	}

	if input.DefaultTurnMode.Valid && !validTurnMode(input.DefaultTurnMode.String) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择受支持的默认协作方式。")
		return
	}
	if input.MaxModelRuns.Valid && (input.MaxModelRuns.Int64 < 0 || input.MaxModelRuns.Int64 > maxProjectModelRunsLimit) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "单项目最大模型调用次数须为 0（不限）或 1 至 1000 的整数。")
		return
	}
	if input.MaxOutputTokens.Valid && input.MaxOutputTokens.Int64 != 0 &&
		(input.MaxOutputTokens.Int64 < minProjectOutputTokensLimit || input.MaxOutputTokens.Int64 > maxProjectOutputTokensLimit) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "单轮最大输出 token 须为 0（默认 8192）或 256 至 32768 的整数。")
		return
	}

	project, err := s.patchProject(r.Context(), r.PathValue("id"), input)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, project)
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于其他项目修改。")
	case errors.Is(err, errProjectRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "项目已更新，请查看最新版本。")
	default:
		writeStoreError(w, "patch project", err)
	}
}

func (s *Store) patchProject(ctx context.Context, id string, input patchProjectInput) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()

	// Reserve the receipt inside the write transaction. A failed CAS rolls it back.
	result, err := tx.ExecContext(ctx, `INSERT INTO project_patch_requests
		(project_id, request_id, expected_revision, title, display_name, status, default_turn_mode, max_model_runs, max_output_tokens)
		SELECT project_id, ?, ?, ?, ?, ?, ?, ?, ? FROM projects WHERE project_id = ?
		ON CONFLICT (project_id, request_id) DO NOTHING`,
		input.RequestID, input.ExpectedRevision, input.Title, input.DisplayName, input.Status, input.DefaultTurnMode,
		input.MaxModelRuns, input.MaxOutputTokens, id)
	if err != nil {
		return Project{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if inserted == 0 {
		var revision int
		var title, name, status, mode sql.NullString
		var maxRuns, maxTokens sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT expected_revision, title, display_name, status, default_turn_mode, max_model_runs, max_output_tokens FROM project_patch_requests
			WHERE project_id = ? AND request_id = ?`, id, input.RequestID).Scan(&revision, &title, &name, &status, &mode, &maxRuns, &maxTokens)
		if err != nil {
			return Project{}, err
		}
		if revision != input.ExpectedRevision || title != input.Title.NullString || name != input.DisplayName.NullString ||
			status != input.Status.NullString || mode != input.DefaultTurnMode.NullString ||
			maxRuns != input.MaxModelRuns.NullInt64 || maxTokens != input.MaxOutputTokens.NullInt64 {
			return Project{}, errRequestConflict
		}
		project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, id))
		if err != nil {
			return Project{}, err
		}
		project.ModelRunsUsed, err = projectModelRunsUsed(ctx, tx, id)
		return project, err
	}

	project, err := scanProject(tx.QueryRowContext(ctx, `UPDATE projects
		SET title = COALESCE(?, title), display_name = COALESCE(?, display_name), status = COALESCE(?, status),
		default_turn_mode = COALESCE(?, default_turn_mode),
		max_model_runs = COALESCE(?, max_model_runs),
		max_output_tokens = COALESCE(?, max_output_tokens),
		revision = revision + 1
		WHERE project_id = ? AND revision = ? RETURNING `+projectColumns,
		input.Title, input.DisplayName, input.Status, input.DefaultTurnMode,
		input.MaxModelRuns, input.MaxOutputTokens, id, input.ExpectedRevision))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, errProjectRevision
	}
	if err != nil {
		return Project{}, err
	}
	if project.TemplateID == engineeringTemplate && project.Status == projectArchived {
		if _, err := tx.ExecContext(ctx, `DELETE FROM engineering_previews WHERE project_id = ?`, id); err != nil {
			return Project{}, err
		}
	}
	project.ModelRunsUsed, err = projectModelRunsUsed(ctx, tx, id)
	if err != nil {
		return Project{}, err
	}
	return project, tx.Commit()
}
