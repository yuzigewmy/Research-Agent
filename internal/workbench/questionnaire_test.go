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
