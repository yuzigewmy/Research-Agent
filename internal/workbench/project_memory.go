package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"research-agent/internal/literature"
)

const projectMemoryBriefMaxRunes = 800

type ProjectMemorySummary struct {
	Project                  projectMemoryMeta        `json:"project"`
	ResearchBrief            *projectMemoryBrief      `json:"researchBrief,omitempty"`
	VerifiedCitationCount    int                      `json:"verifiedCitationCount"`
	CompletedComparisonCount int                      `json:"completedComparisonCount"`
	LatestWorkflow           *projectMemoryWorkflow   `json:"latestWorkflow,omitempty"`
	EvidenceChain            []projectEvidenceChain   `json:"evidenceChain"`
}

type projectMemoryMeta struct {
	ProjectID    string `json:"projectId"`
	Title        string `json:"title"`
	DisplayName  string `json:"displayName,omitempty"`
	TemplateID   string `json:"templateId"`
	Status       string `json:"status"`
	Revision     int    `json:"revision"`
	Goal         string `json:"goal,omitempty"`
	Deliverables string `json:"deliverables,omitempty"`
}

type projectMemoryBrief struct {
	Path      string `json:"path"`
	VersionID string `json:"versionId"`
	SHA256    string `json:"sha256"`
	UpdatedAt string `json:"updatedAt"`
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated"`
}

type projectMemoryWorkflow struct {
	RunID          string `json:"runId"`
	WorkflowKey    string `json:"workflowKey"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
	TotalSteps     int    `json:"totalSteps"`
	SucceededSteps int    `json:"succeededSteps"`
}

type projectEvidenceChain struct {
	Kind                string `json:"kind"`
	LinkID              string `json:"linkId"`
	ManuscriptPath      string `json:"manuscriptPath"`
	ManuscriptVersionID string `json:"manuscriptVersionId"`
	ManuscriptOutdated  bool   `json:"manuscriptOutdated"`
	SelectedText        string `json:"selectedText"`
	SourceSummary       string `json:"sourceSummary"`
	SourceChanged       bool   `json:"sourceChanged"`
	TaskID              string `json:"taskId,omitempty"`
	Metric              string `json:"metric,omitempty"`
	Value               string `json:"value,omitempty"`
	SourcePath          string `json:"sourcePath,omitempty"`
	SourceVersionID     string `json:"sourceVersionId,omitempty"`
	SourceSHA256        string `json:"sourceSha256,omitempty"`
	MaterialID          string `json:"materialId,omitempty"`
	MaterialName        string `json:"materialName,omitempty"`
	Page                int    `json:"page,omitempty"`
	Quote               string `json:"quote,omitempty"`
	CitationDOI         string `json:"citationDoi,omitempty"`
	CitationTitle       string `json:"citationTitle,omitempty"`
}

func (s *Store) registerProjectMemory(mux *http.ServeMux) {
	mux.HandleFunc("GET "+projectsPath+"/{id}/memory-summary", s.handleProjectMemorySummary)
	mux.HandleFunc(projectsPath+"/{id}/memory-summary", methodNotAllowed("GET, HEAD"))
}

func truncateRunes(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	count := 0
	for idx := range text {
		if count == limit {
			return text[:idx], true
		}
		count++
	}
	return text, false
}

func (s *Store) projectMemorySummary(ctx context.Context, projectID string) (ProjectMemorySummary, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectMemorySummary{}, err
	}
	defer tx.Rollback()

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE project_id = ?`, projectID))
	if err != nil {
		return ProjectMemorySummary{}, err
	}

	summary := ProjectMemorySummary{
		Project: projectMemoryMeta{
			ProjectID:    project.ProjectID,
			Title:        project.Title,
			DisplayName:  project.DisplayName,
			TemplateID:   project.TemplateID,
			Status:       project.Status,
			Revision:     project.Revision,
			Goal:         project.Goal,
			Deliverables: project.Deliverables,
		},
		EvidenceChain: make([]projectEvidenceChain, 0),
	}

	if briefFile, err := currentWorkspaceFile(ctx, tx, projectID, "research/brief.md"); err == nil {
		if doc, readErr := s.readWorkspaceDocument(briefFile); readErr == nil {
			excerpt, truncated := truncateRunes(doc.Content, projectMemoryBriefMaxRunes)
			summary.ResearchBrief = &projectMemoryBrief{
				Path:      briefFile.Path,
				VersionID: briefFile.VersionID,
				SHA256:    briefFile.SHA256,
				UpdatedAt: briefFile.CreatedAt,
				Excerpt:   excerpt,
				Truncated: truncated,
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectMemorySummary{}, err
	}

	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM literature_citations WHERE project_id = ?`, projectID).
		Scan(&summary.VerifiedCitationCount); err != nil {
		return ProjectMemorySummary{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND kind = ? AND status = 'SUCCEEDED'`,
		projectID, experimentComparisonTask).Scan(&summary.CompletedComparisonCount); err != nil {
		return ProjectMemorySummary{}, err
	}

	var wf projectMemoryWorkflow
	err = tx.QueryRowContext(ctx, `SELECT run_id, workflow_key, status, created_at FROM workflow_runs
		WHERE project_id = ? ORDER BY ordinal DESC LIMIT 1`, projectID).
		Scan(&wf.RunID, &wf.WorkflowKey, &wf.Status, &wf.CreatedAt)
	if err == nil {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'SUCCEEDED' THEN 1 ELSE 0 END), 0)
			FROM tasks WHERE project_id = ? AND workflow_run_id = ? AND workflow_superseded = 0`,
			projectID, wf.RunID).Scan(&wf.TotalSteps, &wf.SucceededSteps); err != nil {
			return ProjectMemorySummary{}, err
		}
		summary.LatestWorkflow = &wf
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectMemorySummary{}, err
	}

	compRows, err := tx.QueryContext(ctx, `SELECT
		l.link_id, mv.path, l.manuscript_version_id,
		mv.version_id IS NOT (
			SELECT version_id FROM workspace_versions WHERE project_id = l.project_id AND path = mv.path ORDER BY ordinal DESC LIMIT 1
		),
		l.selected_text, l.task_id, l.metric, l.value,
		iv.path, iv.version_id, iv.sha256,
		iv.version_id IS NOT (
			SELECT version_id FROM workspace_versions WHERE project_id = l.project_id AND path = iv.path ORDER BY ordinal DESC LIMIT 1
		)
		FROM manuscript_comparison_links l
		JOIN workspace_versions mv ON mv.project_id = l.project_id AND mv.version_id = l.manuscript_version_id
		JOIN workspace_versions iv ON iv.project_id = l.project_id AND iv.version_id = l.input_version_id
		WHERE l.project_id = ?
		ORDER BY l.ordinal DESC LIMIT 100`, projectID)
	if err != nil {
		return ProjectMemorySummary{}, err
	}
	for compRows.Next() {
		var item projectEvidenceChain
		item.Kind = "comparison"
		if err := compRows.Scan(
			&item.LinkID, &item.ManuscriptPath, &item.ManuscriptVersionID, &item.ManuscriptOutdated,
			&item.SelectedText, &item.TaskID, &item.Metric, &item.Value,
			&item.SourcePath, &item.SourceVersionID, &item.SourceSHA256, &item.SourceChanged,
		); err != nil {
			compRows.Close()
			return ProjectMemorySummary{}, err
		}
		item.SourceSummary = fmt.Sprintf("配对指标 %s = %s（来源：%s）", item.Metric, item.Value, item.SourcePath)
		summary.EvidenceChain = append(summary.EvidenceChain, item)
	}
	if err := compRows.Err(); err != nil {
		compRows.Close()
		return ProjectMemorySummary{}, err
	}
	compRows.Close()

	pdfRows, err := tx.QueryContext(ctx, `SELECT
		l.link_id, mv.path, l.manuscript_version_id,
		mv.version_id IS NOT (
			SELECT version_id FROM workspace_versions WHERE project_id = l.project_id AND path = mv.path ORDER BY ordinal DESC LIMIT 1
		),
		l.selected_text, l.material_id, m.name, m.sha256, l.page, l.quote,
		EXISTS (
			SELECT 1 FROM materials newer WHERE newer.project_id = m.project_id AND newer.name = m.name AND newer.ordinal > m.ordinal
		),
		COALESCE(c.doi, ''), c.metadata_json
		FROM manuscript_pdf_links l
		JOIN workspace_versions mv ON mv.project_id = l.project_id AND mv.version_id = l.manuscript_version_id
		JOIN materials m ON m.project_id = l.project_id AND m.material_id = l.material_id
		LEFT JOIN manuscript_pdf_citations binding ON binding.link_id = l.link_id AND binding.project_id = l.project_id
		LEFT JOIN literature_citations c ON c.project_id = binding.project_id AND c.doi = binding.doi
		WHERE l.project_id = ?
		ORDER BY l.ordinal DESC LIMIT 100`, projectID)
	if err != nil {
		return ProjectMemorySummary{}, err
	}
	for pdfRows.Next() {
		var item projectEvidenceChain
		var metadata []byte
		item.Kind = "pdf"
		if err := pdfRows.Scan(
			&item.LinkID, &item.ManuscriptPath, &item.ManuscriptVersionID, &item.ManuscriptOutdated,
			&item.SelectedText, &item.MaterialID, &item.MaterialName, &item.SourceSHA256, &item.Page, &item.Quote,
			&item.SourceChanged, &item.CitationDOI, &metadata,
		); err != nil {
			pdfRows.Close()
			return ProjectMemorySummary{}, err
		}
		if len(metadata) > 0 {
			var work literature.Work
			if json.Unmarshal(metadata, &work) == nil {
				item.CitationTitle = work.Title
			}
		}
		if item.CitationDOI != "" {
			item.SourceSummary = fmt.Sprintf("PDF 第 %d 页（%s） · DOI %s", item.Page, item.MaterialName, item.CitationDOI)
		} else {
			item.SourceSummary = fmt.Sprintf("PDF 第 %d 页（%s）", item.Page, item.MaterialName)
		}
		summary.EvidenceChain = append(summary.EvidenceChain, item)
	}
	if err := pdfRows.Err(); err != nil {
		pdfRows.Close()
		return ProjectMemorySummary{}, err
	}
	pdfRows.Close()

	return summary, nil
}

func (s *Store) handleProjectMemorySummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.projectMemorySummary(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	case err != nil:
		writeStoreError(w, "project memory summary", err)
	default:
		writeJSON(w, http.StatusOK, summary)
	}
}
