package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"research-agent/internal/inference"
)

const (
	workflowSelectTopic  = "select-topic"
	workflowWaitingTopic = "WAITING_TOPIC_CHOICE"
	topicBrainstormStep  = 1
	topicShortlistStep   = 2
	topicContractStep    = 3
)

const workflowTopicSchema = `
	ALTER TABLE tasks ADD COLUMN workflow_topic_data TEXT NOT NULL DEFAULT '';
	ALTER TABLE workflow_runs ADD COLUMN selected_topic_number INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE workflow_requests ADD COLUMN topic_number INTEGER NOT NULL DEFAULT 0;
	PRAGMA user_version = 23;
`

var (
	errTopicOptions   = errors.New("invalid topic requirements")
	errTopicCandidate = errors.New("unknown topic candidate")
	errTopicIdeas     = errors.New("构想报告需要 8 至 12 个不同的构想，每项提供题名和摘要。")
	errTopicShortlist = errors.New("入围报告须提供 3 至 5 个有效编号、评估和推荐理由。")
)

type topicOptions struct {
	Requirements string `json:"requirements,omitempty"`
}

type topicIdea struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type topicCandidate struct {
	topicIdea
	NoveltyScore *int     `json:"noveltyScore"`
	Strength     string   `json:"strength"`
	Risk         string   `json:"risk"`
	Effort       string   `json:"effort"`
	EvidenceKeys []string `json:"evidenceKeys,omitempty"`
}

type topicChoice struct {
	Candidates           []topicCandidate `json:"candidates"`
	RecommendedNumber    int              `json:"recommendedNumber"`
	RecommendationReason string           `json:"recommendationReason"`
	SelectedNumber       int              `json:"selectedNumber,omitempty"`
}

func (choice *topicChoice) candidate(number int) (topicCandidate, bool) {
	for _, candidate := range choice.Candidates {
		if candidate.Number == number {
			return candidate, true
		}
	}
	return topicCandidate{}, false
}

func readTopicStep[T any](ctx context.Context, db workspaceQuery, runID string, step int) (T, error) {
	var result T
	var data string
	err := db.QueryRowContext(ctx, `SELECT workflow_topic_data FROM tasks WHERE workflow_run_id = ? AND workflow_step = ?
		AND workflow_superseded = 0 AND status = 'SUCCEEDED' ORDER BY ordinal DESC LIMIT 1`, runID, step).Scan(&data)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(data), &result)
	return result, err
}

func (s *Store) generateTopicStep(ctx context.Context, model *inference.Client, task Task, input, evidence, options string, selectedNumber int) (taskResult, error) {
	var sources []reviewSource
	if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
		return taskResult{}, err
	}
	stage := workflowStages[topicWorkflow][task.workflowStep]
	if stage.key == "sources" {
		return taskResult{markdown: reviewSourcesMarkdown(sources)}, nil
	}
	input += "\n\n本轮选题要求（JSON）\n" + options + "\n\n本轮固定的文献元数据（JSON）\n" + evidence
	if len(sources) == 0 {
		input += "\n本轮暂无已核验来源，请注明证据缺口。"
	}
	switch task.workflowStep {
	case topicBrainstormStep:
		output, err := streamTaskMarkdown(ctx, model, stage.prompt, input)
		if err != nil {
			return taskResult{}, err
		}
		return topicIdeasResult(output)
	case topicShortlistStep:
		ideas, err := readTopicStep[[]topicIdea](ctx, s.db, task.workflowRunID, topicBrainstormStep)
		if err != nil {
			return taskResult{}, err
		}
		data, err := json.Marshal(ideas)
		if err != nil {
			return taskResult{}, err
		}
		output, err := streamTaskMarkdown(ctx, model, stage.prompt, input+"\n\n已有构想（JSON）\n"+string(data))
		if err != nil {
			return taskResult{}, err
		}
		return topicShortlistResult(output, ideas, sources)
	case topicContractStep:
		choice, err := readTopicStep[topicChoice](ctx, s.db, task.workflowRunID, topicShortlistStep)
		if err != nil {
			return taskResult{}, err
		}
		selected, ok := choice.candidate(selectedNumber)
		if !ok {
			return taskResult{}, errTopicCandidate
		}
		data, err := json.Marshal(selected)
		if err != nil {
			return taskResult{}, err
		}
		output, err := streamTaskMarkdown(ctx, model, stage.prompt, input+"\n\n所选构想（JSON）\n"+string(data))
		if err != nil {
			return taskResult{}, err
		}
		if err := checkWorkflowCitations(output, sources, false); err != nil {
			return taskResult{}, workflowOutputError{err}
		}
		return taskResult{markdown: output}, nil
	default:
		return taskResult{}, errWorkflowStep
	}
}

func topicIdeasResult(output string) (taskResult, error) {
	var batch struct {
		Ideas []topicIdea `json:"ideas"`
	}
	if err := json.Unmarshal([]byte(output), &batch); err != nil || len(batch.Ideas) < 8 || len(batch.Ideas) > 12 {
		return taskResult{}, workflowOutputError{errTopicIdeas}
	}
	var report strings.Builder
	report.WriteString("# 研究构想\n\n")
	seen := make(map[string]bool, len(batch.Ideas))
	for i := range batch.Ideas {
		idea := &batch.Ideas[i]
		idea.Number = i + 1
		idea.Title = strings.TrimSpace(idea.Title)
		idea.Summary = strings.TrimSpace(idea.Summary)
		key := strings.ToLower(strings.Join(strings.Fields(idea.Title), " "))
		if !validTopicText(idea.Title, 120) || !validTopicText(idea.Summary, 800) || seen[key] {
			return taskResult{}, workflowOutputError{errTopicIdeas}
		}
		seen[key] = true
		fmt.Fprintf(&report, "## %d. %s\n\n%s\n\n", idea.Number, idea.Title, idea.Summary)
	}
	data, err := json.Marshal(batch.Ideas)
	return taskResult{markdown: report.String(), topicData: string(data)}, err
}

func topicShortlistResult(output string, ideas []topicIdea, sources []reviewSource) (taskResult, error) {
	var choice topicChoice
	if err := json.Unmarshal([]byte(output), &choice); err != nil || len(choice.Candidates) < 3 || len(choice.Candidates) > 5 {
		return taskResult{}, workflowOutputError{errTopicShortlist}
	}
	choice.SelectedNumber = 0
	choice.RecommendationReason = strings.TrimSpace(choice.RecommendationReason)
	allowedSources := make(map[string]bool, len(sources))
	for _, source := range sources {
		allowedSources[source.Key] = true
	}
	seen := make(map[int]bool, len(choice.Candidates))
	var report strings.Builder
	report.WriteString("# 研究选题入围报告\n\n分数是对本轮文献范围内新颖性的初步判断。\n\n")
	for i := range choice.Candidates {
		candidate := &choice.Candidates[i]
		if candidate.Number < 1 || candidate.Number > len(ideas) || seen[candidate.Number] ||
			candidate.NoveltyScore == nil || *candidate.NoveltyScore < 0 || *candidate.NoveltyScore > 10 {
			return taskResult{}, workflowOutputError{errTopicShortlist}
		}
		seen[candidate.Number] = true
		candidate.topicIdea = ideas[candidate.Number-1]
		candidate.Strength = strings.TrimSpace(candidate.Strength)
		candidate.Risk = strings.TrimSpace(candidate.Risk)
		candidate.Effort = strings.TrimSpace(candidate.Effort)
		if !validTopicText(candidate.Strength, 400) || !validTopicText(candidate.Risk, 400) || !validTopicText(candidate.Effort, 240) {
			return taskResult{}, workflowOutputError{errTopicShortlist}
		}
		for _, key := range candidate.EvidenceKeys {
			if !allowedSources[key] {
				return taskResult{}, workflowOutputError{errors.New("入围报告含有本轮来源清单之外的文献键。")}
			}
		}
		fmt.Fprintf(&report, "## %d. %s\n\n%s\n\n- 新颖性初评 %d/10\n- 优势 %s\n- 风险 %s\n- 预计投入 %s\n",
			candidate.Number, candidate.Title, candidate.Summary, *candidate.NoveltyScore, candidate.Strength, candidate.Risk, candidate.Effort)
		if len(candidate.EvidenceKeys) > 0 {
			fmt.Fprintf(&report, "- 关联文献 [@%s]\n", strings.Join(candidate.EvidenceKeys, "]、[@"))
		}
		report.WriteByte('\n')
	}
	if !seen[choice.RecommendedNumber] || !validTopicText(choice.RecommendationReason, 600) {
		return taskResult{}, workflowOutputError{errTopicShortlist}
	}
	fmt.Fprintf(&report, "## 推荐第 %d 项\n\n%s\n", choice.RecommendedNumber, choice.RecommendationReason)
	data, err := json.Marshal(choice)
	return taskResult{markdown: report.String(), topicData: string(data)}, err
}

func validTopicText(value string, maxRunes int) bool {
	return value != "" && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes
}
