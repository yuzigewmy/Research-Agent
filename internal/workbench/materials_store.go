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

	"research-agent/internal/wordread"
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
	Format     string `json:"format,omitempty"`
	CreatedAt  string `json:"createdAt"`
	ordinal    int64
}

type MaterialPreview struct {
	Text      string   `json:"text"`
	Truncated bool     `json:"truncated"`
	Sections  []string `json:"sections,omitempty"`
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

func (s *Store) stageMaterial(name string, body io.Reader) (Material, *os.File, error) {
	file, err := os.CreateTemp(s.objectsDir, ".upload-")
	if err != nil {
		return Material{}, nil, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), body)
	if err != nil {
		discardMaterialStage(file)
		return Material{}, nil, err
	}
	material := Material{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, Kind: "file"}
	if softwareArchiveKind(name) != "" {
		return material, file, nil
	}
	if preview, err := previewText(io.NewSectionReader(file, 0, materialPreviewBytes+1), materialPreviewBytes); err == nil &&
		strings.HasPrefix(http.DetectContentType([]byte(preview.Text)), "text/") {
		material.Kind = "text"
	}
	return material, file, nil
}

func discardMaterialStage(file *os.File) {
	_ = file.Close()
	_ = os.Remove(file.Name())
}

func (s *Store) saveMaterial(ctx context.Context, projectID, name string, body io.Reader) (Material, bool, error) {
	staged, file, err := s.stageMaterial(name, body)
	if err != nil {
		return Material{}, false, err
	}
	defer discardMaterialStage(file)
	if err := s.publishObject(file, staged.SHA256); err != nil {
		return Material{}, false, err
	}

	return insertMaterial(ctx, s.db, projectID, staged)
}

func insertMaterial(ctx context.Context, db workspaceQuery, projectID string, staged Material) (Material, bool, error) {
	material, err := scanMaterial(db.QueryRowContext(ctx, `INSERT INTO materials
		(material_id, project_id, name, sha256, size, kind, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, name, sha256) DO NOTHING
		RETURNING `+materialColumns, rand.Text(), projectID, staged.Name, staged.SHA256, staged.Size, staged.Kind,
		time.Now().UTC().Format(createdAtLayout)))
	if errors.Is(err, sql.ErrNoRows) {
		material, err = scanMaterial(db.QueryRowContext(ctx, `SELECT `+materialColumns+`
			FROM materials WHERE project_id = ? AND name = ? AND sha256 = ?`, projectID, staged.Name, staged.SHA256))
		return material, false, err
	}
	return material, err == nil, err
}

func (s *Store) materialObject(digest string) string {
	return filepath.Join(s.objectsDir, digest)
}

func materialFormat(name, kind string) string {
	if kind == "file" {
		if softwareArchiveKind(name) != "" {
			return softwareArchiveFormat
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".docx", ".dotx":
			return "word"
		case ".pdf":
			return "pdf"
		}
	}
	return ""
}

func scanMaterial(row interface{ Scan(...any) error }) (Material, error) {
	var material Material
	err := row.Scan(&material.ordinal, &material.MaterialID, &material.Name, &material.SHA256, &material.Size, &material.Kind, &material.CreatedAt)
	material.Format = materialFormat(material.Name, material.Kind)
	return material, err
}

func (s *Store) getMaterial(ctx context.Context, projectID, materialID string) (Material, error) {
	return lookupMaterial(ctx, s.db, projectID, materialID)
}

func lookupMaterial(ctx context.Context, db workspaceQuery, projectID, materialID string) (Material, error) {
	return scanMaterial(db.QueryRowContext(ctx, `SELECT `+materialColumns+`
		FROM materials WHERE project_id = ? AND material_id = ?`, projectID, materialID))
}

func (s *Store) listMaterials(ctx context.Context, projectID, search string, cursor int64, limit int) ([]Material, error) {
	query := `SELECT ` + materialColumns + ` FROM materials WHERE project_id = ?`
	args := []any{projectID}
	if search != "" {
		query += ` AND instr(name, ?) > 0`
		args = append(args, search)
	}
	query += ` AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`
	args = append(args, cursor, cursor, limit)
	return s.queryMaterials(ctx, query, args...)
}

func (s *Store) queryMaterials(ctx context.Context, query string, args ...any) ([]Material, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Material, 0)
	for rows.Next() {
		material, err := scanMaterial(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, material)
	}
	return items, rows.Err()
}

func (s *Store) previewMaterial(ctx context.Context, material Material, limit int) (MaterialPreview, error) {
	if material.Kind != "text" && material.Format != "word" {
		return MaterialPreview{}, errMaterialNotText
	}
	if material.Format == "word" {
		return s.wordExcerpt(ctx, material.SHA256, limit, true)
	}
	return s.previewObject(material.SHA256, limit)
}

func (s *Store) materialExcerpt(ctx context.Context, digest, format string, limit int) (MaterialPreview, error) {
	if format != "word" {
		return s.previewObject(digest, limit)
	}
	return s.wordExcerpt(ctx, digest, limit, false)
}

func (s *Store) wordExcerpt(ctx context.Context, digest string, limit int, includeSections bool) (MaterialPreview, error) {
	file, err := s.openObject(digest)
	if err != nil {
		return MaterialPreview{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return MaterialPreview{}, err
	}
	var preview MaterialPreview
	var breaks []int
	if includeSections {
		preview.Text, preview.Truncated, breaks, err = wordread.Preview(ctx, file, info.Size(), limit)
	} else {
		preview.Text, preview.Truncated, err = wordread.Excerpt(ctx, file, info.Size(), limit)
	}
	if errors.Is(err, wordread.ErrDocument) {
		return MaterialPreview{}, errMaterialNotText
	}
	if len(breaks) > 0 {
		start := 0
		for _, end := range append(breaks, len(preview.Text)) {
			section := preview.Text[start:end]
			if strings.TrimSpace(section) != "" {
				preview.Sections = append(preview.Sections, section)
			}
			start = end
		}
		if len(preview.Sections) < 2 {
			preview.Sections = nil
		}
	}
	return preview, err
}

func (s *Store) previewObject(digest string, limit int) (MaterialPreview, error) {
	file, err := s.openObject(digest)
	if err != nil {
		return MaterialPreview{}, err
	}
	defer file.Close()
	return previewText(file, limit)
}
