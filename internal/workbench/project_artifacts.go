package workbench

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const projectArtifactPageSize = 30

var projectArtifactKinds = []string{"version", "material", "artifact"}
var projectArtifactFormats = []string{"markdown", "data", "html", "diagram", "pdf", "image", "word", "code", "text", "other"}

// Use the same generated filenames for catalog entries, direct downloads, and ZIP manifests.
var artifactStageNames = func() string {
	type stageFile struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	stages := make(map[string][]stageFile, len(workflowStages))
	for workflow, definitions := range workflowStages {
		files := make([]stageFile, len(definitions))
		for i, stage := range definitions {
			files[i] = stageFile{stage.key, workflowArtifactFilename(workflow, stage)}
		}
		stages[workflow] = files
	}
	data, _ := json.Marshal(stages)
	return string(data)
}()

const projectArtifactsQuery = `WITH originals(kind, id, name, size, created_at, run_id, step_key, media_kind) AS (
	SELECT 'version', version_id, path, size, created_at, '', '', 'text'
	FROM workspace_versions WHERE project_id = :project AND (:kind = '' OR :kind = 'version')
	UNION ALL
	SELECT 'material', material_id, name, size, created_at, '', '', kind
	FROM materials WHERE project_id = :project AND (:kind = '' OR :kind = 'material')
	UNION ALL
	SELECT 'artifact', t.task_id, stage.value ->> 'name', length(CAST(t.markdown AS BLOB)), t.created_at,
		t.workflow_run_id, stage.value ->> 'key', 'text'
	FROM tasks t JOIN workflow_runs w ON w.run_id = t.workflow_run_id AND w.project_id = t.project_id
	JOIN json_each(:stages, '$.' || json_quote(w.workflow_key)) stage ON stage.key = t.workflow_step
	WHERE t.project_id = :project AND t.status = 'SUCCEEDED' AND (:kind = '' OR :kind = 'artifact')
), formatted AS (
	SELECT *, CASE
		WHEN lower(name) GLOB '*.pdf' THEN 'pdf'
		WHEN lower(name) GLOB '*.png' OR lower(name) GLOB '*.jpg' OR lower(name) GLOB '*.jpeg'
			OR lower(name) GLOB '*.gif' OR lower(name) GLOB '*.webp' OR lower(name) GLOB '*.svg' THEN 'image'
		WHEN lower(name) GLOB '*.docx' OR lower(name) GLOB '*.dotx' THEN 'word'
		WHEN lower(name) GLOB '*.flow.json' THEN 'diagram'
		WHEN lower(name) GLOB '*.md' OR lower(name) GLOB '*.markdown' THEN 'markdown'
		WHEN lower(name) GLOB '*.csv' OR lower(name) GLOB '*.tsv' OR lower(name) GLOB '*.json' OR lower(name) GLOB '*.xlsx' THEN 'data'
		WHEN lower(name) GLOB '*.html' OR lower(name) GLOB '*.htm' THEN 'html'
		WHEN lower(name) GLOB '*.py' OR lower(name) GLOB '*.r' OR lower(name) GLOB '*.ipynb' OR lower(name) GLOB '*.js'
			OR lower(name) GLOB '*.ts' OR lower(name) GLOB '*.go' OR lower(name) GLOB '*.rs' OR lower(name) GLOB '*.sql'
			OR lower(name) GLOB '*.m' OR lower(name) GLOB '*.java' OR lower(name) GLOB '*.cpp' OR lower(name) GLOB '*.c'
			OR lower(name) GLOB '*.css' OR lower(name) GLOB '*.tex' THEN 'code'
		WHEN media_kind IN ('text', 'markdown') THEN 'text'
		ELSE 'other'
	END AS format FROM originals
	WHERE (:before = '' OR (created_at, kind, id) < (:before, :cursor_kind, :cursor_id))
	AND (:q = '' OR instr(lower(name), lower(:q)) > 0)
)
SELECT kind, id, name, size, created_at, run_id, step_key, format FROM formatted
WHERE :format = '' OR format = :format
ORDER BY created_at DESC, kind DESC, id DESC LIMIT :limit`

type projectArtifact struct {
	workspaceBundleItem
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"createdAt"`
	Format      string `json:"format"`
	DownloadURL string `json:"downloadUrl"`
}

type projectArtifactCursor struct{ stamp, kind, id string }

func parseProjectArtifactCursor(raw string) (projectArtifactCursor, error) {
	if raw == "" {
		return projectArtifactCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	parts := strings.SplitN(string(data), "/", 3)
	if err != nil || len(parts) != 3 || !slices.Contains(projectArtifactKinds, parts[1]) || !validRequestID(parts[2]) {
		return projectArtifactCursor{}, errors.New("invalid artifacts cursor")
	}
	if _, err := time.Parse(createdAtLayout, parts[0]); err != nil {
		return projectArtifactCursor{}, err
	}
	return projectArtifactCursor{parts[0], parts[1], parts[2]}, nil
}

func (s *Store) handleProjectArtifacts(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "成果查询参数无效。")
		return
	}
	kind, format, search := query.Get("kind"), query.Get("format"), strings.TrimSpace(query.Get("q"))
	if (kind != "" && !slices.Contains(projectArtifactKinds, kind)) || (format != "" && !slices.Contains(projectArtifactFormats, format)) ||
		len(search) > 512 || !utf8.ValidString(search) || strings.ContainsFunc(search, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择有效的成果来源、文件类型和搜索词。")
		return
	}
	cursor, err := parseProjectArtifactCursor(query.Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "成果列表游标无效。")
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	if _, err := s.getProject(ctx, projectID); err != nil {
		writeWorkspaceError(w, "look up artifacts project", err)
		return
	}
	items, err := s.listProjectArtifacts(ctx, projectID, kind, format, search, cursor)
	if err != nil {
		writeStoreError(w, "list project artifacts", err)
		return
	}
	var next string
	if len(items) > projectArtifactPageSize {
		items = items[:projectArtifactPageSize]
		last := items[len(items)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt + "/" + last.Kind + "/" + last.ID))
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []projectArtifact `json:"items"`
		NextCursor string            `json:"nextCursor,omitempty"`
	}{items, next})
}

func (s *Store) listProjectArtifacts(ctx context.Context, projectID, kind, format, search string, cursor projectArtifactCursor) ([]projectArtifact, error) {
	rows, err := s.db.QueryContext(ctx, projectArtifactsQuery,
		sql.Named("project", projectID), sql.Named("kind", kind), sql.Named("format", format), sql.Named("q", search),
		sql.Named("stages", artifactStageNames), sql.Named("before", cursor.stamp), sql.Named("cursor_kind", cursor.kind),
		sql.Named("cursor_id", cursor.id), sql.Named("limit", projectArtifactPageSize+1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]projectArtifact, 0, projectArtifactPageSize+1)
	base := projectsPath + "/" + url.PathEscape(projectID)
	for rows.Next() {
		var item projectArtifact
		var runID, step string
		if err := rows.Scan(&item.Kind, &item.ID, &item.Name, &item.Size, &item.CreatedAt, &runID, &step, &item.Format); err != nil {
			return nil, err
		}
		id := url.PathEscape(item.ID)
		switch item.Kind {
		case "version":
			item.DownloadURL = base + "/files/versions/" + id + "/download"
		case "material":
			item.DownloadURL = base + "/materials/" + id + "/download"
		case "artifact":
			item.DownloadURL = base + "/workflow/" + url.PathEscape(runID) + "/steps/" + url.PathEscape(step) + "/artifacts/" + id
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) handleDownloadWorkspaceVersion(w http.ResponseWriter, r *http.Request) {
	file, err := s.workspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
	if err != nil {
		writeWorkspaceError(w, "find workspace download version", err)
		return
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		writeWorkspaceError(w, "read workspace download version", err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(file.Path)}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.Content)))
	w.Header().Set("X-Source-Version-ID", file.VersionID)
	w.Header().Set("X-Source-SHA256", file.SHA256)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, doc.Content)
	}
}
