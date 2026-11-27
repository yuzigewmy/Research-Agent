package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"research-agent/internal/literature"
)

const manuscriptPDFEvidenceSchema = `
	CREATE TABLE manuscript_pdf_links (
		ordinal INTEGER PRIMARY KEY,
		link_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		manuscript_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		material_id TEXT NOT NULL REFERENCES materials(material_id),
		page INTEGER NOT NULL CHECK (page > 0),
		quote TEXT NOT NULL,
		start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
		end_byte INTEGER NOT NULL CHECK (end_byte > start_byte),
		selected_text TEXT NOT NULL,
		UNIQUE (project_id, manuscript_version_id, material_id, page, start_byte, end_byte, quote)
	);
	PRAGMA user_version = 58;
`

const manuscriptPDFCitationSchema = `
	CREATE TABLE manuscript_pdf_citations (
		link_id TEXT PRIMARY KEY NOT NULL REFERENCES manuscript_pdf_links(link_id),
		project_id TEXT NOT NULL,
		doi TEXT NOT NULL,
		FOREIGN KEY (project_id, doi) REFERENCES literature_citations(project_id, doi)
	);
	PRAGMA user_version = 59;
`

const maxManuscriptPDFLinks = 100

const selectPDFEvidence = `SELECT l.link_id, l.material_id, l.page, l.quote, l.start_byte, l.end_byte,
	l.selected_text, m.name, m.sha256, EXISTS (
		SELECT 1 FROM materials newer WHERE newer.project_id = m.project_id AND newer.name = m.name AND newer.ordinal > m.ordinal
	), c.metadata_json, c.verified_at
	FROM manuscript_pdf_links l
	JOIN materials m ON m.project_id = l.project_id AND m.material_id = l.material_id
	LEFT JOIN manuscript_pdf_citations binding ON binding.link_id = l.link_id AND binding.project_id = l.project_id
	LEFT JOIN literature_citations c ON c.project_id = binding.project_id AND c.doi = binding.doi
	WHERE l.project_id = ? AND l.manuscript_version_id = ?`

type manuscriptPDFInput struct {
	MaterialID string `json:"materialId"`
	SHA256     string `json:"sha256"`
	Page       int    `json:"page"`
	Quote      string `json:"quote"`
	Start      int    `json:"startByte"`
	End        int    `json:"endByte"`
}

type manuscriptPDFLink struct {
	manuscriptPDFInput
	LinkID        string    `json:"linkId"`
	Text          string    `json:"text"`
	Name          string    `json:"name"`
	SourceChanged bool      `json:"sourceChanged"`
	Citation      *Citation `json:"citation,omitempty"`
}

func scanPDFEvidence(row interface{ Scan(...any) error }) (manuscriptPDFLink, error) {
	var link manuscriptPDFLink
	var metadata []byte
	var verifiedAt sql.NullString
	if err := row.Scan(&link.LinkID, &link.MaterialID, &link.Page, &link.Quote, &link.Start, &link.End,
		&link.Text, &link.Name, &link.SHA256, &link.SourceChanged, &metadata, &verifiedAt); err != nil {
		return link, err
	}
	if verifiedAt.Valid {
		link.Citation = &Citation{VerifiedAt: verifiedAt.String}
		if err := json.Unmarshal(metadata, &link.Citation.Work); err != nil {
			return link, err
		}
	}
	return link, nil
}

func (s *Store) createPDFEvidence(ctx context.Context, projectID, versionID string, input manuscriptPDFInput) (manuscriptPDFLink, bool, error) {
	var zero manuscriptPDFLink
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()

	material, err := lookupMaterial(ctx, tx, projectID, input.MaterialID)
	if err != nil {
		return zero, false, err
	}
	if material.Format != "pdf" || material.SHA256 != input.SHA256 {
		return zero, false, errInvalidTurnReference
	}
	query := selectPDFEvidence + ` AND l.material_id = ? AND l.page = ? AND l.start_byte = ? AND l.end_byte = ? AND l.quote = ?`
	args := []any{projectID, versionID, input.MaterialID, input.Page, input.Start, input.End, input.Quote}
	link, err := scanPDFEvidence(tx.QueryRowContext(ctx, query, args...))
	if !errors.Is(err, sql.ErrNoRows) {
		return link, false, err
	}
	selection, err := s.resolveManuscriptSelection(ctx, tx, projectID, &manuscriptSelectionInput{
		VersionID: versionID, StartByte: input.Start, EndByte: input.End,
	})
	if err != nil {
		return zero, false, err
	}
	latest, err := currentWorkspaceFile(ctx, tx, projectID, selection.Path)
	if err != nil {
		return zero, false, err
	}
	if latest.VersionID != versionID {
		return zero, false, errWorkspaceConflict
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM manuscript_pdf_links WHERE project_id = ? AND manuscript_version_id = ?`, projectID, versionID).Scan(&count); err != nil {
		return zero, false, err
	}
	if count >= maxManuscriptPDFLinks {
		return zero, false, errManuscriptEvidenceLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO manuscript_pdf_links
		(link_id, project_id, manuscript_version_id, material_id, page, quote, start_byte, end_byte, selected_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, rand.Text(), projectID, versionID, input.MaterialID,
		input.Page, input.Quote, input.Start, input.End, selection.Text); err != nil {
		return zero, false, err
	}
	link, err = scanPDFEvidence(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return zero, false, err
	}
	return link, true, tx.Commit()
}

func (s *Store) handleCreatePDFEvidence(w http.ResponseWriter, r *http.Request) {
	var input manuscriptPDFInput
	if !readJSON(w, r, &input, 6*referenceExcerptBytes+(4<<10)) {
		return
	}
	if !validRequestID(input.MaterialID) || !validObjectDigest(input.SHA256) || input.Page < 1 || input.Page > 1<<31-1 ||
		len(input.Quote) > referenceExcerptBytes || strings.TrimSpace(input.Quote) == "" || strings.ContainsRune(input.Quote, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择已核对的 PDF 版本及页内节选，最多 8 KiB。")
		return
	}
	link, created, err := s.createPDFEvidence(r.Context(), r.PathValue("id"), r.PathValue("versionId"), input)
	switch {
	case errors.Is(err, errInvalidManuscriptSelection):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SELECTION", "请从已保存的 Markdown 稿件中选取不超过 8 KiB 的完整文字。")
	case errors.Is(err, errInvalidTurnReference):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_PDF_SOURCE", "PDF 类型或原件摘要不符，请重新选取。")
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "MANUSCRIPT_VERSION_CHANGED", "稿件已有新版本。请重新加载并选择要关联的文字。")
	case errors.Is(err, errManuscriptEvidenceLimit):
		writeError(w, http.StatusConflict, "EVIDENCE_LIMIT", "此稿件版本已关联 100 条 PDF 证据。")
	case err != nil:
		writeWorkspaceError(w, "save PDF evidence", err)
	case created:
		writeJSON(w, http.StatusCreated, link)
	default:
		writeJSON(w, http.StatusOK, link)
	}
}

func (s *Store) bindPDFEvidenceCitation(ctx context.Context, projectID, versionID, linkID, doi string) (manuscriptPDFLink, error) {
	var empty manuscriptPDFLink
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()

	query := selectPDFEvidence + ` AND l.link_id = ?`
	link, err := scanPDFEvidence(tx.QueryRowContext(ctx, query, projectID, versionID, linkID))
	if err != nil {
		return empty, err
	}
	if link.Citation != nil && link.Citation.DOI == doi {
		return link, nil
	}
	file, err := lookupWorkspaceVersion(ctx, tx, projectID, versionID)
	if err != nil {
		return empty, err
	}
	latest, err := currentWorkspaceFile(ctx, tx, projectID, file.Path)
	if err != nil {
		return empty, err
	}
	if latest.VersionID != versionID {
		return empty, errWorkspaceConflict
	}
	var savedDOI string
	if err := tx.QueryRowContext(ctx, `SELECT doi FROM literature_citations WHERE project_id = ? AND doi = ?`, projectID, doi).Scan(&savedDOI); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO manuscript_pdf_citations (link_id, project_id, doi)
		VALUES (?, ?, ?) ON CONFLICT (link_id) DO UPDATE SET doi = excluded.doi`, linkID, projectID, savedDOI); err != nil {
		return empty, err
	}
	link, err = scanPDFEvidence(tx.QueryRowContext(ctx, query, projectID, versionID, linkID))
	if err != nil {
		return empty, err
	}
	return link, tx.Commit()
}

func (s *Store) handleBindPDFEvidenceCitation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DOI string `json:"doi"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	doi, err := literature.NormalizeDOI(input.DOI)
	if err != nil {
		writeLiteratureError(w, err)
		return
	}
	link, err := s.bindPDFEvidenceCitation(r.Context(), r.PathValue("id"), r.PathValue("versionId"), r.PathValue("linkId"), doi)
	switch {
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "MANUSCRIPT_VERSION_CHANGED", "稿件已有新版本。请重新加载后关联文献。")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "找不到此稿件的 PDF 证据或本项目中的已核验文献。")
	case err != nil:
		writeStoreError(w, "bind PDF evidence citation", err)
	default:
		writeJSON(w, http.StatusOK, link)
	}
}

func (s *Store) handleListPDFEvidence(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, versionID := r.Context(), r.PathValue("id"), r.PathValue("versionId")
	file, err := s.workspaceVersion(ctx, projectID, versionID)
	if err != nil {
		writeWorkspaceError(w, "find PDF evidence manuscript version", err)
		return
	}
	if !markdownPath(file.Path) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该版本不是 Markdown 稿件。")
		return
	}
	rows, err := s.db.QueryContext(ctx, selectPDFEvidence+` ORDER BY l.ordinal DESC LIMIT ?`, projectID, versionID, maxManuscriptPDFLinks)
	if err != nil {
		writeStoreError(w, "list PDF evidence", err)
		return
	}
	defer rows.Close()
	items := make([]manuscriptPDFLink, 0)
	for rows.Next() {
		link, err := scanPDFEvidence(rows)
		if err != nil {
			writeStoreError(w, "read PDF evidence", err)
			return
		}
		items = append(items, link)
	}
	if err := rows.Err(); err != nil {
		writeStoreError(w, "list PDF evidence", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []manuscriptPDFLink `json:"items"`
	}{Items: items})
}
