package workbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestManuscriptEvidenceKeepsOriginalSelectionAndMetric(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createProject := func(name string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: name, Title: name, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := createProject("现场观测"), createProject("另一项目")
	save := func(project, path, digest, text string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project, path, digest, text)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	csv := save(project, "data/readings.csv", "", "baseline,variant\n1e-400,2e-400\n")
	const heading = "# 观测\n\n"
	const claim = "方案读数高于基线。"
	paper := save(project, "paper/main.md", "", heading+claim+"\n")
	otherPaper := save(other, paper.Path, "", heading+claim+"\n")
	task, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: project, RequestID: "pair", Action: proposeTask,
		Kind: experimentComparisonTask, Instruction: "核对观察差异", InputVersionIDs: []string{csv.VersionID}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: project, TaskID: task.TaskID,
		RequestID: "confirm", Action: confirmTask, ExpectedRevision: task.Revision}, false); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimQueuedTask(ctx, project, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.runExperimentComparison(ctx, project, claimed.Inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, project, claimed, taskSucceeded, "", result); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{})
	path := func(project, version string) string {
		return projectsPath + "/" + project + "/files/versions/" + version + "/comparison-links"
	}
	request := func(method, route, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, route, res.Code, status, res.Body)
		}
		return res
	}
	decodeLink := func(res *httptest.ResponseRecorder) manuscriptEvidenceLink {
		t.Helper()
		var link manuscriptEvidenceLink
		if err := json.Unmarshal(res.Body.Bytes(), &link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	payload := func(metric string, start int) string {
		return fmt.Sprintf(`{"taskId":%q,"metric":%q,"startByte":%d,"endByte":%d}`, task.TaskID, metric, start, len(heading+claim))
	}
	route := path(project, paper.VersionID)
	body := payload("meanDifference", len(heading))
	request(http.MethodPost, path(other, otherPaper.VersionID), body, http.StatusNotFound)
	request(http.MethodGet, path(other, paper.VersionID), "", http.StatusNotFound)
	request(http.MethodPost, route, payload("meanDifference", len(heading)+1), http.StatusUnprocessableEntity)
	original := decodeLink(request(http.MethodPost, route, body, http.StatusCreated))
	if original.LinkID == "" || original.Value != "1e-400" || original.Text != claim || original.Input.VersionID != csv.VersionID ||
		original.Input.SHA256 != csv.SHA256 || original.SourceChanged {
		t.Fatalf("incorrect evidence link: %+v", original)
	}
	if replayed := decodeLink(request(http.MethodPost, route, body, http.StatusOK)); replayed != original {
		t.Fatalf("replay changed the original link: %+v", replayed)
	}

	save(project, csv.Path, csv.SHA256, "baseline,variant\n1,99\n")
	updated := save(project, paper.Path, paper.SHA256, heading+"下一轮待核对。\n")
	request(http.MethodPost, route, payload("variantMean", len(heading)), http.StatusConflict)
	original.SourceChanged = true
	var list struct{ Items []manuscriptEvidenceLink }
	if err := json.Unmarshal(request(http.MethodGet, route, "", http.StatusOK).Body.Bytes(), &list); err != nil ||
		len(list.Items) != 1 || list.Items[0] != original {
		t.Fatalf("old link lost its evidence or change marker: %+v (%v)", list, err)
	}
	if replayed := decodeLink(request(http.MethodPost, route, body, http.StatusOK)); replayed != original {
		t.Fatalf("replay after a version change returned %+v", replayed)
	}
	if got := request(http.MethodGet, path(project, updated.VersionID), "", http.StatusOK).Body.String(); strings.TrimSpace(got) != `{"items":[]}` {
		t.Fatalf("new manuscript inherited an old evidence link: %s", got)
	}

	var archive bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, t.TempDir(), &archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := restoreWorkspace(ctx, bytes.NewReader(archive.Bytes()), int64(archive.Len()), destination, maxRestoreDatabaseBytes); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	handler = NewHandler(restored, nil, HandlerConfig{})
	if link := decodeLink(request(http.MethodPost, route, body, http.StatusOK)); link != original {
		t.Fatalf("restored evidence link changed: %+v", link)
	}
}
