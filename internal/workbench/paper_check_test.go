package workbench

import (
	"strings"
	"testing"
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
