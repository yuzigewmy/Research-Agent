package workbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"research-agent/internal/survey"
)

func TestSurveyWorkbookPinsSelectedSheetAndSourceRows(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project := func(id string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	projectID, otherID := project("河岸调查"), project("其他调查")
	workbook, err := os.ReadFile("testdata/survey-sheets.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := store.saveMaterial(ctx, projectID, "答卷.XLSX", bytes.NewReader(workbook))
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path, body string, wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != wantStatus {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, response.Code, wantStatus, response.Body)
		}
		return response
	}
	base := projectsPath + "/" + projectID
	sheetSuffix := "/materials/" + material.MaterialID + "/survey-sheets"
	var catalog struct{ Sheets []string }
	listed := request(http.MethodGet, base+sheetSuffix, "", http.StatusOK)
	if err := json.Unmarshal(listed.Body.Bytes(), &catalog); err != nil || !slices.Equal(catalog.Sheets, []string{"试填答卷", "正式答卷"}) {
		t.Fatalf("unexpected sheet list: %s (%v)", listed.Body, err)
	}
	request(http.MethodGet, projectsPath+"/"+otherID+sheetSuffix, "", http.StatusNotFound)
	base += "/surveys"
	body := fmt.Sprintf(`{"requestId":"formal","materialId":%q,"options":{"sheet":"正式答卷","reviews":[{"row":2,"accept":true,"reason":"已核对原表"}],"crossTab":{"rowColumn":"站点","columnColumn":"选择"}}}`, material.MaterialID)
	created := request(http.MethodPost, base, body, http.StatusCreated)
	var run struct {
		surveyRunInfo
		Options survey.Options `json:"options"`
		Report  survey.Report  `json:"report"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Options.Sheet != "正式答卷" || run.MaterialSHA256 != material.SHA256 || run.Report.Valid != 3 || !run.Report.Demo {
		t.Fatalf("unexpected workbook report: %s", created.Body)
	}
	var sourceRows []int
	for _, row := range run.Report.Rows {
		sourceRows = append(sourceRows, row.SourceRow)
	}
	if !slices.Equal(sourceRows, []int{3, 5, 6}) || !slices.Contains(run.Report.Rows[1].Reasons, "已核对原表") {
		t.Fatalf("unexpected workbook row mapping: %+v", run.Report.Rows)
	}
	if tab := run.Report.CrossTab; tab == nil || tab.Included != 3 || !slices.Equal(tab.RowLabels, []string{"北岸", "南岸"}) {
		t.Fatalf("unexpected sheet statistics: %+v", tab)
	}
	request(http.MethodPost, base, strings.Replace(body, "正式答卷", "试填答卷", 1), http.StatusConflict)
	request(http.MethodPost, base, fmt.Sprintf(`{"requestId":"no-sheet","materialId":%q}`, material.MaterialID), http.StatusUnprocessableEntity)
	if replay := request(http.MethodPost, base, body, http.StatusOK); !bytes.Equal(replay.Body.Bytes(), created.Body.Bytes()) {
		t.Fatal("workbook replay changed the report")
	}
	if download := request(http.MethodGet, base+"/"+run.AnalysisID+"/download", "", http.StatusOK); !bytes.Equal(download.Body.Bytes(), created.Body.Bytes()) {
		t.Fatal("download did not preserve the selected sheet and original report")
	}
}
