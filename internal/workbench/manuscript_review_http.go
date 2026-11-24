package workbench

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/inference"
)

type manuscriptReviews struct {
	store       *Store
	model       *inference.Client
	reviewModel modelSnapshot
	editModel   modelSnapshot
}

func (api manuscriptReviews) register(mux *http.ServeMux) {
	base := projectsPath + "/{id}/files/versions/{versionId}/reviews"
	mux.HandleFunc("GET "+base, api.list)
	mux.HandleFunc("POST "+base, api.create)
	mux.HandleFunc("GET "+base+"/{reviewId}", api.read(false))
	mux.HandleFunc("GET "+base+"/{reviewId}/download", api.read(true))
	mux.HandleFunc("POST "+base+"/{reviewId}/retry", api.retry)
	mux.HandleFunc("POST "+base+"/{reviewId}/findings/{number}/edits", api.createFindingEdit)
	api.registerAutomaticReviews(mux)
}

func (api manuscriptReviews) source(w http.ResponseWriter, r *http.Request) (WorkspaceFile, bool) {
	file, err := api.store.workspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
	if err != nil {
		writeWorkspaceError(w, "read manuscript review version", err)
		return WorkspaceFile{}, false
	}
	switch strings.ToLower(path.Ext(file.Path)) {
	case ".md", ".markdown":
		return file, true
	default:
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "稿件评审支持已保存的 Markdown 稿件。")
		return WorkspaceFile{}, false
	}
}

func (api manuscriptReviews) list(w http.ResponseWriter, r *http.Request) {
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
	items, err := api.store.listManuscriptReviews(r.Context(), r.PathValue("id"), file, cursor, limit+1)
	if err != nil {
		writeStoreError(w, "list manuscript reviews", err)
		return
	}
	var next string
	if len(items) > limit {
		next = strconv.FormatInt(items[limit-1].ordinal, 10)
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, struct {
		Items           []manuscriptReview `json:"items"`
		NextCursor      string             `json:"nextCursor,omitempty"`
		ModelConfigured bool               `json:"modelConfigured"`
	}{items, next, api.model != nil})
}

func (api manuscriptReviews) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Target string `json:"target"`
	}
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	target := strings.TrimSpace(input.Target)
	if target == "" || utf8.RuneCountInString(target) > 200 || strings.ContainsFunc(target, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请输入一行投稿目标，最多 200 个字符。")
		return
	}
	file, ok := api.source(w, r)
	if !ok {
		return
	}
	if file.Size > maxReviewSourceBytes {
		writeError(w, http.StatusUnprocessableEntity, "SOURCE_TOO_LARGE", "模拟评审支持 64 KiB 以内的稿件。")
		return
	}
	doc, err := api.store.readWorkspaceDocument(file)
	if err != nil {
		writeStoreError(w, "read manuscript for review", err)
		return
	}
	if strings.TrimSpace(doc.Content) == "" {
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_DOCUMENT", "请先保存稿件正文。")
		return
	}
	projectID := r.PathValue("id")
	review, created, err := api.store.startManuscriptReview(r.Context(), projectID, file, target, api.reviewModel)
	api.respond(w, projectID, review, created, err)
}

func (api manuscriptReviews) read(download bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, ok := api.source(w, r)
		if !ok {
			return
		}
		review, err := getManuscriptReview(r.Context(), api.store.db, r.PathValue("id"), file, r.PathValue("reviewId"))
		if err != nil {
			writeTaskError(w, "read manuscript review", err)
			return
		}
		if download {
			if review.Status != taskSucceeded {
				writeError(w, http.StatusNotFound, "NOT_FOUND", "此评审尚无可下载的报告。")
				return
			}
			writeMarkdown(w, r, "manuscript-review.md", review.Report)
			return
		}
		if len(review.Findings) > 0 {
			if err := api.store.attachFindingEdits(r.Context(), r.PathValue("id"), &review); err != nil {
				writeStoreError(w, "read finding edits", err)
				return
			}
		}
		writeJSON(w, http.StatusOK, review)
	}
}

func (api manuscriptReviews) retry(w http.ResponseWriter, r *http.Request) {
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
	file, ok := api.source(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("id")
	review, saved, err := api.store.retryManuscriptReview(r.Context(), projectID, file, r.PathValue("reviewId"), input.RequestID, input.ExpectedRevision, api.reviewModel)
	api.respond(w, projectID, review, saved, err)
}

func (api manuscriptReviews) respond(w http.ResponseWriter, projectID string, review manuscriptReview, applied bool, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "稿件版本或评审记录不存在。")
	case errors.Is(err, errManuscriptReviewState):
		writeError(w, http.StatusConflict, "REVIEW_STATE_CONFLICT", "请刷新并查看评审状态。")
	case err != nil:
		writeTaskError(w, "queue manuscript review", err)
	default:
		if review.Status == taskQueued && api.model != nil {
			api.store.kickTaskQueue(projectID, api.model)
		}
		status := http.StatusOK
		if applied {
			status = http.StatusAccepted
		}
		writeJSON(w, status, review)
	}
}
