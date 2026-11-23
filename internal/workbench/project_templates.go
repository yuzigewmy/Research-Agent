package workbench

import "net/http"

type projectTemplate struct {
	ID           string                  `json:"id"`
	Label        string                  `json:"label"`
	Revision     string                  `json:"revision"`
	Kind         string                  `json:"kind"`
	Summary      string                  `json:"summary"`
	Deliverables []string                `json:"deliverables"`
	Steps        []projectTemplateStep   `json:"steps"`
	Defaults     projectTemplateDefaults `json:"defaults"`
}

type projectTemplateStep struct {
	workflowStepDefinition
	Checkpoint     string `json:"checkpoint,omitempty"`
	Optional       bool   `json:"optional"`
	AcceptMarkdown bool   `json:"acceptMarkdown"`
	RequiresModel  bool   `json:"requiresModel"`
}

type projectTemplateDefaults struct {
	Checkpoints      *bool            `json:"checkpoints,omitempty"`
	Language         string           `json:"language,omitempty"`
	TargetPaperCount int              `json:"targetPaperCount,omitempty"`
	Proposal         *proposalTargets `json:"proposal,omitempty"`
	Course           *courseSettings  `json:"course,omitempty"`
	Software         *softwareOptions `json:"software,omitempty"`
	MaxRounds        int              `json:"maxRounds,omitempty"`
	TargetScore      int              `json:"targetScore,omitempty"`
}

var projectTemplates = []projectTemplate{
	{ID: "freeform", Label: "自由研究", Revision: "freeform-v1", Kind: "chat",
		Summary:      "从项目对话、资料和独立研究任务开始。",
		Deliverables: []string{"对话记录", "确认运行后生成的研究计划"}},
	{ID: "topic", Label: "研究选题", Revision: topicWorkflow, Kind: "workflow",
		Summary:      "从已有文献和选题要求出发，生成候选并选定研究方向。",
		Deliverables: []string{"来源清单", "构想与入围报告", "研究方案"}},
	{ID: "proposal", Label: "开题报告", Revision: proposalWorkflow, Kind: "workflow",
		Summary:      "按学位要求分节撰写开题报告，准备答辩并核对交付。",
		Deliverables: []string{"开题规划", "开题报告", "答辩材料", "交付核对报告"}},
	{ID: "literature_review", Label: "文献综述", Revision: reviewWorkflow, Kind: "workflow",
		Summary:      "利用项目文献库已核验的来源组织主题和证据。",
		Deliverables: []string{"来源清单", "主题规划", "综述稿件"},
		Defaults:     projectTemplateDefaults{TargetPaperCount: defaultReviewTarget}},
	{ID: "paper", Label: "论文写作", Revision: paperWorkflow, Kind: "workflow",
		Summary:      "从大纲组织论证、生成稿件，并核对稿件结构。",
		Deliverables: []string{"论文大纲", "论文稿件", "结构核对报告"},
		Defaults:     projectTemplateDefaults{Language: defaultPaperLanguage}},
	{ID: "experiment", Label: "实验计划", Revision: experimentWorkflow, Kind: "workflow",
		Summary:      "界定研究范围，编制可复查的实验计划。",
		Deliverables: []string{"研究范围", "实验计划", "可选验收清单"}},
	{ID: "automatic_review", Label: "多轮审稿", Revision: "automatic-review-v1", Kind: "review",
		Summary:      "审查已保存的 Markdown 稿件，按授权修订副本并复审。",
		Deliverables: []string{"逐轮评分和意见", "各轮审稿稿件", "多轮审稿报告"},
		Defaults:     projectTemplateDefaults{MaxRounds: defaultReviewRounds, TargetScore: defaultReviewScore}},
	{ID: "course_paper", Label: "课程论文", Revision: coursePaperWorkflow, Kind: "workflow",
		Summary:      "围绕课程论题组织研究方法、证据与分析，按章节撰写中文论文。",
		Deliverables: []string{"写作规划", "分节稿件与汇编稿", "交付核对报告", "可编辑 Word（启用导出组件后）"}},
	{ID: "course_report", Label: "课程报告", Revision: courseReportWorkflow, Kind: "workflow",
		Summary:      "先梳理材料中的事实，再按课程任务撰写实施过程、观察结果和反思。",
		Deliverables: []string{"材料核对单", "写作规划", "分节稿件与汇编稿", "交付核对报告", "可编辑 Word（启用导出组件后）"}},
	{ID: "software_registration", Label: "软件著作权材料", Revision: softwareRegistrationWorkflow, Kind: "workflow",
		Summary:      "整理固定版本的资料和操作手册。没有可用源码时，根据软件说明和手册生成待核对的示例。",
		Deliverables: []string{"材料来源清单", "操作手册草稿", "程序材料 TXT", "申请信息核对单 TXT", "程序和手册 PDF（配置字体后）", "可编辑 Word（启用导出组件后）"},
		Defaults:     projectTemplateDefaults{Software: &softwareOptions{Version: defaultSoftwareVersion}}},
}

func projectTemplateFor(id string) (projectTemplate, bool) {
	for _, template := range projectTemplates {
		if template.ID == id {
			return template, true
		}
	}
	return projectTemplate{}, false
}

func validProjectTemplate(id string) bool {
	_, ok := projectTemplateFor(id)
	return ok
}

func projectWorkflowKey(id string) string {
	if template, ok := projectTemplateFor(id); ok && template.Kind == "workflow" {
		return template.Revision
	}
	return ""
}

func listProjectTemplates(w http.ResponseWriter, _ *http.Request) {
	items := make([]projectTemplate, 0, len(projectTemplates))
	for _, template := range projectTemplates {
		stages := workflowStages[template.Revision]
		template.Steps = make([]projectTemplateStep, 0, len(stages))
		if template.Kind == "workflow" {
			template.Defaults.Checkpoints = new(template.Revision == softwareRegistrationWorkflow)
		}
		switch {
		case template.ID == "proposal":
			defaults := defaultProposalTargets()
			template.Defaults.Proposal = &defaults
		case isCourseWorkflow(template.Revision):
			defaults := defaultCourseSettings(template.Revision)
			template.Defaults.Course = &defaults
		}
		for index, stage := range stages {
			step := projectTemplateStep{workflowStepDefinition: workflowStepDefinition{Key: stage.key, Title: stage.title},
				Optional: stage.optional, AcceptMarkdown: stage.acceptMarkdown, RequiresModel: stage.prompt != ""}
			switch {
			case template.Revision == topicWorkflow && index == topicShortlistStep:
				step.Checkpoint = "choose-topic"
			case stage.checkpoint:
				step.Checkpoint = "confirm"
			}
			template.Steps = append(template.Steps, step)
		}
		items = append(items, template)
	}
	writeJSON(w, http.StatusOK, struct {
		Items []projectTemplate `json:"items"`
	}{items})
}
