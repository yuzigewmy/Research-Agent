package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"research-agent/internal/inference"
)

//go:embed prompts/automatic_review.md
var automaticReviewPrompt string

//go:embed prompts/automatic_revision.md
var automaticRevisionPrompt string

const (
	automaticReviewTargetMet  = "TARGET_MET"
	automaticReviewMaxRounds  = "MAX_ROUNDS"
	automaticReviewNoProgress = "NO_PROGRESS"
)

var errAutomaticRevision = errors.New("model returned edits outside the reviewed lines")

func (s *Store) runAutomaticReview(ctx context.Context, model *inference.Client, projectID, reviewID string) (taskResult, error) {
	review, err := readAutomaticReview(ctx, s.db, projectID, reviewID)
	if err != nil {
		return taskResult{}, err
	}
	for {
		file, err := s.workspaceVersion(ctx, projectID, review.workVersion)
		if err != nil {
			return taskResult{}, err
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			return taskResult{}, err
		}
		lines := strings.Split(doc.Content, "\n")
		if len(review.Rounds) == 0 || review.Rounds[len(review.Rounds)-1].VersionID != doc.VersionID {
			round, err := reviewManuscriptRound(ctx, model, review, doc.WorkspaceFile, lines)
			if err != nil {
				return taskResult{}, err
			}
			review.Rounds = append(review.Rounds, round)
			if err := s.saveAutomaticReviewProgress(ctx, projectID, &review, "", nil); err != nil {
				return taskResult{}, err
			}
		}
		round := review.Rounds[len(review.Rounds)-1]
		switch {
		case round.Score >= float64(review.TargetScore):
			return automaticReviewResult(review, automaticReviewTargetMet), nil
		case len(review.Rounds) >= review.MaxRounds:
			return automaticReviewResult(review, automaticReviewMaxRounds), nil
		case len(round.Findings) == 0 || repeatedReviewFindings(review.Rounds):
			return automaticReviewResult(review, automaticReviewNoProgress), nil
		}
		revised, err := reviseReviewedLines(ctx, model, round, review.Criteria, lines)
		if err != nil {
			return taskResult{}, err
		}
		if revised == doc.Content {
			return automaticReviewResult(review, automaticReviewNoProgress), nil
		}
		workPath := fmt.Sprintf("reviews/%s/round-%02d.md", review.ReviewID, round.Number+1)
		if err := s.saveAutomaticReviewProgress(ctx, projectID, &review, workPath, &revised); err != nil {
			return taskResult{}, err
		}
	}
}

func reviewManuscriptRound(ctx context.Context, model *inference.Client, review automaticReview, file WorkspaceFile, lines []string) (automaticReviewRound, error) {
	input := fmt.Sprintf("投稿目标：%q\n补充评审标准：%q\n审稿轮次：%d\n\n稿件正文：\n%s",
		review.Target, review.Criteria, len(review.Rounds)+1, numberedManuscript(lines))
	output, err := streamTaskMarkdownWithBudget(ctx, model, automaticReviewPrompt, input, 4096)
	if err != nil {
		return automaticReviewRound{}, err
	}
	var verdict struct {
		Score    *float64        `json:"score"`
		Summary  string          `json:"summary"`
		Findings []reviewFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(output), &verdict); err != nil || verdict.Score == nil || *verdict.Score < 1 || *verdict.Score > 10 ||
		strings.TrimSpace(verdict.Summary) == "" || !validateReviewFindings(verdict.Findings, lines) {
		return automaticReviewRound{}, errManuscriptReviewOutput
	}
	return automaticReviewRound{Number: len(review.Rounds) + 1, Score: *verdict.Score, Summary: verdict.Summary,
		VersionID: file.VersionID, SHA256: file.SHA256, Path: file.Path, Findings: verdict.Findings}, nil
}

func repeatedReviewFindings(rounds []automaticReviewRound) bool {
	if len(rounds) < 2 {
		return false
	}
	previous, current := rounds[len(rounds)-2], rounds[len(rounds)-1]
	if current.Score > previous.Score || len(previous.Findings) != len(current.Findings) {
		return false
	}
	type anchor struct {
		line                     int
		quote, severity, finding string
	}
	counts := make(map[anchor]int, len(previous.Findings))
	for _, item := range previous.Findings {
		counts[anchor{item.Line, item.Quote, item.Severity, strings.TrimSpace(item.Finding)}]++
	}
	for _, item := range current.Findings {
		key := anchor{item.Line, item.Quote, item.Severity, strings.TrimSpace(item.Finding)}
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func reviseReviewedLines(ctx context.Context, model *inference.Client, round automaticReviewRound, criteria string, lines []string) (string, error) {
	findings, err := json.Marshal(round.Findings)
	if err != nil {
		return "", err
	}
	input := fmt.Sprintf("补充评审标准：%q\n本轮评审意见：%s\n\n稿件正文：\n%s", criteria, findings, numberedManuscript(lines))
	output, err := streamTaskMarkdownWithBudget(ctx, model, automaticRevisionPrompt, input, 4096)
	if err != nil {
		return "", err
	}
	var result struct {
		Edits []struct {
			Line        int     `json:"line"`
			Replacement *string `json:"replacement"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.Edits == nil {
		return "", errAutomaticRevision
	}
	allowed := make(map[int]bool, len(round.Findings))
	for _, finding := range round.Findings {
		allowed[finding.Line] = true
	}
	for _, edit := range result.Edits {
		if !allowed[edit.Line] || edit.Replacement == nil || strings.ContainsAny(*edit.Replacement, "\r\n\x00") {
			return "", errAutomaticRevision
		}
		delete(allowed, edit.Line)
		line := edit.Line - 1
		ending := ""
		if strings.HasSuffix(lines[line], "\r") {
			ending = "\r"
		}
		lines[line] = *edit.Replacement + ending
	}
	revised := strings.Join(lines, "\n")
	if len(revised) > maxReviewSourceBytes || strings.TrimSpace(revised) == "" {
		return "", errAutomaticRevision
	}
	return revised, nil
}

func automaticReviewResult(review automaticReview, reason string) taskResult {
	stop := map[string]string{automaticReviewTargetMet: "达到目标分数", automaticReviewMaxRounds: "达到轮次上限", automaticReviewNoProgress: "本轮没有可继续的修改"}
	var report strings.Builder
	fmt.Fprintf(&report, "# 多轮审稿报告\n\n- 投稿目标：%s\n- 原稿 SHA-256：`%s`\n- 目标分数：%d / 10\n- 最多轮次：%d\n- 结束原因：%s\n",
		review.Target, review.SourceSHA256, review.TargetScore, review.MaxRounds, stop[reason])
	for _, round := range review.Rounds {
		fmt.Fprintf(&report, "\n## 第 %d 轮 · %g / 10\n\n- 稿件：`%s`\n- 版本：`%s`\n- SHA-256：`%s`\n\n%s\n",
			round.Number, round.Score, round.Path, round.VersionID, round.SHA256, round.Summary)
		for _, finding := range round.Findings {
			fmt.Fprintf(&report, "\n### %d. 第 %d 行 · %s\n\n> %s\n\n%s\n\n修改建议：%s\n",
				finding.Number, finding.Line, reviewSeverities[finding.Severity], finding.Quote, finding.Finding, finding.Suggestion)
		}
	}
	return taskResult{markdown: report.String(), reviewStopReason: reason}
}
