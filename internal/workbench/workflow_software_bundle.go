package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"research-agent/internal/wordexport"
)

type softwareBundleFile struct {
	Name         string `json:"name"`
	ArtifactID   string `json:"artifactId"`
	SourceSHA256 string `json:"sourceSha256"`
	SHA256       string `json:"sha256"`
}

type softwareBundleManifest struct {
	RunID       string               `json:"runId"`
	Revision    int                  `json:"revision"`
	WorkflowKey string               `json:"workflowKey"`
	Title       string               `json:"title"`
	Version     string               `json:"version"`
	Files       []softwareBundleFile `json:"files"`
	Omitted     []string             `json:"omitted"`
}

type softwareBundleArtifact struct {
	key, name, id, text string
}

var errSoftwareBundleWord = errors.New("software bundle Word export failed")

func (s *Store) readSoftwareBundle(ctx context.Context, projectID, runID string, revision int) (softwareBundleManifest, []softwareBundleArtifact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return softwareBundleManifest{}, nil, err
	}
	defer tx.Rollback()

	run, err := readWorkflow(ctx, tx, projectID, runID)
	if err != nil {
		return softwareBundleManifest{}, nil, err
	}
	if run.Software == nil {
		return softwareBundleManifest{}, nil, sql.ErrNoRows
	}
	if run.Revision != revision {
		return softwareBundleManifest{}, nil, errTaskRevision
	}
	if run.Status != workflowDone {
		return softwareBundleManifest{}, nil, errWorkflowState
	}

	manifest := softwareBundleManifest{RunID: runID, Revision: revision, WorkflowKey: run.WorkflowKey,
		Version: run.Software.Version, Omitted: make([]string, 0, 2)}
	if err := tx.QueryRowContext(ctx, `SELECT title FROM projects WHERE project_id = ?`, projectID).Scan(&manifest.Title); err != nil {
		return softwareBundleManifest{}, nil, err
	}
	artifacts := make([]softwareBundleArtifact, 0, len(run.Steps))
	// Read the selected revision and all its bodies from the same database snapshot.
	for index, step := range run.Steps {
		if step.ArtifactID == "" {
			return softwareBundleManifest{}, nil, errWorkflowState
		}
		artifact, err := lookupWorkflowArtifact(ctx, tx, projectID, runID, index, step.ArtifactID)
		if err != nil {
			return softwareBundleManifest{}, nil, err
		}
		name := workflowArtifactFilename(run.WorkflowKey, workflowStages[run.WorkflowKey][index])
		artifacts = append(artifacts, softwareBundleArtifact{step.Key, name, artifact.ArtifactID, artifact.markdown})
	}
	return manifest, artifacts, nil
}

func (api workflows) packSoftwareBundle(ctx context.Context, manifest softwareBundleManifest, artifacts []softwareBundleArtifact) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	if api.softwarePDFFont == "" {
		manifest.Omitted = append(manifest.Omitted, "pdf")
	}
	if api.wordPython == "" {
		manifest.Omitted = append(manifest.Omitted, "docx")
	}
	add := func(name, artifactID, sourceDigest string, data []byte) error {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		if _, err := entry.Write(data); err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, softwareBundleFile{name, artifactID, sourceDigest, fmt.Sprintf("%x", sha256.Sum256(data))})
		return nil
	}
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := []byte(artifact.text)
		sourceDigest := fmt.Sprintf("%x", sha256.Sum256(data))
		if err := add(artifact.name, artifact.id, sourceDigest, data); err != nil {
			return nil, err
		}
		if api.softwarePDFFont != "" && (artifact.key == "manuscript" || artifact.key == "program") {
			pdf, _, err := renderSoftwareArtifactPDF(ctx, api.softwarePDFFont, manifest.Title, manifest.Version, artifact.key, artifact.text)
			if err != nil {
				return nil, err
			}
			if err := add(strings.TrimSuffix(artifact.name, path.Ext(artifact.name))+".pdf", artifact.id, sourceDigest, pdf); err != nil {
				return nil, err
			}
		}
		if api.wordPython != "" && artifact.key == "manuscript" {
			docx, err := wordexport.Export(ctx, api.wordPython, artifact.text)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", errSoftwareBundleWord, err)
			}
			if err := add(wordFilename(artifact.name), artifact.id, sourceDigest, docx); err != nil {
				return nil, err
			}
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

func (api workflows) softwareBundle(w http.ResponseWriter, r *http.Request) {
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请指定要下载的运行修订。")
		return
	}
	manifest, artifacts, err := api.store.readSoftwareBundle(r.Context(), r.PathValue("id"), r.PathValue("runId"), revision)
	switch {
	case errors.Is(err, errTaskRevision):
		writeError(w, http.StatusConflict, "REVISION_CONFLICT", "本轮成果已更新，请刷新后重新下载。")
		return
	case errors.Is(err, errWorkflowState):
		writeError(w, http.StatusConflict, "WORKFLOW_STATE_CONFLICT", "请完成本轮登记材料的全部步骤后下载。")
		return
	case err != nil:
		writeWorkflowArtifactError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	body, err := api.packSoftwareBundle(ctx, manifest, artifacts)
	if errors.Is(err, errSoftwareBundleWord) {
		writeError(w, http.StatusUnprocessableEntity, "EXPORT_FAILED", "手册的 Word 导出失败。请检查排版组件后重试。")
		return
	}
	if err != nil {
		writeSoftwarePDFError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="software-registration.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Workflow-Run-ID", manifest.RunID)
	w.Header().Set("X-Workflow-Revision", strconv.Itoa(manifest.Revision))
	w.Header().Set("X-Bundle-SHA256", fmt.Sprintf("%x", sha256.Sum256(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
