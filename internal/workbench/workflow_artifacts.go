package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type workflowArtifactRecord struct {
	ArtifactID string                  `json:"artifactId"`
	CreatedAt  string                  `json:"createdAt"`
	Current    bool                    `json:"current"`
	Source     *workflowArtifactSource `json:"source,omitempty"`
	ordinal    int64
	markdown   string
}

func workflowArtifactStep(ctx context.Context, db workspaceQuery, projectID, runID, key string) (int, string, error) {
	var workflow string
	if err := db.QueryRowContext(ctx, `SELECT workflow_key FROM workflow_runs WHERE project_id = ? AND run_id = ?`,
		projectID, runID).Scan(&workflow); err != nil {
		return 0, "", err
	}
	for i, stage := range workflowStages[workflow] {
		if stage.key == key {
			return i, workflowArtifactFilename(workflow, stage), nil
		}
	}
	return 0, "", sql.ErrNoRows
}

func workflowArtifactFilename(workflow string, stage workflowStage) string {
	family, _, _ := strings.Cut(workflow, "-v")
	extension := stage.artifactExtension
	if extension == "" {
		extension = ".md"
	}
	return family + "-" + stage.key + extension
}

// An empty artifactID selects the result that is still valid for this run.
func lookupWorkflowArtifact(ctx context.Context, db workspaceQuery, projectID, runID string, step int, artifactID string) (workflowArtifactRecord, error) {
	var artifact workflowArtifactRecord
	err := db.QueryRowContext(ctx, `SELECT ordinal, task_id, markdown FROM tasks
		WHERE project_id = ? AND workflow_run_id = ? AND workflow_step = ? AND status = 'SUCCEEDED'
		AND ((? = '' AND workflow_superseded = 0) OR task_id = ?) ORDER BY ordinal DESC LIMIT 1`,
		projectID, runID, step, artifactID, artifactID).Scan(&artifact.ordinal, &artifact.ArtifactID, &artifact.markdown)
	return artifact, err
}

func (s *Store) workflowArtifact(ctx context.Context, projectID, runID, key string) (string, string, error) {
	step, name, err := workflowArtifactStep(ctx, s.db, projectID, runID, key)
	if err != nil {
		return "", "", err
	}
	artifact, err := lookupWorkflowArtifact(ctx, s.db, projectID, runID, step, "")
	return name, artifact.markdown, err
}

func (s *Store) workflowArtifactVersion(ctx context.Context, projectID, runID, key, artifactID string) (workflowArtifactRecord, error) {
	step, _, err := workflowArtifactStep(ctx, s.db, projectID, runID, key)
	if err != nil {
		return workflowArtifactRecord{}, err
	}
	return lookupWorkflowArtifact(ctx, s.db, projectID, runID, step, artifactID)
}

func (api workflows) artifacts(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "成果历史游标无效。")
		return
	}
	ctx, projectID, runID := r.Context(), r.PathValue("id"), r.PathValue("runId")
	step, _, err := workflowArtifactStep(ctx, api.store.db, projectID, runID, r.PathValue("key"))
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	const limit = 30
	rows, err := api.store.db.QueryContext(ctx, `SELECT t.ordinal, t.task_id, t.created_at, t.workflow_superseded = 0,
		COALESCE(v.version_id, ''), COALESCE(v.path, ''), COALESCE(v.sha256, '')
		FROM tasks t LEFT JOIN workspace_versions v ON v.version_id = t.workflow_source_version_id AND v.project_id = t.project_id
		WHERE t.project_id = ? AND t.workflow_run_id = ? AND t.workflow_step = ? AND t.status = 'SUCCEEDED'
		AND (? = 0 OR t.ordinal < ?) ORDER BY t.ordinal DESC LIMIT ?`, projectID, runID, step, cursor, cursor, limit+1)
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	defer rows.Close()
	page := struct {
		Items      []workflowArtifactRecord `json:"items"`
		NextCursor string                   `json:"nextCursor,omitempty"`
	}{Items: make([]workflowArtifactRecord, 0, limit)}
	for rows.Next() {
		var record workflowArtifactRecord
		var source workflowArtifactSource
		if err := rows.Scan(&record.ordinal, &record.ArtifactID, &record.CreatedAt, &record.Current,
			&source.VersionID, &source.Path, &source.SHA256); err != nil {
			writeWorkflowArtifactError(w, err)
			return
		}
		if len(page.Items) == limit {
			page.NextCursor = strconv.FormatInt(page.Items[limit-1].ordinal, 10)
			break
		}
		if source.VersionID != "" {
			record.Source = &source
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (api workflows) artifact(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, runID := r.Context(), r.PathValue("id"), r.PathValue("runId")
	step, name, err := workflowArtifactStep(ctx, api.store.db, projectID, runID, r.PathValue("key"))
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	artifact, err := lookupWorkflowArtifact(ctx, api.store.db, projectID, runID, step, r.PathValue("artifactId"))
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	switch {
	case strings.HasSuffix(name, ".txt"):
		writeTextAttachment(w, r, name, "text/plain; charset=utf-8", artifact.markdown)
	case strings.HasSuffix(name, ".json"):
		writeTextAttachment(w, r, name, "application/json", artifact.markdown)
	default:
		writeMarkdown(w, r, name, artifact.markdown)
	}
}

func writeWorkflowArtifactError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该步骤暂无可下载的成果。")
		return
	}
	writeStoreError(w, "read workflow artifact", err)
}
