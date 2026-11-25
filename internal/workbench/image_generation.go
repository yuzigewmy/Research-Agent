package workbench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"research-agent/internal/inference"
)

const imageGenerationTask = "image-generation"
const imageGenerationModelID = "image"
const imageGenerationTimeout = 2 * time.Minute

const imageGenerationSchema = `
	ALTER TABLE tasks RENAME COLUMN kind TO previous_kind;
	ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT ''
		CHECK (kind IN ('', 'manuscript-check', 'focused-research', 'experiment-comparison', 'image-review', 'image-generation'));
	UPDATE tasks SET kind = previous_kind WHERE previous_kind <> '';
	ALTER TABLE tasks DROP COLUMN previous_kind;
	CREATE TABLE image_generations (
		project_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		prompt TEXT NOT NULL,
		size TEXT NOT NULL,
		quality TEXT NOT NULL,
		material_id TEXT REFERENCES materials(material_id),
		PRIMARY KEY (project_id, task_id),
		FOREIGN KEY (project_id, task_id) REFERENCES tasks(project_id, task_id)
	);
	PRAGMA user_version = 53;
`

var (
	errImageGenerationBusy       = errors.New("此项目已有排队或运行中的生图任务。请查看该任务的进度。")
	errImageGenerationConnection = errors.New("生图任务记录的服务地址已变更。请核对当前连接后再提交。")
	errImageGenerationOutput     = errors.New("生图服务返回的 PNG 未通过文件或尺寸校验。可以重新生成。")
)

type imageGeneration struct {
	GenerationID string    `json:"generationId"`
	Status       string    `json:"status"`
	Prompt       string    `json:"prompt"`
	Size         string    `json:"size"`
	Quality      string    `json:"quality"`
	Model        string    `json:"model"`
	CreatedAt    string    `json:"createdAt"`
	Message      string    `json:"message,omitempty"`
	Material     *Material `json:"material,omitempty"`
}

type imageGenerationInput struct {
	RequestID string `json:"requestId"`
	Prompt    string `json:"prompt"`
	Size      string `json:"size"`
}

type imageGenerationResponse struct {
	Configured bool             `json:"configured"`
	Model      string           `json:"model,omitempty"`
	Quality    string           `json:"quality,omitempty"`
	Generation *imageGeneration `json:"generation"`
}

type imageGenerationRole struct {
	modelSnapshot
	quality string
}

type imageGenerations struct {
	store *Store
	model *inference.Client
	role  imageGenerationRole
}

func imageGenerationDimensions(size string) (int, int) {
	switch size {
	case "1024x1024":
		return 1024, 1024
	case "1536x1024":
		return 1536, 1024
	case "1024x1536":
		return 1024, 1536
	default:
		return 0, 0
	}
}

func (api imageGenerations) register(mux *http.ServeMux) {
	route := projectsPath + "/{id}/image-generations"
	mux.HandleFunc("GET "+route, api.read)
	mux.HandleFunc("POST "+route, api.start)
	mux.HandleFunc(route, methodNotAllowed("GET, HEAD, POST"))
}

func (api imageGenerations) read(w http.ResponseWriter, r *http.Request) {
	ctx, projectID := r.Context(), r.PathValue("id")
	generationID := r.URL.Query().Get("generationId")
	if generationID != "" && !validRequestID(generationID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的生图任务编号。")
		return
	}
	if _, err := api.store.getProject(ctx, projectID); err != nil {
		writeImageGenerationError(w, err)
		return
	}
	generation, err := lookupImageGeneration(ctx, api.store.db, projectID, generationID)
	if err != nil && !(generationID == "" && errors.Is(err, sql.ErrNoRows)) {
		writeImageGenerationError(w, err)
		return
	}
	api.respond(w, http.StatusOK, generation)
}

func (api imageGenerations) start(w http.ResponseWriter, r *http.Request) {
	var input imageGenerationInput
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	width, _ := imageGenerationDimensions(input.Size)
	if !validRequestID(input.RequestID) || !validTaskInstruction(input.Prompt) || width == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号、8 KiB 以内的生图说明和支持的图片尺寸。")
		return
	}
	projectID := r.PathValue("id")
	generation, created, err := api.store.queueImageGeneration(r.Context(), projectID, input, api.role)
	if err != nil {
		writeImageGenerationError(w, err)
		return
	}
	if generation.Status == taskQueued && api.model != nil {
		api.store.kickTaskQueue(projectID, api.model)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	api.respond(w, status, generation)
}

func (api imageGenerations) respond(w http.ResponseWriter, status int, generation *imageGeneration) {
	writeJSON(w, status, imageGenerationResponse{
		Configured: api.role.Model != "", Model: api.role.Model, Quality: api.role.quality, Generation: generation,
	})
}

func writeImageGenerationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有找到该项目或生图记录。")
	case errors.Is(err, errImageGenerationBusy):
		writeError(w, http.StatusConflict, "IMAGE_GENERATION_BUSY", errImageGenerationBusy.Error())
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "IMAGE_GENERATION_UNAVAILABLE", "请先在服务端配置生图模型。")
	default:
		writeTaskError(w, "image generation", err)
	}
}

func lookupImageGeneration(ctx context.Context, db workspaceQuery, projectID, generationID string) (*imageGeneration, error) {
	query := `SELECT t.task_id, t.status, g.prompt, g.size, g.quality, t.model_name, t.created_at, t.message, COALESCE(g.material_id, '')
		FROM image_generations g JOIN tasks t USING (project_id, task_id) WHERE g.project_id = ?`
	args := []any{projectID}
	if generationID != "" {
		query += ` AND g.task_id = ?`
		args = append(args, generationID)
	} else {
		query += ` ORDER BY t.ordinal DESC LIMIT 1`
	}
	var generation imageGeneration
	var materialID string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&generation.GenerationID, &generation.Status,
		&generation.Prompt, &generation.Size, &generation.Quality, &generation.Model, &generation.CreatedAt,
		&generation.Message, &materialID); err != nil {
		return nil, err
	}
	if generation.Status == taskSucceeded {
		material, err := lookupMaterial(ctx, db, projectID, materialID)
		if err != nil {
			return nil, err
		}
		generation.Material = &material
	}
	return &generation, nil
}

func (s *Store) queueImageGeneration(ctx context.Context, projectID string, input imageGenerationInput, role imageGenerationRole) (*imageGeneration, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var generationID, action string
	err = tx.QueryRowContext(ctx, `SELECT task_id, action FROM task_requests WHERE project_id = ? AND request_id = ?`, projectID, input.RequestID).
		Scan(&generationID, &action)
	if err == nil {
		if action != imageGenerationTask {
			return nil, false, errRequestConflict
		}
		generation, err := lookupImageGeneration(ctx, tx, projectID, generationID)
		if err != nil {
			return nil, false, err
		}
		if generation.Prompt != input.Prompt || generation.Size != input.Size {
			return nil, false, errRequestConflict
		}
		return generation, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks
		WHERE project_id = p.project_id AND kind = ? AND status IN ('QUEUED', 'RUNNING', 'CANCELLING'))
		FROM projects p WHERE p.project_id = ?`, imageGenerationTask, projectID).Scan(&busy); err != nil {
		return nil, false, err
	}
	if busy {
		return nil, false, errImageGenerationBusy
	}
	if role.Model == "" {
		return nil, false, errModelUnavailable
	}
	generation := &imageGeneration{GenerationID: rand.Text(), Status: taskQueued, Prompt: input.Prompt,
		Size: input.Size, Quality: role.quality, Model: role.Model, CreatedAt: time.Now().UTC().Format(createdAtLayout)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
		(project_id, task_id, instruction, status, created_at, queued_seq, kind, model_name, model_connection)
		VALUES (?, ?, '生成科研图片', 'QUEUED', ?, `+nextTaskQueuePosition+`, ?, ?, ?)`,
		projectID, generation.GenerationID, generation.CreatedAt, projectID, imageGenerationTask, role.Model, role.connection); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO image_generations (project_id, task_id, prompt, size, quality)
		VALUES (?, ?, ?, ?, ?)`, projectID, generation.GenerationID, generation.Prompt, generation.Size, generation.Quality); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_requests (project_id, task_id, request_id, action, expected_revision)
		VALUES (?, ?, ?, ?, 0)`, projectID, generation.GenerationID, input.RequestID, imageGenerationTask); err != nil {
		return nil, false, err
	}
	return generation, true, tx.Commit()
}
