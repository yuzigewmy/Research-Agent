package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
	"research-agent/internal/literature"
)

func TestProposalV2ResumesOnlyMissingSections(t *testing.T) {
	const requirements = "Use the department fieldwork rubric."
	const citedDOI, unusedDOI, laterDOI = "10.5555/section-river", "10.5555/section-library", "10.5555/section-later"
	const defense = "# 答辩材料\n\n1. 如何检验方法的可靠性？"
	const truncated = "尚未写完的方法段落。"
	sections := []string{"background", "literature", "objectives", "methods", "feasibility", "schedule"}
	bodies := []string{
		"既有站点提供研究背景。",
		"文献证据来自配对监测 [@" + citationKey(citedDOI) + "]。",
		"主要目标是验证观测之间的一致性。",
		"每月记录两个站点的配对测量。",
		"现有设备满足预定的数据采集方案。",
		"完成初次测量后整理记录并检查缺失值。",
	}
	for i, body := range bodies {
		bodies[i] = body + "\n\n" + strings.Repeat("同步记录观测条件、原始读数与测量不确定性。\n", 120)
	}
	fixture := newProposalFixture(t, []proposalModelReply{
		{body: "# 开题规划"}, {body: bodies[0]}, {body: bodies[1]}, {body: bodies[2]},
		{body: truncated, finishReason: "length"}, {body: bodies[3]}, {body: bodies[4]}, {body: bodies[5]}, {body: defense},
	}, requirements, citedDOI, laterDOI)
	fixture.addSource(citedDOI, "Paired river observations")
	fixture.addSource(unusedDOI, "Uncited library record")
	start := fmt.Sprintf(`{"requestId":"sections","checkpoints":true,"proposal":{"degree":"doctor","degreeType":"professional","targetCharacters":7000,"targetSources":35,"requirements":%q}}`, requirements)
	fixture.request(http.MethodPost, fixture.base, start, http.StatusCreated)
	checkpoint := fixture.waitFor("WAITING_CHECKPOINT")
	if checkpoint.WorkflowKey != "proposal-v2" {
		t.Fatalf("new proposal uses workflow %q, want proposal-v2", checkpoint.WorkflowKey)
	}
	wantTargets := proposalTargets{Degree: "doctor", DegreeType: "professional", TargetCharacters: 7000, TargetSources: 35}
	if checkpoint.Proposal == nil || *checkpoint.Proposal != wantTargets || checkpoint.TargetPaperCount != 35 || checkpoint.SourceCount != 2 || fixture.calls.Load() != 1 {
		t.Fatalf("unexpected checkpoint: %+v, settings=%+v, calls=%d", checkpoint, checkpoint.Proposal, fixture.calls.Load())
	}
	keys := []string{"sources", "outline", "background", "literature", "objectives", "methods", "feasibility", "schedule", "manuscript", "defense", "check"}
	if len(checkpoint.Steps) != len(keys) || checkpoint.Steps[1].Status != taskSucceeded || checkpoint.Steps[2].Status != "PENDING" {
		t.Fatalf("unexpected steps at the outline checkpoint: %+v", checkpoint.Steps)
	}
	fixture.addSource(laterDOI, "Added after freezing")
	fixture.resume(checkpoint, "start-sections", "")
	failed := fixture.waitFor(taskFailed)
	if len(failed.Steps) != len(keys) || failed.Steps[5].Key != "methods" || failed.Steps[5].Status != taskFailed ||
		failed.Steps[5].ArtifactID != "" || fixture.calls.Load() != 5 {
		t.Fatalf("truncated methods output did not stop the workflow: %+v, calls=%d", failed, fixture.calls.Load())
	}
	fixture.request(http.MethodGet, fixture.stepPath(failed.RunID, "methods"), "", http.StatusNotFound)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.openStore()
	restored := fixture.waitFor(taskFailed)
	if restored.RunID != failed.RunID || restored.Revision != failed.Revision || fixture.calls.Load() != 5 {
		t.Fatalf("reopening changed the failed run: %+v, calls=%d", restored, fixture.calls.Load())
	}
	fixture.resume(restored, "retry-methods", "")
	completed := fixture.waitFor("COMPLETED")
	if completed.RunID != checkpoint.RunID || fixture.calls.Load() != 9 || len(completed.Steps) != len(keys) {
		t.Fatalf("workflow did not resume the same run at methods: %+v, calls=%d", completed, fixture.calls.Load())
	}
	for i, key := range keys {
		step := completed.Steps[i]
		if step.Key != key || step.Status != taskSucceeded || step.ArtifactID == "" {
			t.Fatalf("missing current artifact for step %s: %+v", key, step)
		}
		if i >= 2 && i < 5 && step.ArtifactID != failed.Steps[i].ArtifactID {
			t.Fatalf("retry replaced the saved %s artifact", key)
		}
	}

	manuscript := fixture.artifact(completed.RunID, "manuscript")
	if len(manuscript) <= 32<<10 || strings.Contains(manuscript, truncated) {
		t.Fatalf("compiled manuscript has %d bytes or retained a partial methods output", len(manuscript))
	}
	parts := strings.Split(manuscript, "\n## ")
	if len(parts) != len(sections)+2 {
		t.Fatalf("expected six sections and references, got %d second-level headings", len(parts)-1)
	}
	titles := []string{"选题背景与意义", "研究现状", "目标与研究内容", "研究方法与技术路线", "可行性与创新", "进度与预期成果"}
	for i, key := range sections {
		title, body, _ := strings.Cut(parts[i+1], "\n")
		if title != titles[i] || strings.TrimSpace(body) != strings.TrimSpace(bodies[i]) {
			t.Fatalf("compiled section %d should contain %s under %q, got heading %q", i+1, key, titles[i], title)
		}
	}
	title, references, _ := strings.Cut(parts[len(parts)-1], "\n")
	if title != "参考文献" || !strings.Contains(references, citedDOI) || strings.Contains(references, unusedDOI) || strings.Contains(references, laterDOI) {
		t.Fatalf("compiled references should include only the cited frozen source: %s", references)
	}

	document := fixture.saveManuscript(completed.RunID, completed.Steps[8].ArtifactID)
	manuscriptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript)))
	if document.Content != manuscript || document.SHA256 != manuscriptHash {
		t.Fatal("the saved manuscript differs from the checked artifact")
	}
	savedDefense := fixture.artifact(completed.RunID, "defense")
	if savedDefense != defense {
		t.Fatalf("unexpected defense artifact: %q", savedDefense)
	}
	defenseHash := fmt.Sprintf("%x", sha256.Sum256([]byte(savedDefense)))
	report := fixture.artifact(completed.RunID, "check")
	if !strings.Contains(report, "稿件 SHA-256 `"+manuscriptHash+"`") || !strings.Contains(report, "答辩材料 SHA-256 `"+defenseHash+"`") {
		t.Fatalf("delivery check did not bind both current artifacts: %s", report)
	}
	wordPath := fixture.projectPath + "/files/versions/" + url.PathEscape(document.VersionID) + "/word"
	fixture.request(http.MethodGet, wordPath, "", http.StatusOK)
	if os.Getenv("WORDEXPORT_TEST_PYTHON") != "" {
		var receipt wordExport
		fixture.decode(fixture.request(http.MethodPost, wordPath, "", http.StatusOK), &receipt)
		if receipt.SourceSHA256 != manuscriptHash || receipt.Size == 0 {
			t.Fatalf("Word export did not use the confirmed manuscript: %+v", receipt)
		}
	}
}

type proposalModelReply struct {
	body         string
	finishReason string
}

type proposalFixture struct {
	t           *testing.T
	dataDir     string
	config      HandlerConfig
	store       *Store
	handler     http.Handler
	projectID   string
	projectPath string
	base        string
	calls       atomic.Int32
}

func newProposalFixture(t *testing.T, replies []proposalModelReply, requirements, expectedDOI, laterDOI string) *proposalFixture {
	t.Helper()
	fixture := &proposalFixture{t: t, dataDir: t.TempDir()}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("model did not receive the proposal prompt")
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		call := int(fixture.calls.Add(1))
		if call > len(replies) {
			t.Errorf("unexpected model call %d", call)
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(prompt, requirements) || !strings.Contains(prompt, expectedDOI) || strings.Contains(prompt, laterDOI) {
			t.Errorf("call %d did not use the saved school requirements and frozen source", call)
		}
		reply := replies[call-1]
		if reply.finishReason == "" {
			reply.finishReason = "stop"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reply.body, reply.finishReason)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.config = HandlerConfig{Model: model, WordPython: os.Getenv("WORDEXPORT_TEST_PYTHON")}
	fixture.openStore()
	var project Project
	fixture.decode(fixture.request(http.MethodPost, projectsPath, `{"requestId":"proposal-project","title":"River monitoring","templateId":"proposal"}`, http.StatusCreated), &project)
	fixture.projectID = project.ProjectID
	fixture.projectPath = projectsPath + "/" + project.ProjectID
	fixture.base = fixture.projectPath + "/workflow"
	return fixture
}

func (f *proposalFixture) openStore() {
	f.t.Helper()
	store, err := Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = store.Close() })
	f.store = store
	f.handler = NewHandler(store, nil, f.config)
}

func (f *proposalFixture) request(method, path, body string, want int) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	f.handler.ServeHTTP(res, req)
	if res.Code != want {
		f.t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
	}
	return res.Body.String()
}

func (f *proposalFixture) decode(body string, target any) {
	f.t.Helper()
	if err := json.Unmarshal([]byte(body), target); err != nil {
		f.t.Fatalf("invalid response %s: %v", body, err)
	}
}

func (f *proposalFixture) waitFor(status string) WorkflowRun {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var snapshot struct{ Run *WorkflowRun }
		f.decode(f.request(http.MethodGet, f.base, "", http.StatusOK), &snapshot)
		if snapshot.Run != nil && snapshot.Run.Status == status {
			return *snapshot.Run
		}
		if snapshot.Run == nil || (snapshot.Run.Status != taskQueued && snapshot.Run.Status != taskRunning) || time.Now().After(deadline) {
			f.t.Fatalf("want %s, got %+v", status, snapshot.Run)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *proposalFixture) resume(run WorkflowRun, requestID, through string) {
	f.t.Helper()
	body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"throughStep":%q}`, requestID, run.RunID, run.Revision, through)
	f.request(http.MethodPost, f.base+"/continue", body, http.StatusAccepted)
}

func (f *proposalFixture) addSource(doi, title string) {
	f.t.Helper()
	if _, _, err := f.store.saveCitation(f.t.Context(), f.projectID, literature.Work{DOI: doi, Title: title}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *proposalFixture) stepPath(runID, key string) string {
	return f.base + "/" + runID + "/steps/" + key + "/artifact"
}

func (f *proposalFixture) artifact(runID, key string) string {
	f.t.Helper()
	return f.request(http.MethodGet, f.stepPath(runID, key), "", http.StatusOK)
}

func (f *proposalFixture) saveManuscript(runID, artifactID string) WorkspaceDocument {
	f.t.Helper()
	var saved WorkspaceFile
	f.decode(f.request(http.MethodPost, f.base+"/"+runID+"/manuscript?artifactId="+url.QueryEscape(artifactID), "", http.StatusOK), &saved)
	var document WorkspaceDocument
	f.decode(f.request(http.MethodGet, f.projectPath+"/files/versions/"+url.PathEscape(saved.VersionID), "", http.StatusOK), &document)
	if document.SHA256 != saved.SHA256 {
		f.t.Fatal("workspace version differs from the save receipt")
	}
	return document
}
