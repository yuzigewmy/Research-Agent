package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
)

const workspaceBundleLimit = 24

type workspaceBundleEntry struct {
	Name string `json:"name"`
	WorkspaceFile
}

type workspaceBundleManifest struct {
	ProjectID string                 `json:"projectId"`
	Files     []workspaceBundleEntry `json:"files"`
}

func (s *Store) handleWorkspaceBundle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		VersionIDs []string `json:"versionIds"`
	}
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	if len(input.VersionIDs) == 0 || len(input.VersionIDs) > workspaceBundleLimit {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择 1 至 24 份文件版本。")
		return
	}
	seen := make(map[string]bool, len(input.VersionIDs))
	for _, id := range input.VersionIDs {
		if !validRequestID(id) || seen[id] {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择有效且不重复的文件版本。")
			return
		}
		seen[id] = true
	}

	ctx, projectID := r.Context(), r.PathValue("id")
	manifest := workspaceBundleManifest{ProjectID: projectID, Files: make([]workspaceBundleEntry, 0, len(input.VersionIDs))}
	for index, id := range input.VersionIDs {
		file, err := s.workspaceVersion(ctx, projectID, id)
		if err != nil {
			writeWorkspaceError(w, "read workspace bundle version", err)
			return
		}
		manifest.Files = append(manifest.Files, workspaceBundleEntry{workspaceBundleName(index+1, file.Path), file})
	}
	bundle, err := s.packWorkspaceBundle(ctx, manifest)
	if err != nil {
		writeStoreError(w, "pack workspace versions", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="workspace-artifacts.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(bundle)))
	_, _ = w.Write(bundle)
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
	for _, file := range manifest.Files {
		if err := s.addWorkspaceBundleEntry(ctx, archive, file); err != nil {
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

func (s *Store) addWorkspaceBundleEntry(ctx context.Context, archive *zip.Writer, file workspaceBundleEntry) error {
	if file.Size < 0 || file.Size > maxWorkspaceText {
		return fmt.Errorf("invalid size for workspace bundle version %s", file.VersionID)
	}
	source, err := s.openObject(file.SHA256)
	if err != nil {
		return err
	}
	defer source.Close()
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: file.Name, Method: zip.Store})
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(entry, digest), io.LimitReader(contextReader{ctx, source}, int64(file.Size)+1))
	if err != nil {
		return err
	}
	if written != int64(file.Size) || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("invalid content for workspace bundle version %s", file.VersionID)
	}
	return nil
}
