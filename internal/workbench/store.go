package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	projectColumns = `project_id, title, display_name, template_id, status, revision, created_at, goal, deliverables, default_turn_mode`
	// Fixed-width UTC timestamps sort chronologically in SQLite.
	createdAtLayout = "2006-01-02T15:04:05.000000000Z"
)

var errRequestConflict = errors.New("request ID is already used for a different project")

// Project is the API representation of a research project.
type Project struct {
	ProjectID       string `json:"projectId"`
	Title           string `json:"title"`
	DisplayName     string `json:"displayName"`
	TemplateID      string `json:"templateId"`
	Status          string `json:"status"`
	Revision        int    `json:"revision"`
	CreatedAt       string `json:"createdAt"`
	Goal            string `json:"goal,omitempty"`
	Deliverables    string `json:"deliverables,omitempty"`
	DefaultTurnMode string `json:"defaultTurnMode"`
}

type createProjectInput struct {
	RequestID  string `json:"requestId"`
	Title      string `json:"title"`
	TemplateID string `json:"templateId"`
}

type projectCursor struct {
	createdAt string
	projectID string
}

// Store holds the workspace database and serializes access through one connection.
type Store struct {
	db         *sql.DB
	tasks      taskWorkers
	turns      turnWorkers
	objectsDir string
}

// Open creates the data directory if necessary and opens and migrates its database.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	filename, err := filepath.Abs(filepath.Join(dataDir, "projects.sqlite3"))
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	objectsDir := filepath.Join(filepath.Dir(filename), "objects")
	if err := os.MkdirAll(objectsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create object directory: %w", err)
	}
	filename = filepath.ToSlash(filename)
	if !strings.HasPrefix(filename, "/") {
		filename = "/" + filename
	}
	dsn := url.URL{Scheme: "file", Path: filename, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db, objectsDir: objectsDir, turns: turnWorkers{active: make(map[string]context.CancelFunc)}}, nil
}

// Close stops replies and research tasks before releasing the database connection.
func (s *Store) Close() error {
	s.turns.close()
	s.tasks.close()
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 30 {
		return fmt.Errorf("unsupported database version %d", version)
	}
	if version == 0 {
		if _, err := tx.Exec(`
			CREATE TABLE projects (
				project_id TEXT NOT NULL PRIMARY KEY,
				request_id TEXT NOT NULL UNIQUE,
				title TEXT NOT NULL,
				template_id TEXT NOT NULL,
				status TEXT NOT NULL,
				revision INTEGER NOT NULL,
				created_at TEXT NOT NULL
			);
			CREATE INDEX projects_created ON projects (created_at DESC, project_id DESC);
			PRAGMA user_version = 1;
		`); err != nil {
			return err
		}
	}
	if version < 2 {
		if _, err := tx.Exec(turnsSchema); err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err := tx.Exec(tasksSchema); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err := tx.Exec(experimentSchema); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err := tx.Exec(materialsSchema); err != nil {
			return err
		}
	}
	if version < 6 {
		if _, err := tx.Exec(turnReferenceSchema); err != nil {
			return err
		}
	}
	if version < 7 {
		if _, err := tx.Exec(workspaceSchema); err != nil {
			return err
		}
	}
	if version < 8 {
		if _, err := tx.Exec(wordExportSchema); err != nil {
			return err
		}
	}
	if version < 9 {
		if _, err := tx.Exec(surveySchema); err != nil {
			return err
		}
	}
	if version < 10 {
		if _, err := tx.Exec(literatureSchema); err != nil {
			return err
		}
	}
	if version < 11 {
		if _, err := tx.Exec(workflowSchema); err != nil {
			return err
		}
	}
	if version < 12 {
		if _, err := tx.Exec(projectLifecycleSchema); err != nil {
			return err
		}
	}
	if version < 13 {
		if _, err := tx.Exec(paperOptionsSchema); err != nil {
			return err
		}
	}
	if version < 14 {
		if _, err := tx.Exec(manuscriptReviewSchema); err != nil {
			return err
		}
	}
	if version < 15 {
		if _, err := tx.Exec(selectionEditSchema); err != nil {
			return err
		}
	}
	if version < 16 {
		if _, err := tx.Exec(reviewFindingSchema); err != nil {
			return err
		}
	}
	if version < 17 {
		if _, err := tx.Exec(findingRecheckSchema); err != nil {
			return err
		}
	}
	if version < 18 {
		if _, err := tx.Exec(taskEditSchema); err != nil {
			return err
		}
	}
	if version < 19 {
		if _, err := tx.Exec(workflowRangeSchema); err != nil {
			return err
		}
	}
	if version < 20 {
		if _, err := tx.Exec(workflowRerunSchema); err != nil {
			return err
		}
	}
	if version < 21 {
		if _, err := tx.Exec(workflowAcceptanceSchema); err != nil {
			return err
		}
	}
	if version < 22 {
		if _, err := tx.Exec(workflowSkipSchema); err != nil {
			return err
		}
	}
	if version < 23 {
		if _, err := tx.Exec(workflowTopicSchema); err != nil {
			return err
		}
	}
	if version < 24 {
		if _, err := tx.Exec(taskInputSchema); err != nil {
			return err
		}
	}
	if version < 25 {
		if _, err := tx.Exec(taskTurnSchema); err != nil {
			return err
		}
	}
	if version < 26 {
		if _, err := tx.Exec(turnTaskResultsSchema); err != nil {
			return err
		}
	}
	if version < 27 {
		if _, err := tx.Exec(projectDraftSchema); err != nil {
			return err
		}
	}
	if version < 28 {
		if _, err := tx.Exec(draftMaterialSchema); err != nil {
			return err
		}
	}
	if version < 29 {
		if _, err := tx.Exec(turnProposalSchema); err != nil {
			return err
		}
	}
	if version < 30 {
		if _, err := tx.Exec(projectTurnModeSchema); err != nil {
			return err
		}
	}
	if err := recoverDraftPlans(tx); err != nil {
		return err
	}
	if err := recoverInterruptedTurns(tx); err != nil {
		return err
	}
	if err := recoverInterruptedTasks(tx); err != nil {
		return err
	}
	if err := recoverInterruptedWorkflows(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) createProject(ctx context.Context, input createProjectInput) (Project, bool, error) {
	project := Project{
		ProjectID: rand.Text(), Title: input.Title, TemplateID: input.TemplateID,
		Status: projectReady, Revision: 1, CreatedAt: time.Now().UTC().Format(createdAtLayout), DefaultTurnMode: turnModeChat,
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (project_id, title, template_id, status, revision, created_at, request_id) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (request_id) DO NOTHING
	`, project.ProjectID, project.Title, project.TemplateID, project.Status, project.Revision, project.CreatedAt, input.RequestID)
	if err != nil {
		return Project{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Project{}, false, err
	}
	if inserted == 1 {
		return project, true, nil
	}

	// A retry reads the immutable intent stored by the winning insert.
	project, err = scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE request_id = ?`, input.RequestID))
	if err != nil {
		return Project{}, false, err
	}
	if project.Title != input.Title || project.TemplateID != input.TemplateID {
		return Project{}, false, errRequestConflict
	}
	return project, false, nil
}

func (s *Store) getProject(ctx context.Context, id string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, id))
}

func (s *Store) listProjects(ctx context.Context, cursor projectCursor, limit int, status, search string) ([]Project, error) {
	query := `SELECT ` + projectColumns + ` FROM projects WHERE status = ?`
	args := []any{status}
	if search != "" {
		query += ` AND (instr(title, ?) > 0 OR instr(display_name, ?) > 0)`
		args = append(args, search, search)
	}
	if cursor.projectID != "" {
		query += ` AND (created_at, project_id) < (?, ?)`
		args = append(args, cursor.createdAt, cursor.projectID)
	}
	query += ` ORDER BY created_at DESC, project_id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make([]Project, 0, limit)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var project Project
	err := row.Scan(&project.ProjectID, &project.Title, &project.DisplayName, &project.TemplateID, &project.Status,
		&project.Revision, &project.CreatedAt, &project.Goal, &project.Deliverables, &project.DefaultTurnMode)
	return project, err
}
