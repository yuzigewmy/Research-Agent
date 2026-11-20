package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"time"
)

const draftMaterialSchema = `
	CREATE TABLE project_draft_materials (
		ordinal INTEGER PRIMARY KEY,
		material_id TEXT NOT NULL UNIQUE,
		draft_id TEXT NOT NULL REFERENCES project_drafts(draft_id),
		name TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		size INTEGER NOT NULL,
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL,
		UNIQUE(draft_id, name, sha256)
	);
	CREATE INDEX project_draft_materials_latest ON project_draft_materials(draft_id, ordinal DESC);
	CREATE INDEX project_draft_materials_versions ON project_draft_materials(draft_id, name, ordinal DESC);
	PRAGMA user_version = 28;
`

const draftUpload = "upload"

func (s *Store) saveDraftMaterial(ctx context.Context, draftID string, req draftChangeInput, name string, body io.Reader) (projectDraft, Material, bool, error) {
	staged, file, err := s.stageMaterial(name, body)
	if err != nil {
		return projectDraft{}, Material{}, false, err
	}
	defer discardMaterialStage(file)
	payload := staged.SHA256 + ":" + staged.Name
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectDraft{}, Material{}, false, err
	}
	defer tx.Rollback()
	draft, replayed, err := prepareDraftMutation(ctx, tx, draftID, draftUpload, req, payload)
	if err != nil {
		return projectDraft{}, Material{}, false, err
	}
	material, err := scanMaterial(tx.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM project_draft_materials
		WHERE draft_id = ? AND name = ? AND sha256 = ?`, draftID, staged.Name, staged.SHA256))
	if replayed {
		return draft, material, false, err
	}
	created := errors.Is(err, sql.ErrNoRows)
	if created {
		if err := s.publishObject(file, staged.SHA256); err != nil {
			return projectDraft{}, Material{}, false, err
		}
		stamp := time.Now().UTC().Format(createdAtLayout)
		material, err = scanMaterial(tx.QueryRowContext(ctx, `INSERT INTO project_draft_materials
			(material_id, draft_id, name, sha256, size, kind, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			RETURNING `+materialColumns, rand.Text(), draftID, staged.Name, staged.SHA256, staged.Size, staged.Kind, stamp))
		if err != nil {
			return projectDraft{}, Material{}, false, err
		}
		draft, err = scanProjectDraft(tx.QueryRowContext(ctx, `UPDATE project_drafts SET revision = revision + 1, updated_at = ?
			WHERE draft_id = ? RETURNING `+projectDraftColumns, stamp, draftID))
	}
	if err != nil {
		return projectDraft{}, Material{}, false, err
	}
	if err := saveDraftReceipt(ctx, tx, draftID, draftUpload, req, payload); err != nil {
		return projectDraft{}, Material{}, false, err
	}
	return draft, material, created, tx.Commit()
}

func (s *Store) draftMaterial(ctx context.Context, draftID, materialID string) (Material, error) {
	return scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM project_draft_materials
		WHERE draft_id = ? AND material_id = ?`, draftID, materialID))
}

func (s *Store) listDraftMaterials(ctx context.Context, draftID string, cursor int64, limit int) ([]Material, error) {
	if _, err := readProjectDraft(ctx, s.db, draftID); err != nil {
		return nil, err
	}
	return s.queryMaterials(ctx, `SELECT `+materialColumns+` FROM project_draft_materials
		WHERE draft_id = ? AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, draftID, cursor, cursor, limit)
}

func (s *Store) draftPlanMaterials(ctx context.Context, draftID string) ([]turnMaterialExcerpt, error) {
	materials, err := s.queryMaterials(ctx, `SELECT `+materialColumns+` FROM project_draft_materials
		WHERE draft_id = ? AND kind = 'text' AND ordinal IN (
			SELECT MAX(ordinal) FROM project_draft_materials WHERE draft_id = ? GROUP BY name
		) ORDER BY ordinal DESC LIMIT ?`, draftID, draftID, maxTurnReferences)
	if err != nil {
		return nil, err
	}
	refs := make([]TurnReference, 0, len(materials))
	for _, material := range materials {
		refs = append(refs, TurnReference{MaterialID: material.MaterialID, Name: material.Name, SHA256: material.SHA256})
	}
	return s.turnMaterialExcerpts(ctx, refs)
}

func publishDraftMaterials(ctx context.Context, tx *sql.Tx, draftID, projectID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO materials(material_id, project_id, name, sha256, size, kind, created_at)
		SELECT material_id, ?, name, sha256, size, kind, created_at FROM project_draft_materials
		WHERE draft_id = ? ORDER BY ordinal`, projectID, draftID)
	return err
}
