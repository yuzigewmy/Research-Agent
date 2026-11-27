package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProjectArtifactsFiltersAndDownloadsPinnedOriginals(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(title string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: title, Title: title, TemplateID: "paper"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	project, other := create("观测项目"), create("另一项目")
	save := func(project, name, base, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project, name, base, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	old := save(project, "paper/河段%_原稿.md", "", "\ufeff# 初稿\r\n")
	current := save(project, old.Path, old.SHA256, "# 修订稿\n")
	foreign := save(other, old.Path, "", "# 其他项目\n")
	material, _, err := store.saveMaterial(ctx, project, "河段%_图.png", strings.NewReader("示例原件"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyWorkflow(ctx, project, workflowStart, workflowRequest{RequestID: "start", ThroughStep: "outline"}, true); err != nil {
		t.Fatal(err)
	}
	step, err := store.claimQueuedTask(ctx, project, true)
	if err != nil {
		t.Fatal(err)
	}
	const outline = "# 原始大纲\n"
	if err := store.finishTask(ctx, project, step, taskSucceeded, "", taskResult{markdown: outline}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < projectArtifactPageSize; i++ {
		save(project, fmt.Sprintf("data/rows-%02d.csv", i), "", "a,b\n1,2\n")
	}
	const stamp = "2026-01-01T00:00:00.000000000Z"
	if _, err := store.db.ExecContext(ctx, `UPDATE workspace_versions SET created_at = ? WHERE project_id = ?`, stamp, project); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE materials SET created_at = ? WHERE project_id = ?`, stamp, project); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET created_at = ? WHERE project_id = ?`, stamp, project); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	get := func(route string, status int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+route, nil))
		if res.Code != status {
			t.Fatalf("GET %s: got %d, want %d: %s", route, res.Code, status, res.Body)
		}
		return res
	}
	base := projectsPath + "/" + project
	list := func(query string) (page struct {
		Items      []projectArtifact
		NextCursor string
	}) {
		t.Helper()
		if err := json.Unmarshal(get(base+"/artifacts"+query, http.StatusOK).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := list("")
	if len(first.Items) != projectArtifactPageSize || first.NextCursor == "" {
		t.Fatalf("incorrect first page: %d, cursor=%q", len(first.Items), first.NextCursor)
	}
	last := list("?cursor=" + url.QueryEscape(first.NextCursor))
	if len(last.Items) != 4 || last.NextCursor != "" {
		t.Fatalf("incorrect last page: %+v", last)
	}
	seen := map[workspaceBundleItem]bool{}
	for _, item := range append(first.Items, last.Items...) {
		if seen[item.workspaceBundleItem] || item.ID == foreign.VersionID {
			t.Fatalf("unexpected repeated or cross-project result: %+v", item)
		}
		seen[item.workspaceBundleItem] = true
	}
	images := list("?format=image&kind=material")
	if len(images.Items) != 1 || images.Items[0].ID != material.MaterialID || get(images.Items[0].DownloadURL, 200).Body.String() != "示例原件" {
		t.Fatalf("incorrect material result: %+v", images)
	}
	artifacts := list("?kind=artifact&format=markdown&q=PAPER-OUTLINE")
	if len(artifacts.Items) != 1 || artifacts.Items[0].ID != step.TaskID || artifacts.Items[0].Size != int64(len(outline)) || get(artifacts.Items[0].DownloadURL, 200).Body.String() != outline {
		t.Fatalf("incorrect workflow artifact: %+v", artifacts)
	}
	versions := list("?kind=version&format=markdown&q=" + url.QueryEscape("河段%_"))
	if len(versions.Items) != 2 {
		t.Fatalf("incorrect matching versions: %+v", versions)
	}
	for _, item := range versions.Items {
		want := map[string]WorkspaceDocument{old.VersionID: old, current.VersionID: current}[item.ID]
		body := get(item.DownloadURL, http.StatusOK)
		if want.VersionID == "" || body.Body.String() != want.Content || body.Header().Get("X-Source-SHA256") != want.SHA256 {
			t.Fatalf("download changed pinned version: %+v", item)
		}
	}
	get(base+"/files/versions/"+foreign.VersionID+"/download", http.StatusNotFound)
	get(base+"/artifacts?cursor=invalid", http.StatusBadRequest)
}
