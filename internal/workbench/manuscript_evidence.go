package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
)

const manuscriptEvidenceSchema = `
	CREATE TABLE manuscript_comparison_links (
		ordinal INTEGER PRIMARY KEY,
		link_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL,
		manuscript_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		task_id TEXT NOT NULL,
		input_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		metric TEXT NOT NULL CHECK (metric IN ('baselineMean', 'variantMean', 'meanDifference')),
		value TEXT NOT NULL,
		start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
		end_byte INTEGER NOT NULL CHECK (end_byte > start_byte),
		selected_text TEXT NOT NULL,
		UNIQUE (project_id, manuscript_version_id, task_id, metric, start_byte, end_byte),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	PRAGMA user_version = 55;
`

const maxManuscriptComparisonLinks = 100

var errManuscriptEvidenceLimit = errors.New("manuscript comparison link limit reached")

const selectManuscriptEvidence = `SELECT l.link_id, l.task_id, l.metric, l.value, l.start_byte, l.end_byte, l.selected_text,
	v.path, v.version_id, v.sha256, v.version_id IS NOT (
		SELECT version_id FROM workspace_versions WHERE project_id = l.project_id AND path = v.path ORDER BY ordinal DESC LIMIT 1
	)
	FROM manuscript_comparison_links l JOIN workspace_versions v
	ON v.project_id = l.project_id AND v.version_id = l.input_version_id
	WHERE l.project_id = ? AND l.manuscript_version_id = ?`

type manuscriptEvidenceInput struct {
	TaskID string `json:"taskId"`
	Metric string `json:"metric"`
	Start  int    `json:"startByte"`
	End    int    `json:"endByte"`
}

type manuscriptEvidenceLink struct {
	manuscriptEvidenceInput
	LinkID        string    `json:"linkId"`
	Value         string    `json:"value"`
	Text          string    `json:"text"`
	Input         taskInput `json:"input"`
	SourceChanged bool      `json:"sourceChanged"`
}

func scanManuscriptEvidence(row interface{ Scan(...any) error }) (manuscriptEvidenceLink, error) {
	var link manuscriptEvidenceLink
	err := row.Scan(&link.LinkID, &link.TaskID, &link.Metric, &link.Value, &link.Start, &link.End, &link.Text,
		&link.Input.Path, &link.Input.VersionID, &link.Input.SHA256, &link.SourceChanged)
	return link, err
}

func (s *Store) createManuscriptEvidence(ctx context.Context, projectID, versionID string, input manuscriptEvidenceInput) (manuscriptEvidenceLink, bool, error) {
	var empty manuscriptEvidenceLink
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()

	existing, err := scanManuscriptEvidence(tx.QueryRowContext(ctx, selectManuscriptEvidence+`
		AND l.task_id = ? AND l.metric = ? AND l.start_byte = ? AND l.end_byte = ?`, projectID, versionID, input.TaskID, input.Metric, input.Start, input.End))
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, false, err
	}
	selection, err := s.resolveManuscriptSelection(ctx, tx, projectID, &manuscriptSelectionInput{
		VersionID: versionID, StartByte: input.Start, EndByte: input.End,
	})
	if err != nil {
		return empty, false, err
	}
	latest, err := currentWorkspaceFile(ctx, tx, projectID, selection.Path)
	if err != nil {
		return empty, false, err
	}
	if latest.VersionID != versionID {
		return empty, false, errWorkspaceConflict
	}
	report, err := readExperimentReport(ctx, tx, projectID, input.TaskID)
	if err != nil {
		return empty, false, err
	}
	value := comparisonMetricValue(report.Metrics, input.Metric)
	if value == "" {
		return empty, false, errExperimentEvidence
	}
	if _, err := s.readExperimentSourceFrom(ctx, tx, projectID, report.Input); err != nil {
		return empty, false, errExperimentEvidence
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM manuscript_comparison_links WHERE project_id = ? AND manuscript_version_id = ?`, projectID, versionID).Scan(&count); err != nil {
		return empty, false, err
	}
	if count >= maxManuscriptComparisonLinks {
		return empty, false, errManuscriptEvidenceLimit
	}
	id := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO manuscript_comparison_links
		(link_id, project_id, manuscript_version_id, task_id, input_version_id, metric, value, start_byte, end_byte, selected_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, projectID, versionID, input.TaskID, report.Input.VersionID,
		input.Metric, value, input.Start, input.End, selection.Text); err != nil {
		return empty, false, err
	}
	link, err := scanManuscriptEvidence(tx.QueryRowContext(ctx, selectManuscriptEvidence+` AND l.link_id = ?`, projectID, versionID, id))
	if err != nil {
		return empty, false, err
	}
	return link, true, tx.Commit()
}

func comparisonMetricValue(metrics experimentMetrics, metric string) string {
	switch metric {
	case "baselineMean":
		return metrics.BaselineMean.String()
	case "variantMean":
		return metrics.VariantMean.String()
	case "meanDifference":
		return metrics.MeanDifference.String()
	default:
		return ""
	}
}

func (s *Store) handleCreateManuscriptEvidence(w http.ResponseWriter, r *http.Request) {
	var input manuscriptEvidenceInput
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if !validRequestID(input.TaskID) || (input.Metric != "baselineMean" && input.Metric != "variantMean" && input.Metric != "meanDifference") {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择已完成的配对任务和要关联的指标。")
		return
	}
	link, created, err := s.createManuscriptEvidence(r.Context(), r.PathValue("id"), r.PathValue("versionId"), input)
	switch {
	case errors.Is(err, errInvalidManuscriptSelection):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SELECTION", "请从已保存的 Markdown 稿件中选取不超过 8 KiB 的完整文字。")
	case errors.Is(err, errWorkspaceConflict):
		writeError(w, http.StatusConflict, "MANUSCRIPT_VERSION_CHANGED", "稿件已有新版本。请重新加载并选择要关联的文字。")
	case errors.Is(err, errManuscriptEvidenceLimit):
		writeError(w, http.StatusConflict, "EVIDENCE_LIMIT", "此稿件版本已关联 100 条配对计算证据。")
	case err != nil:
		writeExperimentEvidenceError(w, err)
	case created:
		writeJSON(w, http.StatusCreated, link)
	default:
		writeJSON(w, http.StatusOK, link)
	}
}

func (s *Store) handleListManuscriptEvidence(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, versionID := r.Context(), r.PathValue("id"), r.PathValue("versionId")
	file, err := s.workspaceVersion(ctx, projectID, versionID)
	if err != nil {
		writeWorkspaceError(w, "find manuscript evidence version", err)
		return
	}
	if !markdownPath(file.Path) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该版本不是 Markdown 稿件。")
		return
	}
	rows, err := s.db.QueryContext(ctx, selectManuscriptEvidence+` ORDER BY l.ordinal DESC LIMIT ?`, projectID, versionID, maxManuscriptComparisonLinks)
	if err != nil {
		writeStoreError(w, "list manuscript evidence", err)
		return
	}
	defer rows.Close()
	items := make([]manuscriptEvidenceLink, 0)
	for rows.Next() {
		link, err := scanManuscriptEvidence(rows)
		if err != nil {
			writeStoreError(w, "read manuscript evidence", err)
			return
		}
		items = append(items, link)
	}
	if err := rows.Err(); err != nil {
		writeStoreError(w, "list manuscript evidence", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []manuscriptEvidenceLink `json:"items"`
	}{Items: items})
}
