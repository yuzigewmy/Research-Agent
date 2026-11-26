package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExperimentComparisonUsesCompletePairs(t *testing.T) {
	csv := "\ufeffseed,variant,baseline\n7,4,2\n8,9,\n9,8,4\n10,,100\n"
	got, err := compareExperimentMetrics(t.Context(), csv)
	if err != nil {
		t.Fatal(err)
	}
	want := experimentMetrics{PairedRows: 2, SkippedRows: 2, BaselineMean: "3", VariantMean: "6", MeanDifference: "3"}
	if got != want {
		t.Fatalf("paired metrics: got %+v, want %+v", got, want)
	}
	for _, test := range []struct{ csv, difference string }{
		{"baseline,variant\n0,1e40\n0,1e20\n0,1\n0,-1e20\n0,-1e40\n", "0.2"},
		{"baseline,variant\n9007199254740992,9007199254740993\n", "1"},
		{"baseline,variant\n1e-400,2e-400\n", "1e-400"},
		{"baseline,variant\n1,2e+0000\n", "1"},
	} {
		metrics, err := compareExperimentMetrics(t.Context(), test.csv)
		if err != nil || metrics.MeanDifference.String() != test.difference {
			t.Errorf("expected exact difference %s, got %+v (%v)", test.difference, metrics, err)
		}
	}
	for _, test := range []struct{ csv, message string }{
		{"baseline,baseline,variant\n1,2,3", "重复"},
		{"baseline,score\n1,2", "variant"},
		{"baseline,variant\n1,NaN", "第 2 行"},
		{"baseline,variant\n1,1e101", "第 2 行"},
		{"baseline,variant\n1,1e-1000", "第 2 行"},
		{"baseline,variant,note\n1,2,\xff", "UTF-8"},
		{"baseline,variant\n1,", "没有完整"},
		{"baseline,variant\n" + strings.Repeat("1,2\n", 10_001), "10,000"},
	} {
		if _, err := compareExperimentMetrics(t.Context(), test.csv); err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("expected %q in diagnostic, got %v", test.message, err)
		}
	}
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := compareExperimentMetrics(stopped, csv); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled calculation returned %v", err)
	}
}

func TestExperimentComparisonResumesAndDownloadsPinnedResult(t *testing.T) {
	ctx, directory := t.Context(), t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "study", Title: "水温对比", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := store.createProject(ctx, createProjectInput{RequestID: "other", Title: "另一项目", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	base := projectsPath + "/" + project.ProjectID + "/tasks"
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(method, route, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, route, response.Code, status, response.Body)
		}
		return response
	}
	decodeTask := func(response *httptest.ResponseRecorder) Task {
		t.Helper()
		var task Task
		if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	save := func(previous, rows string) WorkspaceDocument {
		t.Helper()
		doc, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "data/readings.csv", previous, "baseline,variant\n"+rows)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := save("", "1,2\n")
	const proposal = `{"requestId":"compare","kind":"experiment-comparison","instruction":"核对观测差异","inputVersionIds":[%q]}`
	body := fmt.Sprintf(proposal, first.VersionID)
	task := decodeTask(request(http.MethodPost, base, body, http.StatusCreated))
	if task.Kind != experimentComparisonTask || task.Status != taskProposed || len(task.Inputs) != 1 || task.Inputs[0].SHA256 != first.SHA256 {
		t.Fatalf("unexpected proposal %+v", task)
	}
	request(http.MethodPost, projectsPath+"/"+other.ProjectID+"/tasks", body, http.StatusUnprocessableEntity)
	request(http.MethodPost, base, strings.Replace(body, `"kind":"experiment-comparison",`, "", 1), http.StatusConflict)
	second := save(first.SHA256, strings.Repeat("2,4\n4,8\n", 26)+"\"9\nignored\",\n,\n")
	taskPath := base + "/" + task.TaskID
	request(http.MethodPost, taskPath+"/confirm", fmt.Sprintf(`{"requestId":"old","expectedRevision":%d}`, task.Revision), http.StatusConflict)
	task = decodeTask(request(http.MethodPatch, taskPath,
		fmt.Sprintf(`{"requestId":"edit","expectedRevision":%d,"instruction":"核对观测差异","inputVersionIds":[%q]}`, task.Revision, second.VersionID), http.StatusOK))
	confirmation := taskRequest{ProjectID: project.ProjectID, TaskID: task.TaskID, RequestID: "confirm", Action: confirmTask, ExpectedRevision: task.Revision}
	queued, created, err := store.applyTaskRequest(ctx, confirmation, false)
	if err != nil || !created || queued.Status != taskQueued {
		t.Fatalf("local calculation was not queued: %+v (%v)", queued, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	if err := store.ResumeQueuedTasks(ctx, nil); err != nil {
		t.Fatal(err)
	}
	store.tasks.wg.Wait()
	task = decodeTask(request(http.MethodGet, taskPath, "", http.StatusOK))
	if task.Status != taskSucceeded {
		t.Fatalf("queued calculation did not resume: %+v", task)
	}
	third := save(second.SHA256, "20,80\n")
	replayed, applied, err := store.applyTaskRequest(ctx, confirmation, false)
	if err != nil || applied || replayed.TaskID != task.TaskID || replayed.Revision != task.Revision {
		t.Fatalf("confirmation replay changed the calculation: %+v (%v)", replayed, err)
	}
	result := request(http.MethodGet, taskPath+"/artifact", "", http.StatusOK)
	var report struct {
		Method  string
		Input   taskInput
		Metrics experimentMetrics
	}
	if err := json.Unmarshal(result.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	want := experimentMetrics{PairedRows: 52, SkippedRows: 2, BaselineMean: "3", VariantMean: "6", MeanDifference: "3"}
	if result.Header().Get("Content-Type") != "application/json" || result.Header().Get("X-Source-SHA256") != second.SHA256 ||
		report.Method != "paired-mean-v1" || report.Input.VersionID != second.VersionID || report.Input.SHA256 != second.SHA256 || report.Metrics != want {
		t.Fatalf("wrong fixed result: %+v %v", report, result.Header())
	}
	request(http.MethodGet, projectsPath+"/"+other.ProjectID+"/tasks/"+task.TaskID+"/artifact", "", http.StatusNotFound)

	evidencePath := taskPath + "/artifact/evidence"
	request(http.MethodGet, strings.Replace(evidencePath, project.ProjectID, other.ProjectID, 1), "", http.StatusNotFound)
	request(http.MethodGet, evidencePath+"?cursor=-1", "", http.StatusBadRequest)
	readPage := func(route string) (page struct {
		Input                   taskInput
		Items                   []experimentRow
		PairedRows, SkippedRows int
		NextCursor              string
	}) {
		t.Helper()
		if err := json.Unmarshal(request(http.MethodGet, route, "", http.StatusOK).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return
	}
	firstPage := readPage(evidencePath)
	if firstPage.Input != report.Input || firstPage.PairedRows != 52 || firstPage.SkippedRows != 2 ||
		len(firstPage.Items) != 50 || firstPage.NextCursor != "50" ||
		firstPage.Items[0] != (experimentRow{Line: 2, Baseline: "2", Variant: "4"}) {
		t.Fatalf("first evidence page did not match original report: %+v", firstPage)
	}
	lastPage := readPage(evidencePath + "?cursor=" + firstPage.NextCursor)
	if lastPage.Input != report.Input || lastPage.NextCursor != "" || len(lastPage.Items) != 4 ||
		lastPage.Items[2] != (experimentRow{Line: 54, Missing: "variant"}) ||
		lastPage.Items[3] != (experimentRow{Line: 56, Missing: "both"}) {
		t.Fatalf("last evidence page did not locate the skipped records: %+v", lastPage)
	}

	plotPath := taskPath + "/artifact/plot.svg"
	request(http.MethodGet, strings.Replace(plotPath, project.ProjectID, other.ProjectID, 1), "", http.StatusNotFound)
	plot := request(http.MethodGet, plotPath, "", http.StatusOK)
	if plot.Header().Get("X-Source-Version-ID") != second.VersionID || plot.Header().Get("X-Source-SHA256") != second.SHA256 ||
		plot.Header().Get("Content-Type") != "image/svg+xml; charset=utf-8" || strings.Count(plot.Body.String(), "<circle ") != 52 ||
		!strings.Contains(plot.Body.String(), `<circle cx="124.00" cy="312.00" r="3" data-line="2">`) ||
		strings.Contains(plot.Body.String(), `data-line="54"`) {
		t.Fatalf("plot did not use the original input and complete pairs: %v", plot.Header())
	}
	if repeated := request(http.MethodGet, plotPath, "", http.StatusOK); repeated.Body.String() != plot.Body.String() {
		t.Fatal("plot export changed across repeat downloads")
	}

	publication := taskPath + "/artifact/workspace"
	request(http.MethodPost, projectsPath+"/"+other.ProjectID+"/tasks/"+task.TaskID+"/artifact/workspace", "", http.StatusNotFound)
	saveReport := func() WorkspaceFile {
		t.Helper()
		var file WorkspaceFile
		if err := json.Unmarshal(request(http.MethodPost, publication, "", http.StatusOK).Body.Bytes(), &file); err != nil {
			t.Fatal(err)
		}
		return file
	}
	file := saveReport()
	saved, err := store.workspaceVersion(ctx, project.ProjectID, file.VersionID)
	if err != nil || saved.Path != "experiments/"+task.TaskID+"/comparison.json" || saved.SHA256 != file.SHA256 {
		t.Fatalf("report was not published in the project: %+v (%v)", saved, err)
	}
	published, err := store.readWorkspaceDocument(saved)
	if err != nil || published.Content != result.Body.String() {
		t.Fatalf("published report differs from download: %+v (%v)", published, err)
	}
	followUp := fmt.Sprintf(`{"requestId":"interpret","instruction":"解释已保存的指标对比","inputVersionIds":[%q]}`, file.VersionID)
	next := decodeTask(request(http.MethodPost, base, followUp, http.StatusCreated))
	if len(next.Inputs) != 1 || next.Inputs[0].SHA256 != file.SHA256 {
		t.Fatalf("follow-up did not pin the published report: %+v", next)
	}
	edited, err := store.saveWorkspaceDocument(ctx, project.ProjectID, saved.Path, saved.SHA256, published.Content+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if replay := saveReport(); replay != file {
		t.Fatalf("repeat save returned a different published version: %+v", replay)
	}
	if latest, err := store.workspaceFile(ctx, project.ProjectID, saved.Path); err != nil || latest.VersionID != edited.VersionID {
		t.Fatalf("repeat save displaced the user's edit: %+v (%v)", latest, err)
	}

	body = strings.Replace(fmt.Sprintf(proposal, third.VersionID), `"compare"`, `"stop"`, 1)
	stopped := decodeTask(request(http.MethodPost, base, body, http.StatusCreated))
	confirmation = taskRequest{ProjectID: project.ProjectID, TaskID: stopped.TaskID, RequestID: "queue-stopped", Action: confirmTask, ExpectedRevision: stopped.Revision}
	if _, _, err := store.applyTaskRequest(ctx, confirmation, false); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimQueuedTask(ctx, project.ProjectID, false)
	if err != nil || claimed.TaskID != stopped.TaskID {
		t.Fatalf("wrong claimed task: %+v (%v)", claimed, err)
	}
	stoppedPath := base + "/" + claimed.TaskID
	request(http.MethodPost, stoppedPath+"/cancel", fmt.Sprintf(`{"requestId":"cancel","expectedRevision":%d}`, claimed.Revision), http.StatusOK)
	if err := store.finishTask(ctx, project.ProjectID, claimed, taskSucceeded, "", taskResult{jsonArtifact: `{}`}); err != nil {
		t.Fatal(err)
	}
	if cancelled := decodeTask(request(http.MethodGet, stoppedPath, "", http.StatusOK)); cancelled.Status != taskCancelled {
		t.Fatalf("late output overrode cancellation: %+v", cancelled)
	}
	request(http.MethodGet, stoppedPath+"/artifact", "", http.StatusNotFound)
	request(http.MethodPost, stoppedPath+"/resume", fmt.Sprintf(`{"requestId":"resume-cancelled","expectedRevision":%d}`, claimed.Revision+2), http.StatusConflict)

	// Simulate a service exit after claiming a calculation, then change the source.
	body = strings.Replace(fmt.Sprintf(proposal, third.VersionID), `"compare"`, `"interrupted"`, 1)
	interrupted := decodeTask(request(http.MethodPost, base, body, http.StatusCreated))
	confirmation = taskRequest{ProjectID: project.ProjectID, TaskID: interrupted.TaskID, RequestID: "queue-interrupted", Action: confirmTask, ExpectedRevision: interrupted.Revision}
	if _, _, err := store.applyTaskRequest(ctx, confirmation, false); err != nil {
		t.Fatal(err)
	}
	beforeRestart, err := store.claimQueuedTask(ctx, project.ProjectID, false)
	if err != nil || beforeRestart.TaskID != interrupted.TaskID {
		t.Fatalf("wrong interrupted task: %+v (%v)", beforeRestart, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	save(third.SHA256, "1,999\n")
	resumePath := base + "/" + beforeRestart.TaskID
	interrupted = decodeTask(request(http.MethodGet, resumePath, "", http.StatusOK))
	if interrupted.Status != taskInterrupted {
		t.Fatalf("unexpected state after restart: %+v", interrupted)
	}
	resume := fmt.Sprintf(`{"requestId":"resume-original","expectedRevision":%d}`, interrupted.Revision)
	request(http.MethodPost, strings.Replace(resumePath, project.ProjectID, other.ProjectID, 1)+"/resume", resume, http.StatusNotFound)
	// Hold the worker so a stale completion can be tested against the reclaimed task.
	store.tasks.closing = true
	resumed := decodeTask(request(http.MethodPost, resumePath+"/resume", resume, http.StatusAccepted))
	if resumed.TaskID != beforeRestart.TaskID || resumed.Status != taskQueued || resumed.Inputs[0].SHA256 != third.SHA256 {
		t.Fatalf("resume changed the task or its fixed input: %+v", resumed)
	}
	request(http.MethodPost, resumePath+"/resume", strings.Replace(resume, "resume-original", "resume-race", 1), http.StatusConflict)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{})
	reclaimed, err := store.claimQueuedTask(ctx, project.ProjectID, false)
	if err != nil || reclaimed.TaskID != resumed.TaskID || reclaimed.Revision <= beforeRestart.Revision {
		t.Fatalf("could not reclaim the original task: %+v (%v)", reclaimed, err)
	}
	if err := store.finishTask(ctx, project.ProjectID, beforeRestart, taskSucceeded, "", taskResult{jsonArtifact: `{}`}); err == nil {
		t.Fatal("an earlier claim could still publish a result")
	}
	recalculated, err := store.runExperimentComparison(ctx, project.ProjectID, reclaimed.Inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTask(ctx, project.ProjectID, reclaimed, taskSucceeded, "", recalculated); err != nil {
		t.Fatal(err)
	}
	if saved := decodeTask(request(http.MethodPost, resumePath+"/resume", resume, http.StatusOK)); saved.TaskID != resumed.TaskID || saved.Status != taskSucceeded {
		t.Fatalf("resume replay changed the task: %+v", saved)
	}
	resumedReport := request(http.MethodGet, resumePath+"/artifact", "", http.StatusOK)
	if err := json.Unmarshal(resumedReport.Body.Bytes(), &report); err != nil || report.Input.VersionID != third.VersionID ||
		report.Input.SHA256 != third.SHA256 || report.Metrics.MeanDifference != "60" {
		t.Fatalf("resumed calculation used the wrong version: %+v (%v)", report, err)
	}
}
