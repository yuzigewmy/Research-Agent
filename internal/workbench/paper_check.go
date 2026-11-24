package workbench

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

var paperHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

// paperCheck reports structural signals for the supplied Markdown version.
func paperCheck(source string) string {
	sections := []struct {
		name    string
		aliases []string
		missing string
		line    int
	}{
		{name: "摘要", aliases: []string{"摘要", "abstract"}},
		{name: "引言", aliases: []string{"引言", "绪论", "introduction"}},
		{name: "方法或证据", aliases: []string{"方法", "证据", "methods", "methodology", "evidence"}},
		{name: "结论", aliases: []string{"结论", "结语", "conclusion", "conclusions"}},
		{name: "局限", aliases: []string{"局限", "不足", "limitations"}},
		// 只确认标题存在，不判断披露正文。英文别名使用小写，以便和下面的标题归一化比较。
		{name: "AI 使用披露", aliases: []string{
			"ai 使用披露", "ai使用披露", "ai 使用声明", "ai使用声明",
			"人工智能使用声明", "人工智能使用披露",
			"ai use disclosure", "ai usage disclosure", "artificial intelligence use disclosure",
		}, missing: "未识别到对应标题。请按投稿要求核对。"},
	}
	var placeholders []int
	var fence string
	for index, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker && strings.Trim(line, marker[:1]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if match := paperHeading.FindStringSubmatch(line); match != nil && !strings.HasPrefix(line, "# ") {
			title := strings.ToLower(match[1])
			for i := range sections {
				for _, alias := range sections[i].aliases {
					if sections[i].line == 0 && strings.Contains(title, alias) {
						sections[i].line = index + 1
					}
				}
			}
		}
		if len(placeholders) < 30 && (strings.Contains(line, "待补") || strings.Contains(line, "TODO")) {
			placeholders = append(placeholders, index+1)
		}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# 稿件结构检查\n\n稿件 SHA-256 `%x`\n\n", sha256.Sum256([]byte(source)))
	for _, section := range sections {
		if section.line > 0 {
			fmt.Fprintf(&report, "- %s：已识别，第 %d 行。\n", section.name, section.line)
		} else if section.missing != "" {
			fmt.Fprintf(&report, "- %s：%s\n", section.name, section.missing)
		} else {
			fmt.Fprintf(&report, "- %s：请核对，未识别到对应标题。\n", section.name)
		}
	}
	for _, line := range placeholders {
		fmt.Fprintf(&report, "- 第 %d 行有待补标记。\n", line)
	}
	return report.String()
}
