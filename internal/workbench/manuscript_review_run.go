package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"research-agent/internal/inference"
)

//go:embed prompts/manuscript_review.md
var manuscriptReviewPrompt string

const maxReviewSourceBytes = 64 << 10

var errManuscriptReviewOutput = errors.New("model returned an invalid review")

type reviewFinding struct {
	Number     int            `json:"number"`
	Line       int            `json:"line"`
	Quote      string         `json:"quote"`
	Severity   string         `json:"severity"`
	Finding    string         `json:"finding"`
	Suggestion string         `json:"suggestion"`
	Edit       *selectionEdit `json:"edit,omitempty"`
}

type modelReview struct {
	Score          *int            `json:"score"`
	Recommendation string          `json:"recommendation"`
	Summary        string          `json:"summary"`
	Findings       []reviewFinding `json:"findings"`
}

var reviewRecommendations = map[string]string{"ACCEPT": "建议接收", "REVISE": "建议修改后再审", "REJECT": "建议退稿"}
var reviewSeverities = map[string]string{"major": "主要问题", "minor": "次要问题"}

func (s *Store) generateManuscriptReview(ctx context.Context, model *inference.Client, projectID string, task Task) (taskResult, error) {
	file, err := s.workspaceVersion(ctx, projectID, task.reviewVersionID)
	if err != nil {
		return taskResult{}, err
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		return taskResult{}, err
	}
	lines := strings.Split(doc.Content, "\n")
	var input strings.Builder
	fmt.Fprintf(&input, "投稿目标：%q\n\n以下是本轮固定版本的稿件正文，每行开头为稿件行号。\n", task.Instruction)
	for index, line := range lines {
		fmt.Fprintf(&input, "%d\t%s\n", index+1, line)
	}
	output, err := streamTaskMarkdown(ctx, model, manuscriptReviewPrompt, input.String())
	if err != nil {
		return taskResult{}, err
	}
	var review modelReview
	if err := json.Unmarshal([]byte(output), &review); err != nil || review.Score == nil || *review.Score < 0 || *review.Score > 100 ||
		reviewRecommendations[review.Recommendation] == "" || strings.TrimSpace(review.Summary) == "" || review.Findings == nil || len(review.Findings) > 12 {
		return taskResult{}, errManuscriptReviewOutput
	}
	for i := range review.Findings {
		finding := &review.Findings[i]
		finding.Number, finding.Edit = i+1, nil
		if finding.Line < 1 || finding.Line > len(lines) || reviewSeverities[finding.Severity] == "" ||
			strings.TrimSpace(finding.Quote) == "" || utf8.RuneCountInString(finding.Quote) > 200 ||
			!strings.Contains(strings.TrimSuffix(lines[finding.Line-1], "\r"), finding.Quote) ||
			strings.TrimSpace(finding.Finding) == "" || strings.TrimSpace(finding.Suggestion) == "" {
			return taskResult{}, errManuscriptReviewOutput
		}
		if finding.Severity == "major" && review.Recommendation == "ACCEPT" {
			review.Recommendation = "REVISE"
		}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# 投稿模拟评审\n\n- 投稿目标：%s\n- 稿件 SHA-256：`%s`\n- 模拟评分：%d / 100\n- 模拟结论：%s\n\n## 总体判断\n\n%s\n\n## 修改建议\n\n",
		task.Instruction, file.SHA256, *review.Score, reviewRecommendations[review.Recommendation], review.Summary)
	if len(review.Findings) == 0 {
		report.WriteString("本轮未列出具体修改事项。\n")
	}
	for _, finding := range review.Findings {
		fmt.Fprintf(&report, "### %d. 第 %d 行 · %s\n\n> %s\n\n%s\n\n修改建议：%s\n\n",
			finding.Number, finding.Line, reviewSeverities[finding.Severity], finding.Quote, finding.Finding, finding.Suggestion)
	}
	return taskResult{markdown: report.String(), findings: review.Findings}, nil
}
