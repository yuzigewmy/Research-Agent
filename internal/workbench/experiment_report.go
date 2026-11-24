package workbench

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) saveComparisonReport(ctx context.Context, projectID, taskID string) (WorkspaceFile, error) {
	artifact, err := s.readTaskArtifact(ctx, projectID, taskID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if artifact.kind != experimentComparisonTask {
		return WorkspaceFile{}, sql.ErrNoRows
	}
	file := workspaceTextFile("experiments/"+taskID+"/comparison.json", artifact.jsonArtifact)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceFile{}, err
	}
	defer tx.Rollback()

	// Prefer the original report even when the workspace holds later edits.
	existing, err := scanWorkspaceFile(tx.QueryRowContext(ctx, `SELECT `+workspaceFileColumns+` FROM workspace_versions
		WHERE project_id = ? AND path = ? ORDER BY (sha256 = ?) DESC, ordinal LIMIT 1`, projectID, file.Path, file.SHA256))
	if err == nil {
		if existing.SHA256 == file.SHA256 {
			return existing, nil
		}
		return WorkspaceFile{}, errWorkspaceConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkspaceFile{}, err
	}
	file, err = s.insertWorkspaceVersion(ctx, tx, projectID, file, &artifact.jsonArtifact)
	if err != nil {
		return WorkspaceFile{}, err
	}
	return file, tx.Commit()
}
