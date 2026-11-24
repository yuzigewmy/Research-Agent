package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const automaticReviewSchema = `
	ALTER TABLE tasks ADD COLUMN automatic_review INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE automatic_reviews (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		request_id TEXT NOT NULL,
		source_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		working_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		max_rounds INTEGER NOT NULL CHECK(max_rounds BETWEEN 1 AND 10),
		target_score INTEGER NOT NULL CHECK(target_score BETWEEN 5 AND 9),
		criteria TEXT NOT NULL,
		rounds_json TEXT NOT NULL DEFAULT '[]',
		stop_reason TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (project_id, task_id),
		UNIQUE (project_id, request_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	CREATE INDEX automatic_reviews_source ON automatic_reviews(project_id, source_version_id);
	PRAGMA user_version = 32;
`

const (
	defaultReviewRounds  = 4
	defaultReviewScore   = 6
	maxReviewCriteria    = 4 << 10
	retryAutomaticReview = "retry-automatic-review"
)

type automaticReviewOptions struct {
	Target      string `json:"target"`
	MaxRounds   int    `json:"maxRounds"`
	TargetScore int    `json:"targetScore"`
	Criteria    string `json:"criteria,omitempty"`
}

type automaticReviewRound struct {
	Number    int             `json:"number"`
	Score     float64         `json:"score"`
	Summary   string          `json:"summary"`
	VersionID string          `json:"versionId"`
	SHA256    string          `json:"sha256"`
	Path      string          `json:"path"`
	Findings  []reviewFinding `json:"findings"`
}

type automaticReview struct {
	ReviewID string `json:"reviewId"`
	automaticReviewOptions
	modelSnapshot
	Status        string                 `json:"status"`
	Revision      int                    `json:"revision"`
	SourceSHA256  string                 `json:"sourceSha256"`
	CreatedAt     string                 `json:"createdAt"`
	Message       string                 `json:"message,omitempty"`
	StopReason    string                 `json:"stopReason,omitempty"`
	Report        string                 `json:"report,omitempty"`
	Rounds        []automaticReviewRound `json:"rounds,omitempty"`
	sourceVersion string
	workVersion   string
	ordinal       int64
}

const automaticReviewColumns = `t.ordinal, t.task_id, t.instruction, t.status, t.revision, t.created_at, t.message,
	a.max_rounds, a.target_score, a.criteria, a.stop_reason, a.source_version_id, a.working_version_id, v.sha256,
	t.model_name, t.reasoning_effort, t.model_connection`

const automaticReviewJoin = ` FROM automatic_reviews a JOIN tasks t ON t.project_id = a.project_id AND t.task_id = a.task_id
	JOIN workspace_versions v ON v.version_id = a.source_version_id WHERE a.project_id = ?`

func scanAutomaticReview(row interface{ Scan(...any) error }) (automaticReview, error) {
	var review automaticReview
	var rounds []byte
	err := row.Scan(&review.ordinal, &review.ReviewID, &review.Target, &review.Status, &review.Revision, &review.CreatedAt,
		&review.Message, &review.MaxRounds, &review.TargetScore, &review.Criteria, &review.StopReason, &review.sourceVersion,
		&review.workVersion, &review.SourceSHA256, &review.Model, &review.ReasoningEffort, &review.connection, &review.Report, &rounds)
	if err == nil {
		err = json.Unmarshal(rounds, &review.Rounds)
	}
	return review, err
}

func readAutomaticReview(ctx context.Context, db workspaceQuery, projectID, reviewID string) (automaticReview, error) {
	return scanAutomaticReview(db.QueryRowContext(ctx, `SELECT `+automaticReviewColumns+`, t.markdown, a.rounds_json`+automaticReviewJoin+
		` AND a.task_id = ?`, projectID, reviewID))
}

func (s *Store) listAutomaticReviews(ctx context.Context, projectID, versionID string, cursor int64, limit int) ([]automaticReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+automaticReviewColumns+`, '', 'null'`+automaticReviewJoin+
		` AND a.source_version_id = ? AND (? = 0 OR t.ordinal < ?) ORDER BY t.ordinal DESC LIMIT ?`, projectID, versionID, cursor, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]automaticReview, 0, limit)
	for rows.Next() {
		review, err := scanAutomaticReview(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, review)
	}
	return items, rows.Err()
}

func (s *Store) startAutomaticReview(ctx context.Context, projectID, versionID, requestID string, options automaticReviewOptions, model modelSnapshot) (automaticReview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return automaticReview{}, false, err
	}
	defer tx.Rollback()
	review, err := scanAutomaticReview(tx.QueryRowContext(ctx, `SELECT `+automaticReviewColumns+`, t.markdown, a.rounds_json`+automaticReviewJoin+
		` AND a.request_id = ?`, projectID, requestID))
	if err == nil {
		if review.sourceVersion != versionID || review.automaticReviewOptions != options {
			return automaticReview{}, false, errRequestConflict
		}
		return review, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return automaticReview{}, false, err
	}
	if model.Model == "" {
		return automaticReview{}, false, errModelUnavailable
	}
	id := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(project_id, task_id, instruction, status, created_at, queued_seq, automatic_review,
		model_name, reasoning_effort, model_connection)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, 1, ?, ?, ?)`, projectID, id, options.Target,
		time.Now().UTC().Format(createdAtLayout), projectID, model.Model, model.ReasoningEffort, model.connection); err != nil {
		return automaticReview{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO automatic_reviews
		(project_id, task_id, request_id, source_version_id, working_version_id, max_rounds, target_score, criteria)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, id, requestID, versionID, versionID, options.MaxRounds, options.TargetScore, options.Criteria); err != nil {
		return automaticReview{}, false, err
	}
	review, err = readAutomaticReview(ctx, tx, projectID, id)
	if err != nil {
		return automaticReview{}, false, err
	}
	return review, true, tx.Commit()
}

func (s *Store) retryAutomaticReview(ctx context.Context, projectID, versionID, id, requestID string, revision int, model modelSnapshot) (automaticReview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return automaticReview{}, false, err
	}
	defer tx.Rollback()
	review, err := readAutomaticReview(ctx, tx, projectID, id)
	if err != nil {
		return automaticReview{}, false, err
	}
	if review.sourceVersion != versionID {
		return automaticReview{}, false, sql.ErrNoRows
	}
	var savedID, action string
	var expected int
	err = tx.QueryRowContext(ctx, `SELECT task_id, action, expected_revision FROM task_requests WHERE project_id = ? AND request_id = ?`,
		projectID, requestID).Scan(&savedID, &action, &expected)
	if err == nil {
		if savedID != id || action != retryAutomaticReview || expected != revision {
			return automaticReview{}, false, errRequestConflict
		}
		return review, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return automaticReview{}, false, err
	}
	if review.Revision != revision {
		return automaticReview{}, false, errTaskRevision
	}
	if !canRetryReview(review.Status) {
		return automaticReview{}, false, errManuscriptReviewState
	}
	if err := review.checkReviewRetry(model); err != nil {
		return automaticReview{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_requests(project_id, task_id, request_id, action, expected_revision)
		VALUES (?, ?, ?, ?, ?)`, projectID, id, requestID, retryAutomaticReview, revision); err != nil {
		return automaticReview{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'QUEUED', revision = revision + 1, message = '',
		queued_seq = `+nextTaskQueuePosition+` WHERE ordinal = ?`, projectID, review.ordinal); err != nil {
		return automaticReview{}, false, err
	}
	review.Status, review.Revision, review.Message = taskQueued, review.Revision+1, ""
	return review, true, tx.Commit()
}

func (s *Store) saveAutomaticReviewProgress(ctx context.Context, projectID string, review *automaticReview, workPath string, revised *string) error {
	encoded, err := json.Marshal(review.Rounds)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `UPDATE tasks SET revision = revision + 1
		WHERE project_id = ? AND task_id = ? AND status = 'RUNNING' RETURNING revision`, projectID, review.ReviewID).
		Scan(&review.Revision); err != nil {
		return err
	}
	if revised != nil {
		// A new round has its own path. Publishing a revision never replaces a manuscript path.
		if _, err := currentWorkspaceFile(ctx, tx, projectID, workPath); !errors.Is(err, sql.ErrNoRows) {
			if err == nil {
				return errWorkspaceConflict
			}
			return err
		}
		file, err := s.insertWorkspaceVersion(ctx, tx, projectID, workspaceTextFile(workPath, *revised), revised)
		if err != nil {
			return err
		}
		review.workVersion = file.VersionID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automatic_reviews SET working_version_id = ?, rounds_json = ?
		WHERE project_id = ? AND task_id = ?`, review.workVersion, string(encoded), projectID, review.ReviewID); err != nil {
		return err
	}
	return tx.Commit()
}
