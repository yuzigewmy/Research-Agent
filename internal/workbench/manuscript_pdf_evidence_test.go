package workbench

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"research-agent/internal/literature"
)

func TestManuscriptPDFEvidenceKeepsOriginalPageAfterRestore(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(title string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: title, Title: title, TemplateID: "paper"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := create("观测"), create("其他")
	savePDF := func(projectID, body string) Material {
		t.Helper()
		file, _, err := store.saveMaterial(ctx, projectID, "观测.pdf", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	const originalPDF = "%PDF-1.7\noriginal observation\n%%EOF"
	pdf := savePDF(project, originalPDF)
	foreignPDF := savePDF(other, originalPDF)
	const path, heading, claim = "paper/main.md", "# 水温\n\n", "甲站水温已记录。"
	paper, err := store.saveWorkspaceDocument(ctx, project, path, "", heading+claim)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, route string, input any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		if input != nil {
			if err := json.NewEncoder(&body).Encode(input); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, &body)
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, res.Code, want, res.Body)
		}
		return res
	}
	linkRoute := func(projectID, versionID string) string {
		return projectsPath + "/" + projectID + "/files/versions/" + versionID + "/pdf-links"
	}
	route := linkRoute(project, paper.VersionID)
	input := manuscriptPDFInput{MaterialID: pdf.MaterialID, SHA256: pdf.SHA256, Page: 2,
		Quote: "Water temperature at station A", Start: len(heading), End: len(heading + claim)}
	invalid := input
	invalid.MaterialID = foreignPDF.MaterialID
	request(http.MethodPost, route, &invalid, http.StatusNotFound)
	invalid = input
	invalid.SHA256 = strings.Repeat("a", 64)
	request(http.MethodPost, route, &invalid, http.StatusUnprocessableEntity)
	invalid = input
	invalid.Start++
	request(http.MethodPost, route, &invalid, http.StatusUnprocessableEntity)
	request(http.MethodGet, linkRoute(other, paper.VersionID), nil, http.StatusNotFound)
	decodeLink := func(res *httptest.ResponseRecorder) (link manuscriptPDFLink) {
		t.Helper()
		if err := json.Unmarshal(res.Body.Bytes(), &link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	link := decodeLink(request(http.MethodPost, route, &input, http.StatusCreated))
	if link.LinkID == "" || link.manuscriptPDFInput != input || link.Text != claim || link.Name != pdf.Name || link.SourceChanged {
		t.Fatalf("unexpected link: %+v", link)
	}
	saveCitation := func(projectID, doi, title string) Citation {
		t.Helper()
		citation, _, err := store.saveCitation(ctx, projectID, literature.Work{DOI: doi, Title: title, Journal: "Hydrology Notes", Year: 2024, Authors: []string{"Test Author"}})
		if err != nil {
			t.Fatal(err)
		}
		citation.ordinal = 0 // The receipt omits the internal pagination ordinal.
		return citation
	}
	firstCitation := saveCitation(project, "10.5555/first-study", "初始书目")
	secondCitation := saveCitation(project, "10.5555/river-study", "河流观测书目")
	foreignCitation := saveCitation(other, "10.5555/foreign-study", "其他项目书目")
	bindingRoute := route + "/" + link.LinkID + "/citation"
	bind := func(doi string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return request(http.MethodPut, bindingRoute, map[string]string{"doi": doi}, status)
	}
	bind(foreignCitation.DOI, http.StatusNotFound)
	request(http.MethodPut, linkRoute(other, paper.VersionID)+"/"+link.LinkID+"/citation", map[string]string{"doi": foreignCitation.DOI}, http.StatusNotFound)
	link.Citation = &firstCitation
	if bound := decodeLink(bind(firstCitation.DOI, http.StatusOK)); !reflect.DeepEqual(bound, link) {
		t.Fatalf("binding changed the evidence fields: %+v", bound)
	}
	link.Citation = &secondCitation
	if bound := decodeLink(bind("https://doi.org/10.5555/River-Study", http.StatusOK)); !reflect.DeepEqual(bound, link) {
		t.Fatalf("replacement did not return saved literature: %+v", bound)
	}
	savePDF(project, "%PDF-1.7\nrevised observation\n%%EOF")
	newer, err := store.saveWorkspaceDocument(ctx, project, path, paper.SHA256, heading+"后续观测。")
	if err != nil {
		t.Fatal(err)
	}
	link.SourceChanged = true
	bind(firstCitation.DOI, http.StatusConflict)
	if replay := decodeLink(bind(secondCitation.DOI, http.StatusOK)); !reflect.DeepEqual(replay, link) {
		t.Fatalf("repeating the same DOI changed the existing association: %+v", replay)
	}
	if replay := decodeLink(request(http.MethodPost, route, &input, http.StatusOK)); !reflect.DeepEqual(replay, link) {
		t.Fatalf("replay lost the original source: %+v", replay)
	}
	second := input
	second.Page = 3
	request(http.MethodPost, route, &second, http.StatusConflict)
	if current := request(http.MethodGet, linkRoute(project, newer.VersionID), nil, http.StatusOK); strings.TrimSpace(current.Body.String()) != `{"items":[]}` {
		t.Fatalf("new manuscript inherited old evidence: %s", current.Body)
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
	var list struct{ Items []manuscriptPDFLink }
	if err := json.Unmarshal(request(http.MethodGet, route, nil, http.StatusOK).Body.Bytes(), &list); err != nil ||
		len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], link) {
		t.Fatalf("restore lost the saved link: %+v (%v)", list, err)
	}
	download := request(http.MethodGet, projectsPath+"/"+project+"/materials/"+pdf.MaterialID+"/download", nil, http.StatusOK)
	if download.Body.String() != originalPDF {
		t.Fatalf("restore changed the original PDF: %q", download.Body)
	}
}
