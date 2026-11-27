package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"research-agent/internal/literature"
)

func TestPaperCheckRecognizesAIDisclosureHeading(t *testing.T) {
	present := strings.Join([]string{
		"# 题目",
		"",
		"## 摘要",
		"研究范围。",
		"",
		"```text",
		"## AI 使用披露",
		"围栏中的标题不计入。",
		"```",
		"",
		"## AI Use Disclosure",
		"本文使用辅助工具整理文字。",
		"",
		"## 方法",
		"TODO: 补采样表。",
	}, "\n")
	report := paperCheck(present)
	for _, want := range []string{
		"摘要：已识别，第 3 行。",
		"AI 使用披露：已识别，第 11 行。",
		"方法或证据：已识别，第 14 行。",
		"第 15 行有待补标记。",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "第 7 行") || strings.Contains(report, "请按投稿要求核对") {
		t.Fatalf("fenced heading was counted or the present heading was reported missing:\n%s", report)
	}

	for _, heading := range []string{"## 人工智能使用声明", "## AI使用披露", "## Artificial Intelligence Use Disclosure"} {
		found := paperCheck("# 题目\n\n" + heading + "\n只确认标题。\n")
		if !strings.Contains(found, "AI 使用披露：已识别，第 3 行。") || strings.Contains(found, "请按投稿要求核对") {
			t.Fatalf("%s was not recognized:\n%s", heading, found)
		}
	}

	missing := strings.Join([]string{
		"## 摘要",
		"没有披露标题。",
		"",
		"## 结论",
		"TODO: 补结论依据。",
	}, "\n")
	absent := paperCheck(missing)
	for _, want := range []string{
		"摘要：已识别，第 1 行。",
		"结论：已识别，第 4 行。",
		"AI 使用披露：未识别到对应标题。请按投稿要求核对。",
		"第 5 行有待补标记。",
		"引言：请核对，未识别到对应标题。",
	} {
		if !strings.Contains(absent, want) {
			t.Fatalf("missing-heading report missing %q:\n%s", want, absent)
		}
	}
	if strings.Contains(absent, "AI 使用披露：已识别") {
		t.Fatalf("missing heading was reported as present:\n%s", absent)
	}
}

func TestPaperCheckIgnoresDisclosureInsideFourBacktickExample(t *testing.T) {
	source := strings.Join([]string{
		"````",
		"```",
		"## AI 使用披露",
		"示例中的标题不计入。",
		"```",
		"````",
		"",
		"## 结论",
		"外部结论仍计入。",
	}, "\n")
	report := paperCheck(source)
	if !strings.Contains(report, "结论：已识别，第 8 行。") {
		t.Fatalf("conclusion after the real fence close was not recognized:\n%s", report)
	}
	if strings.Contains(report, "AI 使用披露：已识别") || strings.Contains(report, "第 3 行") || !strings.Contains(report, "AI 使用披露：未识别到对应标题。请按投稿要求核对。") {
		t.Fatalf("disclosure heading inside the example was counted:\n%s", report)
	}
}

func TestPaperCheckReportsDuplicateCaptionNumbers(t *testing.T) {
	mixed := strings.Join([]string{
		"# 题目",
		"",
		"## 摘要",
		"正文引用见图 1、Figure 1 和表 1，不单独成题注。",
		"",
		"图 1：首次中文图",
		"表 1: 首次中文表",
		"图 2：另一图号",
		"",
		"```text",
		"图 1：围栏内不计入",
		"Table 1: Fence caption",
		"```",
		"",
		"> 图 1：引用块不计入",
		"> Table 2: Quote caption",
		"",
		"Figure 1. Same figure class",
		"Table 1: Same table class",
		"图1：中文标识后无空格",
		"**图 01：整行加粗且前导零**",
		"**Table 01: Bold leading zero**",
		"Figure 2. Second figure duplicates",
		"表 2: 与图 2 互不冲突",
		"Figure1. No space after English marker",
		"图 3 没有标点所以不计",
		"见图 2：正文里的冒号也不计",
		"Table 3: English table first",
		"表 3：与 Table 3 同类",
		"Figure 1.2. Segmented identifier",
	}, "\n")

	var capped []string
	capped = append(capped, "图 1：首次")
	for i := 0; i < 25; i++ {
		capped = append(capped, "Figure 1. duplicate")
	}

	tests := []struct {
		name    string
		source  string
		want    []string
		absent  []string
		notices int
	}{
		{
			name:   "中英文同类重复且图表不冲突、围栏引用块和正文不计",
			source: mixed,
			want: []string{
				"# 稿件结构检查",
				"稿件 SHA-256",
				"摘要：已识别，第 3 行。",
				"引言：请核对，未识别到对应标题。",
				"AI 使用披露：未识别到对应标题。请按投稿要求核对。",
				"重复图表编号：图 1，初次第 6 行，重复第 18 行。",
				"重复图表编号：表 1，初次第 7 行，重复第 19 行。",
				"重复图表编号：图 1，初次第 6 行，重复第 20 行。",
				"重复图表编号：图 1，初次第 6 行，重复第 21 行。",
				"重复图表编号：表 1，初次第 7 行，重复第 22 行。",
				"重复图表编号：图 2，初次第 8 行，重复第 23 行。",
				"重复图表编号：表 3，初次第 28 行，重复第 29 行。",
			},
			absent: []string{
				"初次第 6 行，重复第 7 行",
				"初次第 8 行，重复第 24 行",
				"重复第 4 行",
				"重复第 11 行",
				"重复第 12 行",
				"重复第 15 行",
				"重复第 16 行",
				"重复第 25 行",
				"重复第 26 行",
				"重复第 27 行",
				"重复第 30 行",
				"初次第 4 行",
				"初次第 11 行",
				"初次第 15 行",
				"表 2，初次",
				"第 4 行有待补标记",
			},
			notices: 7,
		},
		{
			name:   "重复提醒最多二十条",
			source: strings.Join(capped, "\n"),
			want: []string{
				"# 稿件结构检查",
				"稿件 SHA-256",
				"摘要：请核对，未识别到对应标题。",
				"重复图表编号：图 1，初次第 1 行，重复第 2 行。",
				"重复图表编号：图 1，初次第 1 行，重复第 21 行。",
			},
			absent: []string{
				"重复第 22 行",
			},
			notices: 20,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := paperCheck(tt.source)
			for _, want := range tt.want {
				if !strings.Contains(report, want) {
					t.Fatalf("report missing %q:\n%s", want, report)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(report, absent) {
					t.Fatalf("report unexpectedly contains %q:\n%s", absent, report)
				}
			}
			var notices int
			for _, line := range strings.Split(report, "\n") {
				if strings.Contains(line, "重复图表编号") {
					notices++
				}
			}
			if notices != tt.notices {
				t.Fatalf("duplicate notices = %d, want %d:\n%s", notices, tt.notices, report)
			}
		})
	}
}

func TestPaperCheckDetailedStagesCitationsAndHTTP(t *testing.T) {
	source := strings.Join([]string{
		"# 实验论文初稿",
		"",
		"## 摘要",
		"本文对比两种方法，见 [@ref-valid] 与 [@missing2026]。",
		"",
		"## 引言",
		"参考综述 \\cite{ref-valid, unknown-ref}，另见 [cite:missing-colon]。",
		"",
		"## 方法",
		"TODO: 补充消融实验。",
		"图 1：系统架构",
		"Figure 1. Duplicate architecture caption",
		"",
		"## 结论",
		"实验表明模型有效。",
	}, "\n")

	valid := map[string]bool{"ref-valid": true}

	plan := paperCheckDetailed(source, "plan", valid)
	if plan.Stage != "plan" || plan.Stats.ChineseChars == 0 || plan.Stats.EnglishWords == 0 {
		t.Fatalf("unexpected plan report: %+v", plan)
	}
	if !slices.Equal(plan.Citations.Matched, []string{"ref-valid"}) {
		t.Fatalf("matched citations = %v", plan.Citations.Matched)
	}
	if !slices.Equal(plan.Citations.Missing, []string{"missing-colon", "missing2026", "unknown-ref"}) {
		t.Fatalf("missing citations = %v", plan.Citations.Missing)
	}
	// Plan stage treats placeholders as info, final treats as error
	foundPlanPlaceholder := false
	for _, d := range plan.Diagnostics {
		if d.Rule == "placeholder" && d.Line == 10 && d.Severity == "info" {
			foundPlanPlaceholder = true
		}
	}
	if !foundPlanPlaceholder {
		t.Fatalf("plan stage placeholder diagnostic mismatch: %+v", plan.Diagnostics)
	}

	draft := paperCheckDetailed(source, "draft", valid)
	foundDraftDup := false
	for _, d := range draft.Diagnostics {
		if d.Rule == "duplicate_caption" && d.Line == 12 && d.Severity == "warning" {
			foundDraftDup = true
		}
	}
	if !foundDraftDup {
		t.Fatalf("draft stage duplicate caption mismatch: %+v", draft.Diagnostics)
	}

	final := paperCheckDetailed(source, "final", valid)
	var hasAIDisclosureErr, hasMissingCiteErr, hasDupErr bool
	for _, d := range final.Diagnostics {
		if d.Rule == "section_missing" && strings.Contains(d.Message, "AI 使用披露") && d.Severity == "error" {
			hasAIDisclosureErr = true
		}
		if d.Rule == "citation_missing" && d.Line == 4 && strings.Contains(d.Message, "missing2026") && d.Severity == "error" {
			hasMissingCiteErr = true
		}
		if d.Rule == "duplicate_caption" && d.Line == 12 && d.Severity == "error" {
			hasDupErr = true
		}
	}
	if !hasAIDisclosureErr || !hasMissingCiteErr || !hasDupErr {
		t.Fatalf("final diagnostics missing expected items: %+v", final.Diagnostics)
	}

	// HTTP endpoint test with project literature & project isolation
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "req-check-1",
		Title:      "检查测试项目",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := store.createProject(ctx, createProjectInput{
		RequestID:  "req-check-2",
		Title:      "隔离项目",
		TemplateID: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	work := literature.Work{DOI: "10.1000/check.2026", Title: "Check Ref", Authors: []string{"Wang"}, Year: 2026}
	if _, _, err := store.saveCitation(ctx, project.ProjectID, work); err != nil {
		t.Fatal(err)
	}
	key := citationKey(work.DOI)
	md := "# 论文\n\n## 摘要\n见 [@" + key + "] 和 [@unregistered]。\n"
	doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/check.md", "", md)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	route := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/files/versions/" + doc.VersionID + "/check"
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, route, strings.NewReader(`{"stage":"final"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("check endpoint returned %d: %s", rec.Code, rec.Body)
	}
	var report paperCheckReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SourceVersionID != doc.VersionID || report.SourceSHA256 != doc.SHA256 || report.Stage != "final" {
		t.Fatalf("unexpected report header: %+v", report)
	}
	if !slices.Equal(report.Citations.Matched, []string{key}) || !slices.Equal(report.Citations.Missing, []string{"unregistered"}) {
		t.Fatalf("unexpected http citations: %+v", report.Citations)
	}

	// Project isolation check
	isoRoute := "http://127.0.0.1" + projectsPath + "/" + other.ProjectID + "/files/versions/" + doc.VersionID + "/check"
	isoReq := httptest.NewRequestWithContext(ctx, http.MethodPost, isoRoute, strings.NewReader(`{"stage":"final"}`))
	isoReq.Header.Set("Content-Type", "application/json")
	isoRec := httptest.NewRecorder()
	handler.ServeHTTP(isoRec, isoReq)
	if isoRec.Code != http.StatusNotFound {
		t.Fatalf("cross-project check returned %d, want 404", isoRec.Code)
	}
}
