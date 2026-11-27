package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestWorkspaceSearchFiltersBeforePagination(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(key string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: key, Title: key, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := create("search-project"), create("other-project")
	save := func(projectID, name, base, content string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, projectID, name, base, content)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := save(project, "Data/河段%_00.md", "", "初稿")
	for i := 1; i <= 50; i++ {
		save(project, fmt.Sprintf("Data/河段%%_%02d.md", i), "", "样本")
	}
	save(project, "Data/河段XY_51.md", "", "相似路径")
	save(project, "Data/河段%_52.csv", "", "样本")
	save(other, "Data/河段%_53.md", "", "其他项目")
	latest := save(project, first.Path, first.SHA256, "更新稿")

	handler := NewHandler(store, nil, HandlerConfig{})
	get := func(suffix string, status int) (page struct {
		Items      []WorkspaceFile
		NextCursor string
	}) {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+project+"/files"+suffix, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s: %d, want %d: %s", suffix, response.Code, status, response.Body)
		}
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page
	}
	query := "?q=" + url.QueryEscape(" DATA/河段%_ ") + "&kind=markdown"
	page := get(query, http.StatusOK)
	if len(page.Items) != 50 || page.NextCursor == "" || page.Items[0].VersionID != latest.VersionID {
		t.Fatalf("unexpected first page: count=%d, cursor=%q", len(page.Items), page.NextCursor)
	}
	last := get(query+"&cursor="+page.NextCursor, http.StatusOK)
	if len(last.Items) != 1 || last.Items[0].Path != "Data/河段%_01.md" || last.NextCursor != "" {
		t.Fatalf("unexpected second page: %+v", last)
	}
	if empty := get("?q=missing", http.StatusOK); len(empty.Items) != 0 {
		t.Fatalf("unexpected search matches: %+v", empty)
	}
	get("?q=%FF", http.StatusBadRequest)
	get("/versions?path="+url.QueryEscape(first.Path)+"&q=Data", http.StatusBadRequest)
}
