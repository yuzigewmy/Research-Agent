package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestManuscriptReviewRetriesAgainstOriginalVersion(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message `json:"messages"`
			Model    string              `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		text := input.Messages[len(input.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(text, `"originalFinding":`) {
			if !strings.Contains(text, `"number":1`) || !strings.Contains(text, "采样日期待补。") || strings.Contains(text, "后来改稿") {
				http.Error(w, "incorrect recheck source", http.StatusBadRequest)
				return
			}
			calls.Add(1)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
				`{"verdict":"RESOLVED","reason":"已给出日期。","line":4,"quote":"2020 年 1 月 1 日"}`)
			return
		}
		if strings.Contains(text, `"selected":"在河流上游观测。"`) {
			if input.Model != "fixture-edit" {
				t.Errorf("targeted revision used model %q", input.Model)
			}
			calls.Add(1)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
				`{"replacement":"在河流上游观测，采样日期待补。","reason":"标明缺少的取样时间。"}`)
			return
		}
		if !strings.Contains(text, "3\t在河流上游观测。") {
			http.Error(w, "expected original numbered text", http.StatusBadRequest)
			return
		}
		if input.Model != "fixture-review" {
			t.Errorf("simulated review used model %q", input.Model)
		}
		quote := "在河流上游观测。"
		if calls.Add(1) == 1 {
			quote = "在湖中观测。"
		}
		result := fmt.Sprintf(`{"score":72,"recommendation":"ACCEPT","summary":"方法需要补充。","findings":[{"line":3,"quote":%q,"severity":"major","finding":"缺少时间范围","suggestion":"写明采样日期"}]}`, quote)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", result)
	}))
	defer upstream.Close()
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "review", Title: "河流观测", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "study.md", "", "# 研究稿\n\n在河流上游观测。")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model, ReviewModelName: "fixture-review"})
	versions := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/files/versions/"
	send := func(method, url, body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, url, res.Code, want, res.Body)
		}
		return res.Body.String()
	}
	request := func(method, suffix, body string, want int) string {
		t.Helper()
		return send(method, versions+first.VersionID+"/reviews"+suffix, body, want)
	}
	decode := func(body string) manuscriptReview {
		t.Helper()
		var review manuscriptReview
		if err := json.Unmarshal([]byte(body), &review); err != nil {
			t.Fatal(err)
		}
		return review
	}
	const start = `{"target":"水文学期刊"}`
	review := decode(request(http.MethodPost, "", start, http.StatusAccepted))
	if review.Model != "fixture-review" || review.ReasoningEffort != "" {
		t.Fatalf("unexpected simulated review model receipt: %+v", review)
	}
	detail := "/" + review.ReviewID
	wait := func(want string) manuscriptReview {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			current := decode(request(http.MethodGet, detail, "", http.StatusOK))
			if current.Status == want {
				return current
			}
			if (current.Status != taskQueued && current.Status != taskRunning) || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", want, current)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	failed := wait(taskFailed)
	if failed.Report != "" || !strings.Contains(failed.Message, "原文定位有误") {
		t.Fatalf("invalid quotation was published: %+v", failed)
	}
	const newerText = "# 修订稿\n\n补充信息。\n在河流上游观测。"
	newer, err := store.saveWorkspaceDocument(ctx, project.ProjectID, first.Path, first.SHA256, newerText)
	if err != nil {
		t.Fatal(err)
	}
	retry := fmt.Sprintf(`{"requestId":"retry-review","expectedRevision":%d}`, failed.Revision)
	request(http.MethodPost, detail+"/retry", retry, http.StatusAccepted)
	completed := wait(taskSucceeded)
	if completed.SourceSHA256 != first.SHA256 || !strings.Contains(completed.Report, "第 3 行") ||
		!strings.Contains(completed.Report, "> 在河流上游观测。") || !strings.Contains(completed.Report, "建议修改后再审") ||
		len(completed.Findings) != 1 || completed.Findings[0].Number != 1 {
		t.Fatalf("unexpected report: %+v", completed)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	if replay := decode(request(http.MethodPost, "", start, http.StatusOK)); !reflect.DeepEqual(replay, completed) {
		t.Fatalf("review replay changed: %+v", replay)
	}
	request(http.MethodPost, detail+"/retry", retry, http.StatusOK)
	if report := request(http.MethodGet, detail+"/download", "", http.StatusOK); report != completed.Report || calls.Load() != 2 {
		t.Fatalf("download changed or repeated the model request: %q, %d calls", report, calls.Load())
	}

	handler = NewHandler(store, nil, HandlerConfig{Model: model, EditModelName: "fixture-edit"})
	const editRequestID = "revise-finding"
	editPath := detail + "/findings/1/edits"
	decodeEdit := func(body string) selectionEdit {
		t.Helper()
		var edit selectionEdit
		if err := json.Unmarshal([]byte(body), &edit); err != nil {
			t.Fatal(err)
		}
		return edit
	}
	job := fmt.Sprintf(`{"requestId":%q,"baseVersionId":%q}`, editRequestID, newer.VersionID)
	queued := decodeEdit(request(http.MethodPost, editPath, job, http.StatusAccepted))
	if replay := decodeEdit(request(http.MethodPost, editPath, job, http.StatusOK)); queued.EditID != replay.EditID ||
		queued.Model != "fixture-edit" || replay.Model != queued.Model {
		t.Fatalf("edit receipt changed: %+v, %+v", queued, replay)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		review := decode(request(http.MethodGet, detail, "", http.StatusOK))
		edit := review.Findings[0].Edit
		if edit != nil && edit.Status == taskSucceeded {
			if edit.VersionID != newer.VersionID || edit.Original != "在河流上游观测。" || edit.Replacement == "" {
				t.Fatalf("unexpected edit preview: %+v", edit)
			}
			break
		}
		if edit == nil || (edit.Status != taskRunning && edit.Status != taskQueued) || time.Now().After(deadline) {
			t.Fatalf("expected proposal, got %+v", edit)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current, err := store.workspaceFile(ctx, project.ProjectID, first.Path); err != nil || current.VersionID != newer.VersionID {
		t.Fatalf("preview unexpectedly changed the manuscript: %+v, %v", current, err)
	}
	editURL := versions + newer.VersionID + "/edits/" + queued.EditID
	recheckURL := editURL + "/recheck"
	if body := send(http.MethodGet, recheckURL, "", http.StatusOK); strings.TrimSpace(body) != "null" {
		t.Fatalf("recheck exists before apply: %s", body)
	}
	const checkRequest = `{"requestId":"recheck-this-finding"}`
	send(http.MethodPost, recheckURL, checkRequest, http.StatusConflict)
	handler = NewHandler(store, nil, HandlerConfig{})
	var applied WorkspaceDocument
	applyReceipt := send(http.MethodPost, editURL+"/apply", "", http.StatusOK)
	if err := json.Unmarshal([]byte(applyReceipt), &applied); err != nil || applied.Content != "# 修订稿\n\n补充信息。\n在河流上游观测，采样日期待补。" || calls.Load() != 3 {
		t.Fatalf("wrong applied document or repeated call: %s, %d calls, %v", applyReceipt, calls.Load(), err)
	}
	if replay := send(http.MethodPost, editURL+"/apply", "", http.StatusOK); replay != applyReceipt {
		t.Fatal("apply replay did not return the same saved version")
	}
	if _, err := store.saveWorkspaceDocument(ctx, project.ProjectID, first.Path, applied.SHA256, "# 后来改稿"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	readRecheck := func(body string) FindingRecheck {
		t.Helper()
		var recheck FindingRecheck
		if err := json.Unmarshal([]byte(body), &recheck); err != nil {
			t.Fatal(err)
		}
		return recheck
	}
	pending := readRecheck(send(http.MethodGet, recheckURL, "", http.StatusOK))
	if pending.Status != taskQueued || pending.AppliedVersionID != applied.VersionID {
		t.Fatalf("automatic recheck was not saved with the applied version: %+v", pending)
	}
	if err := store.ResumeQueuedTasks(ctx, model); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for {
		current := readRecheck(send(http.MethodGet, recheckURL, "", http.StatusOK))
		if current.Status == taskSucceeded {
			if current.SourceSHA256 != applied.SHA256 || current.FindingNumber != 1 || current.Verdict != "UNVERIFIABLE" || current.Quote != "" {
				t.Fatalf("recheck did not validate the applied version: %+v", current)
			}
			if replay := readRecheck(send(http.MethodPost, recheckURL, checkRequest, http.StatusOK)); replay.RecheckID != pending.RecheckID || calls.Load() != 4 {
				t.Fatalf("duplicate recheck: %+v, %d calls", replay, calls.Load())
			}
			break
		}
		if (current.Status != taskQueued && current.Status != taskRunning) || time.Now().After(deadline) {
			t.Fatalf("recheck did not complete: %+v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
