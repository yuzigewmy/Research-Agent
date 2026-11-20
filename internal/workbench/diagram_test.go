package workbench

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagramExportsTheSelectedWorkspaceVersion(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	create := func(title string) string {
		t.Helper()
		project, _, err := store.createProject(t.Context(), createProjectInput{RequestID: title, Title: title, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return projectsPath + "/" + project.ProjectID + "/files"
	}
	base, other := create("河流监测"), create("资料归档")
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, route string, body []byte, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+route, strings.NewReader(string(body)))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, res.Code, status, res.Body)
		}
		return res
	}
	save := func(path, content, baseHash string, status int) WorkspaceDocument {
		t.Helper()
		body, err := json.Marshal(map[string]string{"path": path, "content": content, "baseSha256": baseHash})
		if err != nil {
			t.Fatal(err)
		}
		res := request(http.MethodPut, base+"/text", body, status)
		var result WorkspaceDocument
		if status == http.StatusOK {
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	const path = "diagrams/采样流程.flow.json"
	const original = `{"version":1,"nodes":[{"id":"collect","label":"采样 <A> & B","x":40,"y":60},{"id":"check","label":"复核","x":260,"y":60}],"edges":[{"from":"collect","to":"check"}]}`
	save(path, strings.Replace(original, `"to":"check"`, `"to":"missing"`, 1), "", http.StatusUnprocessableEntity)
	first := save(path, original, "", http.StatusOK)
	second := save(path, strings.Replace(original, "复核", "记录", 1), first.SHA256, http.StatusOK)
	save(path, strings.Replace(original, "复核", "留样", 1), first.SHA256, http.StatusConflict)
	save("notes.md", "## 记录", "", http.StatusOK)
	var list struct{ Items []WorkspaceFile }
	res := request(http.MethodGet, base+"?kind=diagram", nil, http.StatusOK)
	if err := json.Unmarshal(res.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].VersionID != second.VersionID {
		t.Fatalf("diagram list should contain only the latest diagram version: %s, %v", res.Body, err)
	}

	versionPath := "/versions/" + first.VersionID + "/diagram.svg"
	request(http.MethodGet, other+versionPath, nil, http.StatusNotFound)
	exported := request(http.MethodGet, base+versionPath, nil, http.StatusOK)
	disposition, params, err := mime.ParseMediaType(exported.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != "采样流程.svg" ||
		exported.Header().Get("X-Source-SHA256") != first.SHA256 || !strings.HasPrefix(exported.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("SVG download did not identify its source version: %v", exported.Header())
	}
	svg := exported.Body.String()
	if !strings.Contains(svg, "采样 &lt;A&gt; &amp; B") || !strings.Contains(svg, "复核") || strings.Contains(svg, "记录") || !strings.Contains(svg, `<line `) {
		t.Fatalf("SVG did not render the selected nodes and edges as data: %s", svg)
	}
}
