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

const (
	experimentWorkflow = "experiment-v1"
	reviewWorkflow     = "literature-v1"
)

type workflowStage struct {
	key, title, prompt string
	checkpoint         bool
}

var workflowStages = map[string][]workflowStage{
	experimentWorkflow: {
		{key: "scope", title: "界定研究范围", prompt: experimentScopePrompt},
		{key: "plan", title: "编制实验计划", prompt: experimentPlanPrompt, checkpoint: true},
		{key: "checklist", title: "生成验收清单", prompt: experimentChecklistPrompt},
	},
	reviewWorkflow: {
		{key: "sources", title: "固定已核验来源"},
		{key: "themes", title: "整理主题与证据", prompt: reviewThemesPrompt, checkpoint: true},
		{key: "manuscript", title: "撰写综述稿件", prompt: reviewManuscriptPrompt},
	},
}

var projectWorkflows = map[string]string{
	"experiment":        experimentWorkflow,
	"literature_review": reviewWorkflow,
}
