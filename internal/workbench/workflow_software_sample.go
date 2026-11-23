package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"research-agent/internal/inference"
)

const softwareSampleBytes = 8 << 10

var errSoftwareSampleOutput = errors.New("模型未返回有效的示例源码。")

func (s *Store) generateSoftwareSample(ctx context.Context, model *inference.Client, project Project, task Task, prompt string, settings softwareSettings) (taskResult, error) {
	_, manual, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "manuscript")
	if err != nil {
		return taskResult{}, err
	}
	excerpt, err := previewText(strings.NewReader(manual), referenceExcerptBytes)
	if err != nil {
		return taskResult{}, err
	}
	input, err := json.Marshal(struct {
		Title       string          `json:"title"`
		Version     string          `json:"version"`
		Description string          `json:"description,omitempty"`
		Manual      MaterialPreview `json:"manual"`
	}{project.Title, settings.Version, settings.Description, excerpt})
	if err != nil {
		return taskResult{}, err
	}
	text, err := streamTaskText(ctx, model, prompt, string(input), 0, 6*softwareSampleBytes+64)
	if err != nil {
		return taskResult{}, err
	}
	var output struct {
		Source string `json:"source"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF ||
		len(output.Source) > softwareSampleBytes || strings.TrimSpace(output.Source) == "" || strings.ContainsRune(output.Source, 0) {
		return taskResult{}, workflowOutputError{errSoftwareSampleOutput}
	}
	source := softwareSourceText{
		name: "example.py", digest: fmt.Sprintf("%x", sha256.Sum256([]byte(output.Source))), body: output.Source,
	}
	return taskResult{markdown: softwareProgramMaterials(softwareSampleHeading, []softwareSourceText{source})}, nil
}
