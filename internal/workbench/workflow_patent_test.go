package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestPatentDisclosurePinsGuideThroughDefaultCheckpoint(t *testing.T) {
	const (
		description = "利用反馈回路稳定试验台的供水压力。"
		original    = "原始观测记录显示传感器位于回流支路。"
		replacement = "最新记录改为在出水总管上安装传感器。"
		outline     = "# 技术方案草案\n\n管路反馈方案"
		manuscript  = `# 管路压力反馈装置

## 技术领域

涉及供水试验台的压力监测。

## 现有技术与问题

压力变化难以从固定流量读数中识别。

## 技术方案

由回流支路的传感器向控制器反馈压力。

## 实施方式

在回流支路安装传感器并记录压力读数。

## 附图说明

图 1 为压力反馈系统示意图，图 2 为压力读取和阈值比较流程图。

## 建议保护点

核对回流支路与反馈控制器的连接关系。

## 可选实施方案

保留手动核对压力的工作方式。

## 技术效果与待核实项

压力波动幅度仍待复测。`
		systemFigure  = `{"version":1,"nodes":[{"id":"input","label":"压力传感器","x":40,"y":120},{"id":"control","label":"控制器","x":300,"y":120}],"edges":[{"from":"input","to":"control"}]}`
		processFigure = `{"version":1,"nodes":[{"id":"read","label":"读取压力","x":40,"y":120},{"id":"compare","label":"比较阈值","x":300,"y":120}],"edges":[{"from":"read","to":"compare"}]}`
	)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 || call > 4 {
			t.Errorf("unexpected model request %d", call)
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		if call <= 2 && (!strings.Contains(prompt, description) || !strings.Contains(prompt, original) || strings.Contains(prompt, replacement)) {
			t.Errorf("model request %d did not receive the fixed guide and description", call)
		}
		if call >= 3 && !strings.Contains(prompt, "由回流支路的传感器向控制器反馈压力。") {
			t.Errorf("figure request %d is missing the saved manuscript", call)
		}
		var output string
		switch call {
		case 1:
			output = outline
		case 2:
			if !strings.Contains(prompt, "管路反馈方案") {
				t.Error("manuscript request is missing the saved outline")
			}
			output = manuscript
		case 3:
			output = `{"system":` + systemFigure + `}`
		case 4:
			output = `{"system":` + systemFigure + `,"process":` + processFigure + `}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
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
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, status int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	var project Project
	request(http.MethodPost, projectsPath, `{"requestId":"disclosure","title":"管路压力反馈装置","templateId":"patent_disclosure"}`, http.StatusCreated, &project)
	projectPath := projectsPath + "/" + project.ProjectID
	workflow := projectPath + "/workflow"
	saveGuide := func(content string) Material {
		t.Helper()
		file, created, err := store.saveMaterial(ctx, project.ProjectID, "guide.md", strings.NewReader(content))
		if err != nil || !created {
			t.Fatalf("save guide version: created=%t, err=%v", created, err)
		}
		return file
	}
	guide := saveGuide(original)
	type patentRun struct {
		WorkflowRun
		Patent *struct {
			Description string
			MaterialIDs []string
			Materials   []TurnReference
		} `json:"patent"`
	}
	waitFor := func(status string) patentRun {
		t.Helper()
		for {
			var result struct{ Run *patentRun }
			request(http.MethodGet, workflow, "", http.StatusOK, &result)
			run := result.Run
			if run != nil && run.Status == status {
				return *run
			}
			if run == nil || (run.Status != taskQueued && run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	assertSteps := func(run WorkflowRun, statuses ...string) {
		t.Helper()
		keys := []string{"sources", "outline", "manuscript", "figures", "check"}
		if len(run.Steps) != len(keys) {
			t.Fatalf("wrong disclosure steps: %+v", run.Steps)
		}
		for i, key := range keys {
			step, status := run.Steps[i], statuses[i]
			if step.Key != key || step.Status != status || (step.ArtifactID != "") != (status == taskSucceeded) {
				t.Fatalf("wrong state for %s, want %s: %+v", key, status, step)
			}
		}
	}
	start := fmt.Sprintf(`{"requestId":"start","patent":{"description":%q,"materialIds":[%q]}}`, description, guide.MaterialID)
	var started patentRun
	request(http.MethodPost, workflow, start, http.StatusCreated, &started)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	settings := checkpoint.Patent
	if checkpoint.RunID != started.RunID || checkpoint.WorkflowKey != "patent-disclosure-v2" || !checkpoint.Checkpoints || calls.Load() != 1 ||
		settings == nil || settings.Description != description || len(settings.MaterialIDs) != 1 || settings.MaterialIDs[0] != guide.MaterialID || len(settings.Materials) != 1 {
		t.Fatalf("wrong disclosure checkpoint: run=%+v, patent=%+v, model calls=%d", checkpoint, settings, calls.Load())
	}
	if selected := settings.Materials[0]; selected.MaterialID != guide.MaterialID || selected.Name != guide.Name || selected.SHA256 != guide.SHA256 {
		t.Fatalf("wrong fixed guide: %+v", selected)
	}
	assertSteps(checkpoint.WorkflowRun, taskSucceeded, taskSucceeded, "PENDING", "PENDING", "PENDING")
	runPath := workflow + "/" + checkpoint.RunID
	sources := request(http.MethodGet, runPath+"/steps/sources/artifact", "", http.StatusOK, nil).Body.String()
	if !strings.Contains(sources, guide.Name) || !strings.Contains(sources, guide.MaterialID) || !strings.Contains(sources, guide.SHA256) {
		t.Fatalf("source list is missing the fixed guide version: %s", sources)
	}

	latest := saveGuide(replacement)
	resume := func(requestID string, run WorkflowRun) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, requestID, run.RunID, run.Revision)
		request(http.MethodPost, workflow+"/continue", body, http.StatusAccepted, nil)
	}
	resume("continue", checkpoint.WorkflowRun)
	failed := waitFor(taskFailed)
	if failed.RunID != checkpoint.RunID || calls.Load() != 3 {
		t.Fatalf("expected the missing figure to fail on the original run: %+v, calls=%d", failed, calls.Load())
	}
	assertSteps(failed.WorkflowRun, taskSucceeded, taskSucceeded, taskSucceeded, taskFailed, "PENDING")
	request(http.MethodGet, runPath+"/steps/figures/artifact", "", http.StatusNotFound, nil)
	body := request(http.MethodGet, runPath+"/steps/manuscript/artifact", "", http.StatusOK, nil).Body.String()
	if strings.TrimSpace(body) != manuscript {
		t.Fatalf("unexpected disclosure manuscript: %q", body)
	}
	manuscriptID := failed.Steps[2].ArtifactID
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))

	resume("retry-figures", failed.WorkflowRun)
	finished := waitFor(workflowDone)
	assertSteps(finished.WorkflowRun, taskSucceeded, taskSucceeded, taskSucceeded, taskSucceeded, taskSucceeded)
	if finished.RunID != checkpoint.RunID || finished.Steps[2].ArtifactID != manuscriptID || calls.Load() != 4 {
		t.Fatalf("figure retry changed its manuscript or dispatched extra model work: %+v, calls=%d", finished, calls.Load())
	}
	figureID := finished.Steps[3].ArtifactID
	figurePath := runPath + "/steps/figures/artifacts/" + figureID
	var figures struct {
		ManuscriptArtifactID string          `json:"manuscriptArtifactId"`
		ManuscriptSHA256     string          `json:"manuscriptSha256"`
		System               diagramDocument `json:"system"`
		Process              diagramDocument `json:"process"`
	}
	figureBody := request(http.MethodGet, figurePath, "", http.StatusOK, &figures).Body.Bytes()
	if figures.ManuscriptArtifactID != manuscriptID || figures.ManuscriptSHA256 != digest {
		t.Fatalf("figures do not identify the saved manuscript: %s", figureBody)
	}
	for _, figure := range []struct {
		key, firstLabel, secondLabel string
		graph                        diagramDocument
	}{
		{"system", "压力传感器", "控制器", figures.System},
		{"process", "读取压力", "比较阈值", figures.Process},
	} {
		if figure.graph.Version != 1 || len(figure.graph.Nodes) != 2 || len(figure.graph.Edges) != 1 {
			t.Fatalf("wrong saved %s graph: %+v", figure.key, figure.graph)
		}
		svg := request(http.MethodGet, figurePath+"/svg/"+figure.key, "", http.StatusOK, nil)
		mediaType, _, err := mime.ParseMediaType(svg.Header().Get("Content-Type"))
		if err != nil || mediaType != "image/svg+xml" || svg.Header().Get("X-Source-Artifact-ID") != figureID || svg.Header().Get("X-Manuscript-SHA256") != digest {
			t.Fatalf("wrong %s SVG headers: %v", figure.key, svg.Header())
		}
		text := svg.Body.String()
		if !strings.Contains(text, "<svg ") || !strings.Contains(text, "<line ") || !strings.Contains(text, figure.firstLabel) || !strings.Contains(text, figure.secondLabel) {
			t.Fatalf("wrong %s SVG contents: %s", figure.key, text)
		}
	}
	figuresDigest := fmt.Sprintf("%x", sha256.Sum256(figureBody))
	check := request(http.MethodGet, runPath+"/steps/check/artifact", "", http.StatusOK, nil).Body.String()
	if !strings.Contains(check, digest) || !strings.Contains(check, figuresDigest) || !strings.Contains(check, guide.SHA256) || strings.Contains(check, latest.SHA256) {
		t.Fatalf("checklist does not bind the manuscript, figures, and original guide: %s", check)
	}
	var saved WorkspaceFile
	request(http.MethodPost, runPath+"/manuscript?artifactId="+manuscriptID, "", http.StatusOK, &saved)
	var document WorkspaceDocument
	request(http.MethodGet, projectPath+"/files/versions/"+saved.VersionID, "", http.StatusOK, &document)
	if saved.SHA256 != digest || document.VersionID != saved.VersionID || document.SHA256 != digest || document.Content != body {
		t.Fatalf("workspace version differs from the checked manuscript: %+v", saved)
	}

	legacy, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, workflowRequest{
		RequestID: "legacy", Patent: &patentOptions{Description: description},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a run persisted before the figures step was introduced.
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_runs SET workflow_key = 'patent-disclosure-v1' WHERE project_id = ? AND run_id = ?`, project.ProjectID, legacy.RunID); err != nil {
		t.Fatal(err)
	}
	var restored patentRun
	request(http.MethodGet, workflow+"/runs/"+legacy.RunID, "", http.StatusOK, &restored)
	if restored.RunID != legacy.RunID || restored.WorkflowKey != "patent-disclosure-v1" || len(restored.Steps) != 4 ||
		restored.Steps[3].Key != "check" || restored.Patent == nil || restored.Patent.Description != description {
		t.Fatalf("legacy disclosure should retain its original workflow and settings: %+v", restored)
	}
}
