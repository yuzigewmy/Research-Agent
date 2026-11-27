package workbench

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"research-agent/internal/literature"
)

func createLatexStubScript(t *testing.T, name, windowsBody, unixBody string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, name+".cmd")
		if err := os.WriteFile(p, []byte("@echo off\r\n"+windowsBody+"\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := filepath.Join(dir, name+".sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+unixBody+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLatexExportPresetsAndZip(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "req-latex-1",
		Title:      "LaTeX 导出验证项目",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherProject, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "req-latex-other",
		Title:      "隔离项目",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}

	work := literature.Work{
		DOI:     "10.1000/test.2026",
		Title:   "Deterministic Export Study",
		Authors: []string{"Li, Wei"},
		Journal: "Journal of Research",
		Year:    2026,
	}
	if _, _, err := store.saveCitation(ctx, project.ProjectID, work); err != nil {
		t.Fatal(err)
	}
	citeKey := citationKey(work.DOI)

	markdown := strings.Join([]string{
		"# 测试论文标题",
		"",
		"## 摘要",
		"本文研究行内公式 $E = mc^2$ 与文献引用 [@" + citeKey + "]。",
		"关键词：研究工作台；LaTeX 导出",
		"",
		"## 引言",
		"独立公式如下：",
		"$$",
		"\\sum_{i=1}^{n} x_i = S",
		"$$",
		"",
		"| 指标 | 数值 |",
		"| --- | --- |",
		"| 精度 | 98.5% |",
		"| 召回 | 96.2% |",
		"",
		"```python",
		"def run(x): return x + 1",
		"```",
		"",
		"图 1：实验框架示意图",
	}, "\n")

	doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/main.md", "", markdown)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	route := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/files/versions/" + doc.VersionID + "/latex"

	presets := []struct {
		preset string
		expect []string
	}{
		{
			preset: "zh_article",
			expect: []string{`\documentclass[11pt,a4paper]{ctexart}`, `\section{引言}`, `\begin{abstract}`, `$E = mc^2$`, `\sum_{i=1}^{n} x_i = S`, `\toprule`, `\midrule`, `\bottomrule`, `\cite{` + citeKey + `}`, `\bibliography{references}`},
		},
		{
			preset: "en_conference",
			expect: []string{`\documentclass[10pt,twocolumn,letterpaper]{article}`, `\usepackage{times}`, `\section{引言}`},
		},
		{
			preset: "zh_thesis",
			expect: []string{`\documentclass[12pt,a4paper]{ctexrep}`, `\tableofcontents`, `\chapter{引言}`},
		},
		{
			preset: "modeling_contest",
			expect: []string{`\documentclass[12pt,a4paper]{ctexart}`, `\usepackage{float}`, `\begin{lstlisting}`},
		},
	}

	for _, tc := range presets {
		t.Run(tc.preset, func(t *testing.T) {
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, route, strings.NewReader(`{"preset":"`+tc.preset+`","compilePdf":false}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("preset %s returned %d: %s", tc.preset, rec.Code, rec.Body)
			}
			var resp latexExportResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.VersionID != doc.VersionID || resp.SourceSHA256 != doc.SHA256 || resp.Preset != tc.preset || resp.TexFilename != "main.tex" || len(resp.TexSHA256) != 64 {
				t.Fatalf("unexpected export metadata: %+v", resp)
			}
			if !strings.Contains(resp.TexContent, "% Source-Version-ID: "+doc.VersionID) || !strings.Contains(resp.TexContent, "% Source-SHA256: "+doc.SHA256) {
				t.Fatalf("missing provenance comments:\n%s", resp.TexContent)
			}
			if !strings.Contains(resp.BibContent, citeKey) {
				t.Fatalf("bibContent missing citation key %s:\n%s", citeKey, resp.BibContent)
			}
			for _, want := range tc.expect {
				if !strings.Contains(resp.TexContent, want) {
					t.Fatalf("preset %s tex missing %q:\n%s", tc.preset, want, resp.TexContent)
				}
			}
		})
	}

	// ZIP download includes main.tex and references.bib
	zipReq := httptest.NewRequestWithContext(ctx, http.MethodPost, route+"/zip", strings.NewReader(`{"preset":"zh_article"}`))
	zipReq.Header.Set("Content-Type", "application/json")
	zipRec := httptest.NewRecorder()
	handler.ServeHTTP(zipRec, zipReq)
	if zipRec.Code != http.StatusOK || zipRec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("zip export failed: %d %s", zipRec.Code, zipRec.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipRec.Body.Bytes()), int64(zipRec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names[f.Name] = string(data)
	}
	if _, ok := names["main.tex"]; !ok {
		t.Fatalf("zip missing main.tex: %v", names)
	}
	if !strings.Contains(names["references.bib"], citeKey) {
		t.Fatalf("zip missing references.bib content")
	}

	// Project isolation
	otherRoute := "http://127.0.0.1" + projectsPath + "/" + otherProject.ProjectID + "/files/versions/" + doc.VersionID + "/latex"
	isoReq := httptest.NewRequestWithContext(ctx, http.MethodPost, otherRoute, strings.NewReader(`{"preset":"zh_article"}`))
	isoReq.Header.Set("Content-Type", "application/json")
	isoRec := httptest.NewRecorder()
	handler.ServeHTTP(isoRec, isoReq)
	if isoRec.Code != http.StatusNotFound {
		t.Fatalf("cross-project access returned %d, want 404", isoRec.Code)
	}
}

func TestLatexExportCompileMissingCapabilityAndDiagnostics(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "req-latex-compile",
		Title:      "编译诊断项目",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	originalContent := "# 标题\n\n## 引言\n正文段落。\n"
	doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/draft.md", "", originalContent)
	if err != nil {
		t.Fatal(err)
	}
	route := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/files/versions/" + doc.VersionID + "/latex"

	// 1) Unconfigured RESEARCH_LATEX_BIN -> 503 CAPABILITY_MISSING
	t.Run("unconfigured returns 503", func(t *testing.T) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, route, strings.NewReader(`{"preset":"zh_article","compilePdf":true}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		NewHandler(store, nil, HandlerConfig{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "CAPABILITY_MISSING") {
			t.Fatalf("expected 503 CAPABILITY_MISSING, got %d: %s", rec.Code, rec.Body)
		}
	})

	// 2) Stub compiler failing with TeX log -> returns diagnostics without modifying saved document
	t.Run("compile error returns structured diagnostics", func(t *testing.T) {
		failStub := createLatexStubScript(t, "fake-xelatex-fail",
			"echo ! Undefined control sequence.\r\necho l.21 \\unknownmacro\r\nexit /b 1",
			"echo '! Undefined control sequence.'\necho 'l.21 \\unknownmacro'\nexit 1",
		)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, route, strings.NewReader(`{"preset":"zh_article","compilePdf":true}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		NewHandler(store, nil, HandlerConfig{LatexBin: failStub}).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 with diagnostics, got %d: %s", rec.Code, rec.Body)
		}
		var resp latexExportResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Compiled || len(resp.Diagnostics) == 0 || resp.Diagnostics[0].Line != 21 || !strings.Contains(resp.Diagnostics[0].Message, "Undefined control sequence") {
			t.Fatalf("unexpected compile failure response: %+v", resp)
		}
		// Ensure saved document was NOT modified
		after, err := store.workspaceFile(ctx, project.ProjectID, "paper/draft.md")
		if err != nil || after.SHA256 != doc.SHA256 {
			t.Fatalf("saved workspace file changed after failed compile: %+v (%v)", after, err)
		}
	})

	// 3) Stub compiler succeeding (writes main.pdf and checks -no-shell-escape)
	t.Run("compile success returns pdf", func(t *testing.T) {
		okStub := createLatexStubScript(t, "fake-xelatex-ok",
			"echo %* | findstr /C:\"-no-shell-escape\" >nul || exit /b 2\r\necho %%PDF-1.4 stub>main.pdf\r\nexit /b 0",
			"echo \"$*\" | grep -q -- '-no-shell-escape' || exit 2\nprintf '%%PDF-1.4 stub' > main.pdf\nexit 0",
		)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, route, strings.NewReader(`{"preset":"zh_article","compilePdf":true}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		NewHandler(store, nil, HandlerConfig{LatexBin: okStub}).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
		}
		var resp latexExportResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.Compiled || resp.PdfSize == 0 || len(resp.PdfSHA256) != 64 || resp.PdfFilename != "draft.pdf" {
			t.Fatalf("unexpected compile success response: %+v", resp)
		}
		decoded, err := base64.StdEncoding.DecodeString(resp.PdfBase64)
		if err != nil || !strings.HasPrefix(string(decoded), "%PDF-1.4") {
			t.Fatalf("unexpected decoded pdf: %q (%v)", string(decoded), err)
		}
	})

	// 4) Direct log parser unit test
	t.Run("parseLatexLog parses errors and warnings", func(t *testing.T) {
		sampleLog := strings.Join([]string{
			"This is XeTeX, Version 3.141592653",
			"! Missing $ inserted.",
			"<inserted text>",
			"l.42 a_b",
			"main.tex:55: Extra }, or forgotten $.",
			"LaTeX Warning: Reference `fig:1' on page 2 undefined on input line 68.",
		}, "\n")
		diags := parseLatexLog(sampleLog)
		if len(diags) != 3 {
			t.Fatalf("expected 3 diagnostics, got %d: %+v", len(diags), diags)
		}
		if diags[0].Line != 42 || diags[1].Line != 55 || diags[2].Line != 68 {
			t.Fatalf("unexpected diagnostic lines: %+v", diags)
		}
	})
}
