package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const turnReferenceSchema = `
	ALTER TABLE turns ADD COLUMN references_json TEXT NOT NULL DEFAULT '[]';
	PRAGMA user_version = 6;
`

const maxTurnReferences = 4
const referenceExcerptBytes = 8 << 10

var errInvalidTurnReference = errors.New("reference does not identify a readable material in this project")

type TurnReference struct {
	MaterialID string `json:"materialId"`
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Format     string `json:"format,omitempty"`
}

func validTurnReferenceIDs(ids []string) bool {
	if len(ids) > maxTurnReferences {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validRequestID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func resolveTurnReferences(ctx context.Context, tx *sql.Tx, projectID string, ids []string) ([]TurnReference, error) {
	references := make([]TurnReference, 0, len(ids))
	for _, id := range ids {
		ref := TurnReference{MaterialID: id}
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT name, sha256, kind FROM materials
			WHERE project_id = ? AND material_id = ?`, projectID, id).Scan(&ref.Name, &ref.SHA256, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errInvalidTurnReference
		}
		if err != nil {
			return nil, err
		}
		ref.Format = materialFormat(ref.Name, kind)
		if kind != "text" && ref.Format != "word" {
			return nil, errInvalidTurnReference
		}
		references = append(references, ref)
	}
	return references, nil
}

type turnMaterialExcerpt struct {
	TurnReference
	MaterialPreview
}

func (s *Store) turnMaterialExcerpts(ctx context.Context, references []TurnReference) ([]turnMaterialExcerpt, error) {
	materials := make([]turnMaterialExcerpt, 0, len(references))
	for _, ref := range references {
		excerpt, err := s.materialExcerpt(ctx, ref.SHA256, ref.Format, referenceExcerptBytes)
		if err != nil {
			return nil, err
		}
		materials = append(materials, turnMaterialExcerpt{ref, excerpt})
	}
	return materials, nil
}

func (s *Store) turnInput(ctx context.Context, turn Turn) (string, error) {
	if len(turn.References) == 0 {
		return turn.Text, nil
	}
	materials, err := s.turnMaterialExcerpts(ctx, turn.References)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Request   string                `json:"request"`
		Materials []turnMaterialExcerpt `json:"materials"`
	}{turn.Text, materials})
	return "以下 JSON 包含本轮问题和用户选中的资料节选。\n" + string(payload), err
}
