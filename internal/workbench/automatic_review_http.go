package workbench

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (api manuscriptReviews) registerAutomaticReviews(mux *http.ServeMux) {
	base := projectsPath + "/{id}/files/versions/{versionId}/automatic-reviews"
	item := base + "/{reviewId}"
	mux.HandleFunc("GET "+base, api.listAutomaticReviews)
	mux.HandleFunc("POST "+base, api.createAutomaticReview)
	mux.HandleFunc("GET "+item, api.readAutomaticReview)
	mux.HandleFunc("POST "+item+"/retry", api.retryAutomaticReview)
	mux.HandleFunc("GET "+item+"/download", api.downloadAutomaticReview)
	mux.HandleFunc("GET "+item+"/rounds/{number}/manuscript", api.downloadAutomaticReviewRound)
}

func (api manuscriptReviews) listAutomaticReviews(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "评审列表游标无效。")
		return
	}
	file, ok := api.source(w, r)
	if !ok {
		return
	}
	const limit = 30
	items, err := api.store.listAutomaticReviews(r.Context(), r.PathValue("id"), file.VersionID, cursor, limit+1)
	if err != nil {
		writeStoreError(w, "list automatic reviews", err)
		return
	}
	var next string
	if len(items) > limit {
		next = strconv.FormatInt(items[limit-1].ordinal, 10)
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, struct {
		Items           []automaticReview `json:"items"`
		NextCursor      string            `json:"nextCursor,omitempty"`
		ModelConfigured bool              `json:"modelConfigured"`
	}{items, next, api.model != nil})
}

func (api manuscriptReviews) createAutomaticReview(w http.ResponseWriter, r *http.Request) {
	input := struct {
		RequestID string `json:"requestId"`
		automaticReviewOptions
	}{automaticReviewOptions: automaticReviewOptions{MaxRounds: defaultReviewRounds, TargetScore: defaultReviewScore}}
	if !readJSON(w, r, &input, 32<<10) {
		return
	}
	input.Target = strings.TrimSpace(input.Target)
	input.Criteria = strings.TrimSpace(input.Criteria)
	if !validRequestID(input.RequestID) || input.Target == "" || utf8.RuneCountInString(input.Target) > 200 ||
		strings.ContainsFunc(input.Target, unicode.IsControl) || input.MaxRounds < 1 || input.MaxRounds > maxAutomaticReviewRounds ||
		input.TargetScore < 5 || input.TargetScore > 9 || len(input.Criteria) > maxReviewCriteria || strings.ContainsRune(input.Criteria, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请核对投稿目标、1 至 10 轮、5 至 9 分的目标分数和 4 KiB 以内的补充标准。")
		return
	}
	file, ok := api.source(w, r)
	if !ok {
		return
	}
	if file.Size > maxReviewSourceBytes {
		writeError(w, http.StatusUnprocessableEntity, "SOURCE_TOO_LARGE", "多轮审稿支持 64 KiB 以内的稿件。")
		return
	}
	doc, err := api.store.readWorkspaceDocument(file)
	if err != nil {
		writeStoreError(w, "read automatic review source", err)
		return
	}
	if strings.TrimSpace(doc.Content) == "" {
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_DOCUMENT", "请先保存稿件正文。")
		return
	}
	projectID := r.PathValue("id")
	review, created, err := api.store.startAutomaticReview(r.Context(), projectID, file.VersionID, input.RequestID, input.automaticReviewOptions, api.reviewModel)
	api.respondAutomaticReview(w, projectID, review, created, err)
}

func (api manuscriptReviews) retryAutomaticReview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID        string `json:"requestId"`
		ExpectedRevision int    `json:"expectedRevision"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if !validRequestID(input.RequestID) || input.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号和评审修订。")
		return
	}
	projectID := r.PathValue("id")
	review, applied, err := api.store.retryAutomaticReview(r.Context(), projectID, r.PathValue("versionId"), r.PathValue("reviewId"),
		input.RequestID, input.ExpectedRevision, api.reviewModel)
	api.respondAutomaticReview(w, projectID, review, applied, err)
}

func (api manuscriptReviews) respondAutomaticReview(w http.ResponseWriter, projectID string, review automaticReview, applied bool, err error) {
	if errors.Is(err, errManuscriptReviewState) {
		writeError(w, http.StatusConflict, "REVIEW_STATE_CONFLICT", "请刷新并查看多轮审稿状态。")
		return
	}
	if err != nil {
		writeTaskError(w, "queue automatic review", err)
		return
	}
	if review.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if applied {
		status = http.StatusAccepted
	}
	writeJSON(w, status, review)
}

func (api manuscriptReviews) loadAutomaticReview(w http.ResponseWriter, r *http.Request) (automaticReview, bool) {
	review, err := readAutomaticReview(r.Context(), api.store.db, r.PathValue("id"), r.PathValue("reviewId"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && review.sourceVersion != r.PathValue("versionId")) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该版本下没有此多轮审稿记录。")
		return automaticReview{}, false
	}
	if err != nil {
		writeStoreError(w, "read automatic review", err)
		return automaticReview{}, false
	}
	return review, true
}

func (api manuscriptReviews) readAutomaticReview(w http.ResponseWriter, r *http.Request) {
	if review, ok := api.loadAutomaticReview(w, r); ok {
		writeJSON(w, http.StatusOK, review)
	}
}

func (api manuscriptReviews) downloadAutomaticReview(w http.ResponseWriter, r *http.Request) {
	review, ok := api.loadAutomaticReview(w, r)
	if !ok {
		return
	}
	if review.Status != taskSucceeded {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "多轮审稿尚无完整报告。")
		return
	}
	writeMarkdown(w, r, "automatic-review.md", review.Report)
}

func (api manuscriptReviews) downloadAutomaticReviewRound(w http.ResponseWriter, r *http.Request) {
	review, ok := api.loadAutomaticReview(w, r)
	if !ok {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number < 1 || number > len(review.Rounds) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该审稿轮次尚无已审稿件。")
		return
	}
	file, err := api.store.workspaceVersion(r.Context(), r.PathValue("id"), review.Rounds[number-1].VersionID)
	if err != nil {
		writeWorkspaceError(w, "read reviewed manuscript version", err)
		return
	}
	doc, err := api.store.readWorkspaceDocument(file)
	if err != nil {
		writeStoreError(w, "read reviewed manuscript", err)
		return
	}
	writeMarkdown(w, r, fmt.Sprintf("review-round-%02d.md", number), doc.Content)
}
