package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

func (api workflows) saveManuscript(w http.ResponseWriter, r *http.Request) {
	artifactID := r.URL.Query().Get("artifactId")
	file, err := api.store.saveWorkflowManuscript(r.Context(), r.PathValue("id"), r.PathValue("runId"), artifactID)
	if err != nil {
		writeWorkspaceError(w, "save workflow manuscript", err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Store) saveWorkflowManuscript(ctx context.Context, projectID, runID, expectedArtifactID string) (WorkspaceFile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceFile{}, err
	}
	defer tx.Rollback()

	step, _, err := workflowArtifactStep(ctx, tx, projectID, runID, "manuscript")
	if err != nil {
		return WorkspaceFile{}, err
	}
	artifact, err := lookupWorkflowArtifact(ctx, tx, projectID, runID, step, "")
	if expectedArtifactID != "" && (errors.Is(err, sql.ErrNoRows) || (err == nil && artifact.ArtifactID != expectedArtifactID)) {
		return WorkspaceFile{}, errWorkspaceConflict
	}
	if err != nil {
		return WorkspaceFile{}, err
	}
	var firstID string
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE project_id = ? AND workflow_run_id = ?
		AND workflow_step = ? AND status = 'SUCCEEDED' ORDER BY ordinal LIMIT 1`, projectID, runID, step).Scan(&firstID); err != nil {
		return WorkspaceFile{}, err
	}
	path := "manuscripts/" + runID + "/main.md"
	if artifact.ArtifactID != firstID {
		path = "manuscripts/" + runID + "/" + artifact.ArtifactID + ".md"
	}
	file := workspaceTextFile(path, artifact.markdown)
	current, err := currentWorkspaceFile(ctx, tx, projectID, path)
	if errors.Is(err, sql.ErrNoRows) {
		file, err = s.insertWorkspaceVersion(ctx, tx, projectID, file, &artifact.markdown)
		if err != nil {
			return WorkspaceFile{}, err
		}
		return file, tx.Commit()
	}
	if err != nil {
		return WorkspaceFile{}, err
	}

	// Reopening the same result preserves any subsequent edits in the workspace.
	var originalExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_versions
		WHERE project_id = ? AND path = ? AND sha256 = ?)`, projectID, path, file.SHA256).Scan(&originalExists); err != nil {
		return WorkspaceFile{}, err
	}
	if !originalExists {
		return WorkspaceFile{}, errWorkspaceConflict
	}
	return current, nil
}
