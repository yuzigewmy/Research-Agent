package workbench

import (
	"archive/zip"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// RestoreWorkspace verifies a workspace ZIP before publishing a new data directory.
func RestoreWorkspace(ctx context.Context, archivePath, destination string) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := requireNewWorkspace(destination); err != nil {
		return err
	}
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()
	info, err := archiveFile.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(archiveFile, info.Size())
	if err != nil {
		return err
	}
	entries, err := backupArchiveEntries(archive, info.Size())
	if err != nil {
		return err
	}
	manifest, err := readBackupManifest(entries[backupManifestName])
	if err != nil {
		return err
	}
	if len(entries) != len(manifest.Objects)+2 {
		return errors.New("备份成员数量与清单不一致。")
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".research-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	databasePath := filepath.Join(stage, workspaceDatabase)
	if err := extractBackupFile(ctx, entries, workspaceDatabase, databasePath, manifest.Database); err != nil {
		return err
	}
	schema, objects, err := backupInventory(ctx, databasePath)
	if err != nil {
		return err
	}
	if schema != manifest.SchemaVersion || !slices.Equal(objects, manifest.Objects) {
		return errors.New("备份清单与数据库中的资料版本不一致。")
	}
	objectDir := filepath.Join(stage, "objects")
	if err := os.Mkdir(objectDir, 0o700); err != nil {
		return err
	}
	for _, object := range objects {
		if !validBackupFile(object) {
			return errors.New("备份含有无效的对象引用。")
		}
		if err := extractBackupFile(ctx, entries, "objects/"+object.SHA256, filepath.Join(objectDir, object.SHA256), object); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireNewWorkspace(destination); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}

func backupArchiveEntries(archive *zip.Reader, archiveSize int64) (map[string]*zip.File, error) {
	type span struct{ start, end int64 }
	entries := make(map[string]*zip.File, len(archive.File))
	ranges := make([]span, 0, len(archive.File))
	for _, entry := range archive.File {
		if entries[entry.Name] != nil || entry.Method != zip.Store || !entry.Mode().IsRegular() ||
			entry.CompressedSize64 != entry.UncompressedSize64 {
			return nil, errors.New("备份压缩包含有重复或不受支持的成员。")
		}
		offset, err := entry.DataOffset()
		if err != nil || offset < 0 || offset > archiveSize || entry.CompressedSize64 > uint64(archiveSize-offset) {
			return nil, errors.New("备份成员的数据范围无效。")
		}
		if entry.CompressedSize64 != 0 {
			ranges = append(ranges, span{offset, offset + int64(entry.CompressedSize64)})
		}
		entries[entry.Name] = entry
	}
	slices.SortFunc(ranges, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			return nil, errors.New("备份成员的数据范围重叠。")
		}
	}
	return entries, nil
}

func verifyBackupDatabase(ctx context.Context, db *sql.DB) error {
	expected, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	if err := migrate(expected); err != nil {
		return err
	}
	want, err := readBackupSchema(ctx, expected)
	if err != nil {
		return err
	}
	actual, err := readBackupSchema(ctx, db)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, want) {
		return errors.New("备份数据库的结构与当前资料库版本不一致。")
	}
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check(1)`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("备份数据库未通过完整性检查。")
	}
	var brokenLinks bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check)`).Scan(&brokenLinks); err != nil {
		return err
	}
	if brokenLinks {
		return errors.New("备份数据库含有无效的关联记录。")
	}
	return nil
}

type backupSchemaObject struct{ kind, name, table, definition string }

// Compare schema objects without their page offsets, which VACUUM can change.
// https://www.sqlite.org/schematab.html
func readBackupSchema(ctx context.Context, db *sql.DB) ([]backupSchemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM main.sqlite_schema ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []backupSchemaObject
	for rows.Next() {
		var object backupSchemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.table, &object.definition); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func requireNewWorkspace(destination string) error {
	_, err := os.Lstat(destination)
	switch {
	case err == nil:
		return fmt.Errorf("恢复需要新目录，目标已存在 %q", destination)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}

func readBackupManifest(entry *zip.File) (backupManifest, error) {
	var manifest backupManifest
	if entry == nil || entry.UncompressedSize64 > backupManifestMax {
		return manifest, errors.New("备份清单缺失或超过 16 MiB。")
	}
	file, err := entry.Open()
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Version != backupVersion || !validBackupFile(manifest.Database) {
		return manifest, errors.New("备份格式版本或数据库清单无效。")
	}
	return manifest, nil
}

func extractBackupFile(ctx context.Context, entries map[string]*zip.File, name, destination string, expected backupFile) error {
	entry := entries[name]
	if entry == nil || entry.UncompressedSize64 != uint64(expected.Size) {
		return fmt.Errorf("备份成员 %s 缺失或长度与清单不一致。", name)
	}
	source, err := entry.Open()
	if err != nil {
		return fmt.Errorf("读取备份成员 %s: %w", name, err)
	}
	defer source.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := copyBackupFile(ctx, file, source)
	if err != nil {
		return fmt.Errorf("恢复备份成员 %s: %w", name, err)
	}
	if actual != expected {
		return fmt.Errorf("备份成员 %s 的长度或摘要不一致。", name)
	}
	return file.Sync()
}
