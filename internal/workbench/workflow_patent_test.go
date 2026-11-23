package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

管路示意图待补充。

## 建议保护点

核对回流支路与反馈控制器的连接关系。

## 可选实施方案

保留手动核对压力的工作方式。

## 技术效果与待核实项

压力波动幅度仍待复测。`
	)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 || call > 2 {
			t.Errorf("unexpected model request %d", call)
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(prompt, description) || !strings.Contains(prompt, original) || strings.Contains(prompt, replacement) {
			t.Errorf("model request %d did not receive the fixed guide and description", call)
		}
		output := outline
		if call == 2 {
			if !strings.Contains(prompt, "管路反馈方案") {
				t.Error("manuscript request is missing the saved outline")
			}
			output = manuscript
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
	request := func(method, path, body string, status int, out any) string {
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
		return res.Body.String()
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
	assertSteps := func(run WorkflowRun, complete int) {
		t.Helper()
		keys := []string{"sources", "outline", "manuscript", "check"}
		if len(run.Steps) != len(keys) {
			t.Fatalf("wrong disclosure steps: %+v", run.Steps)
		}
		for i, key := range keys {
			want := "PENDING"
			if i < complete {
				want = taskSucceeded
			}
			if step := run.Steps[i]; step.Key != key || step.Status != want || (i < complete && step.ArtifactID == "") {
				t.Fatalf("wrong state for %s: %+v", key, step)
			}
		}
	}
	start := fmt.Sprintf(`{"requestId":"start","patent":{"description":%q,"materialIds":[%q]}}`, description, guide.MaterialID)
	var started patentRun
	request(http.MethodPost, workflow, start, http.StatusCreated, &started)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	settings := checkpoint.Patent
	if checkpoint.RunID != started.RunID || checkpoint.WorkflowKey != "patent-disclosure-v1" || !checkpoint.Checkpoints || calls.Load() != 1 ||
		settings == nil || settings.Description != description || len(settings.MaterialIDs) != 1 || settings.MaterialIDs[0] != guide.MaterialID || len(settings.Materials) != 1 {
		t.Fatalf("wrong disclosure checkpoint: run=%+v, patent=%+v, model calls=%d", checkpoint, settings, calls.Load())
	}
	if selected := settings.Materials[0]; selected.MaterialID != guide.MaterialID || selected.Name != guide.Name || selected.SHA256 != guide.SHA256 {
		t.Fatalf("wrong fixed guide: %+v", selected)
	}
	assertSteps(checkpoint.WorkflowRun, 2)
	runPath := workflow + "/" + checkpoint.RunID
	sources := request(http.MethodGet, runPath+"/steps/sources/artifact", "", http.StatusOK, nil)
	if !strings.Contains(sources, guide.Name) || !strings.Contains(sources, guide.MaterialID) || !strings.Contains(sources, guide.SHA256) {
		t.Fatalf("source list is missing the fixed guide version: %s", sources)
	}

	latest := saveGuide(replacement)
	resume := fmt.Sprintf(`{"requestId":"continue","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, workflow+"/continue", resume, http.StatusAccepted, nil)
	finished := waitFor("COMPLETED")
	if finished.RunID != checkpoint.RunID || calls.Load() != 2 {
		t.Fatalf("wrong continuation receipt: run=%+v, model calls=%d", finished, calls.Load())
	}
	assertSteps(finished.WorkflowRun, 4)
	body := request(http.MethodGet, runPath+"/steps/manuscript/artifact", "", http.StatusOK, nil)
	if strings.TrimSpace(body) != manuscript {
		t.Fatalf("unexpected disclosure manuscript: %q", body)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	check := request(http.MethodGet, runPath+"/steps/check/artifact", "", http.StatusOK, nil)
	if !strings.Contains(check, digest) || !strings.Contains(check, guide.SHA256) || strings.Contains(check, latest.SHA256) {
		t.Fatalf("checklist does not bind the manuscript and original guide: %s", check)
	}
	var saved WorkspaceFile
	request(http.MethodPost, runPath+"/manuscript?artifactId="+finished.Steps[2].ArtifactID, "", http.StatusOK, &saved)
	var document WorkspaceDocument
	request(http.MethodGet, projectPath+"/files/versions/"+saved.VersionID, "", http.StatusOK, &document)
	if saved.SHA256 != digest || document.VersionID != saved.VersionID || document.SHA256 != digest || document.Content != body {
		t.Fatalf("workspace version differs from the checked manuscript: %+v", saved)
	}
}
