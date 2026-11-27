package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
)

const (
	workspaceBundleLimit = 24
	workspaceBundleBytes = 32 << 20
)

type workspaceBundleItem struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type workspaceBundleEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	VersionID  string `json:"versionId,omitempty"`
	MaterialID string `json:"materialId,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	RunID      string `json:"runId,omitempty"`
	StepKey    string `json:"stepKey,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	CreatedAt  string `json:"createdAt"`
}

type workspaceBundleManifest struct {
	ProjectID string                 `json:"projectId"`
	Files     []workspaceBundleEntry `json:"files"`
}

func (s *Store) handleWorkspaceBundle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		VersionIDs []string              `json:"versionIds"`
		Items      []workspaceBundleItem `json:"items"`
	}
	if !readJSON(w, r, &input, 32<<10) {
		return
	}
	if input.Items != nil && input.VersionIDs != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请使用一种选项格式提交打包清单。")
		return
	}
	count := len(input.Items) + len(input.VersionIDs)
	if count == 0 || count > workspaceBundleLimit {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择 1 至 24 项原件。")
		return
	}
	for _, id := range input.VersionIDs {
		input.Items = append(input.Items, workspaceBundleItem{Kind: "version", ID: id})
	}
	seen := make(map[workspaceBundleItem]bool, count)
	for _, item := range input.Items {
		if (item.Kind != "version" && item.Kind != "material" && item.Kind != "artifact") || !validRequestID(item.ID) || seen[item] {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择有效且不重复的原件。")
			return
		}
		seen[item] = true
	}

	ctx, projectID := r.Context(), r.PathValue("id")
	manifest := workspaceBundleManifest{ProjectID: projectID, Files: make([]workspaceBundleEntry, 0, count)}
	var size int64
	for index, item := range input.Items {
		entry, err := s.workspaceBundleSource(ctx, projectID, item)
		if err != nil {
			writeWorkspaceError(w, "read workspace bundle source", err)
			return
		}
		if entry.Size < 0 {
			writeStoreError(w, "read workspace bundle size", fmt.Errorf("invalid size for %s %s", item.Kind, item.ID))
			return
		}
		if entry.Size > workspaceBundleBytes-size {
			writeError(w, http.StatusRequestEntityTooLarge, "BUNDLE_TOO_LARGE", "选择的原件总大小超过 32 MiB，请移除部分原件后重试。")
			return
		}
		size += entry.Size
		entry.Name = workspaceBundleName(index+1, entry.Path)
		manifest.Files = append(manifest.Files, entry)
	}
	bundle, err := s.packWorkspaceBundle(ctx, manifest)
	if err != nil {
		writeStoreError(w, "pack workspace artifacts", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="workspace-artifacts.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(bundle)))
	_, _ = w.Write(bundle)
}

func (s *Store) workspaceBundleSource(ctx context.Context, projectID string, item workspaceBundleItem) (workspaceBundleEntry, error) {
	if item.Kind == "artifact" {
		entry := workspaceBundleEntry{Kind: item.Kind, ArtifactID: item.ID}
		var workflow, optionsJSON string
		var step int
		err := s.db.QueryRowContext(ctx, `SELECT w.run_id, w.workflow_key, w.options_json, t.workflow_step, t.created_at, length(CAST(t.markdown AS BLOB))
			FROM tasks t JOIN workflow_runs w ON w.run_id = t.workflow_run_id AND w.project_id = t.project_id
			WHERE t.project_id = ? AND t.task_id = ? AND t.status = 'SUCCEEDED'`, projectID, item.ID).
			Scan(&entry.RunID, &workflow, &optionsJSON, &step, &entry.CreatedAt, &entry.Size)
		if err != nil {
			return workspaceBundleEntry{}, err
		}
		stages := workflowStagesForRun(workflow, optionsJSON)
		if step < 0 || step >= len(stages) {
			return workspaceBundleEntry{}, sql.ErrNoRows
		}
		entry.StepKey = stages[step].key
		entry.Path = workflowArtifactFilename(workflow, stages[step])
		return entry, nil
	}
	if item.Kind == "material" {
		material, err := s.getMaterial(ctx, projectID, item.ID)
		if err != nil {
			return workspaceBundleEntry{}, err
		}
		return workspaceBundleEntry{Kind: item.Kind, Path: material.Name, MaterialID: material.MaterialID,
			SHA256: material.SHA256, Size: material.Size, CreatedAt: material.CreatedAt}, nil
	}
	file, err := s.workspaceVersion(ctx, projectID, item.ID)
	if err != nil {
		return workspaceBundleEntry{}, err
	}
	return workspaceBundleEntry{Kind: item.Kind, Path: file.Path, VersionID: file.VersionID,
		SHA256: file.SHA256, Size: int64(file.Size), CreatedAt: file.CreatedAt}, nil
}

// Numbered entries remain distinct on case-insensitive filesystems and for multiple versions of one path.
func workspaceBundleName(number int, original string) string {
	extension := strings.ToLower(path.Ext(original))
	if len(extension) > 13 || strings.ContainsFunc(strings.TrimPrefix(extension, "."), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) || extension == "." {
		extension = ""
	}
	return fmt.Sprintf("files/%02d%s", number, extension)
}

func (s *Store) packWorkspaceBundle(ctx context.Context, manifest workspaceBundleManifest) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for i := range manifest.Files {
		if err := s.addWorkspaceBundleEntry(ctx, archive, manifest.ProjectID, &manifest.Files[i]); err != nil {
			return nil, err
		}
	}
	entry, err := archive.Create("manifest.json")
	if err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(entry)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (s *Store) addWorkspaceBundleEntry(ctx context.Context, archive *zip.Writer, projectID string, file *workspaceBundleEntry) error {
	var source io.Reader
	if file.Kind == "artifact" {
		var text string
		err := s.db.QueryRowContext(ctx, `SELECT markdown FROM tasks
			WHERE project_id = ? AND task_id = ? AND workflow_run_id = ? AND status = 'SUCCEEDED'
			AND length(CAST(markdown AS BLOB)) = ?`, projectID, file.ArtifactID, file.RunID, file.Size).Scan(&text)
		if err != nil {
			return err
		}
		source = strings.NewReader(text)
	} else {
		object, err := s.openObject(file.SHA256)
		if err != nil {
			return err
		}
		defer object.Close()
		source = object
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: file.Name, Method: zip.Store})
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(entry, digest), io.LimitReader(contextReader{ctx, source}, file.Size+1))
	if err != nil {
		return err
	}
	actualDigest := hex.EncodeToString(digest.Sum(nil))
	if written != file.Size || (file.Kind != "artifact" && actualDigest != file.SHA256) {
		return fmt.Errorf("invalid content for workspace bundle entry %s", file.Name)
	}
	file.SHA256 = actualDigest
	return nil
}
