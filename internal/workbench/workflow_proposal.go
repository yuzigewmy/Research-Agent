package workbench

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxProposalRequirementsBytes = 4 << 10

var errProposalOptions = errors.New("invalid proposal options")

type proposalTargets struct {
	Degree           string `json:"degree"`
	DegreeType       string `json:"degreeType"`
	TargetCharacters int    `json:"targetCharacters"`
	TargetSources    int    `json:"targetSources"`
}

type proposalOptions struct {
	proposalTargets
	Discipline    string `json:"discipline,omitempty"`
	Method        string `json:"method,omitempty"`
	ProjectSource string `json:"projectSource,omitempty"`
	ProposalMonth string `json:"proposalMonth,omitempty"`
	DefenseMonth  string `json:"defenseMonth,omitempty"`
	Requirements  string `json:"requirements,omitempty"`
}

func defaultProposalTargets() proposalTargets {
	return proposalTargets{Degree: "master", DegreeType: "academic", TargetCharacters: 5000, TargetSources: 30}
}

func normalizeProposalOptions(input *proposalOptions) (proposalOptions, error) {
	var options proposalOptions
	if input != nil {
		options = *input
	}
	defaults := defaultProposalTargets()
	if options.Degree == "" {
		options.Degree = defaults.Degree
	}
	if options.DegreeType == "" {
		options.DegreeType = defaults.DegreeType
	}
	var characters, sources int
	switch options.Degree {
	case "bachelor":
		characters, sources = 3000, 15
	case "master":
		characters, sources = defaults.TargetCharacters, defaults.TargetSources
	case "doctor":
		characters, sources = 15000, 50
	default:
		return proposalOptions{}, errProposalOptions
	}
	if options.TargetCharacters == 0 {
		options.TargetCharacters = characters
	}
	if options.TargetSources == 0 {
		options.TargetSources = sources
	}
	if (options.DegreeType != "academic" && options.DegreeType != "professional") ||
		options.TargetCharacters < 500 || options.TargetCharacters > 30000 ||
		options.TargetSources < 1 || options.TargetSources > 80 {
		return proposalOptions{}, errProposalOptions
	}
	for _, field := range []*string{&options.Discipline, &options.Method, &options.ProjectSource} {
		*field = strings.TrimSpace(*field)
		if utf8.RuneCountInString(*field) > 200 || strings.ContainsFunc(*field, unicode.IsControl) {
			return proposalOptions{}, errProposalOptions
		}
	}
	for _, month := range []*string{&options.ProposalMonth, &options.DefenseMonth} {
		*month = strings.TrimSpace(*month)
		if *month != "" {
			if _, err := time.Parse("2006-01", *month); err != nil || len(*month) != len("2006-01") {
				return proposalOptions{}, errProposalOptions
			}
		}
	}
	if options.ProposalMonth != "" && options.DefenseMonth != "" && options.DefenseMonth < options.ProposalMonth {
		return proposalOptions{}, errProposalOptions
	}
	options.Requirements = strings.TrimSpace(options.Requirements)
	if len(options.Requirements) > maxProposalRequirementsBytes || strings.ContainsRune(options.Requirements, 0) {
		return proposalOptions{}, errProposalOptions
	}
	return options, nil
}

func (s *Store) checkProposal(ctx context.Context, projectID, runID string, goals proposalTargets, sources []reviewSource) (taskResult, error) {
	_, manuscript, err := s.workflowArtifact(ctx, projectID, runID, "manuscript")
	if err != nil {
		return taskResult{}, err
	}
	_, defense, err := s.workflowArtifact(ctx, projectID, runID, "defense")
	if err != nil {
		return taskResult{}, err
	}
	return taskResult{markdown: proposalCheck(manuscript, defense, goals, sources)}, nil
}

var proposalQuestion = regexp.MustCompile(`^\s*(?:#{2,6}\s*|[-*+]\s*|[0-9]+[.)、]\s*)?(?:问题\s*[0-9]+[:：.、 ]+|[Qq][0-9]+[:：.、 ]+|第[0-9]+问[:：.、 ]+)?.+[？?]\s*$`)

func proposalCheck(manuscript, defense string, goals proposalTargets, sources []reviewSource) string {
	sections := []struct {
		label string
		terms []string
	}{
		{"选题背景与意义", []string{"背景", "意义", "研究缘起"}},
		{"研究现状", []string{"现状", "文献综述", "研究进展"}},
		{"目标与研究内容", []string{"目标", "研究内容", "研究问题"}},
		{"研究方法与技术路线", []string{"方法", "技术路线", "研究设计"}},
		{"可行性与创新", []string{"可行性", "创新"}},
		{"进度与预期成果", []string{"进度", "计划安排", "预期成果"}},
		{"参考文献", []string{"参考文献", "引用文献"}},
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# 开题交付核对\n\n稿件 SHA-256 `%x`\n\n答辩材料 SHA-256 `%x`\n\n",
		sha256.Sum256([]byte(manuscript)), sha256.Sum256([]byte(defense)))
	characters := 0
	citations := make(map[string]bool)
	available := make(map[string]bool, len(sources))
	for _, source := range sources {
		available[source.Key] = true
	}
	var headings []struct {
		line int
		text string
	}
	inReferences := false
	for line, text := range proposalLines(manuscript) {
		if match := paperHeading.FindStringSubmatch(text); match != nil {
			if !strings.HasPrefix(text, "# ") {
				headings = append(headings, struct {
					line int
					text string
				}{line, match[1]})
			}
			if strings.HasPrefix(text, "## ") {
				inReferences = strings.Contains(match[1], "参考文献") || strings.Contains(match[1], "引用文献")
			}
			continue
		}
		if inReferences {
			continue
		}
		for _, match := range reviewCitation.FindAllStringSubmatch(text, -1) {
			if available[match[1]] {
				citations[match[1]] = true
			}
		}
		for _, r := range reviewCitation.ReplaceAllString(text, "") {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				characters++
			}
		}
	}
	questions := 0
	for _, text := range proposalLines(defense) {
		if proposalQuestion.MatchString(text) {
			questions++
		}
	}
	fmt.Fprintf(&report, "- 正文字数（字母、汉字与数字）%d / %d%s\n", characters, goals.TargetCharacters, proposalShortfall(characters, goals.TargetCharacters))
	fmt.Fprintf(&report, "- 已引用的本轮文献 %d / %d（本轮可用 %d 条）%s\n", len(citations), goals.TargetSources, len(sources), proposalShortfall(len(citations), goals.TargetSources))
	fmt.Fprintf(&report, "- 可识别答辩问题 %d / 8%s\n\n## 栏目核对\n\n", questions, proposalShortfall(questions, 8))
	for _, section := range sections {
		line := 0
		for _, heading := range headings {
			for _, term := range section.terms {
				if strings.Contains(heading.text, term) {
					line = heading.line
					break
				}
			}
			if line > 0 {
				break
			}
		}
		if line > 0 {
			fmt.Fprintf(&report, "- %s，已识别到第 %d 行的标题。\n", section.label, line)
		} else {
			fmt.Fprintf(&report, "- %s，待核对对应标题。\n", section.label)
		}
	}
	report.WriteString("\n本报告按保存版本核对字数、标题、文献键和问题数量。论点、引文适用性和校方格式需结合原文核对。\n")
	return report.String()
}

func proposalShortfall(actual, target int) string {
	if actual < target {
		return fmt.Sprintf("，还差 %d", target-actual)
	}
	return ""
}

func proposalLines(source string) iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		var fence string
		line := 0
		for raw := range strings.SplitSeq(source, "\n") {
			line++
			text := strings.TrimSpace(raw)
			if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
				marker := text[:3]
				if fence == "" {
					fence = marker
				} else if marker == fence && strings.Trim(text, marker[:1]) == "" {
					fence = ""
				}
				continue
			}
			if fence == "" && text != "" && !yield(line, text) {
				return
			}
		}
	}
}
