package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestCourseWorkflowsKeepSelectedMaterialsAndSaveManuscript(t *testing.T) {
	const requirements = "按课程评分标准说明采样条件与分析限制。"
	const selectedNotes = "选定版本记录了两个站点的月度观测。"
	const latestNotes = "新版本改成了三个站点的日观测。"
	sections := []string{
		"本文比较两个站点的观测数据。",
		"已有记录覆盖同一观测周期。",
		"两个站点采用同一采样方法。",
		"观测频率限制了结论的适用范围。",
		"后续应增加重复观测次数。",
	}
	outputs := append([]string{"- 先说明观测范围，再比较记录。"}, sections...)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(w, "missing course prompt", http.StatusBadRequest)
			return
		}
		call := int(calls.Add(1))
		output := "汇总本轮已保存的资料。"
		if call <= len(outputs) {
			prompt := input.Messages[len(input.Messages)-1].Content
			if !strings.Contains(prompt, requirements) || !strings.Contains(prompt, selectedNotes) || strings.Contains(prompt, latestNotes) {
				t.Errorf("course paper call %d received the wrong requirements or material version", call)
			}
			output = outputs[call-1]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, status int, target any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if target != nil {
			if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
				t.Fatalf("decode %q: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	create := func(template, title string) Project {
		t.Helper()
		var project Project
		body := fmt.Sprintf(`{"requestId":%q,"title":%q,"templateId":%q}`, template, title, template)
		request(http.MethodPost, projectsPath, body, http.StatusCreated, &project)
		return project
	}
	type courseRun struct {
		WorkflowRun
		Course *struct {
			TargetCharacters                         int
			Language, Discipline, Method, ReportType string
			Requirements                             string
			MaterialIDs                              []string
			Materials                                []Material
		} `json:"course"`
	}
	waitFor := func(path, status string) courseRun {
		t.Helper()
		for {
			var snapshot struct{ Run *courseRun }
			request(http.MethodGet, path, "", http.StatusOK, &snapshot)
			if snapshot.Run != nil && snapshot.Run.Status == status {
				return *snapshot.Run
			}
			if snapshot.Run == nil || (snapshot.Run.Status != taskQueued && snapshot.Run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	paper := create("course_paper", "河流观测课程论文")
	paperPath := projectsPath + "/" + paper.ProjectID
	workflow := paperPath + "/workflow"
	selected, _, err := store.saveMaterial(ctx, paper.ProjectID, "stations.txt", strings.NewReader(selectedNotes))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"requestId":"bad-target","course":{"targetCharacters":-1}}`,
		`{"requestId":"bad-method","course":{"method":"unknown"}}`,
	} {
		request(http.MethodPost, workflow, body, http.StatusBadRequest, nil)
	}
	start := fmt.Sprintf(`{"requestId":"start-paper","checkpoints":true,"course":{"discipline":"engineering","method":"experiment","requirements":%q,"materialIds":[%q]}}`, requirements, selected.MaterialID)
	request(http.MethodPost, workflow, start, http.StatusCreated, nil)
	checkpoint := waitFor(workflow, workflowWaiting)
	course := checkpoint.Course
	if !checkpoint.Checkpoints || course == nil || course.TargetCharacters != 8000 || course.Language != "zh" ||
		course.Discipline != "engineering" || course.Method != "experiment" || course.Requirements != requirements ||
		len(course.MaterialIDs) != 1 || course.MaterialIDs[0] != selected.MaterialID || len(course.Materials) != 1 || calls.Load() != 1 {
		t.Fatalf("wrong course paper settings at the outline checkpoint: course=%+v, calls=%d", course, calls.Load())
	}
	material := course.Materials[0]
	if material.MaterialID != selected.MaterialID || material.Name != selected.Name || material.SHA256 != selected.SHA256 {
		t.Fatalf("run did not record the selected material: %+v", material)
	}
	if len(checkpoint.Steps) != 8 || checkpoint.Steps[0].Key != "outline" || checkpoint.Steps[0].Status != taskSucceeded || checkpoint.Steps[1].Status != "PENDING" {
		t.Fatalf("outline did not pause for confirmation: %+v", checkpoint.Steps)
	}
	latest, _, err := store.saveMaterial(ctx, paper.ProjectID, selected.Name, strings.NewReader(latestNotes))
	if err != nil {
		t.Fatal(err)
	}
	continuation := fmt.Sprintf(`{"requestId":"continue-paper","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, workflow+"/continue", continuation, http.StatusAccepted, nil)
	completed := waitFor(workflow, workflowDone)
	paperKeys := []string{"outline", "abstract", "background", "method", "analysis", "conclusion", "manuscript", "check"}
	if completed.RunID != checkpoint.RunID || len(completed.Steps) != len(paperKeys) || calls.Load() != int32(len(outputs)) {
		t.Fatalf("course paper did not complete the original run with one model call per writing step: %+v, calls=%d", completed, calls.Load())
	}
	for i, key := range paperKeys {
		if step := completed.Steps[i]; step.Key != key || step.Status != taskSucceeded || step.ArtifactID == "" {
			t.Fatalf("missing course paper artifact for %s: %+v", key, step)
		}
	}
	runPath := workflow + "/" + completed.RunID
	manuscript := request(http.MethodGet, runPath+"/steps/manuscript/artifact", "", http.StatusOK, nil)
	remaining := manuscript
	for _, section := range sections {
		_, after, found := strings.Cut(remaining, section)
		if !found {
			t.Fatalf("assembled manuscript is missing or misorders section %q", section)
		}
		remaining = after
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript)))
	check := request(http.MethodGet, runPath+"/steps/check/artifact", "", http.StatusOK, nil)
	if !strings.Contains(check, digest) || !strings.Contains(check, selected.SHA256) || strings.Contains(check, latest.SHA256) {
		t.Fatalf("delivery check did not bind the manuscript and the selected material: %s", check)
	}
	var saved WorkspaceFile
	request(http.MethodPost, runPath+"/manuscript?artifactId="+url.QueryEscape(completed.Steps[6].ArtifactID), "", http.StatusOK, &saved)
	var document WorkspaceDocument
	request(http.MethodGet, paperPath+"/files/versions/"+url.PathEscape(saved.VersionID), "", http.StatusOK, &document)
	if document.Content != manuscript || document.SHA256 != digest || saved.SHA256 != digest {
		t.Fatal("workspace version differs from the checked manuscript")
	}

	report := create("course_report", "河流观测课程报告")
	reportWorkflow := projectsPath + "/" + report.ProjectID + "/workflow"
	request(http.MethodPost, reportWorkflow, `{"requestId":"start-report","throughStep":"facts"}`, http.StatusCreated, nil)
	defaults := waitFor(reportWorkflow, workflowRangeCompleted)
	if defaults.Checkpoints || defaults.Course == nil || defaults.Course.TargetCharacters != 10000 || defaults.Course.Language != "zh" ||
		defaults.Course.Discipline != "auto" || defaults.Course.ReportType != "auto" {
		t.Fatalf("wrong default course report settings: run=%+v, course=%+v", defaults, defaults.Course)
	}
	reportKeys := []string{"facts", "outline", "overview", "process", "results", "discussion", "conclusion", "manuscript", "check"}
	if len(defaults.Steps) != len(reportKeys) {
		t.Fatalf("wrong course report steps: %+v", defaults.Steps)
	}
	for i, key := range reportKeys {
		if defaults.Steps[i].Key != key {
			t.Fatalf("report step %d is %q, want %q", i+1, defaults.Steps[i].Key, key)
		}
	}
}
