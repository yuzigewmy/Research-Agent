package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"research-agent/internal/inference"
)

const imageReviewTask = "image-review"

const imageReviewSchema = `
	ALTER TABLE tasks RENAME COLUMN kind TO previous_kind;
	ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT ''
		CHECK (kind IN ('', 'manuscript-check', 'focused-research', 'experiment-comparison', 'image-review'));
	UPDATE tasks SET kind = previous_kind WHERE previous_kind <> '';
	ALTER TABLE tasks DROP COLUMN previous_kind;
	CREATE TABLE image_reviews (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		material_id TEXT NOT NULL REFERENCES materials(material_id),
		source_sha256 TEXT NOT NULL,
		rule_revision INTEGER NOT NULL,
		PRIMARY KEY (project_id, task_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	CREATE INDEX image_reviews_by_material ON image_reviews(project_id, material_id);
	PRAGMA user_version = 52;
`

type imageReviewResponse struct {
	Configured bool         `json:"configured"`
	Review     *imageReview `json:"review"`
}

type imageReview struct {
	ReviewID        string             `json:"reviewId"`
	MaterialID      string             `json:"materialId"`
	SourceSHA256    string             `json:"sourceSha256"`
	Status          string             `json:"status"`
	Model           string             `json:"model"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
	CreatedAt       string             `json:"createdAt"`
	RuleRevision    int                `json:"ruleRevision"`
	Message         string             `json:"message,omitempty"`
	Result          *imageReviewResult `json:"result,omitempty"`
}

type imageReviewResult struct {
	Verdict        string   `json:"verdict"`
	ObservedLabels []string `json:"observedLabels"`
	Relations      []string `json:"relations"`
	Issues         []string `json:"issues"`
}

type imageReviews struct {
	store *Store
	model *inference.Client
	role  modelSnapshot
}

func (api imageReviews) register(mux *http.ServeMux) {
	route := projectsPath + "/{id}/materials/{materialId}/image-review"
	mux.HandleFunc("GET "+route, api.read)
	mux.HandleFunc("POST "+route, api.start)
	mux.HandleFunc(route, methodNotAllowed("GET, HEAD, POST"))
}

func (api imageReviews) read(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, materialID := r.Context(), r.PathValue("id"), r.PathValue("materialId")
	reviewID := r.URL.Query().Get("reviewId")
	if reviewID != "" && !validRequestID(reviewID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的图片复查编号。")
		return
	}
	if _, err := api.store.getMaterial(ctx, projectID, materialID); err != nil {
		writeImageReviewError(w, err)
		return
	}
	review, err := lookupImageReview(ctx, api.store.db, projectID, materialID, reviewID)
	if err != nil && !(reviewID == "" && errors.Is(err, sql.ErrNoRows)) {
		writeImageReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imageReviewResponse{Configured: api.role.Model != "", Review: review})
}

func (api imageReviews) start(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string `json:"requestId"`
	}
	if !readJSON(w, r, &input, 1024) {
		return
	}
	if !validRequestID(input.RequestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号。")
		return
	}
	projectID, materialID := r.PathValue("id"), r.PathValue("materialId")
	review, created, err := api.store.queueImageReview(r.Context(), projectID, materialID, input.RequestID, api.role)
	if err != nil {
		writeImageReviewError(w, err)
		return
	}
	if review.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, imageReviewResponse{Configured: api.role.Model != "", Review: review})
}

func writeImageReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有找到该项目的资料或复查记录。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "VISION_UNAVAILABLE", "请先在服务端配置视觉模型。")
	case errors.Is(err, errMaterialImage):
		writeError(w, http.StatusUnprocessableEntity, "IMAGE_REVIEW_INPUT", errMaterialImage.Error())
	default:
		writeTaskError(w, "image review", err)
	}
}

const selectImageReview = `SELECT t.task_id, i.material_id, i.source_sha256, t.status,
	t.model_name, t.reasoning_effort, t.created_at, i.rule_revision, t.message, t.result_json
	FROM image_reviews i JOIN tasks t USING (project_id, task_id)
	WHERE i.project_id = ?`

func lookupImageReview(ctx context.Context, db workspaceQuery, projectID, materialID, reviewID string) (*imageReview, error) {
	const query = selectImageReview + ` AND i.material_id = ?`
	if reviewID == "" {
		return scanImageReview(db.QueryRowContext(ctx, query+` ORDER BY t.ordinal DESC LIMIT 1`, projectID, materialID))
	}
	return scanImageReview(db.QueryRowContext(ctx, query+` AND i.task_id = ?`, projectID, materialID, reviewID))
}

func scanImageReview(row *sql.Row) (*imageReview, error) {
	var review imageReview
	var storedResult string
	if err := row.Scan(&review.ReviewID, &review.MaterialID, &review.SourceSHA256, &review.Status,
		&review.Model, &review.ReasoningEffort, &review.CreatedAt, &review.RuleRevision, &review.Message, &storedResult); err != nil {
		return nil, err
	}
	if review.Status == taskSucceeded {
		if err := json.Unmarshal([]byte(storedResult), &review.Result); err != nil {
			return nil, err
		}
	}
	return &review, nil
}

func (s *Store) queueImageReview(ctx context.Context, projectID, materialID, requestID string, model modelSnapshot) (*imageReview, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	material, err := lookupMaterial(ctx, tx, projectID, materialID)
	if err != nil {
		return nil, false, err
	}
	var taskID, action string
	err = tx.QueryRowContext(ctx, `SELECT task_id, action FROM task_requests WHERE project_id = ? AND request_id = ?`, projectID, requestID).
		Scan(&taskID, &action)
	if err == nil {
		if action != imageReviewTask {
			return nil, false, errRequestConflict
		}
		review, err := lookupImageReview(ctx, tx, projectID, materialID, taskID)
		if errors.Is(err, sql.ErrNoRows) {
			err = errRequestConflict
		}
		return review, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	review, err := lookupImageReview(ctx, tx, projectID, materialID, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	created := review == nil || (review.Status != taskQueued && review.Status != taskRunning && review.Status != taskCancelling && review.Status != taskSucceeded)
	if created {
		if model.Model == "" {
			return nil, false, errModelUnavailable
		}
		if materialImageType(material.Name) == "" || material.Size <= 0 || material.Size > maxMaterialImageBytes {
			return nil, false, errMaterialImage
		}
		review = &imageReview{ReviewID: rand.Text(), MaterialID: materialID, SourceSHA256: material.SHA256,
			Status: taskQueued, Model: model.Model, ReasoningEffort: model.ReasoningEffort,
			CreatedAt: time.Now().UTC().Format(createdAtLayout), RuleRevision: imageReviewRule}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
			(project_id, task_id, instruction, status, created_at, queued_seq, kind, model_name, reasoning_effort, model_connection)
			VALUES (?, ?, ?, 'QUEUED', ?, `+nextTaskQueuePosition+`, ?, ?, ?, ?)`,
			projectID, review.ReviewID, "图片复查 · "+material.Name, review.CreatedAt, projectID, imageReviewTask,
			model.Model, model.ReasoningEffort, model.connection); err != nil {
			return nil, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_reviews (project_id, task_id, material_id, source_sha256, rule_revision)
			VALUES (?, ?, ?, ?, ?)`, projectID, review.ReviewID, materialID, material.SHA256, imageReviewRule); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_requests (project_id, task_id, request_id, action, expected_revision)
		VALUES (?, ?, ?, ?, 0)`, projectID, review.ReviewID, requestID, imageReviewTask); err != nil {
		return nil, false, err
	}
	return review, created, tx.Commit()
}
