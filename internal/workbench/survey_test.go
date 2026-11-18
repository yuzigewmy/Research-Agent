package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"research-agent/internal/survey"
)

func TestSurveyRunReplaysAndDownloadsSavedReport(t *testing.T) {
	ctx, directory := context.Background(), t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	create := func(id string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	first, second := create("Water samples"), create("Field notes")
	csv, _, err := store.saveMaterial(ctx, first, "samples.CSV", strings.NewReader("Score,_quality_status,__synthetic__\n8,,true\n3,held,true\n"))
	if err != nil {
		t.Fatal(err)
	}
	bad, _, err := store.saveMaterial(ctx, first, "broken.csv", strings.NewReader("Score\n\"unterminated\n"))
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d, want %d, body %s", method, path, res.Code, status, res.Body)
		}
		return res
	}
	base := projectsPath + "/" + first + "/surveys"
	body := fmt.Sprintf(`{"requestId":"analysis-1","materialId":%q,"options":{"requiredColumns":["Score"],"reviews":[{"row":2,"accept":true,"reason":"Confirmed"}]}}`, csv.MaterialID)
	created := request(http.MethodPost, base, body, http.StatusCreated).Body.String()
	var run struct {
		AnalysisID     string        `json:"analysisId"`
		MaterialID     string        `json:"materialId"`
		MaterialName   string        `json:"materialName"`
		MaterialSHA256 string        `json:"materialSha256"`
		Valid          int           `json:"valid"`
		Demo           bool          `json:"demo"`
		Report         survey.Report `json:"report"`
	}
	if err := json.Unmarshal([]byte(created), &run); err != nil || run.AnalysisID == "" || run.Valid != 2 || !run.Demo ||
		!run.Report.Demo || run.Report.Valid != 2 || run.MaterialID != csv.MaterialID || run.MaterialName != csv.Name || run.MaterialSHA256 != csv.SHA256 {
		t.Fatalf("unexpected analysis receipt %s (%v)", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{})
	item := base + "/" + run.AnalysisID
	if replay := request(http.MethodPost, base, body, http.StatusOK).Body.String(); replay != created {
		t.Fatalf("replay changed the saved run: %s", replay)
	}
	request(http.MethodPost, base, strings.Replace(body, "Confirmed", "Amended", 1), http.StatusConflict)
	request(http.MethodPost, base, fmt.Sprintf(`{"requestId":"broken","materialId":%q}`, bad.MaterialID), http.StatusUnprocessableEntity)
	request(http.MethodPost, projectsPath+"/"+second+"/surveys", body, http.StatusNotFound)
	request(http.MethodGet, projectsPath+"/"+second+"/surveys/"+run.AnalysisID, "", http.StatusNotFound)
	if list := request(http.MethodGet, base, "", http.StatusOK).Body.String(); !strings.Contains(list, run.AnalysisID) || strings.Contains(list, `"options"`) || strings.Contains(list, `"report"`) {
		t.Fatalf("unexpected list response: %s", list)
	}
	if detail := request(http.MethodGet, item, "", http.StatusOK).Body.String(); detail != created {
		t.Fatalf("detail changed the saved run: %s", detail)
	}
	download := request(http.MethodGet, item+"/download", "", http.StatusOK)
	if !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") || download.Body.String() != created {
		t.Fatalf("unexpected attachment: %s, %s", download.Header(), download.Body)
	}
}
