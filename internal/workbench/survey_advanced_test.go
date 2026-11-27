package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"research-agent/internal/survey"
)

func TestSurveyAdvancedStatsHTTPReviewMapAndDownload(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	open := func() *Store {
		t.Helper()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	store := open()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "adv-proj", Title: "高级市调统计回归", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}

	// Combined survey CSV supporting all 6 methods + a pending row (row 13) for review recalculation.
	csvV1 := strings.Join([]string{
		"Y,X1,X2,Q1,Q2,Q3,Q4,Q5,Q6,Pos,Neg,TC,C,E,TE,Brand,Price,Rating,_quality_status",
		"5.2,1,2,5,5,4,2,1,2,很喜欢,无所谓,20,35,55,70,A,Low,8.5,",
		"6.1,2,1,4,5,5,1,2,2,1,3,30,45,65,80,A,Low,9.5,",
		"10.4,3,4,5,4,5,3,2,3,喜欢,不喜欢,20,35,55,70,A,High,5.5,",
		"11.2,4,3,3,4,3,2,3,2,1,5,30,45,65,80,A,High,6.5,",
		"16.5,5,6,2,2,1,5,5,4,1,5,20,35,55,70,B,Low,7.0,",
		"16.8,6,5,1,2,2,4,5,5,理所当然,很不喜欢,30,45,65,80,B,Low,8.0,",
		"22.7,7,9,2,1,2,5,4,5,2,5,20,35,55,70,B,High,4.0,",
		"21.5,8,7,3,2,3,4,4,3,无所谓,无所谓,30,45,65,80,B,High,5.0,",
		"25.0,9,8,5,5,5,5,4,5,不喜欢,很喜欢,20,35,55,70,C,Low,6.0,",
		"31.1,10,11,4,4,5,4,5,4,1,1,30,45,65,80,C,Low,5.0,",
		"12.0,3,5,1,1,2,1,2,1,很喜欢,无所谓,20,35,55,70,C,High,2.5,",
		"14.0,4,5,2,1,1,2,1,2,很喜欢,无所谓,30,45,65,80,C,High,3.5,",
		"40.0,12,14,5,5,5,1,1,1,很喜欢,无所谓,15,30,60,85,A,Low,10.0,held",
	}, "\n")
	matV1, _, err := store.saveMaterial(ctx, project.ProjectID, "survey_v1.csv", strings.NewReader(csvV1))
	if err != nil {
		t.Fatal(err)
	}

	// Second version with renamed columns to verify field mapping propagation for advanced stats.
	csvV2 := strings.Replace(csvV1,
		"Y,X1,X2,Q1,Q2,Q3,Q4,Q5,Q6,Pos,Neg,TC,C,E,TE,Brand,Price,Rating,_quality_status",
		"Y_new,X1_new,X2_new,Q1_new,Q2_new,Q3_new,Q4_new,Q5_new,Q6_new,Pos_new,Neg_new,TC_new,C_new,E_new,TE_new,Brand_new,Price_new,Rating_new,_quality_status",
		1)
	matV2, _, err := store.saveMaterial(ctx, project.ProjectID, "survey_v2.csv", strings.NewReader(csvV2))
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	reqJSON := func(method, path, body string, wantCode int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != wantCode {
			t.Fatalf("%s %s: status %d, want %d; body: %s", method, path, res.Code, wantCode, res.Body.String())
		}
		return res
	}

	base := projectsPath + "/" + project.ProjectID + "/surveys"
	fullOpts := `{
		"multipleRegression":{"outcomeColumn":"Y","predictorColumns":["X1","X2"]},
		"factorAnalysis":{"columns":["Q1","Q2","Q3","Q4","Q5","Q6"]},
		"kano":{"functionalColumn":"Pos","dysfunctionalColumn":"Neg"},
		"psm":{"tooCheapColumn":"TC","cheapColumn":"C","expensiveColumn":"E","tooExpensiveColumn":"TE"},
		"conjoint":{"ratingColumn":"Rating","attributeColumns":["Brand","Price"]},
		"cfa":{"constructs":[{"name":"维度一","columns":["Q1","Q2","Q3"]},{"name":"维度二","columns":["Q4","Q5","Q6"]}]}
	}`
	body1 := fmt.Sprintf(`{"requestId":"adv-run-1","materialId":%q,"options":%s}`, matV1.MaterialID, fullOpts)
	res1 := reqJSON(http.MethodPost, base, body1, http.StatusCreated)

	var run1 struct {
		AnalysisID string         `json:"analysisId"`
		Options    survey.Options `json:"options"`
		Report     survey.Report  `json:"report"`
	}
	if err := json.Unmarshal(res1.Body.Bytes(), &run1); err != nil {
		t.Fatal(err)
	}
	if run1.Report.Valid != 12 || run1.Report.Pending != 1 {
		t.Fatalf("unexpected counts before review: valid=%d pending=%d", run1.Report.Valid, run1.Report.Pending)
	}
	if run1.Report.MultipleRegression == nil || run1.Report.MultipleRegression.Fit == nil ||
		run1.Report.FactorAnalysis == nil || run1.Report.FactorAnalysis.Result == nil ||
		run1.Report.Kano == nil || run1.Report.Kano.Result == nil ||
		run1.Report.PSM == nil || run1.Report.PSM.Result == nil ||
		run1.Report.Conjoint == nil || run1.Report.Conjoint.Result == nil ||
		run1.Report.CFA == nil || run1.Report.CFA.Result == nil {
		t.Fatalf("expected all 6 advanced stats results populated")
	}
	kmoBefore := run1.Report.FactorAnalysis.Result.KMO
	kanoDominantBefore := run1.Report.Kano.Result.DominantCode

	// Accept pending row 13 keeping the same advanced options -> triggers recalculation.
	bodyReview := fmt.Sprintf(`{"requestId":"adv-run-2","materialId":%q,"options":{
		"reviews":[{"row":13,"accept":true,"reason":"复核通过纳入计算"}],
		"multipleRegression":{"outcomeColumn":"Y","predictorColumns":["X1","X2"]},
		"factorAnalysis":{"columns":["Q1","Q2","Q3","Q4","Q5","Q6"]},
		"kano":{"functionalColumn":"Pos","dysfunctionalColumn":"Neg"},
		"psm":{"tooCheapColumn":"TC","cheapColumn":"C","expensiveColumn":"E","tooExpensiveColumn":"TE"},
		"conjoint":{"ratingColumn":"Rating","attributeColumns":["Brand","Price"]},
		"cfa":{"constructs":[{"name":"维度一","columns":["Q1","Q2","Q3"]},{"name":"维度二","columns":["Q4","Q5","Q6"]}]}
	}}`, matV1.MaterialID)
	res2 := reqJSON(http.MethodPost, base, bodyReview, http.StatusCreated)
	var run2 struct {
		AnalysisID string        `json:"analysisId"`
		Report     survey.Report `json:"report"`
	}
	if err := json.Unmarshal(res2.Body.Bytes(), &run2); err != nil {
		t.Fatal(err)
	}
	if run2.Report.Valid != 13 || run2.Report.Pending != 0 {
		t.Fatalf("expected valid=13 pending=0 after review, got (%d,%d)", run2.Report.Valid, run2.Report.Pending)
	}
	if run2.Report.MultipleRegression.Included != 13 || run2.Report.FactorAnalysis.Included != 13 ||
		run2.Report.Kano.Included != 13 || run2.Report.PSM.Included != 13 ||
		run2.Report.Conjoint.Included != 13 || run2.Report.CFA.Included != 13 {
		t.Fatalf("expected all 6 methods to recalculate with 13 rows")
	}
	if math.Abs(run2.Report.FactorAnalysis.Result.KMO-kmoBefore) < 1e-6 {
		t.Fatalf("expected KMO to change after accepting row 13, before=%v after=%v", kmoBefore, run2.Report.FactorAnalysis.Result.KMO)
	}
	if kanoDominantBefore != "A" || run2.Report.Kano.Result.DominantCode != "A" {
		t.Fatalf("expected Kano dominant A, got before=%q after=%q", kanoDominantBefore, run2.Report.Kano.Result.DominantCode)
	}

	// Analyze V2 initially so we can map settings from run1 -> V2.
	resV2Init := reqJSON(http.MethodPost, base, fmt.Sprintf(`{"requestId":"adv-v2-init","materialId":%q,"options":{}}`, matV2.MaterialID), http.StatusCreated)
	var runV2Init struct {
		AnalysisID string `json:"analysisId"`
	}
	if err := json.Unmarshal(resV2Init.Body.Bytes(), &runV2Init); err != nil {
		t.Fatal(err)
	}

	cols := []string{"Y", "X1", "X2", "Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Pos", "Neg", "TC", "C", "E", "TE", "Brand", "Price", "Rating"}
	pairs := make([]survey.FieldPair, len(cols))
	for i, c := range cols {
		pairs[i] = survey.FieldPair{From: c, To: c + "_new"}
	}
	mapPayload, _ := json.Marshal(map[string]any{
		"requestId":        "adv-map-v2",
		"targetAnalysisId": runV2Init.AnalysisID,
		"fields":           pairs,
	})
	resMapped := reqJSON(http.MethodPost, base+"/"+run1.AnalysisID+"/map-settings", string(mapPayload), http.StatusCreated)
	var runMapped struct {
		AnalysisID string         `json:"analysisId"`
		Options    survey.Options `json:"options"`
		Report     survey.Report  `json:"report"`
	}
	if err := json.Unmarshal(resMapped.Body.Bytes(), &runMapped); err != nil {
		t.Fatal(err)
	}
	if runMapped.Options.MultipleRegression == nil || runMapped.Options.MultipleRegression.OutcomeColumn != "Y_new" ||
		runMapped.Report.PSM == nil || runMapped.Report.PSM.Result == nil ||
		runMapped.Report.CFA == nil || runMapped.Report.CFA.Result == nil {
		t.Fatalf("mapped settings did not preserve advanced stats: %+v", runMapped.Options)
	}

	// Reopen store and verify historical report GET and JSON download.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{})
	reopened := reqJSON(http.MethodGet, base+"/"+run2.AnalysisID, "", http.StatusOK)
	if reopened.Body.String() != res2.Body.String() {
		t.Fatalf("reopened report differs from saved report")
	}
	downloaded := reqJSON(http.MethodGet, base+"/"+run2.AnalysisID+"/download", "", http.StatusOK)
	if !strings.Contains(downloaded.Header().Get("Content-Disposition"), "survey-"+run2.AnalysisID+".json") {
		t.Fatalf("missing attachment header: %q", downloaded.Header().Get("Content-Disposition"))
	}
	if downloaded.Body.String() != res2.Body.String() {
		t.Fatalf("downloaded JSON differs from saved report")
	}
}
