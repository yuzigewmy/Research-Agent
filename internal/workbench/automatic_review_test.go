package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestAutomaticReviewResumesSavedRoundAndPreservesOriginal(t *testing.T) {
	const target = "水文研究报告"
	const oldLine = "采样间隔没有说明。"
	const newLine = "每月在同一时段采样一次。"
	const original = "# 河流观测\n" + oldLine + "\n所有记录来自同一组站点。\n"
	const revised = "# 河流观测\n" + newLine + "\n所有记录来自同一组站点。\n"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages        []inference.Message `json:"messages"`
			Model           string              `json:"model"`
			ReasoningEffort string              `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing automatic review input", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		call := calls.Add(1)
		if input.Model != "fixture-review" || input.ReasoningEffort != "max" {
			t.Errorf("review call %d used model %q and effort %q", call, input.Model, input.ReasoningEffort)
		}
		finish := "stop"
		var output string
		switch call {
		case 1:
			output = fmt.Sprintf(`{"score":5,"recommendation":"REVISE","summary":"需补充采样频次。","findings":[{"line":2,"quote":%q,"severity":"major","finding":"缺少采样间隔","suggestion":"注明按月采样"}]}`, oldLine)
		case 2:
			output = `{"edits":[{"line":2,"replacement":"不应保存的修订。"}]}`
			finish = "length"
		case 3:
			output = fmt.Sprintf(`{"edits":[{"line":2,"replacement":%q}]}`, newLine)
		case 4:
			output = `{"score":7,"recommendation":"ACCEPT","summary":"采样频次已明确。","findings":[]}`
		default:
			t.Errorf("unexpected model call %d", call)
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		line := oldLine
		if call == 4 {
			line = newLine
		}
		if !strings.Contains(prompt, line) || strings.Contains(prompt, "不应保存的修订。") {
			t.Errorf("model call %d did not read the intended manuscript version", call)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", output, finish)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	var handler http.Handler
	open := func(reviewModel, effort string) *Store {
		t.Helper()
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		handler = NewHandler(store, nil, HandlerConfig{Model: model, ReviewModelName: reviewModel, ReviewReasoningEffort: effort})
		return store
	}
	store := open("fixture-review", "max")
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "river", Title: "河流观测", TemplateID: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "study.md", "", original)
	if err != nil {
		t.Fatal(err)
	}
	projectPath := projectsPath + "/" + project.ProjectID
	base := projectPath + "/files/versions/" + file.VersionID + "/automatic-reviews"
	request := func(method, path, body string, want int, target any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if target != nil {
			if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
				t.Fatalf("decode response %s: %v", res.Body, err)
			}
		}
		return res
	}
	type round struct {
		Number, Score                    int
		VersionID, SHA256, Path, Summary string
		Findings                         []reviewFinding
	}
	type review struct {
		ReviewID, Target, SourceSHA256, Status, StopReason, Report string
		Model, ReasoningEffort                                     string
		Revision, MaxRounds, TargetScore                           int
		Rounds                                                     []round
	}
	start := fmt.Sprintf(`{"requestId":"review","target":%q,"maxRounds":3,"targetScore":6}`, target)
	var started, replayed review
	request(http.MethodPost, base, start, http.StatusAccepted, &started)
	if started.ReviewID == "" || started.Model != "fixture-review" || started.ReasoningEffort != "max" {
		t.Fatalf("unexpected automatic review receipt: %+v", started)
	}
	request(http.MethodPost, base, start, http.StatusOK, &replayed)
	if replayed.ReviewID != started.ReviewID {
		t.Fatalf("start replay returned another review: %+v", replayed)
	}
	detail := base + "/" + started.ReviewID
	waitFor := func(status string) review {
		t.Helper()
		for {
			var current review
			request(http.MethodGet, detail, "", http.StatusOK, &current)
			if current.Status == status {
				return current
			}
			if (current.Status != taskQueued && current.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want %s, got %+v", status, current)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	failed := waitFor(taskFailed)
	if failed.SourceSHA256 != file.SHA256 || len(failed.Rounds) != 1 || failed.Rounds[0].Score != 5 ||
		len(failed.Rounds[0].Findings) != 1 || failed.Rounds[0].Findings[0].Line != 2 || calls.Load() != 2 {
		t.Fatalf("truncated revision lost the saved round: %+v, calls=%d", failed, calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open("fixture-review-new", "low")
	restored := waitFor(taskFailed)
	if restored.Model != started.Model || restored.ReasoningEffort != started.ReasoningEffort ||
		restored.Revision != failed.Revision || len(restored.Rounds) != 1 || restored.Rounds[0].VersionID != failed.Rounds[0].VersionID {
		t.Fatalf("reopening changed the failed review: %+v", restored)
	}
	retry := fmt.Sprintf(`{"requestId":"retry","expectedRevision":%d}`, restored.Revision)
	moved, err := inference.New(inference.Config{BaseURL: upstream.URL + "/changed", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	restoredHandler := handler
	handler = NewHandler(store, nil, HandlerConfig{Model: moved})
	if response := request(http.MethodPost, detail+"/retry", retry, http.StatusConflict, nil); !strings.Contains(response.Body.String(), `"code":"REVIEW_MODEL_CONNECTION_CHANGED"`) {
		t.Fatalf("unexpected retry error: %s", response.Body)
	}
	handler = restoredHandler
	request(http.MethodPost, detail+"/retry", retry, http.StatusAccepted, nil)
	completed := waitFor(taskSucceeded)
	if completed.ReviewID != started.ReviewID || completed.Target != target || completed.SourceSHA256 != file.SHA256 ||
		completed.MaxRounds != 3 || completed.TargetScore != 6 || completed.StopReason != "TARGET_MET" || completed.Report == "" ||
		len(completed.Rounds) != 2 || completed.Rounds[0].Score != 5 || completed.Rounds[1].Score != 7 || calls.Load() != 4 {
		t.Fatalf("review did not resume the saved round and stop at the target: %+v, calls=%d", completed, calls.Load())
	}
	if completed.Rounds[0].VersionID != restored.Rounds[0].VersionID || completed.Rounds[1].VersionID == completed.Rounds[0].VersionID {
		t.Fatalf("retry replaced the saved first round or did not create a new version: %+v", completed.Rounds)
	}
	for i, text := range []string{original, revised} {
		current := completed.Rounds[i]
		download := request(http.MethodGet, fmt.Sprintf("%s/rounds/%d/manuscript", detail, i+1), "", http.StatusOK, nil)
		digest := fmt.Sprintf("%x", sha256.Sum256(download.Body.Bytes()))
		if current.Number != i+1 || current.VersionID == "" || current.Path == "" || current.SHA256 != digest || download.Body.String() != text ||
			!strings.HasPrefix(download.Header().Get("Content-Type"), "text/") || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("unexpected round %d manuscript: %+v, %q", i+1, current, download.Body.String())
		}
	}
	var current WorkspaceDocument
	request(http.MethodGet, projectPath+"/files/text?path=study.md", "", http.StatusOK, &current)
	if current.Content != original || current.VersionID != file.VersionID || current.SHA256 != file.SHA256 {
		t.Fatalf("automatic revision changed the original file: %+v", current)
	}
	request(http.MethodPost, detail+"/retry", retry, http.StatusOK, &replayed)
	if replayed.ReviewID != completed.ReviewID || replayed.Revision != completed.Revision || calls.Load() != 4 {
		t.Fatalf("retry replay dispatched more work: %+v, calls=%d", replayed, calls.Load())
	}
}
