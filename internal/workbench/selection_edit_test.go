package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"research-agent/internal/inference"
)

func TestSelectionEditAppliesOnceToItsSavedVersion(t *testing.T) {
	const selected = "待删除🌊。"
	const source = "# 河流\r\n" + selected + "保留原样。\r\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil ||
			!strings.Contains(input.Messages[1].Content, `"selected":"`+selected+`"`) {
			http.Error(w, "unexpected selection", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", `{"replacement":"","reason":"删除选区。"}`)
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "河流记录", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "paper/main.md", "", source)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(source, selected)
	input := selectionEditInput{RequestID: "edit", StartByte: start, EndByte: start + len(selected), Instruction: "删除这段文字。"}
	broken := input
	broken.StartByte++
	if _, _, err := store.queueSelectionEdit(ctx, project.ProjectID, base, broken, true); !errors.Is(err, errSelectionRange) {
		t.Fatalf("split UTF-8 accepted: %v", err)
	}
	queued, created, err := store.queueSelectionEdit(ctx, project.ProjectID, base, input, true)
	if err != nil || !created || queued.Original != selected {
		t.Fatalf("queue returned %+v, %t, %v", queued, created, err)
	}
	task, err := store.claimQueuedTask(ctx, project.ProjectID)
	if err != nil || !task.selectionEdit || task.TaskID != queued.EditID {
		t.Fatalf("claim returned %+v, %v", task, err)
	}
	output, err := store.generateTaskMarkdown(ctx, model, project.ProjectID, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, project.ProjectID, task, taskSucceeded, "", output); err != nil {
		t.Fatal(err)
	}
	latest, err := store.workspaceFile(ctx, project.ProjectID, base.Path)
	if err != nil || latest.VersionID != base.VersionID {
		t.Fatalf("proposal changed the manuscript: %+v, %v", latest, err)
	}
	applied, err := store.applySelectionEdit(ctx, project.ProjectID, base.VersionID, queued.EditID)
	if err != nil || applied.VersionID == base.VersionID || applied.Content != strings.Replace(source, selected, "", 1) {
		t.Fatalf("apply returned %+v, %v", applied, err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, project.ProjectID, base.Path, applied.SHA256, "后来的版本"); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.applySelectionEdit(ctx, project.ProjectID, base.VersionID, queued.EditID)
	if err != nil || replayed.VersionID != applied.VersionID || replayed.Content != applied.Content {
		t.Fatalf("repeat apply returned %+v, %v", replayed, err)
	}
}
