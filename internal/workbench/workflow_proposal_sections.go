package workbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"research-agent/internal/inference"
)

const firstProposalSection = 2

var proposalSections = [...]struct {
	key, title, focus string
	share             int
}{
	{"background", "选题背景与意义", "交代研究对象、现实背景和研究价值。", 15},
	{"literature", "研究现状", "围绕研究问题比较已有证据和解释，指出具体缺口。", 20},
	{"objectives", "目标与研究内容", "列出各项研究目标、可检验问题及其对应任务。", 15},
	{"methods", "研究方法与技术路线", "使用文字或表格说明样本、方法、对照、指标、技术路线和适用条件。", 20},
	{"feasibility", "可行性与创新", "区分现有条件与待补条件，说明创新边界、风险和可行性。", 15},
	{"schedule", "进度与预期成果", "把研究任务、阶段产物与月份安排对应，日期未定时采用相对月份。", 15},
}

func sectionedProposalStages() []workflowStage {
	stages := []workflowStage{
		{key: "sources", title: "固定本轮文献"},
		{key: "outline", title: "编制开题规划", prompt: proposalOutlinePrompt, checkpoint: true, acceptMarkdown: true},
	}
	for _, section := range proposalSections {
		stages = append(stages, workflowStage{key: section.key, title: section.title, prompt: proposalSectionPrompt})
	}
	return append(stages,
		workflowStage{key: "manuscript", title: "汇编开题报告"},
		workflowStage{key: "defense", title: "准备开题答辩", prompt: proposalDefensePrompt, acceptMarkdown: true},
		workflowStage{key: "check", title: "核对开题交付"},
	)
}

func (s *Store) sectionedProposalResult(ctx context.Context, model *inference.Client, project Project, task Task, input string, goals proposalTargets, sources []reviewSource) (taskResult, error) {
	if task.workflowStep == firstProposalSection+len(proposalSections) {
		return s.assembleProposal(ctx, project, task, sources)
	}
	_, outline, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "outline")
	if err != nil {
		return taskResult{}, err
	}
	input += "\n\n已保存的开题规划\n" + outline
	sections, err := s.currentProposalSections(ctx, project.ProjectID, task.workflowRunID, task.workflowStep)
	if err != nil {
		return taskResult{}, err
	}
	for _, markdown := range sections {
		input += "\n\n已完成章节节选\n" + excerptRunes(markdown, 600)
	}
	stage := workflowStages[proposalWorkflow][task.workflowStep]
	if stage.key == "defense" {
		markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input)
		if err == nil {
			if err := checkWorkflowCitations(markdown, sources, false); err != nil {
				return taskResult{}, workflowOutputError{err}
			}
		}
		return taskResult{markdown: markdown}, err
	}
	section := proposalSections[task.workflowStep-firstProposalSection]
	goal := goals.TargetCharacters * section.share / 100
	input += fmt.Sprintf("\n\n本节标题：%s\n本节目标字数：%d\n本节写作要点：%s", section.title, goal, section.focus)
	markdown, err := streamTaskMarkdownWithBudget(ctx, model, stage.prompt, input, 2*goal+512)
	if err != nil {
		return taskResult{}, err
	}
	if err := checkWorkflowCitations(markdown, sources, false); err != nil {
		return taskResult{}, workflowOutputError{err}
	}
	body := strings.TrimSpace(markdown)
	body = strings.TrimSpace(strings.TrimPrefix(body, "## "+section.title+"\n"))
	if body == "" {
		return taskResult{}, workflowOutputError{errors.New("此节缺少正文。")}
	}
	for _, line := range proposalLines(body) {
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			return taskResult{}, workflowOutputError{errors.New("此节正文中的小标题须从三级开始。")}
		}
	}
	return taskResult{markdown: "## " + section.title + "\n\n" + body + "\n"}, nil
}

func (s *Store) currentProposalSections(ctx context.Context, projectID, runID string, before int) ([]string, error) {
	end := min(before, firstProposalSection+len(proposalSections))
	rows, err := s.db.QueryContext(ctx, `SELECT markdown FROM tasks WHERE project_id = ? AND workflow_run_id = ?
		AND workflow_step >= ? AND workflow_step < ? AND status = 'SUCCEEDED' AND workflow_superseded = 0 ORDER BY workflow_step`,
		projectID, runID, firstProposalSection, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sections := make([]string, 0, end-firstProposalSection)
	for rows.Next() {
		var markdown string
		if err := rows.Scan(&markdown); err != nil {
			return nil, err
		}
		sections = append(sections, markdown)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sections) != end-firstProposalSection {
		return nil, sql.ErrNoRows
	}
	return sections, nil
}

func (s *Store) assembleProposal(ctx context.Context, project Project, task Task, sources []reviewSource) (taskResult, error) {
	sections, err := s.currentProposalSections(ctx, project.ProjectID, task.workflowRunID, task.workflowStep)
	if err != nil {
		return taskResult{}, err
	}
	var manuscript strings.Builder
	fmt.Fprintf(&manuscript, "# %s：开题报告\n\n", strings.Join(strings.Fields(project.Title), " "))
	cited := make(map[string]bool)
	for _, markdown := range sections {
		manuscript.WriteString(markdown)
		manuscript.WriteByte('\n')
		for _, line := range proposalLines(markdown) {
			for _, match := range reviewCitation.FindAllStringSubmatch(line, -1) {
				cited[match[1]] = true
			}
		}
	}
	manuscript.WriteString("## 参考文献\n\n")
	if len(cited) == 0 {
		manuscript.WriteString("正文尚无本轮已核验的文献引用。\n")
	}
	for _, source := range sources {
		if cited[source.Key] {
			fmt.Fprintf(&manuscript, "- [@%s] %s。DOI `%s`\n", source.Key, strings.Join(strings.Fields(source.Title), " "), source.DOI)
		}
	}
	return taskResult{markdown: manuscript.String()}, nil
}
