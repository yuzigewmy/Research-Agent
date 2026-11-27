package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"research-agent/internal/inference"
)

const (
	coursePaperWorkflow   = "course-paper-v1"
	courseReportWorkflow  = "course-report-v1"
	maxCourseRequirements = 4 << 10
)

//go:embed prompts/course_facts.md
var courseFactsPrompt string

//go:embed prompts/course_outline.md
var courseOutlinePrompt string

//go:embed prompts/course_section.md
var courseSectionPrompt string

var errCourseOptions = errors.New("invalid course writing options")

type courseOptions struct {
	TargetCharacters int      `json:"targetCharacters"`
	Discipline       string   `json:"discipline"`
	Method           string   `json:"method,omitempty"`
	ReportType       string   `json:"reportType,omitempty"`
	Requirements     string   `json:"requirements,omitempty"`
	MaterialIDs      []string `json:"materialIds,omitempty"`
}

type courseSettings struct {
	courseOptions
	Language  string          `json:"language"`
	Materials []TurnReference `json:"materials,omitempty"`
}

func isCourseWorkflow(key string) bool {
	return key == coursePaperWorkflow || key == courseReportWorkflow
}

func defaultCourseSettings(key string) courseSettings {
	options := courseOptions{Discipline: "auto"}
	if key == coursePaperWorkflow {
		options.TargetCharacters, options.Method = 8000, "auto"
	} else {
		options.TargetCharacters, options.ReportType = 10000, "auto"
	}
	return courseSettings{courseOptions: options, Language: "zh"}
}

func normalizeCourseOptions(key string, input *courseOptions) (courseSettings, error) {
	settings := defaultCourseSettings(key)
	if input == nil {
		return settings, nil
	}
	options := *input
	if options.TargetCharacters == 0 {
		options.TargetCharacters = settings.TargetCharacters
	}
	if options.Discipline == "" {
		options.Discipline = settings.Discipline
	}
	options.Requirements = strings.TrimSpace(options.Requirements)
	if options.TargetCharacters < 1000 || options.TargetCharacters > 30000 ||
		!slices.Contains([]string{"auto", "computing", "humanities", "business", "engineering"}, options.Discipline) ||
		len(options.Requirements) > maxCourseRequirements || strings.ContainsRune(options.Requirements, 0) ||
		!validTurnReferences(options.MaterialIDs, nil) {
		return courseSettings{}, errCourseOptions
	}
	if key == coursePaperWorkflow {
		if options.Method == "" {
			options.Method = settings.Method
		}
		if options.ReportType != "" || !slices.Contains([]string{"auto", "experiment", "modeling", "simulation", "clinical", "regression", "survey", "qualitative", "textual", "mixed"}, options.Method) {
			return courseSettings{}, errCourseOptions
		}
	} else {
		if options.ReportType == "" {
			options.ReportType = settings.ReportType
		}
		if options.Method != "" || !slices.Contains([]string{"auto", "project", "experiment", "survey", "literature"}, options.ReportType) {
			return courseSettings{}, errCourseOptions
		}
	}
	settings.courseOptions = options
	return settings, nil
}

func freezeCourseSettings(ctx context.Context, tx *sql.Tx, projectID, key string, input *courseOptions) (string, error) {
	settings, err := normalizeCourseOptions(key, input)
	if err != nil {
		return "", err
	}
	settings.Materials, err = resolveTurnReferences(ctx, tx, projectID, settings.MaterialIDs, nil)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(settings)
	return string(body), err
}

type courseSection struct {
	key, title, focus string
	share             int
}

var courseSections = map[string][]courseSection{
	coursePaperWorkflow: {
		{"abstract", "摘要与关键词", "概括课题、所用方法和材料可支持的结论，给出关键词。", 5},
		{"background", "引言与研究背景", "界定论题，说明课程背景、已有证据和研究问题。", 20},
		{"method", "研究设计与方法", "解释方法选择及其与材料和论题的关系。", 20},
		{"analysis", "证据与分析", "围绕论点组织证据、解释与适用条件。", 30},
		{"conclusion", "结论与局限", "给出有证据支撑的结论、局限及后续问题。", 25},
	},
	courseReportWorkflow: {
		{"overview", "任务概况", "说明课程任务、目标、对象和现有材料。", 15},
		{"process", "实施过程", "说明可核实的设计、操作、调研或阅读过程。", 25},
		{"results", "结果与观察", "列出实际获得的记录、结果及其出处。", 25},
		{"discussion", "分析与反思", "从结果与课程要求出发，总结解释、局限和改进。", 25},
		{"conclusion", "总结与后续工作", "整理任务结论、交付物及仍待补齐的证据。", 10},
	},
}

func courseStages(key string) []workflowStage {
	var stages []workflowStage
	if key == courseReportWorkflow {
		stages = append(stages, workflowStage{key: "facts", title: "梳理材料与事实", prompt: courseFactsPrompt})
	}
	stages = append(stages, workflowStage{key: "outline", title: "编制课程写作规划", prompt: courseOutlinePrompt, checkpoint: true, acceptMarkdown: true})
	for _, section := range courseSections[key] {
		stages = append(stages, workflowStage{key: section.key, title: section.title, prompt: courseSectionPrompt})
	}
	return append(stages,
		workflowStage{key: "manuscript", title: "汇编课程稿件"},
		workflowStage{key: "check", title: "核对课程交付"},
	)
}

func (s *Store) generateCourseStep(ctx context.Context, model *inference.Client, project Project, task Task, key, input, evidence, options string) (taskResult, error) {
	var settings courseSettings
	if err := json.Unmarshal([]byte(options), &settings); err != nil {
		return taskResult{}, err
	}
	var sources []reviewSource
	if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
		return taskResult{}, err
	}
	stages, sections := workflowStagesForRun(key, options), courseSections[key]
	stage := stages[task.workflowStep]
	// The final two steps compile and check the preceding section artifacts.
	firstSection := len(stages) - len(sections) - 2
	switch stage.key {
	case "manuscript":
		parts, err := s.workflowSectionBodies(ctx, project.ProjectID, task.workflowRunID, firstSection, task.workflowStep)
		if err != nil {
			return taskResult{}, err
		}
		var manuscript strings.Builder
		template, _ := projectTemplateFor(project.TemplateID)
		fmt.Fprintf(&manuscript, "# %s：%s\n\n", strings.Join(strings.Fields(project.Title), " "), template.Label)
		for _, part := range parts {
			manuscript.WriteString(part)
			manuscript.WriteByte('\n')
		}
		appendCitedWorkflowReferences(&manuscript, parts, sources)
		return taskResult{markdown: manuscript.String()}, nil
	case "check":
		_, manuscript, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "manuscript")
		if err != nil {
			return taskResult{}, err
		}
		return taskResult{markdown: courseDeliveryCheck(manuscript, settings, sections)}, nil
	}

	materials, err := s.turnMaterialExcerpts(ctx, settings.Materials)
	if err != nil {
		return taskResult{}, err
	}
	payload, err := json.Marshal(struct {
		Settings  courseOptions         `json:"course"`
		Materials []turnMaterialExcerpt `json:"materials"`
		Sources   []reviewSource        `json:"sources"`
	}{settings.courseOptions, materials, sources})
	if err != nil {
		return taskResult{}, err
	}
	input += "\n\n本轮固定的课程写作输入（JSON）\n" + string(payload)
	if key == courseReportWorkflow && stage.key != "facts" {
		_, facts, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "facts")
		if err != nil {
			return taskResult{}, err
		}
		input += "\n\n已保存的材料核对节选\n" + excerptRunes(facts, 2400)
	}
	if task.workflowStep < firstSection {
		markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input)
		if err != nil {
			return taskResult{}, err
		}
		if err := checkWorkflowCitations(markdown, sources, false); err != nil {
			return taskResult{}, workflowOutputError{err}
		}
		return taskResult{markdown: markdown}, nil
	}
	_, outline, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "outline")
	if err != nil {
		return taskResult{}, err
	}
	input += "\n\n已保存的课程写作规划\n" + outline
	previous, err := s.workflowSectionBodies(ctx, project.ProjectID, task.workflowRunID, firstSection, task.workflowStep)
	if err != nil {
		return taskResult{}, err
	}
	for _, markdown := range previous {
		input += "\n\n已完成章节节选\n" + excerptRunes(markdown, 600)
	}
	var section courseSection
	for _, candidate := range sections {
		if candidate.key == stage.key {
			section = candidate
			break
		}
	}
	goal := settings.TargetCharacters * section.share / 100
	input += fmt.Sprintf("\n\n本节标题：%s\n本节目标字数：%d\n本节写作要点：%s", section.title, goal, section.focus)
	markdown, err := streamTaskMarkdownWithBudget(ctx, model, stage.prompt, input, 2*goal+512)
	if err != nil {
		return taskResult{}, err
	}
	return checkedWorkflowSection(markdown, section.title, sources)
}

func courseDeliveryCheck(manuscript string, settings courseSettings, sections []courseSection) string {
	lower, upper := settings.TargetCharacters*4/5, settings.TargetCharacters*6/5
	characters := 0
	headings := make(map[string]int)
	var pending []int
	inReferences := false
	for line, text := range proposalLines(manuscript) {
		if heading := paperHeading.FindStringSubmatch(text); heading != nil {
			if strings.HasPrefix(text, "## ") {
				headings[heading[1]] = line
				inReferences = heading[1] == "参考文献"
			}
			continue
		}
		if inReferences {
			continue
		}
		for _, runeValue := range reviewCitation.ReplaceAllString(text, "") {
			if unicode.IsLetter(runeValue) || unicode.IsNumber(runeValue) {
				characters++
			}
		}
		if len(pending) < 30 && (strings.Contains(text, "待补") || strings.Contains(text, "TODO")) {
			pending = append(pending, line)
		}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# 课程交付核对\n\n稿件 SHA-256 `%x`\n\n- 正文字数（字母、汉字与数字）：%d / %d，目标范围 %d–%d 字。\n",
		sha256.Sum256([]byte(manuscript)), characters, settings.TargetCharacters, lower, upper)
	if characters < lower || characters > upper {
		report.WriteString("- 正文字数待调整。\n")
	}
	for _, section := range sections {
		if line, ok := headings[section.title]; ok {
			fmt.Fprintf(&report, "- %s，第 %d 行。\n", section.title, line)
		} else {
			fmt.Fprintf(&report, "- %s，待核对标题。\n", section.title)
		}
	}
	for _, line := range pending {
		fmt.Fprintf(&report, "- 第 %d 行有待补标记。\n", line)
	}
	if len(settings.Materials) > 0 {
		report.WriteString("\n## 本轮选定的资料\n\n")
		for _, material := range settings.Materials {
			fmt.Fprintf(&report, "- %s，SHA-256 `%s`\n", material.Name, material.SHA256)
		}
	}
	return report.String()
}
