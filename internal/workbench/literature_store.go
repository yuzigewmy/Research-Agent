package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"research-agent/internal/literature"
)

const literatureSchema = `
	CREATE TABLE literature_citations (
		ordinal INTEGER PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		doi TEXT NOT NULL,
		metadata_json TEXT NOT NULL,
		verified_at TEXT NOT NULL,
		UNIQUE(project_id, doi)
	);
	CREATE INDEX literature_latest ON literature_citations(project_id, ordinal DESC);
	PRAGMA user_version = 10;
`

const citationColumns = `ordinal, metadata_json, verified_at`

type Citation struct {
	literature.Work
	VerifiedAt string `json:"verifiedAt"`
	ordinal    int64
}

func scanCitation(row interface{ Scan(...any) error }) (Citation, error) {
	var citation Citation
	var metadata []byte
	if err := row.Scan(&citation.ordinal, &metadata, &citation.VerifiedAt); err != nil {
		return Citation{}, err
	}
	err := json.Unmarshal(metadata, &citation.Work)
	return citation, err
}

func (s *Store) getCitation(ctx context.Context, projectID, doi string) (Citation, error) {
	return scanCitation(s.db.QueryRowContext(ctx, `SELECT `+citationColumns+` FROM literature_citations
		WHERE project_id = ? AND doi = ?`, projectID, doi))
}

func (s *Store) saveCitation(ctx context.Context, projectID string, work literature.Work) (Citation, bool, error) {
	metadata, err := json.Marshal(work)
	if err != nil {
		return Citation{}, false, err
	}
	citation, err := scanCitation(s.db.QueryRowContext(ctx, `INSERT INTO literature_citations
		(project_id, doi, metadata_json, verified_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id, doi) DO NOTHING RETURNING `+citationColumns,
		projectID, work.DOI, string(metadata), time.Now().UTC().Format(createdAtLayout)))
	if errors.Is(err, sql.ErrNoRows) {
		citation, err = s.getCitation(ctx, projectID, work.DOI)
		return citation, false, err
	}
	return citation, err == nil, err
}

func (s *Store) listCitations(ctx context.Context, projectID string, before int64, limit int) ([]Citation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+citationColumns+` FROM literature_citations
		WHERE project_id = ? AND (? = 0 OR ordinal < ?) ORDER BY ordinal DESC LIMIT ?`, projectID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Citation{}
	for rows.Next() {
		citation, err := scanCitation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, citation)
	}
	return items, rows.Err()
}

func citationKey(doi string) string {
	digest := sha256.Sum256([]byte(doi))
	return fmt.Sprintf("ref-%x", digest[:8])
}

var bibTeXEscapes = strings.NewReplacer(
	`\`, `\textbackslash{}`, `{`, `\{`, `}`, `\}`, `%`, `\%`, `&`, `\&`, `$`, `\$`, `#`, `\#`,
	`_`, `\_`, `^`, `\textasciicircum{}`, `~`, `\textasciitilde{}`, "\r", " ", "\n", " ",
)

func bibliography(citations []Citation) string {
	var out strings.Builder
	for i := len(citations) - 1; i >= 0; i-- {
		item := citations[i]
		kind := "misc"
		if item.Journal != "" {
			kind = "article"
		}
		fmt.Fprintf(&out, "@%s{%s,\n", kind, citationKey(item.DOI))
		field := func(key, value string) {
			if value != "" {
				fmt.Fprintf(&out, "  %s = {%s},\n", key, bibTeXEscapes.Replace(value))
			}
		}
		field("title", item.Title)
		field("author", strings.Join(item.Authors, " and "))
		field("journal", item.Journal)
		if item.Year != 0 {
			field("year", fmt.Sprint(item.Year))
		}
		field("doi", item.DOI)
		out.WriteString("}\n\n")
	}
	return out.String()
}
