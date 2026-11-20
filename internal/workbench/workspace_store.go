package workbench

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

const workspaceSchema = `
	CREATE TABLE workspace_versions (
		ordinal INTEGER PRIMARY KEY,
		version_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		path TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX workspace_history ON workspace_versions(project_id, path, ordinal DESC);
	PRAGMA user_version = 7;
`

const workspaceFileColumns = `ordinal, path, version_id, sha256, size, created_at`

var errWorkspaceConflict = errors.New("workspace file version changed")

type WorkspaceFile struct {
	Path      string `json:"path"`
	VersionID string `json:"versionId"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
	CreatedAt string `json:"createdAt"`
	ordinal   int64
}

type WorkspaceDocument struct {
	WorkspaceFile
	Content string `json:"content"`
}

func scanWorkspaceFile(row interface{ Scan(...any) error }) (WorkspaceFile, error) {
	var file WorkspaceFile
	err := row.Scan(&file.ordinal, &file.Path, &file.VersionID, &file.SHA256, &file.Size, &file.CreatedAt)
	return file, err
}

type workspaceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) workspaceFile(ctx context.Context, projectID, name string) (WorkspaceFile, error) {
	return currentWorkspaceFile(ctx, s.db, projectID, name)
}

func currentWorkspaceFile(ctx context.Context, db workspaceQuery, projectID, name string) (WorkspaceFile, error) {
	return scanWorkspaceFile(db.QueryRowContext(ctx, `SELECT `+workspaceFileColumns+` FROM workspace_versions
		WHERE project_id = ? AND path = ? ORDER BY ordinal DESC LIMIT 1`, projectID, name))
}

func (s *Store) workspaceVersion(ctx context.Context, projectID, versionID string) (WorkspaceFile, error) {
	return lookupWorkspaceVersion(ctx, s.db, projectID, versionID)
}

func lookupWorkspaceVersion(ctx context.Context, db workspaceQuery, projectID, versionID string) (WorkspaceFile, error) {
	return scanWorkspaceFile(db.QueryRowContext(ctx, `SELECT `+workspaceFileColumns+` FROM workspace_versions
		WHERE project_id = ? AND version_id = ?`, projectID, versionID))
}

func (s *Store) readWorkspaceDocument(file WorkspaceFile) (WorkspaceDocument, error) {
	content, err := os.ReadFile(s.materialObject(file.SHA256))
	return WorkspaceDocument{WorkspaceFile: file, Content: string(content)}, err
}

func (s *Store) listWorkspaceFiles(ctx context.Context, projectID, name, kind string, cursor int64, limit int) ([]WorkspaceFile, error) {
	query := `SELECT ` + workspaceFileColumns + ` FROM workspace_versions WHERE project_id = ?`
	args := []any{projectID}
	if name == "" {
		query += ` AND ordinal IN (SELECT MAX(ordinal) FROM workspace_versions WHERE project_id = ? GROUP BY path)`
		args = append(args, projectID)
	} else {
		query += ` AND path = ?`
		args = append(args, name)
	}
	switch kind {
	case "diagram":
		query += ` AND path GLOB 'diagrams/?*.flow.json' AND path NOT GLOB 'diagrams/*/*'`
	case "markdown":
		query += ` AND (LOWER(path) LIKE '%.md' OR LOWER(path) LIKE '%.markdown')`
	}
	if cursor > 0 {
		query += ` AND ordinal < ?`
		args = append(args, cursor)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY ordinal DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WorkspaceFile, 0, limit)
	for rows.Next() {
		file, err := scanWorkspaceFile(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, file)
	}
	return items, rows.Err()
}

func (s *Store) saveWorkspaceDocument(ctx context.Context, projectID, name, baseSHA256, content string) (WorkspaceDocument, error) {
	if err := validateDiagramDocument(name, content); err != nil {
		return WorkspaceDocument{}, err
	}
	file := workspaceTextFile(name, content)
	// The object is written only if the current version can be advanced.
	file, err := s.publishWorkspaceVersion(ctx, projectID, baseSHA256, file, &content)
	return WorkspaceDocument{WorkspaceFile: file, Content: content}, err
}

func (s *Store) restoreWorkspaceVersion(ctx context.Context, projectID, versionID, baseSHA256 string) (WorkspaceDocument, error) {
	file, err := s.workspaceVersion(ctx, projectID, versionID)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	original, err := s.readWorkspaceDocument(file)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	if err := validateDiagramDocument(file.Path, original.Content); err != nil {
		return WorkspaceDocument{}, err
	}
	file, err = s.publishWorkspaceVersion(ctx, projectID, baseSHA256, file, nil)
	return WorkspaceDocument{WorkspaceFile: file, Content: original.Content}, err
}

func (s *Store) publishWorkspaceVersion(ctx context.Context, projectID, baseSHA256 string, file WorkspaceFile, content *string) (WorkspaceFile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceFile{}, err
	}
	defer tx.Rollback()
	latest, err := currentWorkspaceFile(ctx, tx, projectID, file.Path)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkspaceFile{}, err
	}
	if latest.SHA256 == file.SHA256 {
		return latest, nil
	}
	if latest.SHA256 != baseSHA256 {
		return WorkspaceFile{}, errWorkspaceConflict
	}
	file, err = s.insertWorkspaceVersion(ctx, tx, projectID, file, content)
	if err != nil {
		return WorkspaceFile{}, err
	}
	return file, tx.Commit()
}

func workspaceTextFile(name, content string) WorkspaceFile {
	return WorkspaceFile{Path: name, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), Size: len(content)}
}

func (s *Store) insertWorkspaceVersion(ctx context.Context, tx *sql.Tx, projectID string, file WorkspaceFile, content *string) (WorkspaceFile, error) {
	if content != nil {
		if err := s.saveTextObject(file.SHA256, *content); err != nil {
			return WorkspaceFile{}, err
		}
	}
	return scanWorkspaceFile(tx.QueryRowContext(ctx, `INSERT INTO workspace_versions
		(version_id, project_id, path, sha256, size, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING `+workspaceFileColumns,
		rand.Text(), projectID, file.Path, file.SHA256, file.Size, time.Now().UTC().Format(createdAtLayout)))
}

func (s *Store) saveTextObject(digest, content string) error {
	staged, err := os.CreateTemp(s.objectsDir, ".text-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if _, err := staged.WriteString(content); err != nil {
		return err
	}
	return s.publishObject(staged, digest)
}
