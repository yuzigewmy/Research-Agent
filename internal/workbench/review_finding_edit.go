package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const reviewFindingSchema = `
	ALTER TABLE tasks ADD COLUMN review_findings TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE selection_edits ADD COLUMN review_id TEXT;
	ALTER TABLE selection_edits ADD COLUMN finding_number INTEGER;
	CREATE INDEX selection_edits_by_finding ON selection_edits(project_id, review_id, finding_number) WHERE review_id IS NOT NULL;
	PRAGMA user_version = 16;
`

var errFindingAnchor = errors.New("review quotation no longer identifies one selection")

type reviewFindingRef struct {
	reviewID string
	number   int
}

func (api manuscriptReviews) createFindingEdit(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID     string `json:"requestId"`
		BaseVersionID string `json:"baseVersionId"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number < 1 || !validRequestID(input.RequestID) || !validRequestID(input.BaseVersionID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择评审意见和当前稿件版本。")
		return
	}
	source, ok := api.source(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("id")
	ref := reviewFindingRef{reviewID: r.PathValue("reviewId"), number: number}
	edit, created, err := api.store.queueReviewFindingEdit(r.Context(), projectID, source, input.BaseVersionID, input.RequestID, ref, api.model != nil)
	if errors.Is(err, errFindingAnchor) {
		writeError(w, http.StatusUnprocessableEntity, "FINDING_LOCATION_AMBIGUOUS", "当前稿件中无法唯一定位意见摘录。请在编辑器手动选中要修改的文字。")
		return
	}
	if err != nil {
		writeSelectionEditError(w, err)
		return
	}
	if edit.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, edit)
}

func (s *Store) queueReviewFindingEdit(ctx context.Context, projectID string, source WorkspaceFile, baseVersionID, requestID string, ref reviewFindingRef, modelReady bool) (selectionEdit, bool, error) {
	review, err := getManuscriptReview(ctx, s.db, projectID, source, ref.reviewID)
	if err != nil {
		return selectionEdit{}, false, err
	}
	if review.Status != taskSucceeded || ref.number > len(review.Findings) {
		return selectionEdit{}, false, sql.ErrNoRows
	}
	finding := review.Findings[ref.number-1]
	base, err := s.workspaceVersion(ctx, projectID, baseVersionID)
	if err != nil {
		return selectionEdit{}, false, err
	}
	if base.Path != source.Path {
		return selectionEdit{}, false, sql.ErrNoRows
	}
	doc, err := s.readWorkspaceDocument(base)
	if err != nil {
		return selectionEdit{}, false, err
	}
	original := doc.Content
	if source.VersionID != base.VersionID {
		saved, err := s.readWorkspaceDocument(source)
		if err != nil {
			return selectionEdit{}, false, err
		}
		original = saved.Content
	}
	start, err := findingOffset(original, doc.Content, finding)
	if err != nil {
		return selectionEdit{}, false, err
	}
	return s.queueSelectionEditWithFinding(ctx, projectID, doc, selectionEditInput{
		RequestID: requestID, StartByte: start, EndByte: start + len(finding.Quote),
		Instruction: "针对评审意见修改选定文字。\n发现：" + finding.Finding + "\n修改建议：" + finding.Suggestion,
	}, ref, modelReady)
}

func findingOffset(original, base string, finding reviewFinding) (int, error) {
	start, lineNumber := 0, 1
	for text := range strings.SplitSeq(original, "\n") {
		if lineNumber == finding.Line {
			line := strings.TrimSuffix(text, "\r")
			quoteOffset := strings.Index(line, finding.Quote)
			if quoteOffset < 0 || quoteOffset != strings.LastIndex(line, finding.Quote) {
				return 0, errFindingAnchor
			}
			if original != base {
				// When the source has moved, the enclosing line must still identify it.
				start = uniqueLineOffset(base, line)
				if start < 0 || uniqueLineOffset(original, line) < 0 {
					return 0, errFindingAnchor
				}
			}
			return start + quoteOffset, nil
		}
		start += len(text) + 1
		lineNumber++
	}
	return 0, errFindingAnchor
}

func uniqueLineOffset(text, line string) int {
	start, match := 0, -1
	for current := range strings.SplitSeq(text, "\n") {
		if strings.TrimSuffix(current, "\r") == line {
			if match >= 0 {
				return -1
			}
			match = start
		}
		start += len(current) + 1
	}
	return match
}

func (s *Store) attachFindingEdits(ctx context.Context, projectID string, review *manuscriptReview) error {
	rows, err := s.db.QueryContext(ctx, selectEdit+` AND e.review_id = ? AND t.ordinal = (
		SELECT t2.ordinal FROM selection_edits e2 JOIN tasks t2 ON t2.project_id = e2.project_id AND t2.task_id = e2.task_id
		WHERE e2.project_id = e.project_id AND e2.review_id = e.review_id AND e2.finding_number = e.finding_number
		ORDER BY e2.applied_version_id IS NOT NULL DESC, t2.ordinal DESC LIMIT 1)`, projectID, review.ReviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		edit, err := scanSelectionEdit(rows)
		if err != nil {
			return err
		}
		review.Findings[edit.finding.number-1].Edit = &edit
	}
	return rows.Err()
}
