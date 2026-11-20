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

const (
	experimentWorkflowV1 = "experiment-v1"
	experimentWorkflow   = "experiment-v2"
	reviewWorkflow       = "literature-v1"
	paperWorkflow        = "paper-v1"
)

type workflowStage struct {
	key, title, prompt string
	checkpoint         bool
	acceptMarkdown     bool
	optional           bool
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
}

var projectWorkflows = map[string]string{
	"experiment":        experimentWorkflow,
	"literature_review": reviewWorkflow,
	"paper":             paperWorkflow,
}
