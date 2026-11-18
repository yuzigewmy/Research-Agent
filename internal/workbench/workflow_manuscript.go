package workbench

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
)

func (api workflows) saveManuscript(w http.ResponseWriter, r *http.Request) {
	file, err := api.store.saveWorkflowManuscript(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeWorkspaceError(w, "save workflow manuscript", err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Store) saveWorkflowManuscript(ctx context.Context, projectID, runID string) (WorkspaceFile, error) {
	_, markdown, err := s.workflowArtifact(ctx, projectID, runID, "manuscript")
	if err != nil {
		return WorkspaceFile{}, err
	}
	path := "manuscripts/" + runID + "/main.md"
	document, err := s.saveWorkspaceDocument(ctx, projectID, path, "", markdown)
	if !errors.Is(err, errWorkspaceConflict) {
		return document.WorkspaceFile, err
	}

	// Reopening a published manuscript keeps its later edits. A different file
	// created at this path before the initial handoff is a genuine conflict.
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(markdown)))
	var originalExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_versions
		WHERE project_id = ? AND path = ? AND sha256 = ?)`, projectID, path, digest).Scan(&originalExists); err != nil {
		return WorkspaceFile{}, err
	}
	if !originalExists {
		return WorkspaceFile{}, errWorkspaceConflict
	}
	return s.workspaceFile(ctx, projectID, path)
}
