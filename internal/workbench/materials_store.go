package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const materialColumns = `ordinal, material_id, name, sha256, size, kind, created_at`
const materialPreviewBytes = 64 << 10

const materialsSchema = `
	CREATE TABLE materials (
		ordinal INTEGER PRIMARY KEY,
		material_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		name TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size INTEGER NOT NULL,
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL,
		UNIQUE (project_id, name, sha256)
	);
	CREATE INDEX materials_latest ON materials(project_id, ordinal DESC);
	PRAGMA user_version = 5;
`

// Material is a project-scoped record of an uploaded file.
type Material struct {
	MaterialID string `json:"materialId"`
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"createdAt"`
	ordinal    int64
}

type MaterialPreview struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

var errMaterialNotText = errors.New("material prefix is not UTF-8 text")

func previewText(reader io.Reader, limit int) (MaterialPreview, error) {
	prefix, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return MaterialPreview{}, err
	}
	truncated := len(prefix) > limit
	if truncated {
		end := limit
		for end > 0 && !utf8.RuneStart(prefix[end]) {
			end--
		}
		prefix = prefix[:end]
	}
	if !utf8.Valid(prefix) || bytes.IndexByte(prefix, 0) >= 0 {
		return MaterialPreview{}, errMaterialNotText
	}
	return MaterialPreview{Text: string(bytes.TrimPrefix(prefix, []byte("\xef\xbb\xbf"))), Truncated: truncated}, nil
}

func (s *Store) saveMaterial(ctx context.Context, projectID, name string, body io.Reader) (Material, bool, error) {
	file, err := os.CreateTemp(s.objectsDir, ".upload-")
	if err != nil {
		return Material{}, false, err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), body)
	if err != nil {
		return Material{}, false, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	kind := "file"
	if preview, err := previewText(io.NewSectionReader(file, 0, materialPreviewBytes+1), materialPreviewBytes); err == nil &&
		strings.HasPrefix(http.DetectContentType([]byte(preview.Text)), "text/") {
		kind = "text"
	}
	if err := file.Sync(); err != nil {
		return Material{}, false, err
	}
	if err := file.Close(); err != nil {
		return Material{}, false, err
	}

	// Place the object before publishing its record. Identical uploads share bytes.
	destination := s.materialObject(digest)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(file.Name(), destination); err != nil {
			// Another upload may have published the same digest in the meantime.
			if _, statErr := os.Stat(destination); statErr != nil {
				return Material{}, false, err
			}
		}
	} else if err != nil {
		return Material{}, false, err
	}

	material, err := scanMaterial(s.db.QueryRowContext(ctx, `INSERT INTO materials
		(material_id, project_id, name, sha256, size, kind, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, name, sha256) DO NOTHING
		RETURNING `+materialColumns, rand.Text(), projectID, name, digest, size, kind, time.Now().UTC().Format(createdAtLayout)))
	if errors.Is(err, sql.ErrNoRows) {
		material, err = scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+`
			FROM materials WHERE project_id = ? AND name = ? AND sha256 = ?`, projectID, name, digest))
		return material, false, err
	}
	return material, err == nil, err
}

func (s *Store) materialObject(digest string) string {
	return filepath.Join(s.objectsDir, digest)
}

func scanMaterial(row interface{ Scan(...any) error }) (Material, error) {
	var material Material
	err := row.Scan(&material.ordinal, &material.MaterialID, &material.Name, &material.SHA256, &material.Size, &material.Kind, &material.CreatedAt)
	return material, err
}

func (s *Store) getMaterial(ctx context.Context, projectID, materialID string) (Material, error) {
	return scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+`
		FROM materials WHERE project_id = ? AND material_id = ?`, projectID, materialID))
}

func (s *Store) listMaterials(ctx context.Context, projectID string, cursor int64, limit int) ([]Material, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+materialColumns+` FROM materials
		WHERE project_id = ? AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, projectID, cursor, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Material, 0, limit)
	for rows.Next() {
		material, err := scanMaterial(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, material)
	}
	return items, rows.Err()
}

func (s *Store) previewMaterial(material Material, limit int) (MaterialPreview, error) {
	if material.Kind != "text" {
		return MaterialPreview{}, errMaterialNotText
	}
	file, err := os.Open(s.materialObject(material.SHA256))
	if err != nil {
		return MaterialPreview{}, err
	}
	defer file.Close()
	return previewText(file, limit)
}
