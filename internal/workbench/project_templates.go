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
	Checkpoints      *bool                 `json:"checkpoints,omitempty"`
	Language         string                `json:"language,omitempty"`
	TargetPaperCount int                   `json:"targetPaperCount,omitempty"`
	Proposal         *proposalTargets      `json:"proposal,omitempty"`
	Course           *courseSettings       `json:"course,omitempty"`
	Software         *softwareOptions      `json:"software,omitempty"`
	MaxRounds        int                   `json:"maxRounds,omitempty"`
	TargetScore      int                   `json:"targetScore,omitempty"`
	MaxStepRetries   int                   `json:"maxStepRetries,omitempty"`
	ContestID        string                `json:"contestId,omitempty"`
	Contests         []mathModelingContest `json:"contests,omitempty"`
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
	{ID: "patent_disclosure", Label: "专利技术交底", Revision: patentDisclosureWorkflow, Kind: "workflow",
		Summary:      "依据固定资料梳理方案，确认后编写交底稿、生成两张附图并核对证据。",
		Deliverables: []string{"技术材料清单", "技术方案草案", "技术交底稿", "系统框图和操作流程图 SVG", "交底核对单", "可编辑 Word（启用导出组件后）"}},
	{ID: questionnaireTemplate, Label: "互动问卷", Revision: "survey-collection-v1", Kind: "survey",
		Summary:      "编辑问卷并发布填写链接，关闭回收后仍可导出答卷和进行清洗分析。",
		Deliverables: []string{"可填写的问卷链接", "答卷 CSV", "问卷分析报告"}},
	{ID: engineeringTemplate, Label: "工程项目", Revision: "engineering-preview-v1", Kind: "engineering",
		Summary:      "在工作区编辑静态页面，按固定文件版本启动、切换和关闭隔离预览。",
		Deliverables: []string{"已保存的工程文件", "版本化 HTML 预览"}},
	{ID: "full_research", Label: "科研全流程", Revision: fullResearchWorkflow, Kind: "workflow",
		Summary:      "从研究边界界定、文献梳理与研究契约确认到实验计划、论文撰写与交付核对的一体化流程。",
		Deliverables: []string{"研究边界", "文献与候选分析", "选题与研究契约", "实验与验证计划", "论文稿件", "交付核对报告"},
		Defaults:     projectTemplateDefaults{Language: defaultPaperLanguage}},
	{ID: "chinese_paper", Label: "中文论文", Revision: chinesePaperWorkflow, Kind: "workflow",
		Summary:      "面向中文学术期刊与会议论文，先界定文体范围、确认中文大纲，再完成正文撰写与结构核对。",
		Deliverables: []string{"研究范围与文体规范", "中文论文大纲", "中文论文稿件", "稿件结构核对报告"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
	{ID: "humanities_paper", Label: "人文社科论文", Revision: humanitiesPaperWorkflow, Kind: "workflow",
		Summary:      "梳理人文社科议题与文献脉络，经论证大纲确认后分章写作并核对引注与论证结构。",
		Deliverables: []string{"议题与文献脉络", "论证大纲", "人文社科论文稿件", "引注与结构核对报告"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
	{ID: "thesis", Label: "学位论文", Revision: thesisWorkflow, Kind: "workflow",
		Summary:      "依据学位规格搭建框架、规划目录与文献，再撰写主体章节并完成摘要与交付核对。",
		Deliverables: []string{"学位规格与框架", "目录与文献规划", "学位论文主体稿件", "摘要与交付核对报告"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
	{ID: "existing_materials", Label: "已有材料成稿", Revision: existingMaterialsWorkflow, Kind: "workflow",
		Summary:      "先清点现有笔记、数据与草稿并识别缺口，确认成稿规划后整合撰写并核对交付。",
		Deliverables: []string{"材料清点与缺口识别", "成稿规划", "整合稿件", "交付核对报告"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
	{ID: "market_research", Label: "市场调查", Revision: marketResearchWorkflow, Kind: "workflow",
		Summary:      "围绕调研目标制定样本计划与问卷编码，经确认后梳理统计解读并形成调研报告与附录。",
		Deliverables: []string{"调研目标与样本计划", "问卷与编码设计", "数据清洗与统计解读", "调研报告与附录"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
	{ID: "math_modeling", Label: "数学建模竞赛", Revision: mathModelingWorkflow, Kind: "workflow",
		Summary:      "覆盖国内外 22 项主流公开数模赛事规格，按题意剖析、假设拆解、求解验证与论文摘要核对推进。",
		Deliverables: []string{"题意与数据剖析", "问题拆解与假设", "模型构建与求解验证", "竞赛论文与摘要核对"},
		Defaults:     projectTemplateDefaults{Language: "zh", ContestID: defaultMathModelingContestID}},
	{ID: "big_data_contest", Label: "大数据竞赛", Revision: bigDataContestWorkflow, Kind: "workflow",
		Summary:      "围绕赛题数据探索、特征工程与基线确认、模型迭代消融验证到竞赛报告与代码核对。",
		Deliverables: []string{"赛题与数据探索", "特征与基线方案", "模型迭代与消融验证", "竞赛报告与代码核对"},
		Defaults:     projectTemplateDefaults{Language: "zh"}},
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
			template.Defaults.Checkpoints = new(defaultTemplateCheckpoints(template.Revision))
			template.Defaults.MaxStepRetries = defaultMaxStepRetries
		}
		switch {
		case template.ID == "proposal":
			defaults := defaultProposalTargets()
			template.Defaults.Proposal = &defaults
		case isCourseWorkflow(template.Revision):
			defaults := defaultCourseSettings(template.Revision)
			template.Defaults.Course = &defaults
		case template.ID == "math_modeling":
			template.Defaults.Contests = allMathModelingContests()
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
