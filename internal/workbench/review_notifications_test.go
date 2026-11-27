package workbench

import (
	"math"
	"testing"
)

func TestReviewNotificationsRetainOriginalManuscriptVersion(t *testing.T) {
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
	projectID := project.ProjectID
	paper, err := store.saveWorkspaceDocument(ctx, projectID, "paper/main.md", "", "# 水温记录\n\n原始观测。\n")
	if err != nil {
		t.Fatal(err)
	}
	model := modelSnapshot{Model: "fixture"}
	simulation, _, err := store.startManuscriptReview(ctx, projectID, paper.WorkspaceFile, "水文期刊", model)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimQueuedTask(ctx, projectID, true)
	if err != nil || claimed.TaskID != simulation.ReviewID {
		t.Fatalf("claim simulation: %+v (%v)", claimed, err)
	}
	if err := store.finishTask(ctx, projectID, claimed, taskSucceeded, "", taskResult{markdown: "# 模拟评审\n"}); err != nil {
		t.Fatal(err)
	}
	automatic, _, err := store.startAutomaticReview(ctx, projectID, paper.VersionID, "automatic",
		automaticReviewOptions{Target: "水文期刊", MaxRounds: 4, TargetScore: 6}, model)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = store.claimQueuedTask(ctx, projectID, true)
	if err != nil || claimed.TaskID != automatic.ReviewID {
		t.Fatalf("claim automatic review: %+v (%v)", claimed, err)
	}
	if _, err := store.saveWorkspaceDocument(ctx, projectID, paper.Path, paper.SHA256, "# 水温记录\n\n更新后的观测。\n"); err != nil {
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
		t.Fatalf("unexpected review notifications: %+v (%v)", page, err)
	}
	for index, want := range []struct{ id, kind, status string }{
		{automatic.ReviewID, "automatic", taskInterrupted},
		{simulation.ReviewID, "simulation", taskSucceeded},
	} {
		item := page.Items[index]
		if item.ProjectID != projectID || item.TaskID != want.id || item.ReviewVersionID != paper.VersionID ||
			item.ReviewKind != want.kind || item.Status != want.status || item.WorkflowRunID != "" {
			t.Fatalf("notice did not retain review target: %+v", item)
		}
	}
}
