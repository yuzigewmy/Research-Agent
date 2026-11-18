package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestPaperWorkflowChecksItsSavedManuscript(t *testing.T) {
	const outline = "# Study outline\n\nCompare two observation sites."
	const draft = "# River observations\n\n## Abstract\nThe scope is limited to two sites.\n\n## Methods\nTODO: include the sampling schedule."
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		step := calls.Add(1)
		last := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(last, `"language":"en"`) || !strings.Contains(last, "Compare two observation sites.") ||
			(step == 2 && !strings.Contains(last, "# Saved outline")) {
			http.Error(w, "missing the saved writing inputs", http.StatusBadRequest)
			return
		}
		output := "# Saved outline"
		if step == 2 {
			output = draft
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	defer upstream.Close()
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "paper", Title: "River observations", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	start := workflowRequest{RequestID: "start", Paper: &paperOptions{Language: "en", Outline: outline}}
	run, created, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, start, true)
	if err != nil || !created || run.Language != "en" {
		t.Fatalf("unexpected start receipt: %+v, created %t, error %v", run, created, err)
	}
	changed := start
	changed.Paper = &paperOptions{Language: "en", Outline: "Different outline"}
	if _, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, changed, true); !errors.Is(err, errRequestConflict) {
		t.Fatalf("changed outline replay returned %v", err)
	}
	store.kickTaskQueue(project.ProjectID, model)
	deadline := time.Now().Add(4 * time.Second)
	for run.Status != workflowDone {
		run, err = store.latestWorkflow(ctx, project.ProjectID)
		if err != nil || run.Status == taskFailed || time.Now().After(deadline) {
			t.Fatalf("paper workflow did not finish: %+v, %v", run, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, report, err := store.workflowArtifact(ctx, project.ProjectID, run.RunID, "structure")
	if err != nil || !strings.Contains(report, fmt.Sprintf("%x", sha256.Sum256([]byte(draft)))) ||
		!strings.Contains(report, "摘要：已识别，第 3 行") || !strings.Contains(report, "第 7 行有待补标记") || calls.Load() != 2 {
		t.Fatalf("unexpected structure report: %s, calls %d, error %v", report, calls.Load(), err)
	}
}
