package workbench

import _ "embed"

//go:embed prompts/experiment_scope.md
var experimentScopePrompt string

//go:embed prompts/experiment_plan.md
var experimentPlanPrompt string

//go:embed prompts/experiment_checklist.md
var experimentChecklistPrompt string

//go:embed prompts/review_themes.md
var reviewThemesPrompt string

//go:embed prompts/review_manuscript.md
var reviewManuscriptPrompt string

//go:embed prompts/paper_outline.md
var paperOutlinePrompt string

//go:embed prompts/paper_draft.md
var paperDraftPrompt string

//go:embed prompts/topic_brainstorm.md
var topicBrainstormPrompt string

//go:embed prompts/topic_shortlist.md
var topicShortlistPrompt string

//go:embed prompts/topic_contract.md
var topicContractPrompt string

//go:embed prompts/proposal_outline.md
var proposalOutlinePrompt string

//go:embed prompts/proposal_manuscript.md
var proposalManuscriptPrompt string

//go:embed prompts/proposal_defense.md
var proposalDefensePrompt string

//go:embed prompts/proposal_section.md
var proposalSectionPrompt string

const (
	experimentWorkflowV1 = "experiment-v1"
	experimentWorkflow   = "experiment-v2"
	reviewWorkflow       = "literature-v1"
	paperWorkflow        = "paper-v1"
	topicWorkflow        = "topic-v1"
	proposalWorkflowV1   = "proposal-v1"
	proposalWorkflow     = "proposal-v2"
)

func isProposalWorkflow(key string) bool {
	return key == proposalWorkflowV1 || key == proposalWorkflow
}

type workflowStage struct {
	key, title, prompt string
	checkpoint         bool
	acceptMarkdown     bool
	optional           bool
	artifactExtension  string
}

func experimentStages(optionalChecklist bool) []workflowStage {
	return []workflowStage{
		{key: "scope", title: "界定研究范围", prompt: experimentScopePrompt, acceptMarkdown: true},
		{key: "plan", title: "编制实验计划", prompt: experimentPlanPrompt, checkpoint: true, acceptMarkdown: true},
		{key: "checklist", title: "生成验收清单", prompt: experimentChecklistPrompt, acceptMarkdown: true, optional: optionalChecklist},
	}
}

var workflowStages = map[string][]workflowStage{
	experimentWorkflowV1: experimentStages(false),
	experimentWorkflow:   experimentStages(true),
	reviewWorkflow: {
		{key: "sources", title: "固定已核验来源"},
		{key: "themes", title: "整理主题与证据", prompt: reviewThemesPrompt, checkpoint: true, acceptMarkdown: true},
		{key: "manuscript", title: "撰写综述稿件", prompt: reviewManuscriptPrompt, acceptMarkdown: true},
	},
	paperWorkflow: {
		{key: "outline", title: "编制论文大纲", prompt: paperOutlinePrompt, checkpoint: true, acceptMarkdown: true},
		{key: "manuscript", title: "撰写论文稿件", prompt: paperDraftPrompt, acceptMarkdown: true},
		{key: "structure", title: "核对稿件结构"},
	},
	topicWorkflow: {
		{key: "sources", title: "固定本轮文献"},
		{key: "brainstorm", title: "生成研究构想", prompt: topicBrainstormPrompt},
		{key: "shortlist", title: "评估并筛选构想", prompt: topicShortlistPrompt},
		{key: "contract", title: "编写研究方案", prompt: topicContractPrompt, acceptMarkdown: true},
	},
	proposalWorkflowV1: {
		{key: "sources", title: "固定本轮文献"},
		{key: "outline", title: "编制开题规划", prompt: proposalOutlinePrompt, checkpoint: true, acceptMarkdown: true},
		{key: "manuscript", title: "撰写开题报告", prompt: proposalManuscriptPrompt, acceptMarkdown: true},
		{key: "defense", title: "准备开题答辩", prompt: proposalDefensePrompt, acceptMarkdown: true},
		{key: "check", title: "核对开题交付"},
	},
	proposalWorkflow:               sectionedProposalStages(),
	coursePaperWorkflow:            courseStages(coursePaperWorkflow),
	courseReportWorkflow:           courseStages(courseReportWorkflow),
	softwareRegistrationWorkflowV1: softwareRegistrationStages(""),
	softwareRegistrationWorkflow:   softwareRegistrationStages(softwareSamplePrompt),
	patentDisclosureWorkflowV1:     patentDisclosureStages(false),
	patentDisclosureWorkflow:       patentDisclosureStages(true),
}

func patentDisclosureStages(withFigures bool) []workflowStage {
	stages := []workflowStage{
		{key: "sources", title: "固定技术材料"},
		{key: "outline", title: "梳理技术方案草案", prompt: patentOutlinePrompt, checkpoint: true, acceptMarkdown: true},
		{key: "manuscript", title: "编写技术交底稿", prompt: patentManuscriptPrompt, acceptMarkdown: true},
	}
	if withFigures {
		stages = append(stages, workflowStage{key: "figures", title: "编制技术附图", prompt: patentFiguresPrompt, artifactExtension: ".json"})
	}
	return append(stages, workflowStage{key: "check", title: "核对交底成果"})
}

func defaultTemplateCheckpoints(workflow string) bool {
	return isSoftwareRegistrationWorkflow(workflow) || isPatentWorkflow(workflow)
}

func softwareRegistrationStages(programPrompt string) []workflowStage {
	return []workflowStage{
		{key: "sources", title: "固定材料与来源"},
		{key: "manuscript", title: "编制操作手册草稿", prompt: softwareManualPrompt, checkpoint: true, acceptMarkdown: true},
		{key: "program", title: "整理程序材料", prompt: programPrompt, artifactExtension: ".txt"},
		{key: "application", title: "生成申请信息核对单", artifactExtension: ".txt"},
	}
}
