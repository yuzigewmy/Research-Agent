package workbench

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQuestionnairePublishesCollectsAndExportsResponses(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path, body string, status int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}

	var project Project
	request(http.MethodPost, projectsPath, `{"requestId":"stations","title":"站点观测项目","templateId":"interactive_survey"}`, http.StatusCreated, &project)
	projectPath := projectsPath + "/" + project.ProjectID
	admin := projectPath + "/questionnaire"
	type question struct {
		ID, Label, Type string
		Required        bool
		Options         []string
	}
	type form struct {
		Title, Intro string
		Questions    []question
	}
	type snapshot struct {
		form
		Revision, ResponseCount int
		Status, PublicID        string
	}
	initial := snapshot{Revision: -1, ResponseCount: -1}
	request(http.MethodGet, admin, "", http.StatusOK, &initial)
	if initial.Revision != 0 || initial.Status != "DRAFT" || initial.Questions == nil || len(initial.Questions) != 0 || initial.ResponseCount != 0 {
		t.Fatalf("wrong initial questionnaire: %+v", initial)
	}
	const title, intro, label = "河流观测问卷", "请选择你观测的站点。", "你在哪个站点观测？"
	const questionColumn = "q1 - " + label
	wantForm := form{Title: title, Intro: intro, Questions: []question{{ID: "q1", Label: label, Type: "single", Required: true, Options: []string{"甲", "乙"}}}}
	edit := func(revision int, title string) string {
		return fmt.Sprintf(`{"expectedRevision":%d,"title":%q,"intro":%q,"questions":[{"id":"q1","label":%q,"type":"single","required":true,"options":["甲","乙"]}]}`,
			revision, title, intro, label)
	}
	revisionBody := func(revision int) string { return fmt.Sprintf(`{"expectedRevision":%d}`, revision) }
	var draft, published snapshot
	request(http.MethodPut, admin, edit(initial.Revision, title), http.StatusOK, &draft)
	if draft.Status != "DRAFT" || draft.Revision <= initial.Revision || !reflect.DeepEqual(draft.form, wantForm) {
		t.Fatalf("wrong saved draft: %+v", draft)
	}
	request(http.MethodPost, admin+"/publish", revisionBody(draft.Revision), http.StatusOK, &published)
	if published.Status != "PUBLISHED" || published.Revision <= draft.Revision || published.PublicID == "" || !reflect.DeepEqual(published.form, wantForm) {
		t.Fatalf("wrong published questionnaire: %+v", published)
	}
	request(http.MethodPut, admin, edit(published.Revision, "更改已发布问卷"), http.StatusConflict, nil)
	public := "/api/v1/questionnaires/" + published.PublicID
	res := request(http.MethodGet, public, "", http.StatusOK, nil)
	decoder := json.NewDecoder(res.Body)
	decoder.DisallowUnknownFields()
	var visible form
	if err := decoder.Decode(&visible); err != nil || !reflect.DeepEqual(visible, wantForm) {
		t.Fatalf("unexpected public questionnaire: %+v, %v", visible, err)
	}

	const answer = `{"requestId":"answer","answers":{"q1":"甲"}}`
	var accepted, replayed struct{ ResponseID, SubmittedAt string }
	request(http.MethodPost, public+"/responses", answer, http.StatusCreated, &accepted)
	request(http.MethodPost, public+"/responses", answer, http.StatusOK, &replayed)
	if accepted.ResponseID == "" || accepted.SubmittedAt == "" || replayed != accepted {
		t.Fatalf("response replay changed the receipt: first=%+v, replay=%+v", accepted, replayed)
	}
	request(http.MethodPost, public+"/responses", strings.Replace(answer, "甲", "乙", 1), http.StatusConflict, nil)
	for _, invalid := range []string{
		`{"requestId":"bad-key","answers":{"q1":"甲","unknown":"甲"}}`,
		`{"requestId":"bad-option","answers":{"q1":"丙"}}`,
	} {
		request(http.MethodPost, public+"/responses", invalid, http.StatusUnprocessableEntity, nil)
	}
	var current, closed snapshot
	request(http.MethodGet, admin, "", http.StatusOK, &current)
	request(http.MethodPost, admin+"/close", revisionBody(current.Revision), http.StatusOK, &closed)
	if current.ResponseCount != 1 || closed.ResponseCount != 1 || closed.Status != "CLOSED" || closed.Revision <= current.Revision {
		t.Fatalf("wrong response count or closed state: open=%+v, closed=%+v", current, closed)
	}
	request(http.MethodGet, public, "", http.StatusGone, nil)
	request(http.MethodPost, public+"/responses", `{"requestId":"too-late","answers":{"q1":"乙"}}`, http.StatusGone, nil)

	export := request(http.MethodGet, admin+"/responses.csv", "", http.StatusOK, nil)
	mediaType, _, err := mime.ParseMediaType(export.Header().Get("Content-Type"))
	if err != nil || mediaType != "text/csv" {
		t.Fatalf("wrong CSV content type: %v", export.Header())
	}
	columns := []string{"response_id", "submitted_at", "source", questionColumn}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(export.Body.String(), "\uFEFF"))).ReadAll()
	if err != nil || len(rows) != 2 || !slices.Equal(rows[0], columns) ||
		!slices.Equal(rows[1], []string{accepted.ResponseID, accepted.SubmittedAt, "web", "甲"}) {
		t.Fatalf("CSV does not contain the single accepted response: %q, %v", rows, err)
	}
	var material, sameMaterial Material
	request(http.MethodPost, admin+"/responses/material", "", http.StatusCreated, &material)
	request(http.MethodPost, admin+"/responses/material", "", http.StatusOK, &sameMaterial)
	if material.MaterialID == "" || material.Name != "questionnaire-responses.csv" ||
		material.SHA256 != fmt.Sprintf("%x", sha256.Sum256(export.Body.Bytes())) || sameMaterial != material {
		t.Fatalf("saved CSV does not match the export: first=%+v, replay=%+v", material, sameMaterial)
	}
	var analysis surveyRun
	body := fmt.Sprintf(`{"requestId":"analysis","materialId":%q,"options":{"requiredColumns":[%q]}}`, material.MaterialID, questionColumn)
	request(http.MethodPost, projectPath+"/surveys", body, http.StatusCreated, &analysis)
	if analysis.AnalysisID == "" || analysis.MaterialID != material.MaterialID || analysis.MaterialSHA256 != material.SHA256 ||
		analysis.Total != 1 || analysis.Valid != 1 || analysis.Demo {
		t.Fatalf("analysis did not use the single collected response: %+v", analysis.surveyRunInfo)
	}
}

func TestQuestionnaireBranchingValidationAndSubmission(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, path, body string, status int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}

	var project Project
	request(http.MethodPost, projectsPath, `{"requestId":"branch-survey","title":"分支问卷项目","templateId":"interactive_survey"}`, http.StatusCreated, &project)
	projectPath := projectsPath + "/" + project.ProjectID
	admin := projectPath + "/questionnaire"

	invalidPayloads := []struct {
		name string
		body string
	}{
		{
			name: "jump to self",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"意愿","type":"single","required":true,"options":["是","否"],"branches":[{"option":"是","target":"q1"}]},
				{"id":"q2","label":"补充","type":"text","required":true}
			]}`,
		},
		{
			name: "jump backward cycle",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"第一题","type":"single","required":true,"options":["甲","乙"]},
				{"id":"q2","label":"第二题","type":"single","required":true,"options":["丙","丁"],"branches":[{"option":"丙","target":"q1"}]}
			]}`,
		},
		{
			name: "unknown target",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"第一题","type":"single","required":true,"options":["甲","乙"],"branches":[{"option":"甲","target":"q99"}]},
				{"id":"q2","label":"第二题","type":"text","required":false}
			]}`,
		},
		{
			name: "invalid option",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"第一题","type":"single","required":true,"options":["甲","乙"],"branches":[{"option":"丙","target":"q2"}]},
				{"id":"q2","label":"第二题","type":"text","required":false}
			]}`,
		},
		{
			name: "duplicate branch option",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"第一题","type":"single","required":true,"options":["甲","乙"],"branches":[{"option":"甲","target":"q2"},{"option":"甲","target":"__END__"}]},
				{"id":"q2","label":"第二题","type":"text","required":false}
			]}`,
		},
		{
			name: "text question with branch",
			body: `{"expectedRevision":0,"title":"测试","intro":"","questions":[
				{"id":"q1","label":"开放题","type":"text","required":true,"branches":[{"option":"甲","target":"__END__"}]},
				{"id":"q2","label":"第二题","type":"text","required":false}
			]}`,
		},
	}
	for _, tc := range invalidPayloads {
		t.Run(tc.name, func(t *testing.T) {
			request(http.MethodPut, admin, tc.body, http.StatusBadRequest, nil)
		})
	}

	validDraft := `{
		"expectedRevision": 0,
		"title": "园区服务回访问卷",
		"intro": "请根据实际情况作答。",
		"questions": [
			{
				"id": "q1",
				"label": "本次体验方式",
				"type": "single",
				"required": true,
				"options": ["现场办理", "线上办理", "暂不参与"],
				"branches": [
					{"option": "线上办理", "target": "q3"},
					{"option": "暂不参与", "target": "__END__"}
				]
			},
			{"id": "q2", "label": "现场窗口说明", "type": "text", "required": true},
			{"id": "q3", "label": "整体满意度", "type": "single", "required": true, "options": ["满意", "一般"]},
			{"id": "q4", "label": "其他建议", "type": "text", "required": false}
		]
	}`
	var draft, published questionnaireSnapshot
	request(http.MethodPut, admin, validDraft, http.StatusOK, &draft)
	if len(draft.Questions) != 4 || len(draft.Questions[0].Branches) != 2 {
		t.Fatalf("branches not preserved in saved draft: %+v", draft)
	}
	request(http.MethodPost, admin+"/publish", fmt.Sprintf(`{"expectedRevision":%d}`, draft.Revision), http.StatusOK, &published)
	public := "/api/v1/questionnaires/" + published.PublicID
	var visibleDef questionnaireDefinition
	request(http.MethodGet, public, "", http.StatusOK, &visibleDef)
	if len(visibleDef.Questions) != 4 || len(visibleDef.Questions[0].Branches) != 2 ||
		visibleDef.Questions[0].Branches[0].Target != "q3" || visibleDef.Questions[0].Branches[1].Target != "__END__" {
		t.Fatalf("unexpected public definition branches: %+v", visibleDef)
	}

	// 分支 A：选「线上办理」跳过必答题 q2，进入 q3 和 q4。
	const branchABody = `{"requestId":"branch-a","answers":{"q1":"线上办理","q3":"满意"}}`
	var receiptA, replayA questionnaireReceipt
	request(http.MethodPost, public+"/responses", branchABody, http.StatusCreated, &receiptA)
	request(http.MethodPost, public+"/responses", branchABody, http.StatusOK, &replayA)
	if receiptA != replayA || receiptA.ResponseID == "" {
		t.Fatalf("branch A replay mismatch: first=%+v, replay=%+v", receiptA, replayA)
	}
	// 分支 A 缺少可见必答题 q3 -> 拒绝。
	request(http.MethodPost, public+"/responses", `{"requestId":"branch-a-missing-q3","answers":{"q1":"线上办理"}}`, http.StatusUnprocessableEntity, nil)
	// 分支 A 提交被跳过题 q2 的非空答案 -> 拒绝。
	request(http.MethodPost, public+"/responses", `{"requestId":"branch-a-hidden-q2","answers":{"q1":"线上办理","q2":"不应填写","q3":"满意"}}`, http.StatusUnprocessableEntity, nil)

	// 分支 B：选「暂不参与」直接结束问卷，跳过必答题 q2、q3 和选答题 q4。
	const branchBBody = `{"requestId":"branch-b","answers":{"q1":"暂不参与"}}`
	var receiptB, replayB questionnaireReceipt
	request(http.MethodPost, public+"/responses", branchBBody, http.StatusCreated, &receiptB)
	request(http.MethodPost, public+"/responses", branchBBody, http.StatusOK, &replayB)
	if receiptB != replayB || receiptB.ResponseID == "" {
		t.Fatalf("branch B replay mismatch: first=%+v, replay=%+v", receiptB, replayB)
	}
	// 分支 B 提交被跳过题 q3 的非空答案 -> 拒绝。
	request(http.MethodPost, public+"/responses", `{"requestId":"branch-b-hidden-q3","answers":{"q1":"暂不参与","q3":"满意"}}`, http.StatusUnprocessableEntity, nil)

	// 默认路径：选「现场办理」按题序展示 q2（必答）、q3（必答）、q4（选答）。
	request(http.MethodPost, public+"/responses", `{"requestId":"default-missing-q2","answers":{"q1":"现场办理","q3":"一般"}}`, http.StatusUnprocessableEntity, nil)
	var receiptC questionnaireReceipt
	request(http.MethodPost, public+"/responses", `{"requestId":"default-path","answers":{"q1":"现场办理","q2":"三号窗口","q3":"一般","q4":"等候时间可缩短"}}`, http.StatusCreated, &receiptC)

	// 核对 CSV 导出：完整题序列，被跳过的题目为空单元格。
	export := request(http.MethodGet, admin+"/responses.csv", "", http.StatusOK, nil)
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(export.Body.String(), "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"response_id", "submitted_at", "source", "q1 - 本次体验方式", "q2 - 现场窗口说明", "q3 - 整体满意度", "q4 - 其他建议"}
	if len(rows) != 4 || !slices.Equal(rows[0], wantHeader) {
		t.Fatalf("unexpected CSV header or row count: %q", rows)
	}
	wantRows := [][]string{
		{receiptA.ResponseID, receiptA.SubmittedAt, "web", "线上办理", "", "满意", ""},
		{receiptB.ResponseID, receiptB.SubmittedAt, "web", "暂不参与", "", "", ""},
		{receiptC.ResponseID, receiptC.SubmittedAt, "web", "现场办理", "三号窗口", "一般", "等候时间可缩短"},
	}
	for i, want := range wantRows {
		if !slices.Equal(rows[i+1], want) {
			t.Fatalf("CSV row %d = %q, want %q", i+1, rows[i+1], want)
		}
	}

	// 保存为资料并能进入原有问卷分析。
	var material Material
	request(http.MethodPost, admin+"/responses/material", "", http.StatusCreated, &material)
	var analysis surveyRun
	request(http.MethodPost, projectPath+"/surveys",
		fmt.Sprintf(`{"requestId":"branch-analysis","materialId":%q,"options":{"requiredColumns":["q1 - 本次体验方式"]}}`, material.MaterialID),
		http.StatusCreated, &analysis)
	if analysis.Total != 3 || analysis.Valid != 3 {
		t.Fatalf("unexpected analysis counts: %+v", analysis.surveyRunInfo)
	}
}
