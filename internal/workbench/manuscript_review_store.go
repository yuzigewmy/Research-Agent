package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

const manuscriptReviewSchema = `
	ALTER TABLE tasks ADD COLUMN review_version_id TEXT REFERENCES workspace_versions(version_id);
	CREATE UNIQUE INDEX manuscript_review_target ON tasks(review_version_id, instruction) WHERE review_version_id IS NOT NULL;
	CREATE INDEX manuscript_review_latest ON tasks(review_version_id, ordinal DESC) WHERE review_version_id IS NOT NULL;
	PRAGMA user_version = 14;
`

const manuscriptReviewColumns = `ordinal, task_id, instruction, status, revision, created_at, message`
const retryManuscriptReview = "retry-review"

var errManuscriptReviewState = errors.New("review cannot be retried in its current state")

type manuscriptReview struct {
	ReviewID     string `json:"reviewId"`
	Target       string `json:"target"`
	Status       string `json:"status"`
	Revision     int    `json:"revision"`
	SourceSHA256 string `json:"sourceSha256"`
	CreatedAt    string `json:"createdAt"`
	Message      string `json:"message,omitempty"`
	Report       string `json:"report,omitempty"`
	ordinal      int64
}

func scanManuscriptReview(row interface{ Scan(...any) error }, file WorkspaceFile) (manuscriptReview, error) {
	review := manuscriptReview{SourceSHA256: file.SHA256}
	err := row.Scan(&review.ordinal, &review.ReviewID, &review.Target, &review.Status, &review.Revision, &review.CreatedAt, &review.Message, &review.Report)
	return review, err
}

func (s *Store) listManuscriptReviews(ctx context.Context, projectID string, file WorkspaceFile, before int64, limit int) ([]manuscriptReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+manuscriptReviewColumns+`, '' FROM tasks
		WHERE project_id = ? AND review_version_id = ? AND (? = 0 OR ordinal < ?)
		ORDER BY ordinal DESC LIMIT ?`, projectID, file.VersionID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]manuscriptReview, 0, limit)
	for rows.Next() {
		review, err := scanManuscriptReview(rows, file)
		if err != nil {
			return nil, err
		}
		items = append(items, review)
	}
	return items, rows.Err()
}

type reviewQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getManuscriptReview(ctx context.Context, db reviewQuery, projectID string, file WorkspaceFile, id string) (manuscriptReview, error) {
	return scanManuscriptReview(db.QueryRowContext(ctx, `SELECT `+manuscriptReviewColumns+`, markdown FROM tasks
		WHERE project_id = ? AND review_version_id = ? AND task_id = ?`, projectID, file.VersionID, id), file)
}

func (s *Store) startManuscriptReview(ctx context.Context, projectID string, file WorkspaceFile, target string, modelReady bool) (manuscriptReview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return manuscriptReview{}, false, err
	}
	defer tx.Rollback()
	review, err := scanManuscriptReview(tx.QueryRowContext(ctx, `SELECT `+manuscriptReviewColumns+`, markdown FROM tasks
		WHERE project_id = ? AND review_version_id = ? AND instruction = ?`, projectID, file.VersionID, target), file)
	if !errors.Is(err, sql.ErrNoRows) {
		return review, false, err
	}
	if !modelReady {
		return manuscriptReview{}, false, errModelUnavailable
	}
	review, err = scanManuscriptReview(tx.QueryRowContext(ctx, `INSERT INTO tasks
		(project_id, task_id, instruction, status, created_at, queued_seq, review_version_id)
		VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, ?) RETURNING `+manuscriptReviewColumns+`, markdown`,
		projectID, rand.Text(), target, time.Now().UTC().Format(createdAtLayout), projectID, file.VersionID), file)
	if err != nil {
		return manuscriptReview{}, false, err
	}
	return review, true, tx.Commit()
}

func (s *Store) retryManuscriptReview(ctx context.Context, projectID string, file WorkspaceFile, id, requestID string, revision int, modelReady bool) (manuscriptReview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return manuscriptReview{}, false, err
	}
	defer tx.Rollback()
	review, err := getManuscriptReview(ctx, tx, projectID, file, id)
	if err != nil {
		return manuscriptReview{}, false, err
	}
	var savedID, action string
	var expected int
	err = tx.QueryRowContext(ctx, `SELECT task_id, action, expected_revision FROM task_requests WHERE project_id = ? AND request_id = ?`,
		projectID, requestID).Scan(&savedID, &action, &expected)
	if err == nil {
		if savedID != id || action != retryManuscriptReview || expected != revision {
			return manuscriptReview{}, false, errRequestConflict
		}
		return review, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manuscriptReview{}, false, err
	}
	if review.Revision != revision {
		return manuscriptReview{}, false, errTaskRevision
	}
	if review.Status != taskFailed && review.Status != taskInterrupted {
		return manuscriptReview{}, false, errManuscriptReviewState
	}
	if !modelReady {
		return manuscriptReview{}, false, errModelUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_requests (project_id, task_id, request_id, action, expected_revision)
		VALUES (?, ?, ?, ?, ?)`, projectID, id, requestID, retryManuscriptReview, revision); err != nil {
		return manuscriptReview{}, false, err
	}
	review, err = scanManuscriptReview(tx.QueryRowContext(ctx, `UPDATE tasks SET status = 'QUEUED', message = '',
		revision = revision + 1, queued_seq = `+nextTaskQueuePosition+`
		WHERE ordinal = ? RETURNING `+manuscriptReviewColumns+`, markdown`, projectID, review.ordinal), file)
	if err != nil {
		return manuscriptReview{}, false, err
	}
	return review, true, tx.Commit()
}
