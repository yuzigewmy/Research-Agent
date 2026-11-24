package workbench

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

type workspaceStorage struct {
	Directory     string `json:"directory"`
	SchemaVersion int    `json:"schemaVersion"`
	DatabaseBytes int64  `json:"databaseBytes"`
	ObjectBytes   int64  `json:"objectBytes"`
	ObjectCount   int    `json:"objectCount"`
	TotalBytes    int64  `json:"totalBytes"`
}

func (s *Store) handleWorkspaceStorage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.workspaceStorage(r.Context())
	if err != nil {
		log.Printf("workspace storage: %v", err)
		writeError(w, http.StatusInternalServerError, "WORKSPACE_STORAGE_FAILED", "暂时无法读取资料库用量，请重试。")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Store) workspaceStorage(ctx context.Context) (workspaceStorage, error) {
	directory := filepath.Dir(s.objectsDir)
	usage := workspaceStorage{Directory: directory, SchemaVersion: workspaceSchemaVersion}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(filepath.Join(directory, workspaceDatabase+suffix))
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return workspaceStorage{}, err
		}
		usage.DatabaseBytes += info.Size()
	}

	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return workspaceStorage{}, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return workspaceStorage{}, err
		}
		if !entry.Type().IsRegular() || !validObjectDigest(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return workspaceStorage{}, err
		}
		usage.ObjectBytes += info.Size()
		usage.ObjectCount++
	}
	usage.TotalBytes = usage.DatabaseBytes + usage.ObjectBytes
	return usage, nil
}
