package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const defaultReviewTarget = 20

type reviewSource struct {
	Key       string `json:"key"`
	DOI       string `json:"doi"`
	Title     string `json:"title,omitempty"`
	Journal   string `json:"journal,omitempty"`
	Year      int    `json:"year,omitempty"`
	Language  string `json:"language,omitempty"`
	Excerpt   string `json:"abstractExcerpt,omitempty"`
	CheckedAt string `json:"metadataCheckedAt"`
}

func reviewTarget(key, action string, requested int) (int, error) {
	if key != reviewWorkflow || action != workflowStart {
		return 0, nil
	}
	if requested == 0 {
		return defaultReviewTarget, nil
	}
	if requested < 10 || requested > 80 {
		return 0, errReviewTarget
	}
	return requested, nil
}

func freezeReviewSources(ctx context.Context, tx *sql.Tx, projectID string, limit int) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+citationColumns+` FROM literature_citations
		WHERE project_id = ? ORDER BY ordinal DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	sources := make([]reviewSource, 0, limit)
	for rows.Next() {
		citation, err := scanCitation(rows)
		if err != nil {
			return "", err
		}
		sources = append(sources, reviewSource{
			Key: citationKey(citation.DOI), DOI: citation.DOI, Title: excerptRunes(citation.Title, 240),
			Journal: excerptRunes(citation.Journal, 120), Year: citation.Year, Language: citation.Language,
			Excerpt: excerptRunes(citation.Abstract, 360), CheckedAt: citation.VerifiedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(sources) == 0 {
		return "", errReviewSources
	}
	encoded, err := json.Marshal(sources)
	return string(encoded), err
}

func excerptRunes(value string, limit int) string {
	for offset := range value {
		if limit == 0 {
			return value[:offset] + "…"
		}
		limit--
	}
	return value
}

var tableText = strings.NewReplacer("\\", "\\\\", "|", "\\|", "\n", " ", "\r", " ")

func reviewSourcesMarkdown(sources []reviewSource) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 本轮文献来源\n\n共纳入 %d 条已核对登记元数据的来源。摘要仅保留节选。\n\n", len(sources))
	out.WriteString("| 文献键 | 题名 | DOI |\n| --- | --- | --- |\n")
	for _, source := range sources {
		fmt.Fprintf(&out, "| %s | %s | %s |\n", source.Key, tableText.Replace(source.Title), tableText.Replace(source.DOI))
	}
	return out.String()
}

var reviewCitation = regexp.MustCompile(`\[@([^\]\s]+)\]`)

func checkReviewCitations(markdown string, sources []reviewSource) error {
	allowed := make(map[string]bool, len(sources))
	for _, source := range sources {
		allowed[source.Key] = true
	}
	matches := reviewCitation.FindAllStringSubmatch(markdown, -1)
	if len(matches) == 0 {
		return errors.New("综述结果缺少文献引用。")
	}
	for _, match := range matches {
		if !allowed[match[1]] {
			return errors.New("综述结果引用了本轮来源清单之外的文献键。")
		}
	}
	return nil
}
