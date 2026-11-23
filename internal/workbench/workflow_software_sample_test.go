package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestSoftwareRegistrationUsesSampleOnlyAfterConfirmation(t *testing.T) {
	const (
		title       = "河流水位记录软件"
		description = "按测点汇总水位观测记录。"
		guide       = "从站点列表选择河岸测点后导入 CSV。"
		manual      = "# 操作手册：按站点导入，再核对水位读数。"
		sample      = "# 样例\nprint('demo')\n"
	)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 || call > 3 {
			t.Errorf("unexpected model request %d", call)
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		evidence, output := guide, manual
		if call == 3 {
			evidence, output = manual, fmt.Sprintf(`{"source":%q}`, sample)
		}
		for _, text := range []string{title, description, evidence} {
			if !strings.Contains(prompt, text) {
				t.Errorf("model request %d is missing %q", call, text)
			}
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "software", Title: title, TemplateID: "software_registration"})
	if err != nil {
		t.Fatal(err)
	}
	document, _, err := store.saveMaterial(ctx, project.ProjectID, "guide.md", strings.NewReader(guide))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/workflow"
	request := func(method, path, body string, status int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	waitFor := func(status string) WorkflowRun {
		t.Helper()
		for {
			var result struct{ Run *WorkflowRun }
			request(http.MethodGet, "", "", http.StatusOK, &result)
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
	start := func(id string, wantCalls int32) WorkflowRun {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"checkpoints":true,"software":{"description":%q,"materialIds":[%q]}}`, id, description, document.MaterialID)
		request(http.MethodPost, "", body, http.StatusCreated, nil)
		run := waitFor("WAITING_CHECKPOINT")
		if len(run.Steps) != 4 || run.Steps[1].Key != "manuscript" || run.Steps[1].Status != taskSucceeded ||
			run.Steps[2].Key != "program" || run.Steps[2].Status != "PENDING" || calls.Load() != wantCalls {
			t.Fatalf("expected the manual checkpoint, got steps=%+v, model calls=%d", run.Steps, calls.Load())
		}
		return run
	}
	resume := func(id string, checkpoint WorkflowRun) WorkflowRun {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, id, checkpoint.RunID, checkpoint.Revision)
		request(http.MethodPost, "/continue", body, http.StatusAccepted, nil)
		run := waitFor("COMPLETED")
		if run.RunID != checkpoint.RunID {
			t.Fatalf("continue switched runs from %s to %s", checkpoint.RunID, run.RunID)
		}
		return run
	}
	artifact := func(run WorkflowRun, step string) string {
		t.Helper()
		return request(http.MethodGet, "/"+run.RunID+"/steps/"+step+"/artifact", "", http.StatusOK, nil)
	}

	legacy := start("legacy", 1)
	// Represent an existing v1 run at its saved manual checkpoint.
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_runs SET workflow_key = 'software-registration-v1' WHERE project_id = ? AND run_id = ?`, project.ProjectID, legacy.RunID); err != nil {
		t.Fatal(err)
	}
	legacy = resume("resume-legacy", legacy)
	if program := artifact(legacy, "program"); legacy.WorkflowKey != "software-registration-v1" || calls.Load() != 1 ||
		program != "程序材料\n\n本轮未选择源码。请补充用于登记的软件源码。\n" {
		t.Fatalf("v1 should retain the missing-source notice, got %q with %d model calls", program, calls.Load())
	}

	checkpoint := start("sample", 2)
	finished := resume("resume-sample", checkpoint)
	program := artifact(finished, "program")
	if checkpoint.WorkflowKey != "software-registration-v2" || finished.WorkflowKey != checkpoint.WorkflowKey || calls.Load() != 3 ||
		!strings.HasPrefix(program, "示例源码 · 待核对\n\n") {
		t.Fatalf("expected a labeled v2 sample after confirmation, got workflow=%q, calls=%d, program=%q", finished.WorkflowKey, calls.Load(), program)
	}
	sections, err := softwareProgramSections(program)
	if err != nil || len(sections) != 1 || sections[0].label != "example.py" || sections[0].text != sample {
		t.Fatalf("sample did not round-trip through the program materials parser: %+v, %v", sections, err)
	}
	if application := artifact(finished, "application"); !strings.Contains(application, "模型生成的示例（待核对）") {
		t.Fatalf("application checklist is missing the sample source notice: %s", application)
	}
	if font := os.Getenv("SOFTWARE_PDF_TEST_FONT"); font != "" {
		handler = NewHandler(store, nil, HandlerConfig{SoftwarePDFFont: font})
		path := "/" + finished.RunID + "/steps/program/artifacts/" + finished.Steps[2].ArtifactID + "/pdf"
		if pdf := request(http.MethodGet, path, "", http.StatusOK, nil); !strings.HasPrefix(pdf, "%PDF-") {
			t.Fatal("generated example did not export as PDF")
		}
	}
}
