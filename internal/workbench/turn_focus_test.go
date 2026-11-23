package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestFocusedResearchWaitsForApprovalAndUsesPinnedExcerpts(t *testing.T) {
	const (
		askText     = "请比较两份稿件中的采样记录。"
		followText  = "请总结刚才的聚焦研究。"
		instruction = "比较两个站点的采样条件。"
		approved    = "按更新稿核对两个站点的采样周期。"
		original    = "旧版采样表仅记录第一个站点。"
		updated     = "新版采样表记录了两个站点。"
		notes       = "补充记录说明两个站点使用相同的观测周期。"
		outside     = "这是节选范围之外的旧附录。"
		analysis    = "两个站点的观测周期一致，可比较同一时段的结果。"
		turnBody    = `{"requestId":"focus","mode":"AUTO","text":"` + askText + `"}`
	)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "river", Title: "河流观测", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	save := func(path, base, content string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, path, base, content)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := save("paper/main.md", "", original)
	second := save("paper/notes.markdown", "", notes)
	inputOf := func(doc WorkspaceDocument) taskInput {
		return taskInput{VersionID: doc.VersionID, Path: doc.Path, SHA256: doc.SHA256}
	}

	var proposals, executions, followups atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []inference.Message
			Tools    []struct{ Function inference.Tool }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("missing model input")
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		var prompt strings.Builder
		for _, msg := range input.Messages {
			fmt.Fprintln(&prompt, msg.Content)
		}
		contextText := prompt.String()
		last := input.Messages[len(input.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		switch last {
		case askText:
			proposals.Add(1)
			if len(input.Tools) != 1 || input.Tools[0].Function.Name != "propose_research_task" ||
				!strings.Contains(string(input.Tools[0].Function.Parameters), `"focusVersionIds"`) {
				t.Error("AUTO did not offer focused research")
			}
			for _, value := range []string{`"manuscriptVersions"`, first.Path, first.VersionID, first.SHA256, second.Path, second.VersionID, second.SHA256} {
				if !strings.Contains(contextText, value) {
					t.Errorf("missing manuscript metadata %q", value)
				}
			}
			if strings.Contains(contextText, original) || strings.Contains(contextText, notes) {
				t.Error("the proposal prompt included manuscript text before approval")
			}
			args := fmt.Sprintf(`{"instruction":%q,"focusVersionIds":[%q,%q]}`, instruction, first.VersionID, second.VersionID)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已整理分析建议。\",\"tool_calls\":[{\"index\":0,\"type\":\"function\",\"function\":{\"name\":\"propose_research_task\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", args)
		case followText:
			followups.Add(1)
			if !strings.Contains(contextText, analysis) {
				t.Error("the next turn did not receive the saved analysis")
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已读取分析结果。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			executions.Add(1)
			if len(input.Tools) != 0 || !strings.Contains(last, approved) || !strings.Contains(last, updated) ||
				!strings.Contains(last, notes) || strings.Contains(contextText, original) || strings.Contains(contextText, outside) {
				t.Error("research execution did not use the approved bounded excerpts")
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", analysis)
		}
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID
	request := func(method, path, body string, want int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	var turn, replay Turn
	request(http.MethodPost, "/turns", turnBody, http.StatusAccepted, &turn)
	stream := request(http.MethodGet, "/turns/"+turn.TurnID+"/stream", "", http.StatusOK, nil).Body.String()
	request(http.MethodPost, "/turns", turnBody, http.StatusOK, &replay)
	if replay.TurnID != turn.TurnID || replay.Status != "COMPLETED" || replay.ProposedTaskID == "" ||
		!strings.Contains(stream, `"proposedTaskId":"`+replay.ProposedTaskID+`"`) {
		t.Fatalf("the AUTO turn did not save a research proposal: %s", stream)
	}
	taskPath := "/tasks/" + replay.ProposedTaskID
	readTask := func() Task {
		t.Helper()
		var page taskPage
		request(http.MethodGet, "/tasks?sourceTurnId="+turn.TurnID, "", http.StatusOK, &page)
		if len(page.Items) != 1 || page.Items[0].TaskID != replay.ProposedTaskID || page.Items[0].SourceTurnID != turn.TurnID || page.Items[0].Kind != "focused-research" {
			t.Fatalf("wrong focused research task: %+v", page.Items)
		}
		return page.Items[0]
	}
	task := readTask()
	if task.Status != taskProposed || task.Instruction != instruction || !slices.Equal(task.Inputs, []taskInput{inputOf(first), inputOf(second)}) ||
		proposals.Load() != 1 || executions.Load() != 0 {
		t.Fatalf("research must await approval with its proposed inputs: %+v", task)
	}
	request(http.MethodGet, taskPath+"/artifact", "", http.StatusNotFound, nil)
	latest := save(first.Path, first.SHA256, updated+"\n"+strings.Repeat("。", 3000)+outside)
	confirm := func(id string, revision, status int, out any) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, id, revision)
		request(http.MethodPost, taskPath+"/confirm", body, status, out)
	}
	var conflict struct{ Error apiError }
	confirm("stale", task.Revision, http.StatusConflict, &conflict)
	if conflict.Error.Code != "TASK_INPUT_CHANGED" || executions.Load() != 0 {
		t.Fatalf("stale input was dispatched: %+v", conflict.Error)
	}
	patch := fmt.Sprintf(`{"requestId":"repin","expectedRevision":%d,"instruction":%q,"inputVersionIds":[%q,%q]}`, task.Revision, approved, latest.VersionID, second.VersionID)
	var edited Task
	request(http.MethodPatch, taskPath, patch, http.StatusOK, &edited)
	wantInputs := []taskInput{inputOf(latest), inputOf(second)}
	if edited.TaskID != task.TaskID || edited.Kind != task.Kind || edited.Status != taskProposed || edited.Instruction != approved || !slices.Equal(edited.Inputs, wantInputs) {
		t.Fatalf("edit did not repin the proposed task: %+v", edited)
	}
	confirm("approve", edited.Revision, http.StatusAccepted, nil)
	var complete Task
	for {
		complete = readTask()
		if complete.Status == taskSucceeded {
			break
		}
		if (complete.Status != taskQueued && complete.Status != taskRunning) || ctx.Err() != nil {
			t.Fatalf("focused research did not succeed: %+v", complete)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var receipt Task
	confirm("approve", edited.Revision, http.StatusOK, &receipt)
	if receipt.TaskID != complete.TaskID || receipt.Status != taskSucceeded || receipt.Revision != complete.Revision || !slices.Equal(receipt.Inputs, wantInputs) {
		t.Fatalf("confirmation replay changed the finished research: %+v", receipt)
	}

	artifact := request(http.MethodGet, taskPath+"/artifact", "", http.StatusOK, nil)
	body, manifest, ok := strings.Cut(artifact.Body.String(), "\n## 固定输入\n")
	if artifact.Header().Get("Content-Disposition") != `attachment; filename="focused-research.md"` ||
		!strings.HasPrefix(body, "# 聚焦研究报告\n") || !strings.Contains(body, analysis) || !ok || strings.Contains(manifest, first.VersionID) {
		t.Fatalf("wrong focused research report: %s", artifact.Body)
	}
	for _, input := range wantInputs {
		if !strings.Contains(manifest, input.Path) || !strings.Contains(manifest, input.VersionID) || !strings.Contains(manifest, input.SHA256) {
			t.Fatalf("report did not record the approved version %+v: %s", input, manifest)
		}
	}
	assertDocument := func(path string, want WorkspaceDocument) {
		t.Helper()
		var got WorkspaceDocument
		request(http.MethodGet, path, "", http.StatusOK, &got)
		if inputOf(got) != inputOf(want) || got.Content != want.Content {
			t.Fatalf("research changed manuscript %s at version %s", want.Path, want.VersionID)
		}
	}
	assertDocument("/files/versions/"+first.VersionID, first)
	for _, doc := range []WorkspaceDocument{latest, second} {
		assertDocument("/files/text?path="+url.QueryEscape(doc.Path), doc)
	}
	var next Turn
	follow := `{"requestId":"follow","mode":"CHAT","text":"` + followText + `"}`
	request(http.MethodPost, "/turns", follow, http.StatusAccepted, &next)
	stream = request(http.MethodGet, "/turns/"+next.TurnID+"/stream", "", http.StatusOK, nil).Body.String()
	if len(next.TaskResults) != 1 || next.TaskResults[0].TaskID != task.TaskID || !strings.Contains(stream, `"status":"COMPLETED"`) ||
		proposals.Load() != 1 || executions.Load() != 1 || followups.Load() != 1 {
		t.Fatalf("unexpected result sync or dispatch: results=%+v, proposals=%d, executions=%d, followups=%d", next.TaskResults, proposals.Load(), executions.Load(), followups.Load())
	}
}
