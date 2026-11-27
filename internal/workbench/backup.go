package workbench

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	backupVersion            = 1
	backupManifestName       = "manifest.json"
	backupManifestMax        = 16 << 20
	maxBackupObjects         = backupManifestMax / (2 * sha256.Size)
	firstBackupSchemaVersion = 50
)

type backupFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type backupManifest struct {
	Version       int           `json:"version"`
	SchemaVersion int           `json:"schemaVersion"`
	CreatedAt     string        `json:"createdAt"`
	Database      backupFile    `json:"database"`
	Objects       backupObjects `json:"objects"`
}

func (s *Store) handleWorkspaceBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupMu.TryLock() {
		writeError(w, http.StatusConflict, "BACKUP_RUNNING", "正在生成工作区备份，请稍后再试。")
		return
	}
	defer s.backupMu.Unlock()
	stage, err := os.MkdirTemp("", "research-backup-")
	if err != nil {
		writeBackupError(w, err)
		return
	}
	defer os.RemoveAll(stage)
	file, err := os.OpenFile(filepath.Join(stage, "backup.zip"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	defer file.Close()
	if err := s.writeWorkspaceBackup(r.Context(), stage, file); err != nil {
		writeBackupError(w, err)
		return
	}
	length, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeBackupError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="research-workspace-`+time.Now().UTC().Format("20060102-150405Z")+`.zip"`)
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(backupDownloadWriter{w, http.NewResponseController(w)}, file)
}

// Healthy downloads can continue indefinitely. A stalled client releases the transfer lock.
type backupDownloadWriter struct {
	io.Writer
	*http.ResponseController
}

func (w backupDownloadWriter) Write(data []byte) (int, error) {
	if err := w.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return w.Writer.Write(data)
}

func writeBackupError(w http.ResponseWriter, err error) {
	log.Printf("workspace backup: %v", err)
	writeError(w, http.StatusInternalServerError, "BACKUP_FAILED", "无法生成工作区备份。请检查临时目录的可用空间及工作区资料，再重试。")
}

func (s *Store) writeWorkspaceBackup(ctx context.Context, stage string, out io.Writer) error {
	databasePath := filepath.Join(stage, workspaceDatabase)
	// The snapshot includes committed WAL changes. Object files are immutable and
	// published before their references commit, so the snapshot selects a complete set.
	// https://www.sqlite.org/lang_vacuum.html#vacuum_with_an_into_clause
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, databasePath); err != nil {
		return err
	}
	schema, objects, err := backupInventory(ctx, databasePath)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(out)
	databaseFile, err := os.Open(databasePath)
	if err != nil {
		return err
	}
	database, err := zipBackupFile(ctx, archive, workspaceDatabase, databaseFile)
	if err != nil {
		return err
	}
	for _, object := range objects {
		source, err := s.openObject(object.SHA256)
		if err != nil {
			return fmt.Errorf("读取资料对象 %s: %w", object.SHA256, err)
		}
		saved, err := zipBackupFile(ctx, archive, "objects/"+object.SHA256, source)
		if err != nil {
			return err
		}
		if saved != object {
			return fmt.Errorf("资料对象 %s 的长度或摘要不一致。", object.SHA256)
		}
	}
	manifest, err := json.Marshal(backupManifest{Version: backupVersion, SchemaVersion: schema,
		CreatedAt: time.Now().UTC().Format(createdAtLayout), Database: database, Objects: objects})
	if err != nil {
		return err
	}
	if len(manifest) > backupManifestMax {
		return errors.New("备份清单超过 16 MiB。")
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: backupManifestName, Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := entry.Write(manifest); err != nil {
		return err
	}
	return archive.Close()
}

func zipBackupFile(ctx context.Context, archive *zip.Writer, name string, source *os.File) (backupFile, error) {
	defer source.Close()
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return backupFile{}, err
	}
	return copyBackupFile(ctx, entry, source)
}

func validBackupFile(file backupFile) bool {
	return file.Size >= 0 && validObjectDigest(file.SHA256)
}

func copyBackupFile(ctx context.Context, destination io.Writer, source io.Reader) (backupFile, error) {
	hash := sha256.New()
	bytes, err := io.Copy(io.MultiWriter(destination, hash), contextReader{ctx, source})
	return backupFile{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: bytes}, err
}

func backupInventory(ctx context.Context, databasePath string) (int, []backupFile, error) {
	// This staged database has no WAL and is unchanged while the inventory is read.
	// https://sqlite.org/uri.html#recognized_query_parameters
	db, err := sql.Open("sqlite", workspaceSQLiteURI(databasePath, "mode=ro&immutable=1&_pragma=trusted_schema(OFF)"))
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, nil, err
	}
	if version < firstBackupSchemaVersion || version > workspaceSchemaVersion {
		return 0, nil, fmt.Errorf("请用支持第 %d 版资料库的程序恢复此备份。", version)
	}
	if err := verifyBackupDatabase(ctx, db, version); err != nil {
		return 0, nil, err
	}
	// Bound values before they cross into Go. UNION ALL avoids sorting or
	// deduplicating unvalidated values in SQLite.
	objectSources := `SELECT sha256, size FROM materials
			UNION ALL SELECT sha256, size FROM workspace_versions
			UNION ALL SELECT sha256, size FROM project_draft_materials`
	if version >= 64 {
		objectSources += `
			UNION ALL SELECT sha256, size FROM script_run_artifacts`
	}
	rows, err := db.QueryContext(ctx, `SELECT
		CASE WHEN typeof(sha256) = 'text' AND octet_length(sha256) IN (64, 128) THEN sha256 ELSE '' END,
		CASE WHEN typeof(size) = 'integer' THEN size ELSE -1 END
		FROM (`+objectSources+`)`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	objects := make([]backupFile, 0)
	sizes := make(map[string]int64)
	for rows.Next() {
		var object backupFile
		if err := rows.Scan(&object.SHA256, &object.Size); err != nil {
			return 0, nil, err
		}
		if !validBackupFile(object) {
			return 0, nil, errors.New("资料库含有无效的对象引用。")
		}
		if size, found := sizes[object.SHA256]; found {
			if size != object.Size {
				return 0, nil, fmt.Errorf("资料对象 %s 的索引长度不一致。", object.SHA256)
			}
			continue
		}
		if len(objects) == maxBackupObjects {
			return 0, nil, errors.New("资料对象数量超出备份清单上限。")
		}
		sizes[object.SHA256] = object.Size
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	slices.SortFunc(objects, func(a, b backupFile) int { return strings.Compare(a.SHA256, b.SHA256) })
	return version, objects, ctx.Err()
}
