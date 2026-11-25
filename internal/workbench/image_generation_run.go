package workbench

import (
	"bytes"
	"context"
	"image/png"
	"strings"

	"research-agent/internal/inference"
)

func (s *Store) runImageGeneration(ctx context.Context, model *inference.Client, projectID string, task Task) (taskResult, error) {
	if task.model.connection != model.ConnectionFingerprint() {
		return taskResult{}, errImageGenerationConnection
	}
	generation, err := lookupImageGeneration(ctx, s.db, projectID, task.TaskID)
	if err != nil {
		return taskResult{}, err
	}
	model = model.ForModel(task.model.Model, "")
	body, err := model.GeneratePNG(ctx, generation.Prompt, generation.Size, generation.Quality)
	if err != nil {
		return taskResult{}, err
	}
	if err := validateGeneratedPNG(ctx, generation.Size, body); err != nil {
		return taskResult{}, err
	}

	name := "科研配图-" + strings.ToLower(task.TaskID) + ".png"
	material, staged, err := s.stageMaterial(name, contextReader{ctx, bytes.NewReader(body)})
	if err != nil {
		return taskResult{}, err
	}
	defer discardMaterialStage(staged)
	if err := s.publishObject(staged, material.SHA256); err != nil {
		return taskResult{}, err
	}
	return taskResult{imageMaterial: material}, nil
}

func validateGeneratedPNG(ctx context.Context, size string, body []byte) error {
	width, height := imageGenerationDimensions(size)
	config, err := png.DecodeConfig(contextReader{ctx, bytes.NewReader(body)})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || config.Width != width || config.Height != height {
		return errImageGenerationOutput
	}
	if err := validateMaterialImage(ctx, "png", body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errImageGenerationOutput
	}
	return nil
}
