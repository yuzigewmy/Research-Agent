package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"research-agent/internal/inference"
)

const patentDisclosureWorkflow = "patent-disclosure-v1"

//go:embed prompts/patent_outline.md
var patentOutlinePrompt string

//go:embed prompts/patent_manuscript.md
var patentManuscriptPrompt string

var errPatentOptions = errors.New("invalid patent disclosure options")

var patentSectionTitles = [...]string{
	"技术领域", "现有技术与问题", "技术方案", "实施方式", "附图说明", "建议保护点", "可选实施方案", "技术效果与待核实项",
}

type patentOptions struct {
	Description string   `json:"description,omitempty"`
	MaterialIDs []string `json:"materialIds,omitempty"`
}

type patentSettings struct {
	patentOptions
	Materials []TurnReference `json:"materials,omitempty"`
}

func freezePatentSettings(ctx context.Context, tx *sql.Tx, projectID string, input *patentOptions) (string, error) {
	var options patentOptions
	if input != nil {
		options = *input
	}
	options.Description = strings.TrimSpace(options.Description)
	if len(options.Description) > 4<<10 || strings.ContainsRune(options.Description, 0) || !validTurnReferences(options.MaterialIDs, nil) {
		return "", errPatentOptions
	}
	materials, err := resolveTurnReferences(ctx, tx, projectID, options.MaterialIDs, nil)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(patentSettings{patentOptions: options, Materials: materials})
	return string(body), err
}

func patentSourceList(title string, settings patentSettings) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 技术材料清单\n\n技术方案名称 %s\n\n", strings.Join(strings.Fields(title), " "))
	if settings.Description != "" {
		fmt.Fprintf(&out, "## 本轮技术说明\n\n%s\n\n", settings.Description)
	}
	out.WriteString("## 固定资料版本\n\n")
	if len(settings.Materials) == 0 {
		out.WriteString("本轮未选择项目资料。请补充可核对的实施记录或文档。\n")
	}
	for _, material := range settings.Materials {
		fmt.Fprintf(&out, "- %s · 版本 `%s` · SHA-256 `%s`\n", material.Name, material.MaterialID, material.SHA256)
	}
	return out.String()
}

func patentManuscriptSections(markdown string) ([]int, error) {
	lines := make([]int, 0, len(patentSectionTitles))
	var fence string
	hasBody := true
	for index, raw := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker && strings.Trim(line, marker[:1]) == "" {
				fence = ""
			}
			continue
		}
		if line == "" || (fence != "" && len(lines) == 0) {
			continue
		}
		if title, heading := strings.CutPrefix(line, "## "); heading && fence == "" {
			if !hasBody {
				return nil, fmt.Errorf("章节「%s」缺少正文。", patentSectionTitles[len(lines)-1])
			}
			if len(lines) == len(patentSectionTitles) {
				return nil, fmt.Errorf("交底稿包含额外的二级标题「%s」。", title)
			}
			if title != patentSectionTitles[len(lines)] {
				return nil, fmt.Errorf("第 %d 节应为「%s」。", len(lines)+1, patentSectionTitles[len(lines)])
			}
			lines = append(lines, index+1)
			hasBody = false
		} else if !paperHeading.MatchString(line) || fence != "" {
			hasBody = true
		}
	}
	if !hasBody {
		return nil, fmt.Errorf("章节「%s」缺少正文。", patentSectionTitles[len(lines)-1])
	}
	if len(lines) < len(patentSectionTitles) {
		return nil, fmt.Errorf("交底稿缺少章节「%s」。", patentSectionTitles[len(lines)])
	}
	return lines, nil
}

func (s *Store) generatePatentStep(ctx context.Context, model *inference.Client, project Project, task Task, input, options string) (taskResult, error) {
	var settings patentSettings
	if err := json.Unmarshal([]byte(options), &settings); err != nil {
		return taskResult{}, err
	}
	stage := workflowStages[patentDisclosureWorkflow][task.workflowStep]
	switch stage.key {
	case "sources":
		return taskResult{markdown: patentSourceList(project.Title, settings)}, nil
	case "check":
		return s.patentDeliveryCheck(ctx, project.ProjectID, task.workflowRunID, settings)
	}

	excerpts, err := s.turnMaterialExcerpts(ctx, settings.Materials)
	if err != nil {
		return taskResult{}, err
	}
	payload, err := json.Marshal(struct {
		Description string                `json:"description"`
		Materials   []turnMaterialExcerpt `json:"materials"`
	}{settings.Description, excerpts})
	if err != nil {
		return taskResult{}, err
	}
	input += "\n\n本轮固定的交底材料（JSON）\n" + string(payload)
	if stage.key == "manuscript" {
		_, outline, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "outline")
		if err != nil {
			return taskResult{}, err
		}
		input += "\n\n已保存的技术方案草案\n" + excerptRunes(outline, 3000)
	}
	markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input)
	if err != nil {
		return taskResult{}, err
	}
	if stage.key == "manuscript" {
		if _, err := patentManuscriptSections(markdown); err != nil {
			return taskResult{}, workflowOutputError{err}
		}
	}
	return taskResult{markdown: markdown}, nil
}

func (s *Store) patentDeliveryCheck(ctx context.Context, projectID, runID string, settings patentSettings) (taskResult, error) {
	_, manuscript, err := s.workflowArtifact(ctx, projectID, runID, "manuscript")
	if err != nil {
		return taskResult{}, err
	}
	_, outline, err := s.workflowArtifact(ctx, projectID, runID, "outline")
	if err != nil {
		return taskResult{}, err
	}
	lines, err := patentManuscriptSections(manuscript)
	if err != nil {
		return taskResult{}, workflowOutputError{err}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# 技术交底核对单\n\n交底稿 SHA-256 `%x`\n\n方案草案 SHA-256 `%x`\n\n",
		sha256.Sum256([]byte(manuscript)), sha256.Sum256([]byte(outline)))
	for index, line := range lines {
		fmt.Fprintf(&report, "- %s · 第 %d 行\n", patentSectionTitles[index], line)
	}
	report.WriteString("\n## 提交前复核\n\n- 核对专利文献编号和公开状态。\n- 核对实施参数、附图中的部件关系及效果数据。\n\n## 本轮材料指纹\n\n")
	if len(settings.Materials) == 0 {
		report.WriteString("仅使用技术说明。\n")
	}
	for _, material := range settings.Materials {
		fmt.Fprintf(&report, "- %s · SHA-256 `%s`\n", material.Name, material.SHA256)
	}
	return taskResult{markdown: report.String()}, nil
}
