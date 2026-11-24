package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	csv, _, err := store.saveMaterial(ctx, first, "samples.CSV", strings.NewReader(`Score,Station,Choice,Scale A,Scale B,_quality_status,__synthetic__
8,North,Yes,8,7,,true
7,North,No,7,4,,true
3,South,Yes,3,4,held,true
4,South,,,NaN,,true
1,South,No,2,3,held,true
5,North,Yes,4,8,invalid,true
6,South,,1,+Inf,,true
8,South,Yes,3,20,,true
`))
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
	body := fmt.Sprintf(`{"requestId":"analysis-1","materialId":%q,"options":{"requiredColumns":["Score"],"duplicateColumns":["Score"],"reviews":[{"row":3,"accept":true,"reason":"Confirmed"}],"crossTab":{"rowColumn":"station","columnColumn":"Choice"},"reliability":{"columns":[" scale b ","SCALE A"]}}}`, csv.MaterialID)
	created := request(http.MethodPost, base, body, http.StatusCreated).Body.String()
	var run struct {
		AnalysisID     string         `json:"analysisId"`
		MaterialID     string         `json:"materialId"`
		MaterialName   string         `json:"materialName"`
		MaterialSHA256 string         `json:"materialSha256"`
		Valid          int            `json:"valid"`
		Demo           bool           `json:"demo"`
		Options        survey.Options `json:"options"`
		Report         survey.Report  `json:"report"`
	}
	if err := json.Unmarshal([]byte(created), &run); err != nil || run.AnalysisID == "" || run.Valid != 5 || !run.Demo ||
		!run.Report.Demo || run.Report.Valid != 5 || run.Report.Pending != 1 || run.Report.Invalid != 2 ||
		run.MaterialID != csv.MaterialID || run.MaterialName != csv.Name || run.MaterialSHA256 != csv.SHA256 {
		t.Fatalf("unexpected analysis receipt %s (%v)", created, err)
	}
	want := &survey.CrossTab{
		CrossTabOptions: survey.CrossTabOptions{RowColumn: "Station", ColumnColumn: "Choice"},
		Included:        3, Missing: 2, RowLabels: []string{"North", "South"}, ColumnLabels: []string{"No", "Yes"},
		Counts: [][]int{{1, 1}, {0, 1}}, RowTotals: []int{2, 1}, ColumnTotals: []int{1, 2},
	}
	if !reflect.DeepEqual(run.Report.CrossTab, want) {
		t.Fatalf("unexpected cross-tab: got %+v, want %+v", run.Report.CrossTab, want)
	}
	// Variances of the complete responses (8,7), (7,4), and (3,4) are
	// 7 and 3 for the items and 16 for the row totals. Alpha is 2*(1-10/16).
	reliability := run.Report.Reliability
	if reliability == nil || !reflect.DeepEqual(reliability.Columns, []string{"Scale B", "Scale A"}) ||
		reliability.Included != 3 || reliability.Missing != 1 || reliability.NonNumeric != 1 ||
		reliability.Alpha == nil || math.Abs(*reliability.Alpha-0.75) > 1e-12 {
		t.Fatalf("unexpected reliability report: %+v", reliability)
	}
	if run.Options.Reliability == nil || !reflect.DeepEqual(run.Options.Reliability.Columns, []string{" scale b ", "SCALE A"}) {
		t.Fatalf("reliability options changed: %+v", run.Options.Reliability)
	}
	newCSV, _, err := store.saveMaterial(ctx, first, csv.Name, strings.NewReader("Score,Station,Choice\n9,East,Yes\n9,East,Yes\n"))
	if err != nil {
		t.Fatal(err)
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
	request(http.MethodPost, base, strings.Replace(body, `" scale b ","SCALE A"`, `"Scale A","Scale B"`, 1), http.StatusConflict)
	newBody := fmt.Sprintf(`{"requestId":"analysis-2","materialId":%q,"options":{"crossTab":{"rowColumn":"Station","columnColumn":"Choice"}}}`, newCSV.MaterialID)
	var replacement struct {
		AnalysisID     string        `json:"analysisId"`
		MaterialSHA256 string        `json:"materialSha256"`
		Report         survey.Report `json:"report"`
	}
	updated := request(http.MethodPost, base, newBody, http.StatusCreated).Body.Bytes()
	if err := json.Unmarshal(updated, &replacement); err != nil || replacement.AnalysisID == run.AnalysisID ||
		replacement.MaterialSHA256 != newCSV.SHA256 || replacement.Report.CrossTab == nil ||
		!reflect.DeepEqual(replacement.Report.CrossTab.Counts, [][]int{{2}}) {
		t.Fatalf("same-name material did not produce a separate report: %s (%v)", updated, err)
	}
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
