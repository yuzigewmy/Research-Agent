package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
	"research-agent/internal/literature"
)

func TestProposalWorkflowFreezesSourcesAndChecksSavedDeliverables(t *testing.T) {
	const requirements = "Follow the university fieldwork rubric and include a monthly schedule."
	const originalDOI, laterDOI = "10.5555/proposal-river", "10.5555/proposal-followup"
	const outline = "# 开题提纲\n\n## 研究计划\n按月记录河流观测。"
	manuscript := "# 河流监测开题报告\n\n## 研究背景\n既有站点提供观测基础 [@" + citationKey(originalDOI) + "]。\n\n## 研究方法\n每月对两个站点配对观测。"
	const defense = "# 开题答辩材料\n\n## 陈述提纲\n说明配对观测与时间安排。\n\n## 答辩问题\n1. 为什么选择配对观测？\n2. 如何记录缺失数据？"
	outputs := []string{outline, manuscript, defense}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("model did not receive a proposal prompt")
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		call := int(calls.Add(1))
		if call > len(outputs) {
			t.Errorf("unexpected model call %d", call)
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(prompt, requirements) || !strings.Contains(prompt, originalDOI) || strings.Contains(prompt, laterDOI) {
			t.Errorf("call %d did not use the saved school requirements and frozen source", call)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", outputs[call-1])
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, want int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res.Body.String()
	}
	decode := func(body string, out any) {
		t.Helper()
		if err := json.Unmarshal([]byte(body), out); err != nil {
			t.Fatalf("invalid response %s: %v", body, err)
		}
	}
	var project Project
	decode(request(http.MethodPost, projectsPath, `{"requestId":"proposal-project","title":"River monitoring","templateId":"proposal"}`, http.StatusCreated), &project)
	projectPath := projectsPath + "/" + project.ProjectID
	base := projectPath + "/workflow"
	addSource := func(doi, title string) {
		t.Helper()
		if _, _, err := store.saveCitation(t.Context(), project.ProjectID, literature.Work{DOI: doi, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	type observedProposal struct {
		Degree           string `json:"degree"`
		DegreeType       string `json:"degreeType"`
		TargetCharacters int    `json:"targetCharacters"`
		TargetSources    int    `json:"targetSources"`
	}
	type observedRun struct {
		WorkflowRun
		Proposal *observedProposal `json:"proposal"`
	}
	waitFor := func(status string) observedRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run *observedRun }
			decode(request(http.MethodGet, base, "", http.StatusOK), &snapshot)
			if snapshot.Run != nil && snapshot.Run.Status == status {
				return *snapshot.Run
			}
			if snapshot.Run == nil || (snapshot.Run.Status != taskQueued && snapshot.Run.Status != taskRunning) || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	addSource(originalDOI, "River station baselines")
	start := fmt.Sprintf(`{"requestId":"start-proposal","checkpoints":true,"proposal":{"requirements":%q}}`, requirements)
	request(http.MethodPost, base, start, http.StatusCreated)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	defaults := observedProposal{Degree: "master", DegreeType: "academic", TargetCharacters: 5000, TargetSources: 30}
	if checkpoint.WorkflowKey != "proposal-v1" || checkpoint.Proposal == nil || *checkpoint.Proposal != defaults ||
		checkpoint.TargetPaperCount != defaults.TargetSources || checkpoint.SourceCount != 1 || calls.Load() != 1 {
		t.Fatalf("unexpected proposal defaults: run=%+v, proposal=%+v, model calls=%d", checkpoint, checkpoint.Proposal, calls.Load())
	}
	if len(checkpoint.Steps) != 5 || checkpoint.Steps[1].Key != "outline" || checkpoint.Steps[1].Status != "SUCCEEDED" || checkpoint.Steps[2].Status != "PENDING" {
		t.Fatalf("outline did not stop at the checkpoint: %+v", checkpoint.Steps)
	}
	addSource(laterDOI, "Later field study")
	next := fmt.Sprintf(`{"requestId":"continue-proposal","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, base+"/continue", next, http.StatusAccepted)
	completed := waitFor("COMPLETED")
	steps := completed.Steps
	if completed.RunID != checkpoint.RunID || completed.SourceCount != 1 || calls.Load() != 3 || len(steps) != 5 {
		t.Fatalf("unexpected completed proposal: %+v, model calls=%d", completed, calls.Load())
	}
	for i, key := range []string{"sources", "outline", "manuscript", "defense", "check"} {
		if steps[i].Key != key || steps[i].Status != "SUCCEEDED" || steps[i].ArtifactID == "" {
			t.Fatalf("step %s did not produce a current artifact: %+v", key, steps[i])
		}
	}
	runPath := base + "/" + checkpoint.RunID
	artifact := func(step string) string {
		t.Helper()
		return request(http.MethodGet, runPath+"/steps/"+step+"/artifact", "", http.StatusOK)
	}
	if got := artifact("sources"); !strings.Contains(got, originalDOI) || strings.Contains(got, laterDOI) {
		t.Fatalf("wrong frozen source report: %s", got)
	}
	var saved WorkspaceFile
	decode(request(http.MethodPost, runPath+"/manuscript?artifactId="+url.QueryEscape(steps[2].ArtifactID), "", http.StatusOK), &saved)
	var document WorkspaceDocument
	decode(request(http.MethodGet, projectPath+"/files/versions/"+url.PathEscape(saved.VersionID), "", http.StatusOK), &document)
	manuscriptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript)))
	if document.Content != manuscript || document.SHA256 != manuscriptHash || document.SHA256 != saved.SHA256 {
		t.Fatalf("saved manuscript differs from the workflow output: %+v", document)
	}
	if got := artifact("defense"); got != defense {
		t.Fatalf("unexpected defense materials: %q", got)
	}
	defenseHash := fmt.Sprintf("%x", sha256.Sum256([]byte(defense)))
	report := artifact("check")
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
			t.Errorf("delivery report missing %q:\n%s", text, report)
		}
	}

	override := `{"requestId":"school-targets","throughStep":"sources","proposal":{"degree":"doctor","degreeType":"professional","targetCharacters":7000,"targetSources":35}}`
	request(http.MethodPost, base, override, http.StatusCreated)
	custom := waitFor("RANGE_COMPLETED")
	want := observedProposal{Degree: "doctor", DegreeType: "professional", TargetCharacters: 7000, TargetSources: 35}
	if custom.RunID == completed.RunID || custom.Proposal == nil || *custom.Proposal != want ||
		custom.TargetPaperCount != want.TargetSources || custom.SourceCount != 2 || calls.Load() != 3 {
		t.Fatalf("school targets were not applied: run=%+v, proposal=%+v, model calls=%d", custom, custom.Proposal, calls.Load())
	}
}
