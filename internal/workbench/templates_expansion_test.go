package workbench

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestTemplatesExpansionCatalogAndContests(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/templates", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/templates returned %d: %s", res.Code, res.Body)
	}
	var catalog struct {
		Items []projectTemplate `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 21 {
		t.Fatalf("expected 21 templates, got %d", len(catalog.Items))
	}
	byID := make(map[string]projectTemplate, len(catalog.Items))
	for _, item := range catalog.Items {
		byID[item.ID] = item
	}
	expectedNew := map[string]struct {
		revision   string
		steps      []string
		checkpoint string
		optional   string
	}{
		"full_research":      {fullResearchWorkflow, []string{"boundary", "literature", "contract", "experiment", "manuscript", "check"}, "contract", "check"},
		"chinese_paper":      {chinesePaperWorkflow, []string{"scope", "outline", "manuscript", "check"}, "outline", "check"},
		"humanities_paper":   {humanitiesPaperWorkflow, []string{"context", "outline", "manuscript", "check"}, "outline", "check"},
		"thesis":             {thesisWorkflow, []string{"spec", "outline", "manuscript", "check"}, "outline", "check"},
		"existing_materials": {existingMaterialsWorkflow, []string{"inventory", "plan", "manuscript", "check"}, "plan", "check"},
		"market_research":    {marketResearchWorkflow, []string{"design", "questionnaire", "analysis", "manuscript"}, "questionnaire", "manuscript"},
		"math_modeling":      {mathModelingWorkflow, []string{"analysis", "decomposition", "modeling", "manuscript"}, "decomposition", "manuscript"},
		"big_data_contest":   {bigDataContestWorkflow, []string{"eda", "baseline", "iteration", "manuscript"}, "baseline", "manuscript"},
	}
	for id, want := range expectedNew {
		tpl, ok := byID[id]
		if !ok {
			t.Fatalf("missing template %s", id)
		}
		if tpl.Revision != want.revision || tpl.Kind != "workflow" || tpl.Defaults.MaxStepRetries != defaultMaxStepRetries {
			t.Fatalf("unexpected metadata for %s: %+v", id, tpl)
		}
		var keys []string
		foundCheckpoint, foundOptional := false, false
		for _, step := range tpl.Steps {
			keys = append(keys, step.Key)
			if step.Key == want.checkpoint && step.Checkpoint == "confirm" {
				foundCheckpoint = true
			}
			if step.Key == want.optional && step.Optional {
				foundOptional = true
			}
		}
		if !slices.Equal(keys, want.steps) || !foundCheckpoint || !foundOptional {
			t.Fatalf("unexpected steps for %s: keys=%v checkpoint=%v optional=%v", id, keys, foundCheckpoint, foundOptional)
		}
	}
	mm := byID["math_modeling"]
	if mm.Defaults.ContestID != "cumcm" || len(mm.Defaults.Contests) != 22 {
		t.Fatalf("expected 22 math modeling contests with default cumcm, got id=%s len=%d", mm.Defaults.ContestID, len(mm.Defaults.Contests))
	}
	wantContests := []string{
		"cumcm", "mcm_icm", "huawei_cup", "mathorcup", "apmcm_zh", "apmcm_en",
		"stat_modeling", "teddy_cup", "tipdm_cert", "huazhong_cup", "huadong_cup",
		"may_day_cup", "shuwei_cup", "zhongqing_cup", "yangtze_delta", "diangong_cup",
		"shenzhen_cup", "huashu_cup", "tianfu_cup", "northeast_liaoning", "mini_mcm", "shuwei_intl",
	}
	seen := make(map[string]mathModelingContest, len(mm.Defaults.Contests))
	for _, contest := range mm.Defaults.Contests {
		seen[contest.ID] = contest
		if contest.Name == "" || (contest.Language != "zh" && contest.Language != "en") ||
			contest.RuleYear != 2026 || contest.VerifiedAt == "" || len(contest.Reminders) == 0 {
			t.Fatalf("incomplete contest metadata: %+v", contest)
		}
	}
	for _, cid := range wantContests {
		if _, ok := seen[cid]; !ok {
			t.Fatalf("missing required contest %s", cid)
		}
	}
	if seen["mcm_icm"].Language != "en" || seen["apmcm_en"].Language != "en" || seen["cumcm"].Language != "zh" {
		t.Fatalf("unexpected contest language defaults: mcm=%s apmcm_en=%s cumcm=%s",
			seen["mcm_icm"].Language, seen["apmcm_en"].Language, seen["cumcm"].Language)
	}
}

func startFixtureModelServer(t *testing.T, fn func(call int, body []byte) (int, string)) (*inference.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := int(calls.Add(1))
		status, text := fn(call, body)
		if status != http.StatusOK {
			http.Error(w, text, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(text)
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", payload)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	client, err := inference.New(inference.Config{BaseURL: server.URL, Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	return client, &calls
}

func TestTemplatesExpansionWorkflowsAndCheckpoints(t *testing.T) {
	dataDir := t.TempDir()
	var failCalls atomic.Bool
	model, _ := startFixtureModelServer(t, func(call int, body []byte) (int, string) {
		if failCalls.Load() {
			return http.StatusInternalServerError, "fixture failure"
		}
		return http.StatusOK, "# 阶段稿件\n\n## 摘要\n\n固定测试摘要。\n\n## 引言\n\n研究背景。\n\n## 方法或证据\n\n核心模型与步骤。\n\n## 结论\n\n主要结论。\n\n## 局限\n\n适用边界。\n\n## AI 使用披露\n\n仅用于辅助润色。\n"
	})

	openHandler := func() (*Store, http.Handler) {
		store, err := Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		return store, NewHandler(store, nil, HandlerConfig{Model: model})
	}
	store, handler := openHandler()
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	reqJSON := func(h http.Handler, method, path, body string, wantStatus int, out any) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != wantStatus {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.Code, wantStatus, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("%s %s: decode JSON: %v", method, path, err)
			}
		}
	}

	waitStatus := func(h http.Handler, projectID, want string) WorkflowRun {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var snap struct {
				Run *WorkflowRun `json:"run"`
			}
			reqJSON(h, http.MethodGet, projectsPath+"/"+projectID+"/workflow", "", http.StatusOK, &snap)
			if snap.Run != nil && snap.Run.Status == want {
				return *snap.Run
			}
			if snap.Run != nil && (snap.Run.Status == taskFailed || snap.Run.Status == taskCancelled) && want != snap.Run.Status {
				t.Fatalf("workflow %s entered unexpected status %s: %+v", projectID, snap.Run.Status, snap.Run)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for workflow %s status %s", projectID, want)
		return WorkflowRun{}
	}

	// 1. 数学建模竞赛（美赛英文规格 + 检查点 + 跨重启恢复 + 保存稿件）
	var mmProject Project
	reqJSON(handler, http.MethodPost, projectsPath,
		`{"requestId":"mm-create","title":"城市应急物资调度优化","templateId":"math_modeling","startMode":"collaborate"}`,
		http.StatusCreated, &mmProject)
	if mmProject.DefaultTurnMode != turnModeConfirm {
		t.Fatalf("expected collaborate startMode to set CONFIRM, got %s", mmProject.DefaultTurnMode)
	}
	var started WorkflowRun
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mmProject.ProjectID+"/workflow",
		`{"requestId":"mm-start","checkpoints":true,"contestId":"mcm_icm","maxStepRetries":3}`,
		http.StatusCreated, &started)
	waiting := waitStatus(handler, mmProject.ProjectID, workflowWaiting)
	if waiting.Contest == nil || waiting.Contest.ID != "mcm_icm" || waiting.Contest.Language != "en" || waiting.MaxStepRetries != 3 {
		t.Fatalf("unexpected math modeling run metadata: %+v", waiting)
	}
	// 重启服务验证检查点与赛事配置持久化
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openHandler()
	var afterRestart struct {
		Run *WorkflowRun `json:"run"`
	}
	reqJSON(handler, http.MethodGet, projectsPath+"/"+mmProject.ProjectID+"/workflow", "", http.StatusOK, &afterRestart)
	if afterRestart.Run == nil || afterRestart.Run.Status != workflowWaiting ||
		afterRestart.Run.Contest == nil || afterRestart.Run.Contest.ID != "mcm_icm" || afterRestart.Run.MaxStepRetries != 3 {
		t.Fatalf("checkpoint or contest lost after restart: %+v", afterRestart.Run)
	}
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mmProject.ProjectID+"/workflow/continue",
		fmt.Sprintf(`{"requestId":"mm-cont","runId":%q,"expectedRevision":%d}`, afterRestart.Run.RunID, afterRestart.Run.Revision),
		http.StatusAccepted, &started)
	completed := waitStatus(handler, mmProject.ProjectID, workflowDone)
	var savedFile WorkspaceFile
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mmProject.ProjectID+"/workflow/"+completed.RunID+"/manuscript",
		"", http.StatusOK, &savedFile)
	doc, err := store.readWorkspaceDocument(savedFile)
	if err != nil || !strings.Contains(doc.Content, "赛事交付核对清单") || !strings.Contains(doc.Content, "美赛 MCM/ICM") {
		t.Fatalf("saved math modeling manuscript missing contest checklist: %v", err)
	}

	// 2. 科研全流程（预选跳过可选步骤 check + 第二轮完整跑通确定性结构检查）
	var frProject Project
	reqJSON(handler, http.MethodPost, projectsPath,
		`{"requestId":"fr-create","title":"多模态检索增强评测","templateId":"full_research"}`,
		http.StatusCreated, &frProject)
	reqJSON(handler, http.MethodPost, projectsPath+"/"+frProject.ProjectID+"/workflow",
		`{"requestId":"fr-start-skip","checkpoints":false,"skippedSteps":["check"]}`,
		http.StatusCreated, &started)
	frDone := waitStatus(handler, frProject.ProjectID, workflowDone)
	if frDone.Steps[5].Key != "check" || frDone.Steps[5].Status != workflowStepSkipped {
		t.Fatalf("expected optional check step to be skipped: %+v", frDone.Steps[5])
	}
	reqJSON(handler, http.MethodPost, projectsPath+"/"+frProject.ProjectID+"/workflow",
		`{"requestId":"fr-start-full","checkpoints":true}`,
		http.StatusCreated, &started)
	frWait := waitStatus(handler, frProject.ProjectID, workflowWaiting)
	reqJSON(handler, http.MethodPost, projectsPath+"/"+frProject.ProjectID+"/workflow/continue",
		fmt.Sprintf(`{"requestId":"fr-cont","runId":%q,"expectedRevision":%d}`, frWait.RunID, frWait.Revision),
		http.StatusAccepted, &started)
	frFullDone := waitStatus(handler, frProject.ProjectID, workflowDone)
	_, checkReport, err := store.workflowArtifact(t.Context(), frProject.ProjectID, frFullDone.RunID, "check")
	if err != nil || !strings.Contains(checkReport, "科研全流程交付要点") || !strings.Contains(checkReport, "稿件 SHA-256") {
		t.Fatalf("unexpected full_research check report: %v %s", err, checkReport)
	}

	// 3. 市场调查（检查点确认 + 重试预算 maxStepRetries=1 耗尽返回 409）
	var mrProject Project
	reqJSON(handler, http.MethodPost, projectsPath,
		`{"requestId":"mr-create","title":"新能源汽车售后满意度调查","templateId":"market_research"}`,
		http.StatusCreated, &mrProject)
	failCalls.Store(true)
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow",
		`{"requestId":"mr-fail-start","checkpoints":false,"maxStepRetries":1}`,
		http.StatusCreated, &started)
	mrFail1 := waitStatus(handler, mrProject.ProjectID, taskFailed)
	// 第 1 次重试（允许，仍失败）
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow/continue",
		fmt.Sprintf(`{"requestId":"mr-retry-1","runId":%q,"expectedRevision":%d}`, mrFail1.RunID, mrFail1.Revision),
		http.StatusAccepted, &started)
	mrFail2 := waitStatus(handler, mrProject.ProjectID, taskFailed)
	// 第 2 次重试超出 maxStepRetries=1 -> 409 WORKFLOW_RETRY_BUDGET_EXCEEDED
	var retryErr struct{ Error apiError }
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow/continue",
		fmt.Sprintf(`{"requestId":"mr-retry-2","runId":%q,"expectedRevision":%d}`, mrFail2.RunID, mrFail2.Revision),
		http.StatusConflict, &retryErr)
	if retryErr.Error.Code != "WORKFLOW_RETRY_BUDGET_EXCEEDED" {
		t.Fatalf("expected WORKFLOW_RETRY_BUDGET_EXCEEDED, got %+v", retryErr)
	}
	// 取消失败轮次后正常跑通市场调查完整流程
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow/cancel",
		fmt.Sprintf(`{"requestId":"mr-cancel","runId":%q,"expectedRevision":%d}`, mrFail2.RunID, mrFail2.Revision),
		http.StatusOK, &started)
	failCalls.Store(false)
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow",
		`{"requestId":"mr-ok-start","checkpoints":true}`,
		http.StatusCreated, &started)
	mrWait := waitStatus(handler, mrProject.ProjectID, workflowWaiting)
	reqJSON(handler, http.MethodPost, projectsPath+"/"+mrProject.ProjectID+"/workflow/continue",
		fmt.Sprintf(`{"requestId":"mr-ok-cont","runId":%q,"expectedRevision":%d}`, mrWait.RunID, mrWait.Revision),
		http.StatusAccepted, &started)
	mrDone := waitStatus(handler, mrProject.ProjectID, workflowDone)
	if mrDone.Steps[3].Status != taskSucceeded {
		t.Fatalf("expected market_research manuscript succeeded, got %+v", mrDone.Steps)
	}
}

func TestTemplatesExpansionProjectBudgetAndMigrationV64(t *testing.T) {
	ctx, root := t.Context(), t.TempDir()
	// 1. 验证 v63 -> v64 升级兼容
	v63Dir := filepath.Join(root, "v63")
	if err := os.MkdirAll(v63Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite", filepath.Join(v63Dir, workspaceDatabase))
	if err != nil {
		t.Fatal(err)
	}
	legacyDB.SetMaxOpenConns(1)
	if err := migrate(legacyDB, 63); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO projects (project_id, request_id, title, created_title, template_id, status, revision, created_at)
		VALUES ('p-v63', 'req-v63', '旧版课题', '旧版课题', 'paper', 'READY', 1, '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	var observedMaxTokens atomic.Int64
	model, _ := startFixtureModelServer(t, func(call int, body []byte) (int, string) {
		var payload struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(body, &payload); err == nil {
			observedMaxTokens.Store(int64(payload.MaxTokens))
		}
		return http.StatusOK, "固定模型回复"
	})

	store, err := Open(v63Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var schemaVer int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVer); err != nil || schemaVer != 64 {
		t.Fatalf("expected schema version 64, got %d (%v)", schemaVer, err)
	}
	upgraded, err := store.getProject(ctx, "p-v63")
	if err != nil || upgraded.MaxModelRuns != 0 || upgraded.MaxOutputTokens != 0 || upgraded.ModelRunsUsed != 0 {
		t.Fatalf("unexpected upgraded v63 project: %+v (%v)", upgraded, err)
	}

	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	callAPI := func(method, path, body string, want int, out any) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.Code, want, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 设置 maxModelRuns=1, maxOutputTokens=512
	var updated Project
	callAPI(http.MethodPatch, projectsPath+"/p-v63",
		`{"requestId":"set-budget","expectedRevision":1,"maxModelRuns":1,"maxOutputTokens":512}`,
		http.StatusOK, &updated)
	if updated.MaxModelRuns != 1 || updated.MaxOutputTokens != 512 || updated.Revision != 2 {
		t.Fatalf("unexpected updated limits: %+v", updated)
	}
	// 非法参数校验
	callAPI(http.MethodPatch, projectsPath+"/p-v63",
		`{"requestId":"bad-budget","expectedRevision":2,"maxModelRuns":1001}`,
		http.StatusBadRequest, nil)
	callAPI(http.MethodPatch, projectsPath+"/p-v63",
		`{"requestId":"bad-tokens","expectedRevision":2,"maxOutputTokens":100}`,
		http.StatusBadRequest, nil)

	// 消耗第 1 次模型调用（对话）
	var turn Turn
	callAPI(http.MethodPost, projectsPath+"/p-v63/turns",
		`{"requestId":"turn-1","text":"请概括研究问题"}`, http.StatusAccepted, &turn)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.getTurn(ctx, "p-v63", turn.TurnID)
		if err == nil && current.Status == "COMPLETED" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if observedMaxTokens.Load() != 512 {
		t.Fatalf("expected project maxOutputTokens 512 sent to model, got %d", observedMaxTokens.Load())
	}
	var inspected Project
	callAPI(http.MethodGet, projectsPath+"/p-v63", "", http.StatusOK, &inspected)
	if inspected.ModelRunsUsed != 1 {
		t.Fatalf("expected modelRunsUsed=1, got %d", inspected.ModelRunsUsed)
	}

	// 第 2 次对话与工作流均应返回 429 MODEL_RUN_BUDGET_EXCEEDED
	var budgetErr struct{ Error apiError }
	callAPI(http.MethodPost, projectsPath+"/p-v63/turns",
		`{"requestId":"turn-2","text":"继续追问"}`, http.StatusTooManyRequests, &budgetErr)
	if budgetErr.Error.Code != "MODEL_RUN_BUDGET_EXCEEDED" {
		t.Fatalf("expected turn MODEL_RUN_BUDGET_EXCEEDED, got %+v", budgetErr)
	}
	callAPI(http.MethodPost, projectsPath+"/p-v63/workflow",
		`{"requestId":"wf-blocked","checkpoints":false}`, http.StatusTooManyRequests, &budgetErr)
	if budgetErr.Error.Code != "MODEL_RUN_BUDGET_EXCEEDED" {
		t.Fatalf("expected workflow MODEL_RUN_BUDGET_EXCEEDED, got %+v", budgetErr)
	}

	// 调高上限后恢复对话与工作流
	callAPI(http.MethodPatch, projectsPath+"/p-v63",
		`{"requestId":"raise-budget","expectedRevision":2,"maxModelRuns":5}`,
		http.StatusOK, &updated)
	callAPI(http.MethodPost, projectsPath+"/p-v63/turns",
		`{"requestId":"turn-3","text":"上限调高后继续"}`, http.StatusAccepted, &turn)
	callAPI(http.MethodPost, projectsPath+"/p-v63/workflow",
		`{"requestId":"wf-ok","checkpoints":true}`, http.StatusCreated, nil)

	// 验证 v64 备份与恢复保留技术上限
	stage := t.TempDir()
	var archive bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, stage, &archive); err != nil {
		t.Fatal(err)
	}
	backupFile := filepath.Join(root, "v64-backup.zip")
	if err := os.WriteFile(backupFile, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(root, "v64-restored")
	if err := RestoreWorkspace(ctx, backupFile, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.getProject(ctx, "p-v63")
	if err != nil || got.MaxModelRuns != 5 || got.MaxOutputTokens != 512 || got.ModelRunsUsed < 2 {
		t.Fatalf("restored v64 project lost technical limits or usage: %+v (%v)", got, err)
	}
}
