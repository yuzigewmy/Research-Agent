package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/inference"
)

const (
	imageReviewRule        = 1
	imageReviewTimeout     = 60 * time.Second
	maxImageReviewResponse = 12 << 10
)

var (
	errImageReviewConnection   = errors.New("视觉模型服务地址已变更。请恢复原地址，或重新提交图片复查。")
	errImageReviewOutput       = errors.New("模型回复未形成有效的图片复查记录。请重试。")
	errImageReviewUnverifiable = errors.New("模型未能从图片中取得足够的观察依据。请核对原图清晰度后重试。")
)

//go:embed prompts/image_review_v1.md
var imageReviewPrompt string

func (s *Store) runImageReview(ctx context.Context, model *inference.Client, projectID string, task Task) (taskResult, error) {
	if model == nil || task.model.Model == "" {
		return taskResult{}, errModelUnavailable
	}
	if model.ConnectionFingerprint() != task.model.connection {
		return taskResult{}, errImageReviewConnection
	}
	model = model.ForModel(task.model.Model, task.model.ReasoningEffort)
	var materialID, digest string
	var rule int
	if err := s.db.QueryRowContext(ctx, `SELECT material_id, source_sha256, rule_revision FROM image_reviews
		WHERE project_id = ? AND task_id = ?`, projectID, task.TaskID).Scan(&materialID, &digest, &rule); err != nil {
		return taskResult{}, err
	}
	if rule != imageReviewRule {
		return taskResult{}, errors.New("unknown image review rule revision")
	}
	material, err := s.getMaterial(ctx, projectID, materialID)
	if err != nil {
		return taskResult{}, err
	}
	if material.SHA256 != digest {
		return taskResult{}, errMaterialImage
	}
	body, format, err := s.readMaterialImage(ctx, material)
	if err != nil {
		return taskResult{}, err
	}
	var output strings.Builder
	if err := model.StreamImage(ctx, imageReviewPrompt, format, body, 2048, func(text string) error {
		if len(text) > maxImageReviewResponse-output.Len() {
			return errImageReviewOutput
		}
		output.WriteString(text)
		return nil
	}); err != nil {
		return taskResult{}, err
	}
	result, err := parseImageReview(output.String())
	if err != nil {
		return taskResult{}, err
	}
	encoded, err := json.Marshal(result)
	return taskResult{jsonArtifact: string(encoded)}, err
}

func parseImageReview(text string) (imageReviewResult, error) {
	var result imageReviewResult
	if !utf8.ValidString(text) {
		return result, errImageReviewOutput
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return imageReviewResult{}, errImageReviewOutput
	}
	if result.Verdict == "UNVERIFIABLE" {
		return imageReviewResult{}, errImageReviewUnverifiable
	}
	for _, list := range [][]string{result.ObservedLabels, result.Relations, result.Issues} {
		if list == nil || len(list) > 16 {
			return imageReviewResult{}, errImageReviewOutput
		}
		for i, item := range list {
			list[i] = strings.TrimSpace(item)
			if list[i] == "" || len(list[i]) > 256 || strings.ContainsFunc(list[i], unicode.IsControl) {
				return imageReviewResult{}, errImageReviewOutput
			}
		}
	}
	switch result.Verdict {
	case "PASS":
		if len(result.Issues) == 0 && len(result.ObservedLabels)+len(result.Relations) > 0 {
			return result, nil
		}
	case "ISSUES":
		if len(result.Issues) > 0 {
			return result, nil
		}
	}
	return imageReviewResult{}, errImageReviewOutput
}
