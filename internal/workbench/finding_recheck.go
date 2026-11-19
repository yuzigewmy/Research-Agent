package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"research-agent/internal/inference"
)

//go:embed prompts/finding_recheck.md
var findingRecheckPrompt string

const findingRecheckSchema = `
	ALTER TABLE tasks ADD COLUMN finding_recheck INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE finding_rechecks (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		edit_id TEXT NOT NULL,
		applied_version_id TEXT NOT NULL REFERENCES workspace_versions(version_id),
		PRIMARY KEY (project_id, task_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id),
		FOREIGN KEY (project_id, edit_id) REFERENCES selection_edits(project_id, task_id)
	);
	CREATE INDEX finding_rechecks_by_edit ON finding_rechecks(project_id, edit_id);
	PRAGMA user_version = 17;
`

const findingRecheckAction = "recheck-finding"

var errFindingRecheckOutput = errors.New("model returned an invalid finding recheck")

// FindingRecheck records one task against the immutable version produced by the edit.
type FindingRecheck struct {
	RecheckID        string `json:"recheckId"`
	Status           string `json:"status"`
	AppliedVersionID string `json:"appliedVersionId"`
	SourceSHA256     string `json:"sourceSha256"`
	FindingNumber    int    `json:"findingNumber"`
	Message          string `json:"message,omitempty"`
	findingRecheckResult
}

type findingRecheckResult struct {
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Line    int    `json:"line,omitempty"`
	Quote   string `json:"quote,omitempty"`
}

type findingRechecks struct {
	store *Store
	model *inference.Client
}

func (api findingRechecks) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/files/versions/{versionId}/edits/{editId}/recheck"
	mux.HandleFunc("GET "+path, api.read)
	mux.HandleFunc("POST "+path, api.create)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
}

func (api findingRechecks) read(w http.ResponseWriter, r *http.Request) {
	ctx, projectID := r.Context(), r.PathValue("id")
	edit, err := readFindingRecheckEdit(ctx, api.store.db, projectID, r.PathValue("versionId"), r.PathValue("editId"))
	if err != nil {
		writeFindingRecheckError(w, err)
		return
	}
	recheck, err := latestFindingRecheck(ctx, api.store.db, projectID, edit.EditID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeFindingRecheckError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recheck)
}

func (api findingRechecks) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string `json:"requestId"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if !validRequestID(input.RequestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号。")
		return
	}
	projectID := r.PathValue("id")
	recheck, created, err := api.store.queueFindingRecheck(r.Context(), projectID, r.PathValue("versionId"),
		r.PathValue("editId"), input.RequestID, api.model != nil)
	if err != nil {
		writeFindingRecheckError(w, err)
		return
	}
	if recheck.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, recheck)
}

func writeFindingRecheckError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "未找到关联评审意见的修改建议。")
	case errors.Is(err, errSelectionState):
		writeError(w, http.StatusConflict, "EDIT_STATE_CONFLICT", "请先将修改建议应用到稿件。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已用于其他操作。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先配置模型。")
	default:
		writeStoreError(w, "finding recheck", err)
	}
}

func readFindingRecheckEdit(ctx context.Context, db workspaceQuery, projectID, versionID, editID string) (selectionEdit, error) {
	edit, err := readSelectionEdit(ctx, db, projectID, versionID, editID)
	if err == nil && (edit.finding.reviewID == "" || edit.finding.number < 1) {
		err = sql.ErrNoRows
	}
	return edit, err
}

const selectFindingRecheck = `SELECT t.task_id, t.status, f.applied_version_id, v.sha256, e.finding_number, t.message, t.markdown
	FROM finding_rechecks f
	JOIN tasks t ON t.project_id = f.project_id AND t.task_id = f.task_id
	JOIN selection_edits e ON e.project_id = f.project_id AND e.task_id = f.edit_id
	JOIN workspace_versions v ON v.project_id = f.project_id AND v.version_id = f.applied_version_id
	WHERE f.project_id = ? AND f.edit_id = ?`

func scanFindingRecheck(row *sql.Row) (FindingRecheck, error) {
	var recheck FindingRecheck
	var output string
	if err := row.Scan(&recheck.RecheckID, &recheck.Status, &recheck.AppliedVersionID, &recheck.SourceSHA256,
		&recheck.FindingNumber, &recheck.Message, &output); err != nil {
		return FindingRecheck{}, err
	}
	if recheck.Status == taskSucceeded {
		if err := json.Unmarshal([]byte(output), &recheck.findingRecheckResult); err != nil {
			return FindingRecheck{}, err
		}
	}
	return recheck, nil
}

func latestFindingRecheck(ctx context.Context, db workspaceQuery, projectID, editID string) (FindingRecheck, error) {
	return scanFindingRecheck(db.QueryRowContext(ctx, selectFindingRecheck+` ORDER BY t.ordinal DESC LIMIT 1`, projectID, editID))
}

func (s *Store) queueFindingRecheck(ctx context.Context, projectID, versionID, editID, requestID string, modelReady bool) (FindingRecheck, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FindingRecheck{}, false, err
	}
	defer tx.Rollback()

	edit, err := readFindingRecheckEdit(ctx, tx, projectID, versionID, editID)
	if err != nil {
		return FindingRecheck{}, false, err
	}
	if edit.Status != taskSucceeded || edit.AppliedVersionID == "" {
		return FindingRecheck{}, false, errSelectionState
	}

	var savedID, action string
	err = tx.QueryRowContext(ctx, `SELECT task_id, action FROM task_requests WHERE project_id = ? AND request_id = ?`,
		projectID, requestID).Scan(&savedID, &action)
	if err == nil {
		if action != findingRecheckAction {
			return FindingRecheck{}, false, errRequestConflict
		}
		recheck, err := scanFindingRecheck(tx.QueryRowContext(ctx, selectFindingRecheck+` AND f.task_id = ?`, projectID, editID, savedID))
		if errors.Is(err, sql.ErrNoRows) {
			err = errRequestConflict
		}
		return recheck, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FindingRecheck{}, false, err
	}

	recheck, err := latestFindingRecheck(ctx, tx, projectID, editID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FindingRecheck{}, false, err
	}
	created := recheck.Status != taskQueued && recheck.Status != taskRunning && recheck.Status != taskSucceeded
	if created {
		if !modelReady {
			return FindingRecheck{}, false, errModelUnavailable
		}
		applied, err := lookupWorkspaceVersion(ctx, tx, projectID, edit.AppliedVersionID)
		if err != nil {
			return FindingRecheck{}, false, err
		}
		recheck, err = insertFindingRecheck(ctx, tx, projectID, edit, applied)
		if err != nil {
			return FindingRecheck{}, false, err
		}
	}
	// A coalesced request still has a receipt so its replay targets the same attempt.
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_requests (project_id, task_id, request_id, action, expected_revision)
		VALUES (?, ?, ?, ?, 0)`, projectID, recheck.RecheckID, requestID, findingRecheckAction); err != nil {
		return FindingRecheck{}, false, err
	}
	return recheck, created, tx.Commit()
}

func insertFindingRecheck(ctx context.Context, tx *sql.Tx, projectID string, edit selectionEdit, applied WorkspaceFile) (FindingRecheck, error) {
	recheck := FindingRecheck{
		RecheckID: rand.Text(), Status: taskQueued, AppliedVersionID: applied.VersionID,
		SourceSHA256: applied.SHA256, FindingNumber: edit.finding.number,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
		(project_id, task_id, instruction, status, created_at, queued_seq, finding_recheck)
		VALUES (?, ?, '', 'QUEUED', ?, `+nextTaskQueuePosition+`, 1)`,
		projectID, recheck.RecheckID, time.Now().UTC().Format(createdAtLayout), projectID); err != nil {
		return FindingRecheck{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO finding_rechecks (project_id, task_id, edit_id, applied_version_id)
		VALUES (?, ?, ?, ?)`, projectID, recheck.RecheckID, edit.EditID, applied.VersionID); err != nil {
		return FindingRecheck{}, err
	}
	return recheck, nil
}

func (s *Store) generateFindingRecheck(ctx context.Context, model *inference.Client, projectID string, task Task) (string, error) {
	var editID, baseID, appliedID, reviewVersionID string
	if err := s.db.QueryRowContext(ctx, `SELECT f.edit_id, e.source_version_id, f.applied_version_id, rv.review_version_id
		FROM finding_rechecks f
		JOIN selection_edits e ON e.project_id = f.project_id AND e.task_id = f.edit_id AND e.applied_version_id = f.applied_version_id
		JOIN tasks rv ON rv.project_id = f.project_id AND rv.task_id = e.review_id
		WHERE f.project_id = ? AND f.task_id = ?`, projectID, task.TaskID).Scan(&editID, &baseID, &appliedID, &reviewVersionID); err != nil {
		return "", err
	}
	edit, err := readFindingRecheckEdit(ctx, s.db, projectID, baseID, editID)
	if err != nil {
		return "", err
	}
	if edit.Status != taskSucceeded || edit.AppliedVersionID != appliedID {
		return "", errSelectionState
	}
	applied, err := s.workspaceVersion(ctx, projectID, appliedID)
	if err != nil {
		return "", err
	}
	original, err := s.workspaceVersion(ctx, projectID, reviewVersionID)
	if err != nil {
		return "", err
	}
	if original.Path != applied.Path {
		return "", sql.ErrNoRows
	}
	review, err := getManuscriptReview(ctx, s.db, projectID, original, edit.finding.reviewID)
	if err != nil {
		return "", err
	}
	if review.Status != taskSucceeded || edit.finding.number > len(review.Findings) {
		return "", sql.ErrNoRows
	}
	finding := review.Findings[edit.finding.number-1]
	if finding.Number != edit.finding.number || finding.Quote != edit.Original {
		return "", sql.ErrNoRows
	}
	finding.Edit = nil

	doc, err := s.readWorkspaceDocument(applied)
	if err != nil {
		return "", err
	}
	start, end := edit.startByte, edit.startByte+len(edit.Replacement)
	if start < 0 || end < start || end > len(doc.Content) || doc.Content[start:end] != edit.Replacement ||
		(start < len(doc.Content) && !utf8.RuneStart(doc.Content[start])) || (end < len(doc.Content) && !utf8.RuneStart(doc.Content[end])) {
		return "", errSelectionRange
	}
	excerpts, limited := findingRecheckExcerpts(doc.Content, start, end)
	input, err := json.Marshal(struct {
		OriginalFinding reviewFinding          `json:"originalFinding"`
		AppliedExcerpts [][]findingRecheckLine `json:"appliedExcerpts"`
		ExcerptLimited  bool                   `json:"excerptLimited"`
	}{finding, excerpts, limited})
	if err != nil {
		return "", err
	}
	output, err := streamTaskMarkdown(ctx, model, findingRecheckPrompt, string(input))
	if err != nil {
		return "", err
	}
	return validateFindingRecheck(output, excerpts, limited)
}

type findingRecheckLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func findingRecheckExcerpts(content string, start, end int) ([][]findingRecheckLine, bool) {
	left, right := max(0, start-selectionContextBytes), min(len(content), end+selectionContextBytes)
	if end-start <= 2*selectionContextBytes {
		return [][]findingRecheckLine{numberedRecheckLines(content, left, right)}, left > 0 || right < len(content)
	}
	return [][]findingRecheckLine{
		numberedRecheckLines(content, left, start+selectionContextBytes),
		numberedRecheckLines(content, end-selectionContextBytes, right),
	}, true
}

func numberedRecheckLines(content string, start, end int) []findingRecheckLine {
	for start < end && !utf8.RuneStart(content[start]) {
		start++
	}
	for end > start && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	line := 1 + strings.Count(content[:start], "\n")
	lines := make([]findingRecheckLine, 0)
	for text := range strings.SplitSeq(content[start:end], "\n") {
		lines = append(lines, findingRecheckLine{Line: line, Text: strings.TrimSuffix(text, "\r")})
		line++
	}
	return lines
}

func validateFindingRecheck(output string, excerpts [][]findingRecheckLine, limited bool) (string, error) {
	var decoded struct {
		Verdict string          `json:"verdict"`
		Reason  string          `json:"reason"`
		Line    json.RawMessage `json:"line"`
		Quote   json.RawMessage `json:"quote"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return "", errFindingRecheckOutput
	}
	result := findingRecheckResult{Verdict: decoded.Verdict, Reason: strings.TrimSpace(decoded.Reason)}
	if result.Reason == "" || strings.ContainsRune(result.Reason, 0) {
		return "", errFindingRecheckOutput
	}
	switch result.Verdict {
	case "RESOLVED", "OPEN", "UNVERIFIABLE":
	default:
		return "", errFindingRecheckOutput
	}
	if json.Unmarshal(decoded.Line, &result.Line) != nil || json.Unmarshal(decoded.Quote, &result.Quote) != nil ||
		!hasFindingRecheckEvidence(result, excerpts) {
		result.Line, result.Quote = 0, ""
		if result.Verdict == "RESOLVED" {
			result.Verdict = "UNVERIFIABLE"
			result.Reason = "缺少可在应用稿件中逐字核对的行号和摘录，无法确认此意见已解决。"
		}
	}
	if limited {
		result.Reason += "本次复查仅覆盖改动附近的稿件节选。"
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func hasFindingRecheckEvidence(result findingRecheckResult, excerpts [][]findingRecheckLine) bool {
	if result.Line < 1 || strings.TrimSpace(result.Quote) == "" || utf8.RuneCountInString(result.Quote) > 200 {
		return false
	}
	for _, excerpt := range excerpts {
		for _, line := range excerpt {
			if line.Line == result.Line && strings.Contains(line.Text, result.Quote) {
				return true
			}
		}
	}
	return false
}
