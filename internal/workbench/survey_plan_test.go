package workbench

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"research-agent/internal/survey"
)

func TestSurveyAnalysisPlanVersioningReplayIsolationAndReportBinding(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	createProject := func(id string) string {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: id, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ProjectID
	}
	first, second := createProject("campus-survey"), createProject("other-project")

	csv, _, err := store.saveMaterial(ctx, first, "responses.csv", strings.NewReader("Score,Group\n5,A\n4,B\n3,A\n"))
	if err != nil {
		t.Fatal(err)
	}
	otherCSV, _, err := store.saveMaterial(ctx, second, "other.csv", strings.NewReader("Score,Group\n2,A\n"))
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	req := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d, body=%s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}

	firstPlansPath := projectsPath + "/" + first + "/survey-plans"
	secondPlansPath := projectsPath + "/" + second + "/survey-plans"

	// Empty project returns latestVersion=0 and empty items.
	var emptyList surveyPlanList
	if err := json.Unmarshal(req(http.MethodGet, firstPlansPath, "", http.StatusOK).Body.Bytes(), &emptyList); err != nil {
		t.Fatal(err)
	}
	if emptyList.LatestVersion != 0 || len(emptyList.Items) != 0 {
		t.Fatalf("expected empty plan list, got %+v", emptyList)
	}
	// Non-existent project rejected.
	req(http.MethodGet, projectsPath+"/missing-project/survey-plans", "", http.StatusNotFound)

	// Invalid sample size parameter rejected with 422.
	badPlan := `{
		"requestId":"plan-bad",
		"expectedVersion":0,
		"title":"非法置信水平",
		"objective":"验证参数边界",
		"targetPopulation":"测试总体",
		"samplingMethod":"简单随机抽样",
		"plannedMethods":["描述统计"],
		"missingStrategy":"完整配对",
		"sampleSizeInput":{"confidenceLevel":80,"expectedProportion":0.5,"marginOfError":0.05,"responseRate":1}
	}`
	req(http.MethodPost, firstPlansPath, badPlan, http.StatusUnprocessableEntity)

	// Create version 1 with fixed baseline: 95%, p=0.5, e=0.05, N=1000, r=0.8 -> 385 / 278 / 348.
	v1Body := `{
		"requestId":"plan-req-1",
		"expectedVersion":0,
		"title":"春季校园调研初版计划",
		"objective":"核查学生对延后闭馆的支持比例",
		"targetPopulation":"在校全日制学生（N=1000）",
		"samplingMethod":"分层随机抽样",
		"plannedMethods":["描述统计","交叉统计","量表信度（Cronbach α）"],
		"missingStrategy":"必答列缺失超过 20% 判为无效，其余统计按完整配对纳入",
		"notes":"预留 20% 回收损耗",
		"sampleSizeInput":{
			"confidenceLevel":95,
			"expectedProportion":0.5,
			"marginOfError":0.05,
			"populationSize":1000,
			"responseRate":0.8
		}
	}`
	v1Resp := req(http.MethodPost, firstPlansPath, v1Body, http.StatusCreated).Body.String()
	var v1 surveyPlanRecord
	if err := json.Unmarshal([]byte(v1Resp), &v1); err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v1.PlanID == "" || v1.ProjectID != first ||
		v1.SampleSizeResult.Method != survey.SampleSizeMethodCochran ||
		v1.SampleSizeResult.UnadjustedSampleSize != 385 ||
		v1.SampleSizeResult.FinitePopulationAdjusted != 278 ||
		v1.SampleSizeResult.PlannedInvitations != 348 {
		t.Fatalf("unexpected v1 plan: %+v", v1)
	}

	// Idempotent replay with same requestId, expectedVersion and content returns 200 and identical payload.
	if replay := req(http.MethodPost, firstPlansPath, v1Body, http.StatusOK).Body.String(); replay != v1Resp {
		t.Fatalf("idempotent replay mismatch:\ngot:  %s\nwant: %s", replay, v1Resp)
	}
	// Same requestId with different content -> 409 conflict.
	req(http.MethodPost, firstPlansPath, strings.Replace(v1Body, "预留 20% 回收损耗", "变更备注", 1), http.StatusConflict)
	// Same requestId with different expectedVersion -> 409 conflict.
	req(http.MethodPost, firstPlansPath, strings.Replace(v1Body, `"expectedVersion":0`, `"expectedVersion":1`, 1), http.StatusConflict)
	// Stale expectedVersion with new requestId -> 409 VERSION_CONFLICT.
	staleBody := strings.Replace(v1Body, `"plan-req-1"`, `"plan-req-stale"`, 1)
	staleResp := req(http.MethodPost, firstPlansPath, staleBody, http.StatusConflict).Body.String()
	if !strings.Contains(staleResp, "VERSION_CONFLICT") {
		t.Fatalf("expected VERSION_CONFLICT, got %s", staleResp)
	}

	// Create version 2 with 99%, p=0.5, e=0.05, N=1000, r=0.8 -> 664 / 400 / 500.
	v2Body := `{
		"requestId":"plan-req-2",
		"expectedVersion":1,
		"title":"春季校园调研修订版计划",
		"objective":"提高置信水平至 99% 并补充回归分析",
		"targetPopulation":"在校全日制学生（N=1000）",
		"samplingMethod":"分层随机抽样",
		"plannedMethods":["描述统计","交叉统计","一元线性回归"],
		"missingStrategy":"必答列缺失超过 20% 判为无效",
		"notes":"正式施测口径",
		"sampleSizeInput":{
			"confidenceLevel":99,
			"expectedProportion":0.5,
			"marginOfError":0.05,
			"populationSize":1000,
			"responseRate":0.8
		}
	}`
	var v2 surveyPlanRecord
	if err := json.Unmarshal(req(http.MethodPost, firstPlansPath, v2Body, http.StatusCreated).Body.Bytes(), &v2); err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.PlanID == v1.PlanID ||
		v2.SampleSizeResult.UnadjustedSampleSize != 664 ||
		v2.SampleSizeResult.FinitePopulationAdjusted != 400 ||
		v2.SampleSizeResult.PlannedInvitations != 500 {
		t.Fatalf("unexpected v2 plan: %+v", v2)
	}

	// Cross-project isolation: second project has no plans and cannot bind first project's plan.
	var secondList surveyPlanList
	if err := json.Unmarshal(req(http.MethodGet, secondPlansPath, "", http.StatusOK).Body.Bytes(), &secondList); err != nil {
		t.Fatal(err)
	}
	if secondList.LatestVersion != 0 || len(secondList.Items) != 0 {
		t.Fatalf("cross-project leak: %+v", secondList)
	}
	crossSurveyBody := fmt.Sprintf(`{"requestId":"cross-bind","materialId":%q,"options":{"planId":%q}}`, otherCSV.MaterialID, v2.PlanID)
	req(http.MethodPost, projectsPath+"/"+second+"/surveys", crossSurveyBody, http.StatusNotFound)
	// Non-existent project plan creation returns 404.
	req(http.MethodPost, projectsPath+"/missing-project/survey-plans", v1Body, http.StatusNotFound)

	// Create survey run bound to v2 plan.
	surveysBase := projectsPath + "/" + first + "/surveys"
	boundBody := fmt.Sprintf(`{"requestId":"run-with-plan","materialId":%q,"options":{"planId":%q}}`, csv.MaterialID, v2.PlanID)
	boundRaw := req(http.MethodPost, surveysBase, boundBody, http.StatusCreated).Body.String()
	var boundRun struct {
		AnalysisID string         `json:"analysisId"`
		Options    survey.Options `json:"options"`
		Report     survey.Report  `json:"report"`
	}
	if err := json.Unmarshal([]byte(boundRaw), &boundRun); err != nil {
		t.Fatal(err)
	}
	if boundRun.Options.PlanID != v2.PlanID || boundRun.Report.PlanSource == nil {
		t.Fatalf("expected bound planSource in report: %s", boundRaw)
	}
	ps := boundRun.Report.PlanSource
	if ps.PlanID != v2.PlanID || ps.Version != 2 || ps.Title != v2.Title || ps.Objective != v2.Objective ||
		ps.Method != survey.SampleSizeMethodCochran || ps.ConfidenceLevel != 99 ||
		ps.UnadjustedSampleSize != 664 || ps.FinitePopulationAdjusted != 400 || ps.PlannedInvitations != 500 ||
		!reflect.DeepEqual(ps.PlannedMethods, v2.PlannedMethods) || ps.CreatedAt != v2.CreatedAt {
		t.Fatalf("planSource fields mismatch: got %+v", ps)
	}

	// Download JSON preserves exact planSource and options.
	downloaded := req(http.MethodGet, surveysBase+"/"+boundRun.AnalysisID+"/download", "", http.StatusOK).Body.String()
	if downloaded != boundRaw {
		t.Fatalf("download payload changed:\ngot:  %s\nwant: %s", downloaded, boundRaw)
	}

	// Unbound survey report remains backward-compatible without planSource.
	unboundBody := fmt.Sprintf(`{"requestId":"run-no-plan","materialId":%q,"options":{}}`, csv.MaterialID)
	unboundRaw := req(http.MethodPost, surveysBase, unboundBody, http.StatusCreated).Body.String()
	if strings.Contains(unboundRaw, `"planSource"`) || strings.Contains(unboundRaw, `"planId"`) {
		t.Fatalf("unbound report should omit plan fields: %s", unboundRaw)
	}

	// Reopen store and verify history order (v2 then v1) and persisted bound report.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(open(), nil, HandlerConfig{})
	var listed surveyPlanList
	if err := json.Unmarshal(req(http.MethodGet, firstPlansPath, "", http.StatusOK).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.LatestVersion != 2 || len(listed.Items) != 2 || listed.Items[0].Version != 2 || listed.Items[1].Version != 1 {
		t.Fatalf("unexpected persisted plan list: %+v", listed)
	}
	if detail := req(http.MethodGet, surveysBase+"/"+boundRun.AnalysisID, "", http.StatusOK).Body.String(); detail != boundRaw {
		t.Fatalf("reopened report changed: %s", detail)
	}
}

func TestSurveyPlanSchema63UpgradeAndSchema64BackupRestore(t *testing.T) {
	ctx, root := t.Context(), t.TempDir()

	// 1) Upgrade from schema 63 database to schema 64.
	v63Dir := filepath.Join(root, "v63")
	if err := os.MkdirAll(filepath.Join(v63Dir, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	v63DBPath := filepath.Join(v63Dir, workspaceDatabase)
	legacyDB, err := sql.Open("sqlite", v63DBPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDB.SetMaxOpenConns(1)
	if err := migrate(legacyDB, 63); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	legacyStore := &Store{db: legacyDB, objectsDir: filepath.Join(v63Dir, "objects")}
	project, _, err := legacyStore.createProject(ctx, createProjectInput{RequestID: "legacy-63", Title: "旧版问卷项目", TemplateID: "freeform"})
	if err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	csv, _, err := legacyStore.saveMaterial(ctx, project.ProjectID, "legacy.csv", strings.NewReader("Score\n5\n4\n"))
	if err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	// Open with current schema (v64) -> migrates automatically.
	upgraded, err := Open(v63Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	var version int
	if err := upgraded.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != workspaceSchemaVersion {
		t.Fatalf("expected upgraded schema %d, got %d (%v)", workspaceSchemaVersion, version, err)
	}

	spec, err := survey.BuildAnalysisPlanSpec(survey.AnalysisPlanSpec{
		Title:            "升级后样本量计划",
		Objective:        "核对升级与备份恢复",
		TargetPopulation: "目标人群",
		SamplingMethod:   "简单随机抽样",
		PlannedMethods:   []string{"描述统计"},
		MissingStrategy:  "按完整样本纳入",
		SampleSizeInput: survey.SampleSizeInput{
			ConfidenceLevel:    95,
			ExpectedProportion: 0.5,
			MarginOfError:      0.05,
			PopulationSize:     func() *int { v := 1000; return &v }(),
			ResponseRate:       0.8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, created, err := upgraded.createSurveyPlan(ctx, project.ProjectID, "upgrade-plan-1", 0, spec)
	if err != nil || !created || plan.Version != 1 || plan.SampleSizeResult.PlannedInvitations != 348 {
		t.Fatalf("unexpected plan after upgrade: %+v created=%v err=%v", plan, created, err)
	}
	run, created, err := upgraded.createSurveyRun(ctx, project.ProjectID, "upgrade-run-1", csv.MaterialID, survey.Options{PlanID: plan.PlanID})
	if err != nil || !created {
		t.Fatalf("createSurveyRun failed: %v", err)
	}

	// 2) Backup schema 64 workspace and restore to a clean directory.
	var archive bytes.Buffer
	if err := upgraded.writeWorkspaceBackup(ctx, t.TempDir(), &archive); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "v64-backup.zip")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(root, "restored-v64")
	if err := RestoreWorkspace(ctx, archivePath, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })

	listed, err := restored.listSurveyPlans(ctx, project.ProjectID)
	if err != nil || listed.LatestVersion != 1 || len(listed.Items) != 1 || listed.Items[0].PlanID != plan.PlanID ||
		listed.Items[0].SampleSizeResult.FinitePopulationAdjusted != 278 ||
		listed.Items[0].SampleSizeResult.PlannedInvitations != 348 {
		t.Fatalf("restored plans mismatch: %+v (%v)", listed, err)
	}
	restoredRun, err := restored.getSurveyRun(ctx, project.ProjectID, run.AnalysisID)
	if err != nil {
		t.Fatal(err)
	}
	var report survey.Report
	if err := json.Unmarshal(restoredRun.Report, &report); err != nil {
		t.Fatal(err)
	}
	if report.PlanSource == nil || report.PlanSource.PlanID != plan.PlanID || report.PlanSource.Version != 1 || report.PlanSource.PlannedInvitations != 348 {
		t.Fatalf("restored report lost planSource: %+v", report.PlanSource)
	}
}
