package workbench

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

var paperHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

// 图与 Figure 同类，表与 Table 另一类。中文标识后的空格可选，英文标识后至少一个空格。
// 冒号或中文冒号可紧接标题；句点必须先有空格或制表符。标题去掉空白后必须非空。前导零不改变编号。
var paperCaption = regexp.MustCompile(`^(?:(图|表)[ \t]*|(Figure|Table)[ \t]+)([0-9]+)(?:[：:][ \t]*|\.[ \t]+)(.+)$`)

// paperCaptionKey 按类别和数值编号区分题注。1 到 99999，前导零视为同一编号。
type paperCaptionKey struct {
	kind string
	num  int
}

// paperCaptionDup 是一条重复编号提醒：初次出现的源行，以及再次出现的源行。
type paperCaptionDup struct {
	kind  string
	num   int
	first int
	again int
}

// fenceLine 识别行首至少三个连续的反引号或波浪号。
// length 是这串标记的数量，rest 是其后的文本。
func fenceLine(line string) (marker byte, length int, rest string, ok bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, "", false
	}
	marker = line[0]
	for length < len(line) && line[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	return marker, length, line[length:], true
}

// paperCaptionValue 把题注编号换成数值。去掉前导零后须是 1 到 5 位十进制数字。
func paperCaptionValue(digits string) (int, bool) {
	digits = strings.TrimLeft(digits, "0")
	if len(digits) < 1 || len(digits) > 5 {
		return 0, false
	}
	n := 0
	for _, c := range digits {
		n = n*10 + int(c-'0')
	}
	return n, true
}

// paperCaptionOf 识别单独成行的图表题注。整行用 ** 或 __ 加粗时先去掉这一层标记。
// 调用方已跳过代码围栏；以 > 开头的引用块不在这里识别。
func paperCaptionOf(line string) (paperCaptionKey, bool) {
	if len(line) >= 4 {
		switch {
		case strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**"):
			line = strings.TrimSpace(line[2 : len(line)-2])
		case strings.HasPrefix(line, "__") && strings.HasSuffix(line, "__"):
			line = strings.TrimSpace(line[2 : len(line)-2])
		}
	}
	match := paperCaption.FindStringSubmatch(line)
	if match == nil || strings.TrimSpace(match[4]) == "" {
		return paperCaptionKey{}, false
	}
	num, ok := paperCaptionValue(match[3])
	if !ok {
		return paperCaptionKey{}, false
	}
	switch {
	case match[1] != "":
		return paperCaptionKey{kind: match[1], num: num}, true
	case match[2] == "Figure":
		return paperCaptionKey{kind: "图", num: num}, true
	case match[2] == "Table":
		return paperCaptionKey{kind: "表", num: num}, true
	default:
		return paperCaptionKey{}, false
	}
}

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
	seenCaption := map[paperCaptionKey]int{}
	var duplicateCaptions []paperCaptionDup
	var fenceMarker byte
	var fenceLen int
	for index, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if marker, length, rest, ok := fenceLine(line); ok {
			// 关闭围栏须为同种标记，数量不短于开头，且标记后只有空白。
			if fenceLen == 0 {
				fenceMarker = marker
				fenceLen = length
			} else if marker == fenceMarker && length >= fenceLen && strings.TrimSpace(rest) == "" {
				fenceMarker = 0
				fenceLen = 0
			}
			continue
		}
		if fenceLen != 0 {
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
		// 引用块不是题注。围栏内容已在上面跳过，普通正文则对不上单独成行的题注。
		if strings.HasPrefix(line, ">") {
			continue
		}
		key, captionOK := paperCaptionOf(line)
		if !captionOK {
			continue
		}
		if first, exists := seenCaption[key]; exists {
			if len(duplicateCaptions) < 20 {
				duplicateCaptions = append(duplicateCaptions, paperCaptionDup{
					kind:  key.kind,
					num:   key.num,
					first: first,
					again: index + 1,
				})
			}
			continue
		}
		seenCaption[key] = index + 1
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
	for _, dup := range duplicateCaptions {
		fmt.Fprintf(&report, "- 重复图表编号：%s %d，初次第 %d 行，重复第 %d 行。\n", dup.kind, dup.num, dup.first, dup.again)
	}
	return report.String()
}
