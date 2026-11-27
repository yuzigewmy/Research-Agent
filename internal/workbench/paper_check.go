package workbench

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var paperHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

// 图与 Figure 同类，表与 Table 另一类。中文标识后的空格可选，英文标识后至少一个空格。
// 冒号或中文冒号可紧接标题；句点必须先有空格或制表符。标题去掉空白后必须非空。前导零不改变编号。
var paperCaption = regexp.MustCompile(`^(?:(图|表)[ \t]*|(Figure|Table)[ \t]+)([0-9]+)(?:[：:][ \t]*|\.[ \t]+)(.+)$`)

// 引用键匹配：[@key]、[@k1; @k2]、[cite:key]、\cite{k1,k2}、\citep{...}、\citet{...}
var (
	citeBracketAtPattern = regexp.MustCompile(`\[(@[A-Za-z0-9._:/-]+(?:\s*;\s*@[A-Za-z0-9._:/-]+)*)\]`)
	citeColonPattern     = regexp.MustCompile(`\[cite:([A-Za-z0-9._:/-]+(?:\s*,\s*[A-Za-z0-9._:/-]+)*)\]`)
	citeLatexPattern     = regexp.MustCompile(`\\cite[pt]?\{([A-Za-z0-9._:/-]+(?:\s*,\s*[A-Za-z0-9._:/-]+)*)\}`)
	englishWordPattern   = regexp.MustCompile(`[A-Za-z0-9]+(?:[-'][A-Za-z0-9]+)*`)
)

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

type paperCheckSection struct {
	Name     string `json:"name"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Line     int    `json:"line,omitempty"`
}

type paperCheckDiagnostic struct {
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

type paperCheckCitations struct {
	Matched []string `json:"matched"`
	Missing []string `json:"missing"`
}

type paperCheckStats struct {
	ChineseChars int `json:"chineseChars"`
	EnglishWords int `json:"englishWords"`
	TotalLines   int `json:"totalLines"`
}

type paperCheckReport struct {
	SourceVersionID string                 `json:"sourceVersionId,omitempty"`
	SourcePath      string                 `json:"sourcePath,omitempty"`
	SourceSHA256    string                 `json:"sourceSha256"`
	Stage           string                 `json:"stage"`
	Summary         string                 `json:"summary"`
	Markdown        string                 `json:"markdown"`
	Stats           paperCheckStats        `json:"stats"`
	Sections        []paperCheckSection    `json:"sections"`
	Citations       paperCheckCitations    `json:"citations"`
	Diagnostics     []paperCheckDiagnostic `json:"diagnostics"`
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

func extractCitationKeysFromLine(line string) []string {
	var keys []string
	for _, m := range citeBracketAtPattern.FindAllStringSubmatch(line, -1) {
		for _, part := range strings.Split(m[1], ";") {
			key := strings.TrimPrefix(strings.TrimSpace(part), "@")
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	for _, m := range citeColonPattern.FindAllStringSubmatch(line, -1) {
		for _, part := range strings.Split(m[1], ",") {
			key := strings.TrimSpace(part)
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	for _, m := range citeLatexPattern.FindAllStringSubmatch(line, -1) {
		for _, part := range strings.Split(m[1], ",") {
			key := strings.TrimSpace(part)
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func normalizeCheckStage(stage string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "", "final":
		return "final", true
	case "plan":
		return "plan", true
	case "draft":
		return "draft", true
	default:
		return "", false
	}
}

func checkStageLabel(stage string) string {
	switch stage {
	case "plan":
		return "规划阶段"
	case "draft":
		return "初稿阶段"
	default:
		return "终稿阶段"
	}
}

// paperCheckDetailed runs a deterministic stage-aware manuscript check with citation verification and line diagnostics.
func paperCheckDetailed(source string, stage string, validCiteKeys map[string]bool) paperCheckReport {
	normalizedStage, ok := normalizeCheckStage(stage)
	if !ok {
		normalizedStage = "final"
	}

	sectionDefs := []struct {
		name      string
		aliases   []string
		missing   string
		reqPlan   bool
		reqDraft  bool
		reqFinal  bool
		line      int
	}{
		{name: "摘要", aliases: []string{"摘要", "abstract"}, reqPlan: true, reqDraft: true, reqFinal: true},
		{name: "引言", aliases: []string{"引言", "绪论", "introduction"}, reqPlan: true, reqDraft: true, reqFinal: true},
		{name: "方法或证据", aliases: []string{"方法", "证据", "methods", "methodology", "evidence"}, reqPlan: true, reqDraft: true, reqFinal: true},
		{name: "结论", aliases: []string{"结论", "结语", "conclusion", "conclusions"}, reqPlan: false, reqDraft: true, reqFinal: true},
		{name: "局限", aliases: []string{"局限", "不足", "limitations"}, reqPlan: false, reqDraft: false, reqFinal: true},
		{name: "AI 使用披露", aliases: []string{
			"ai 使用披露", "ai使用披露", "ai 使用声明", "ai使用声明",
			"人工智能使用声明", "人工智能使用披露",
			"ai use disclosure", "ai usage disclosure", "artificial intelligence use disclosure",
		}, missing: "未识别到对应标题。请按投稿要求核对。", reqPlan: false, reqDraft: false, reqFinal: true},
	}

	var placeholders []int
	seenCaption := map[paperCaptionKey]int{}
	var duplicateCaptions []paperCaptionDup
	matchedCiteSet := map[string]bool{}
	missingCiteSet := map[string]bool{}
	type citeMiss struct {
		line int
		key  string
	}
	var missingCiteHits []citeMiss

	var fenceMarker byte
	var fenceLen int
	var chineseChars, englishWords int
	lines := strings.Split(source, "\n")

	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if marker, length, rest, isFence := fenceLine(line); isFence {
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

		for _, r := range raw {
			if unicode.Is(unicode.Han, r) {
				chineseChars++
			}
		}
		englishWords += len(englishWordPattern.FindAllString(raw, -1))

		if match := paperHeading.FindStringSubmatch(line); match != nil && !strings.HasPrefix(line, "# ") {
			title := strings.ToLower(match[1])
			for i := range sectionDefs {
				for _, alias := range sectionDefs[i].aliases {
					if sectionDefs[i].line == 0 && strings.Contains(title, alias) {
						sectionDefs[i].line = index + 1
					}
				}
			}
		}
		if len(placeholders) < 30 && (strings.Contains(line, "待补") || strings.Contains(line, "TODO")) {
			placeholders = append(placeholders, index+1)
		}

		for _, key := range extractCitationKeysFromLine(line) {
			if validCiteKeys[key] || validCiteKeys[strings.ToLower(key)] {
				matchedCiteSet[key] = true
			} else {
				missingCiteSet[key] = true
				if len(missingCiteHits) < 40 {
					missingCiteHits = append(missingCiteHits, citeMiss{line: index + 1, key: key})
				}
			}
		}

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

	sectionRequired := func(def struct {
		name     string
		aliases  []string
		missing  string
		reqPlan  bool
		reqDraft bool
		reqFinal bool
		line     int
	}) bool {
		switch normalizedStage {
		case "plan":
			return def.reqPlan
		case "draft":
			return def.reqDraft
		default:
			return def.reqFinal
		}
	}

	issueSeverity := "error"
	switch normalizedStage {
	case "plan":
		issueSeverity = "info"
	case "draft":
		issueSeverity = "warning"
	}

	sections := make([]paperCheckSection, 0, len(sectionDefs))
	diagnostics := make([]paperCheckDiagnostic, 0)
	for _, def := range sectionDefs {
		req := sectionRequired(def)
		sections = append(sections, paperCheckSection{
			Name:     def.name,
			Found:    def.line > 0,
			Required: req,
			Line:     def.line,
		})
		if def.line == 0 && req {
			msg := fmt.Sprintf("缺少「%s」章节标题。", def.name)
			if def.missing != "" {
				msg = fmt.Sprintf("%s：%s", def.name, def.missing)
			}
			sev := issueSeverity
			if normalizedStage == "plan" {
				sev = "warning"
			}
			diagnostics = append(diagnostics, paperCheckDiagnostic{
				Line:     1,
				Severity: sev,
				Rule:     "section_missing",
				Message:  msg,
			})
		}
	}

	for _, lineNum := range placeholders {
		diagnostics = append(diagnostics, paperCheckDiagnostic{
			Line:     lineNum,
			Severity: issueSeverity,
			Rule:     "placeholder",
			Message:  fmt.Sprintf("第 %d 行存在待补标记（待补 / TODO）。", lineNum),
		})
	}
	for _, dup := range duplicateCaptions {
		diagnostics = append(diagnostics, paperCheckDiagnostic{
			Line:     dup.again,
			Severity: issueSeverity,
			Rule:     "duplicate_caption",
			Message:  fmt.Sprintf("重复图表编号：%s %d（初次第 %d 行，重复第 %d 行）。", dup.kind, dup.num, dup.first, dup.again),
		})
	}
	for _, miss := range missingCiteHits {
		diagnostics = append(diagnostics, paperCheckDiagnostic{
			Line:     miss.line,
			Severity: issueSeverity,
			Rule:     "citation_missing",
			Message:  fmt.Sprintf("第 %d 行引用键「%s」未在项目文献库登记。", miss.line, miss.key),
		})
	}
	if normalizedStage == "final" && chineseChars+englishWords < 100 {
		diagnostics = append(diagnostics, paperCheckDiagnostic{
			Line:     1,
			Severity: "warning",
			Rule:     "length_short",
			Message:  fmt.Sprintf("当前正文篇幅较短（中文 %d 字、英文 %d 词），请确认终稿内容完整。", chineseChars, englishWords),
		})
	}

	matched := make([]string, 0, len(matchedCiteSet))
	for k := range matchedCiteSet {
		matched = append(matched, k)
	}
	slices.Sort(matched)
	missing := make([]string, 0, len(missingCiteSet))
	for k := range missingCiteSet {
		missing = append(missing, k)
	}
	slices.Sort(missing)

	var errCount, warnCount, infoCount int
	for _, d := range diagnostics {
		switch d.Severity {
		case "error":
			errCount++
		case "warning":
			warnCount++
		default:
			infoCount++
		}
	}

	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	summary := fmt.Sprintf("%s · 错误 %d / 警告 %d / 提示 %d · 中文 %d 字 · 英文 %d 词 · 引用已匹配 %d / 未登记 %d",
		checkStageLabel(normalizedStage), errCount, warnCount, infoCount, chineseChars, englishWords, len(matched), len(missing))

	var md strings.Builder
	fmt.Fprintf(&md, "# 稿件结构检查（%s）\n\n稿件 SHA-256 `%s`\n\n", checkStageLabel(normalizedStage), digest)
	fmt.Fprintf(&md, "- 篇幅统计：中文 %d 字，英文 %d 词，共 %d 行。\n", chineseChars, englishWords, len(lines))
	for _, section := range sections {
		if section.Found {
			fmt.Fprintf(&md, "- %s：已识别，第 %d 行。\n", section.Name, section.Line)
		} else if section.Required {
			fmt.Fprintf(&md, "- %s：请核对，未识别到对应标题。\n", section.Name)
		} else {
			fmt.Fprintf(&md, "- %s：当前阶段可选，未识别到对应标题。\n", section.Name)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(&md, "- 已匹配文献键：%s。\n", strings.Join(matched, "、"))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&md, "- 未登记引用键：%s。\n", strings.Join(missing, "、"))
	}
	for _, d := range diagnostics {
		fmt.Fprintf(&md, "- [%s] 第 %d 行（%s）：%s\n", d.Severity, d.Line, d.Rule, d.Message)
	}

	return paperCheckReport{
		SourceSHA256: digest,
		Stage:        normalizedStage,
		Summary:      summary,
		Markdown:     md.String(),
		Stats: paperCheckStats{
			ChineseChars: chineseChars,
			EnglishWords: englishWords,
			TotalLines:   len(lines),
		},
		Sections:    sections,
		Citations:   paperCheckCitations{Matched: matched, Missing: missing},
		Diagnostics: diagnostics,
	}
}

func (s *Store) registerPaperCheck(mux *http.ServeMux) {
	route := projectsPath + "/{id}/files/versions/{versionId}/check"
	mux.HandleFunc("POST "+route, s.handlePaperCheck)
	mux.HandleFunc(route, methodNotAllowed("POST"))
}

func (s *Store) handlePaperCheck(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Stage string `json:"stage"`
	}
	if r.ContentLength != 0 {
		if !readJSON(w, r, &input, 4<<10) {
			return
		}
	}
	stage, ok := normalizeCheckStage(input.Stage)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "检查阶段须为 plan、draft 或 final。")
		return
	}
	ctx, projectID, versionID := r.Context(), r.PathValue("id"), r.PathValue("versionId")
	file, err := s.workspaceVersion(ctx, projectID, versionID)
	if err != nil {
		writeWorkspaceError(w, "read check source version", err)
		return
	}
	switch strings.ToLower(path.Ext(file.Path)) {
	case ".md", ".markdown":
	default:
		writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "稿件检查仅支持已保存的 Markdown 文件。")
		return
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		writeStoreError(w, "read document for paper check", err)
		return
	}
	citations, err := s.listCitations(ctx, projectID, 0, -1)
	if err != nil {
		writeStoreError(w, "read citations for paper check", err)
		return
	}
	validCiteKeys := make(map[string]bool, len(citations)*3)
	for _, c := range citations {
		validCiteKeys[citationKey(c.DOI)] = true
		validCiteKeys[c.DOI] = true
		validCiteKeys[strings.ToLower(c.DOI)] = true
	}
	report := paperCheckDetailed(doc.Content, stage, validCiteKeys)
	report.SourceVersionID = file.VersionID
	report.SourcePath = file.Path
	writeJSON(w, http.StatusOK, report)
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
