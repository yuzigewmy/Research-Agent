package workbench

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestProposalWorkflowFreezesSourcesAndChecksSavedDeliverables(t *testing.T) {
	const requirements = "Follow the university fieldwork rubric and include a monthly schedule."
	const originalDOI, laterDOI = "10.5555/proposal-river", "10.5555/proposal-followup"
	const outline = "# 开题提纲\n\n## 研究计划\n按月记录河流观测。"
	manuscript := "# 河流监测开题报告\n\n## 研究背景\n既有站点提供观测基础 [@" + citationKey(originalDOI) + "]。\n\n## 研究方法\n每月对两个站点配对观测。"
	const defense = "# 开题答辩材料\n\n## 陈述提纲\n说明配对观测与时间安排。\n\n## 答辩问题\n1. 为什么选择配对观测？\n2. 如何记录缺失数据？"
	fixture := newProposalFixture(t, []proposalModelReply{{body: outline}, {body: manuscript}, {body: defense}}, requirements, originalDOI, laterDOI)
	fixture.addSource(originalDOI, "River station baselines")
	start := fmt.Sprintf(`{"requestId":"start-proposal","checkpoints":true,"throughStep":"sources","proposal":{"requirements":%q}}`, requirements)
	fixture.request(http.MethodPost, fixture.base, start, http.StatusCreated)
	frozen := fixture.waitFor("RANGE_COMPLETED")
	if fixture.calls.Load() != 0 || frozen.SourceCount != 1 {
		t.Fatalf("unexpected source checkpoint: %+v, model calls=%d", frozen, fixture.calls.Load())
	}
	// The sources step is shared by both revisions. Pin this fixture to the legacy definition before continuing.
	if _, err := fixture.store.db.ExecContext(t.Context(), `UPDATE workflow_runs SET workflow_key = 'proposal-v1' WHERE project_id = ? AND run_id = ?`, fixture.projectID, frozen.RunID); err != nil {
		t.Fatal(err)
	}
	fixture.addSource(laterDOI, "Later field study")
	fixture.resume(frozen, "continue-to-outline", "")
	checkpoint := fixture.waitFor("WAITING_CHECKPOINT")
	defaults := proposalTargets{Degree: "master", DegreeType: "academic", TargetCharacters: 5000, TargetSources: 30}
	if checkpoint.WorkflowKey != "proposal-v1" || checkpoint.Proposal == nil || *checkpoint.Proposal != defaults ||
		checkpoint.TargetPaperCount != defaults.TargetSources || checkpoint.SourceCount != 1 || fixture.calls.Load() != 1 {
		t.Fatalf("unexpected legacy proposal defaults: run=%+v, proposal=%+v, model calls=%d", checkpoint, checkpoint.Proposal, fixture.calls.Load())
	}
	if len(checkpoint.Steps) != 5 || checkpoint.Steps[1].Key != "outline" || checkpoint.Steps[1].Status != taskSucceeded || checkpoint.Steps[2].Status != "PENDING" {
		t.Fatalf("legacy outline did not stop at the checkpoint: %+v", checkpoint.Steps)
	}
	fixture.resume(checkpoint, "continue-to-delivery", "")
	completed := fixture.waitFor("COMPLETED")
	steps := completed.Steps
	if completed.RunID != frozen.RunID || completed.SourceCount != 1 || fixture.calls.Load() != 3 || len(steps) != 5 {
		t.Fatalf("unexpected completed legacy proposal: %+v, model calls=%d", completed, fixture.calls.Load())
	}
	for i, key := range []string{"sources", "outline", "manuscript", "defense", "check"} {
		if steps[i].Key != key || steps[i].Status != taskSucceeded || steps[i].ArtifactID == "" {
			t.Fatalf("legacy step %s did not produce a current artifact: %+v", key, steps[i])
		}
	}
	if got := fixture.artifact(completed.RunID, "sources"); !strings.Contains(got, originalDOI) || strings.Contains(got, laterDOI) {
		t.Fatalf("wrong frozen source report: %s", got)
	}
	manuscriptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript)))
	document := fixture.saveManuscript(completed.RunID, steps[2].ArtifactID)
	if document.Content != manuscript || document.SHA256 != manuscriptHash {
		t.Fatalf("saved legacy manuscript differs from the workflow output: %+v", document)
	}
	if got := fixture.artifact(completed.RunID, "defense"); got != defense {
		t.Fatalf("unexpected legacy defense materials: %q", got)
	}
	defenseHash := fmt.Sprintf("%x", sha256.Sum256([]byte(defense)))
	report := fixture.artifact(completed.RunID, "check")
	for _, text := range []string{
		"稿件 SHA-256 `" + manuscriptHash + "`",
		"答辩材料 SHA-256 `" + defenseHash + "`",
		"21 / 5000，还差 4979",
		"1 / 30（本轮可用 1 条），还差 29",
		"答辩问题 2 /",
		"选题背景与意义，已识别到第 3 行",
		"研究现状，待核对对应标题",
	} {
		if !strings.Contains(report, text) {
			t.Errorf("legacy delivery report missing %q:\n%s", text, report)
		}
	}
}
