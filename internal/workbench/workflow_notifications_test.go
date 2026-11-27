package workbench

import (
	"math"
	"testing"
)

func TestWorkflowNotificationsKeepTheirRunAfterRestart(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "水温记录", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	id := project.ProjectID
	start := workflowRequest{RequestID: "first", ThroughStep: "outline"}
	first, _, err := store.applyWorkflow(ctx, id, workflowStart, start, true)
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.claimQueuedTask(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, id, step, taskSucceeded, "", taskResult{markdown: "# 水温观测大纲"}); err != nil {
		t.Fatal(err)
	}
	if _, applied, err := store.applyWorkflow(ctx, id, workflowStart, start, true); err != nil || applied {
		t.Fatalf("request replay changed the run: applied=%v (%v)", applied, err)
	}
	current, err := store.latestWorkflow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyWorkflow(ctx, id, workflowCancel, workflowRequest{RequestID: "close-first", RunID: first.RunID, ExpectedRevision: current.Revision}, true); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.applyWorkflow(ctx, id, workflowStart, workflowRequest{RequestID: "second"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimQueuedTask(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.listNotifications(ctx, math.MaxInt64, 30)
	if err != nil || len(page.Items) != 2 || page.UnreadCount != 2 {
		t.Fatalf("unexpected workflow notification page: %+v (%v)", page, err)
	}
	if item := page.Items[0]; item.WorkflowRunID != second.RunID || item.Status != taskInterrupted || item.ProjectID != id {
		t.Fatalf("interrupted notification did not identify second run: %+v", item)
	}
	if item := page.Items[1]; item.WorkflowRunID != first.RunID || item.Status != taskSucceeded || item.TaskID != step.TaskID {
		t.Fatalf("range notification did not retain first run: %+v", item)
	}
}
