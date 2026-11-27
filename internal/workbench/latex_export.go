package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	latexPresetZhArticle       = "zh_article"
	latexPresetEnConference    = "en_conference"
	latexPresetZhThesis        = "zh_thesis"
	latexPresetModelingContest = "modeling_contest"
)

type latexDiagnostic struct {
	Line       int    `json:"line"`
	SourceLine int    `json:"sourceLine,omitempty"`
	Message    string `json:"message"`
}

type latexExportRequest struct {
	Preset     string `json:"preset"`
	CompilePDF bool   `json:"compilePdf"`
}

type latexExportResponse struct {
	VersionID    string            `json:"versionId"`
	SourcePath   string            `json:"sourcePath"`
	SourceSHA256 string            `json:"sourceSha256"`
	Preset       string            `json:"preset"`
	TexFilename  string            `json:"texFilename"`
	TexContent   string            `json:"texContent"`
	BibContent   string            `json:"bibContent"`
	TexSHA256    string            `json:"texSha256"`
	Compiled     bool              `json:"compiled"`
	PdfFilename  string            `json:"pdfFilename,omitempty"`
	PdfSHA256    string            `json:"pdfSha256,omitempty"`
	PdfSize      int               `json:"pdfSize,omitempty"`
	PdfBase64    string            `json:"pdfBase64,omitempty"`
	CompileError string            `json:"compileError,omitempty"`
	Diagnostics  []latexDiagnostic `json:"diagnostics,omitempty"`
}

var (
	latexTableSepPattern    = regexp.MustCompile(`^\|?(?:\s*:?-{2,}:?\s*\|)+\s*:?-{2,}:?\s*\|?$`)
	latexOrderedListPattern = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	latexImagePattern       = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)]+)\)$`)
	latexErrLinePattern     = regexp.MustCompile(`^l\.(\d+)\b(.*)$`)
	latexFileLineErrPattern = regexp.MustCompile(`^[^:\r\n]+\.tex:(\d+):\s*(.+)$`)
	latexWarnLinePattern    = regexp.MustCompile(`(?:LaTeX|Package\s+\S+)\s+Warning:\s*(.+?)\s+on input line (\d+)\.?`)
	latexTextEscapes        = strings.NewReplacer(
		`\`, `\textbackslash{}`,
		`{`, `\{`,
		`}`, `\}`,
		`%`, `\%`,
		`&`, `\&`,
		`#`, `\#`,
		`_`, `\_`,
		`^`, `\textasciicircum{}`,
		`~`, `\textasciitilde{}`,
	)
)

func normalizeLatexPreset(preset string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "", latexPresetZhArticle:
		return latexPresetZhArticle, true
	case latexPresetEnConference:
		return latexPresetEnConference, true
	case latexPresetZhThesis:
		return latexPresetZhThesis, true
	case latexPresetModelingContest:
		return latexPresetModelingContest, true
	default:
		return "", false
	}
}

func texFilenameFor(workspacePath string) string {
	base := path.Base(workspacePath)
	return strings.TrimSuffix(base, path.Ext(base)) + ".tex"
}

func pdfFilenameFor(workspacePath string) string {
	base := path.Base(workspacePath)
	return strings.TrimSuffix(base, path.Ext(base)) + ".pdf"
}

func (s *Store) registerLatex(mux *http.ServeMux, latexBin string) {
	route := projectsPath + "/{id}/files/versions/{versionId}/latex"
	mux.HandleFunc("POST "+route, s.handleLatexExport(latexBin, false))
	mux.HandleFunc(route, methodNotAllowed("POST"))
	zipRoute := route + "/zip"
	mux.HandleFunc("POST "+zipRoute, s.handleLatexExport(latexBin, true))
	mux.HandleFunc(zipRoute, methodNotAllowed("POST"))
}

func (s *Store) handleLatexExport(latexBin string, asZip bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input latexExportRequest
		if r.ContentLength != 0 {
			if !readJSON(w, r, &input, 4<<10) {
				return
			}
		}
		preset, ok := normalizeLatexPreset(input.Preset)
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "版式预设须为 zh_article、en_conference、zh_thesis 或 modeling_contest。")
			return
		}
		ctx, projectID, versionID := r.Context(), r.PathValue("id"), r.PathValue("versionId")
		file, err := s.workspaceVersion(ctx, projectID, versionID)
		if err != nil {
			writeWorkspaceError(w, "read LaTeX export source", err)
			return
		}
		switch strings.ToLower(path.Ext(file.Path)) {
		case ".md", ".markdown":
		default:
			writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "LaTeX 导出仅支持已保存的 Markdown 稿件。")
			return
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			writeStoreError(w, "read Markdown for LaTeX export", err)
			return
		}
		if strings.TrimSpace(doc.Content) == "" {
			writeError(w, http.StatusUnprocessableEntity, "EMPTY_DOCUMENT", "请先保存稿件正文。")
			return
		}
		citations, err := s.listCitations(ctx, projectID, 0, -1)
		if err != nil {
			writeStoreError(w, "read citations for LaTeX export", err)
			return
		}
		bibContent := bibliography(citations)
		texContent, lineMap := renderMarkdownToLatex(doc, preset, bibContent)
		texName := texFilenameFor(file.Path)
		texDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(texContent)))

		if asZip {
			zipBytes, err := buildLatexZip(texName, texContent, bibContent)
			if err != nil {
				writeStoreError(w, "build LaTeX ZIP", err)
				return
			}
			zipName := strings.TrimSuffix(texName, ".tex") + "-latex.zip"
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": zipName}))
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Source-SHA256", file.SHA256)
			w.Header().Set("X-Tex-SHA256", texDigest)
			w.Header().Set("Content-Length", strconv.Itoa(len(zipBytes)))
			_, _ = w.Write(zipBytes)
			return
		}

		resp := latexExportResponse{
			VersionID:    file.VersionID,
			SourcePath:   file.Path,
			SourceSHA256: file.SHA256,
			Preset:       preset,
			TexFilename:  texName,
			TexContent:   texContent,
			BibContent:   bibContent,
			TexSHA256:    texDigest,
		}

		if !input.CompilePDF {
			writeJSON(w, http.StatusOK, resp)
			return
		}

		if latexBin == "" {
			writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING",
				"未配置 RESEARCH_LATEX_BIN，无法执行本地 PDF 编译。当前仍可预览、保存或下载 .tex 源码。")
			return
		}

		compileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		pdfBytes, diags, compileErr := compileLatexSource(compileCtx, latexBin, texContent, bibContent)
		for i := range diags {
			if diags[i].Line >= 1 && diags[i].Line <= len(lineMap) && lineMap[diags[i].Line-1] > 0 {
				diags[i].SourceLine = lineMap[diags[i].Line-1]
			}
		}
		resp.Diagnostics = diags
		if compileErr != nil {
			resp.Compiled = false
			resp.CompileError = compileErr.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp.Compiled = true
		resp.PdfFilename = pdfFilenameFor(file.Path)
		resp.PdfSHA256 = fmt.Sprintf("%x", sha256.Sum256(pdfBytes))
		resp.PdfSize = len(pdfBytes)
		resp.PdfBase64 = base64.StdEncoding.EncodeToString(pdfBytes)
		writeJSON(w, http.StatusOK, resp)
	}
}

func buildLatexZip(texName, texContent, bibContent string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addFile := func(name, content string) error {
		header := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Unix(0, 0).UTC(),
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte(content))
		return err
	}
	if err := addFile(texName, texContent); err != nil {
		return nil, err
	}
	if strings.TrimSpace(bibContent) != "" {
		if err := addFile("references.bib", bibContent); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compileLatexSource(ctx context.Context, latexBin, texContent, bibContent string) ([]byte, []latexDiagnostic, error) {
	tmpDir, err := os.MkdirTemp("", "research-latex-*")
	if err != nil {
		return nil, nil, fmt.Errorf("无法创建临时编译目录。")
	}
	defer os.RemoveAll(tmpDir)

	texPath := filepath.Join(tmpDir, "main.tex")
	if err := os.WriteFile(texPath, []byte(texContent), 0o600); err != nil {
		return nil, nil, fmt.Errorf("无法写入临时 LaTeX 源码。")
	}
	if strings.TrimSpace(bibContent) != "" {
		if err := os.WriteFile(filepath.Join(tmpDir, "references.bib"), []byte(bibContent), 0o600); err != nil {
			return nil, nil, fmt.Errorf("无法写入临时参考文献库。")
		}
	}

	// 严格禁止 -shell-escape，强制传入 -no-shell-escape。
	cmd := exec.CommandContext(ctx, latexBin,
		"-interaction=nonstopmode",
		"-halt-on-error",
		"-no-shell-escape",
		"-output-directory", tmpDir,
		texPath,
	)
	cmd.Dir = tmpDir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()

	logText := combined.String()
	if fileLog, err := os.ReadFile(filepath.Join(tmpDir, "main.log")); err == nil && len(fileLog) > 0 {
		logText = logText + "\n" + string(fileLog)
	}
	diagnostics := parseLatexLog(logText)

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, diagnostics, fmt.Errorf("LaTeX 编译超时（超过 30 秒），已中止且未修改原稿。")
	}
	pdfPath := filepath.Join(tmpDir, "main.pdf")
	pdfBytes, readErr := os.ReadFile(pdfPath)
	if runErr != nil || readErr != nil || len(pdfBytes) == 0 {
		if len(diagnostics) == 0 {
			diagnostics = append(diagnostics, latexDiagnostic{
				Line:    1,
				Message: "编译器返回非零退出码，请检查 LaTeX 源码语法或本机宏包环境。",
			})
		}
		return nil, diagnostics, fmt.Errorf("LaTeX 编译失败，共解析到 %d 条诊断信息，已保存稿件未被覆盖。", len(diagnostics))
	}
	return pdfBytes, diagnostics, nil
}

// parseLatexLog extracts structured line diagnostics from TeX compiler logs.
func parseLatexLog(logText string) []latexDiagnostic {
	lines := strings.Split(strings.ReplaceAll(logText, "\r\n", "\n"), "\n")
	var result []latexDiagnostic
	seen := map[string]bool{}
	add := func(line int, msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" || line <= 0 || len(result) >= 50 {
			return
		}
		key := fmt.Sprintf("%d:%s", line, msg)
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, latexDiagnostic{Line: line, Message: msg})
	}

	pendingErr := ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "! ") {
			pendingErr = strings.TrimSpace(strings.TrimPrefix(line, "! "))
			continue
		}
		if pendingErr != "" {
			if m := latexErrLinePattern.FindStringSubmatch(line); m != nil {
				if num, err := strconv.Atoi(m[1]); err == nil {
					detail := strings.TrimSpace(m[2])
					if detail != "" {
						add(num, fmt.Sprintf("%s（%s）", pendingErr, detail))
					} else {
						add(num, pendingErr)
					}
					pendingErr = ""
					continue
				}
			}
		}
		if m := latexFileLineErrPattern.FindStringSubmatch(line); m != nil {
			if num, err := strconv.Atoi(m[1]); err == nil {
				add(num, m[2])
				continue
			}
		}
		if m := latexWarnLinePattern.FindStringSubmatch(line); m != nil {
			if num, err := strconv.Atoi(m[2]); err == nil {
				add(num, "警告："+m[1])
				continue
			}
		}
	}
	if pendingErr != "" && len(result) == 0 {
		add(1, pendingErr)
	}
	return result
}

func renderMarkdownToLatex(doc WorkspaceDocument, preset, bibContent string) (string, []int) {
	type outLine struct {
		text    string
		srcLine int
	}
	var out []outLine
	emit := func(srcLine int, text string) {
		for _, part := range strings.Split(text, "\n") {
			out = append(out, outLine{text: part, srcLine: srcLine})
		}
	}

	lines := strings.Split(strings.ReplaceAll(doc.Content, "\r\n", "\n"), "\n")
	title := strings.TrimSuffix(path.Base(doc.Path), path.Ext(doc.Path))
	titleLine := 1
	for idx, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			titleLine = idx + 1
			break
		}
	}

	emit(1, "% Research Workbench LaTeX Export")
	emit(1, fmt.Sprintf("%% Source-Path: %s", doc.Path))
	emit(1, fmt.Sprintf("%% Source-Version-ID: %s", doc.VersionID))
	emit(1, fmt.Sprintf("%% Source-SHA256: %s", doc.SHA256))
	emit(1, fmt.Sprintf("%% Preset: %s", preset))

	switch preset {
	case latexPresetEnConference:
		emit(1, `\documentclass[10pt,twocolumn,letterpaper]{article}`)
		emit(1, `\usepackage[margin=0.75in]{geometry}`)
		emit(1, `\usepackage{times}`)
	case latexPresetZhThesis:
		emit(1, `\documentclass[12pt,a4paper]{ctexrep}`)
		emit(1, `\usepackage[margin=2.5cm]{geometry}`)
		emit(1, `\usepackage{setspace}`)
		emit(1, `\onehalfspacing`)
	case latexPresetModelingContest:
		emit(1, `\documentclass[12pt,a4paper]{ctexart}`)
		emit(1, `\usepackage[margin=2.5cm]{geometry}`)
		emit(1, `\usepackage{float}`)
	default:
		emit(1, `\documentclass[11pt,a4paper]{ctexart}`)
		emit(1, `\usepackage[margin=2.5cm]{geometry}`)
	}
	emit(1, `\usepackage{amsmath,amssymb}`)
	emit(1, `\usepackage{booktabs}`)
	emit(1, `\usepackage{graphicx}`)
	emit(1, `\usepackage{listings}`)
	emit(1, `\usepackage{hyperref}`)
	emit(1, `\lstset{basicstyle=\ttfamily\small,breaklines=true,frame=single}`)
	emit(titleLine, fmt.Sprintf(`\title{%s}`, formatLatexInline(title)))
	emit(titleLine, `\author{}`)
	emit(titleLine, `\date{}`)
	emit(1, "")
	emit(1, `\begin{document}`)
	emit(titleLine, `\maketitle`)
	if preset == latexPresetZhThesis {
		emit(titleLine, `\tableofcontents`)
	}
	emit(1, "")

	inAbstract := false
	closeAbstract := func(srcLine int) {
		if inAbstract {
			emit(srcLine, `\end{abstract}`)
			emit(srcLine, "")
			inAbstract = false
		}
	}

	inList := ""
	closeList := func(srcLine int) {
		if inList != "" {
			emit(srcLine, fmt.Sprintf(`\end{%s}`, inList))
			inList = ""
		}
	}

	var fenceMarker byte
	var fenceLen int
	inDisplayMath := false
	titleSkipped := false

	for i := 0; i < len(lines); i++ {
		srcLine := i + 1
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)

		if marker, length, rest, ok := fenceLine(trimmed); ok {
			if fenceLen == 0 {
				closeList(srcLine)
				fenceMarker = marker
				fenceLen = length
				emit(srcLine, `\begin{lstlisting}`)
			} else if marker == fenceMarker && length >= fenceLen && strings.TrimSpace(rest) == "" {
				fenceMarker = 0
				fenceLen = 0
				emit(srcLine, `\end{lstlisting}`)
			} else {
				emit(srcLine, raw)
			}
			continue
		}
		if fenceLen != 0 {
			emit(srcLine, raw)
			continue
		}

		if inDisplayMath {
			if trimmed == "$$" {
				emit(srcLine, `\]`)
				inDisplayMath = false
			} else {
				emit(srcLine, raw)
			}
			continue
		}
		if trimmed == "$$" {
			closeList(srcLine)
			emit(srcLine, `\[`)
			inDisplayMath = true
			continue
		}
		if strings.HasPrefix(trimmed, "$$") && strings.HasSuffix(trimmed, "$$") && len(trimmed) > 4 {
			closeList(srcLine)
			expr := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
			emit(srcLine, `\[ `+expr+` \]`)
			continue
		}

		if trimmed == "" {
			closeList(srcLine)
			emit(srcLine, "")
			continue
		}

		if !titleSkipped && strings.HasPrefix(trimmed, "# ") {
			titleSkipped = true
			continue
		}

		// Markdown table -> booktabs three-line table
		if strings.Contains(trimmed, "|") && i+1 < len(lines) && latexTableSepPattern.MatchString(strings.TrimSpace(lines[i+1])) {
			closeList(srcLine)
			headers := splitMarkdownTableRow(trimmed)
			var bodyRows [][]string
			j := i + 2
			for j < len(lines) {
				rowLine := strings.TrimSpace(lines[j])
				if rowLine == "" || !strings.Contains(rowLine, "|") {
					break
				}
				bodyRows = append(bodyRows, splitMarkdownTableRow(rowLine))
				j++
			}
			colSpec := strings.Repeat("l", max(1, len(headers)))
			emit(srcLine, `\begin{table}[htbp]`)
			emit(srcLine, `\centering`)
			emit(srcLine, fmt.Sprintf(`\begin{tabular}{%s}`, colSpec))
			emit(srcLine, `\toprule`)
			emit(srcLine, formatLatexTableRow(headers)+` \\`)
			emit(srcLine, `\midrule`)
			for rIdx, row := range bodyRows {
				for len(row) < len(headers) {
					row = append(row, "")
				}
				emit(i+3+rIdx, formatLatexTableRow(row[:len(headers)])+` \\`)
			}
			emit(srcLine, `\bottomrule`)
			emit(srcLine, `\end{tabular}`)
			emit(srcLine, `\end{table}`)
			emit(srcLine, "")
			i = j - 1
			continue
		}

		if match := paperHeading.FindStringSubmatch(trimmed); match != nil {
			closeList(srcLine)
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			headingText := strings.TrimSpace(match[1])
			lowerHeading := strings.ToLower(headingText)
			if level == 2 && (lowerHeading == "摘要" || lowerHeading == "abstract") {
				closeAbstract(srcLine)
				emit(srcLine, `\begin{abstract}`)
				inAbstract = true
				continue
			}
			closeAbstract(srcLine)
			cmd := `\section`
			if preset == latexPresetZhThesis {
				switch level {
				case 2:
					cmd = `\chapter`
				case 3:
					cmd = `\section`
				case 4:
					cmd = `\subsection`
				default:
					cmd = `\subsubsection`
				}
			} else {
				switch level {
				case 2:
					cmd = `\section`
				case 3:
					cmd = `\subsection`
				default:
					cmd = `\subsubsection`
				}
			}
			emit(srcLine, fmt.Sprintf(`%s{%s}`, cmd, formatLatexInline(headingText)))
			continue
		}

		// Keywords line
		if strings.HasPrefix(trimmed, "关键词：") || strings.HasPrefix(trimmed, "关键词:") {
			closeList(srcLine)
			kw := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "关键词："), "关键词:"))
			emit(srcLine, fmt.Sprintf(`\noindent\textbf{关键词：}%s`, formatLatexInline(kw)))
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "keywords:") {
			closeList(srcLine)
			kw := strings.TrimSpace(trimmed[len("keywords:"):])
			emit(srcLine, fmt.Sprintf(`\noindent\textbf{Keywords: }%s`, formatLatexInline(kw)))
			continue
		}

		// Unordered list
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			if inList != "itemize" {
				closeList(srcLine)
				emit(srcLine, `\begin{itemize}`)
				inList = "itemize"
			}
			emit(srcLine, `  \item `+formatLatexInline(strings.TrimSpace(trimmed[2:])))
			continue
		}

		// Ordered list
		if m := latexOrderedListPattern.FindStringSubmatch(trimmed); m != nil {
			if inList != "enumerate" {
				closeList(srcLine)
				emit(srcLine, `\begin{enumerate}`)
				inList = "enumerate"
			}
			emit(srcLine, `  \item `+formatLatexInline(strings.TrimSpace(m[1])))
			continue
		}

		closeList(srcLine)

		// Image reference ![alt](src)
		if m := latexImagePattern.FindStringSubmatch(trimmed); m != nil {
			alt := strings.TrimSpace(m[1])
			if alt == "" {
				alt = path.Base(strings.TrimSpace(m[2]))
			}
			emit(srcLine, `\begin{figure}[htbp]`)
			emit(srcLine, `\centering`)
			emit(srcLine, fmt.Sprintf(`%% \includegraphics[width=0.8\linewidth]{%s}`, strings.TrimSpace(m[2])))
			emit(srcLine, fmt.Sprintf(`\caption{%s}`, formatLatexInline(alt)))
			emit(srcLine, `\end{figure}`)
			continue
		}

		// Blockquote
		if strings.HasPrefix(trimmed, ">") {
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			emit(srcLine, fmt.Sprintf(`\begin{quote}%s\end{quote}`, formatLatexInline(quote)))
			continue
		}

		// Standalone figure/table caption
		if key, ok := paperCaptionOf(trimmed); ok {
			emit(srcLine, fmt.Sprintf(`%% 题注（%s %d）`, key.kind, key.num))
			emit(srcLine, fmt.Sprintf(`\begin{center}\textbf{%s}\end{center}`, formatLatexInline(trimmed)))
			continue
		}

		emit(srcLine, formatLatexInline(trimmed))
	}

	lastLine := max(1, len(lines))
	closeList(lastLine)
	closeAbstract(lastLine)
	if inDisplayMath {
		emit(lastLine, `\]`)
	}
	if fenceLen != 0 {
		emit(lastLine, `\end{lstlisting}`)
	}

	if strings.TrimSpace(bibContent) != "" {
		emit(lastLine, "")
		emit(lastLine, `\bibliographystyle{plain}`)
		emit(lastLine, `\bibliography{references}`)
	}
	emit(lastLine, "")
	emit(lastLine, `\end{document}`)

	var tex strings.Builder
	lineMap := make([]int, len(out))
	for idx, entry := range out {
		lineMap[idx] = entry.srcLine
		tex.WriteString(entry.text)
		tex.WriteByte('\n')
	}
	return tex.String(), lineMap
}

func splitMarkdownTableRow(row string) []string {
	trimmed := strings.TrimSpace(row)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func formatLatexTableRow(cells []string) string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = formatLatexInline(c)
	}
	return strings.Join(out, " & ")
}

// formatLatexInline converts Markdown inline constructs while preserving inline math ($...$) and \cite{...}.
func formatLatexInline(text string) string {
	var out strings.Builder
	i := 0
	n := len(text)
	for i < n {
		// Preserve inline math $...$
		if text[i] == '$' && (i+1 >= n || text[i+1] != '$') {
			end := strings.IndexByte(text[i+1:], '$')
			if end >= 0 {
				out.WriteString(text[i : i+1+end+1])
				i += end + 2
				continue
			}
		}
		// Preserve existing \cite{...} / \citep{...} / \citet{...}
		if strings.HasPrefix(text[i:], `\cite`) {
			if m := citeLatexPattern.FindStringIndex(text[i:]); m != nil && m[0] == 0 {
				out.WriteString(text[i : i+m[1]])
				i += m[1]
				continue
			}
		}
		// Convert [@key] or [@k1; @k2] to \cite{k1,k2}
		if text[i] == '[' && strings.HasPrefix(text[i:], "[@") {
			if m := citeBracketAtPattern.FindStringSubmatchIndex(text[i:]); m != nil && m[0] == 0 {
				rawKeys := text[i+m[2] : i+m[3]]
				var keys []string
				for _, part := range strings.Split(rawKeys, ";") {
					k := strings.TrimPrefix(strings.TrimSpace(part), "@")
					if k != "" {
						keys = append(keys, k)
					}
				}
				fmt.Fprintf(&out, `\cite{%s}`, strings.Join(keys, ","))
				i += m[1]
				continue
			}
		}
		// Convert [cite:key] to \cite{key}
		if text[i] == '[' && strings.HasPrefix(text[i:], "[cite:") {
			if m := citeColonPattern.FindStringSubmatchIndex(text[i:]); m != nil && m[0] == 0 {
				rawKeys := text[i+m[2] : i+m[3]]
				var keys []string
				for _, part := range strings.Split(rawKeys, ",") {
					k := strings.TrimSpace(part)
					if k != "" {
						keys = append(keys, k)
					}
				}
				fmt.Fprintf(&out, `\cite{%s}`, strings.Join(keys, ","))
				i += m[1]
				continue
			}
		}
		// Inline code `...`
		if text[i] == '`' {
			end := strings.IndexByte(text[i+1:], '`')
			if end >= 0 {
				code := text[i+1 : i+1+end]
				fmt.Fprintf(&out, `\texttt{%s}`, escapeLatexPlain(code))
				i += end + 2
				continue
			}
		}
		// Bold **...**
		if strings.HasPrefix(text[i:], "**") {
			end := strings.Index(text[i+2:], "**")
			if end > 0 {
				inner := text[i+2 : i+2+end]
				fmt.Fprintf(&out, `\textbf{%s}`, formatLatexInline(inner))
				i += end + 4
				continue
			}
		}
		// Italic *...*
		if text[i] == '*' && (i+1 < n && text[i+1] != ' ' && text[i+1] != '*') {
			end := strings.IndexByte(text[i+1:], '*')
			if end > 0 {
				inner := text[i+1 : i+1+end]
				fmt.Fprintf(&out, `\textit{%s}`, formatLatexInline(inner))
				i += end + 2
				continue
			}
		}
		// Plain character
		out.WriteString(escapeLatexPlain(text[i : i+1]))
		i++
	}
	return out.String()
}

func escapeLatexPlain(s string) string {
	return latexTextEscapes.Replace(strings.ReplaceAll(s, "$", `\$`))
}
