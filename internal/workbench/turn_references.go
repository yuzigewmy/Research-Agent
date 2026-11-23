package workbench

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
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
	Page       int    `json:"page,omitempty"`
	Quote      string `json:"quote,omitempty"`
}

type pdfQuote struct {
	SHA256 string `json:"sha256"`
	Page   int    `json:"page"`
	Text   string `json:"text"`
}

func validTurnReferences(ids []string, quotes map[string]pdfQuote) bool {
	if len(ids) > maxTurnReferences || len(quotes) > len(ids) {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validRequestID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	for id, quote := range quotes {
		if !seen[id] || len(quote.SHA256) != 64 || quote.Page < 1 || quote.Page > 1<<31-1 ||
			len(quote.Text) > referenceExcerptBytes || strings.TrimSpace(quote.Text) == "" || strings.ContainsRune(quote.Text, 0) {
			return false
		}
		if _, err := hex.DecodeString(quote.SHA256); err != nil {
			return false
		}
	}
	return true
}

func resolveTurnReferences(ctx context.Context, tx *sql.Tx, projectID string, ids []string, quotes map[string]pdfQuote) ([]TurnReference, error) {
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
		if quote, selected := quotes[id]; selected {
			if ref.Format != "pdf" || ref.SHA256 != quote.SHA256 {
				return nil, errInvalidTurnReference
			}
			ref.Page, ref.Quote = quote.Page, quote.Text
		} else if kind != "text" && ref.Format != "word" {
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
		excerpt := MaterialPreview{Text: ref.Quote}
		if ref.Page != 0 {
			ref.Quote = ""
		} else {
			var err error
			excerpt, err = s.materialExcerpt(ctx, ref.SHA256, ref.Format, referenceExcerptBytes)
			if err != nil {
				return nil, err
			}
		}
		materials = append(materials, turnMaterialExcerpt{ref, excerpt})
	}
	return materials, nil
}

func (s *Store) turnInput(ctx context.Context, turn Turn) (string, error) {
	if len(turn.References) == 0 && turn.ManuscriptSelection == nil && len(turn.ManuscriptSelections) == 0 {
		return turn.Text, nil
	}
	materials, err := s.turnMaterialExcerpts(ctx, turn.References)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Request              string                `json:"request"`
		Materials            []turnMaterialExcerpt `json:"materials"`
		ManuscriptSelection  *manuscriptSelection  `json:"manuscriptSelection,omitempty"`
		ManuscriptSelections []manuscriptSelection `json:"manuscriptSelections,omitempty"`
	}{turn.Text, materials, turn.ManuscriptSelection, turn.ManuscriptSelections})
	return "以下 JSON 包含本轮问题和用户选中的资料节选。\n" + string(payload), err
}
