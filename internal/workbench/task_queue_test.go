package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestProjectTaskQueueStopPreservesReceiptsAndNewWork(t *testing.T) {
	const existingArtifact = "# Previously published sampling plan"
	const newArtifact = "# New sampling plan"
	const secondInstruction = "Prepare the second sampling plan."
	const newInstruction = "Prepare a new plan after stopping the queue."
	var calls atomic.Int32
	var started [3]chan struct{}
	var cancelled [2]chan struct{}
	for i := range started {
		started[i] = make(chan struct{})
	}
	for i := range cancelled {
		cancelled[i] = make(chan struct{})
	}
	release, cleanup := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read model input", http.StatusBadRequest)
			return
		}
		index := int(calls.Add(1)) - 1
		want := []string{"界定研究范围", secondInstruction, newInstruction}
		if index >= len(want) || !strings.Contains(string(body), want[index]) {
			t.Errorf("unexpected task dispatched at call %d", index+1)
			http.Error(w, "unexpected model input", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Partial output\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started[index])
		if index < len(cancelled) {
			select {
			case <-r.Context().Done():
				close(cancelled[index])
			case <-cleanup:
			}
			return
		}
		select {
		case <-release:
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "\n"+newArtifact)
		case <-r.Context().Done():
		case <-cleanup:
		}
	}))
	t.Cleanup(upstream.Close)
	defer close(cleanup)
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
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "project", Title: "River observations", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ProjectID
	base := projectsPath + "/" + projectID
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, suffix, body string, want int, target any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		if target != nil {
			if err := json.Unmarshal(res.Body.Bytes(), target); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	propose := func(id, instruction string) Task {
		t.Helper()
		var task Task
		body := fmt.Sprintf(`{"requestId":%q,"instruction":%q}`, id, instruction)
		request(http.MethodPost, "/tasks", body, http.StatusCreated, &task)
		return task
	}
	confirm := func(id string, task Task) {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"expectedRevision":%d}`, id, task.Revision)
		request(http.MethodPost, "/tasks/"+task.TaskID+"/confirm", body, http.StatusAccepted, nil)
	}
	await := func(signal <-chan struct{}, event string) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", event)
		}
	}
	type queueItem struct {
		TaskID, Title, Kind, Status string
		Revision                    int
	}
	readQueue := func(wantQueued int) *queueItem {
		t.Helper()
		var queue struct {
			Current     *queueItem
			QueuedCount int
		}
		request(http.MethodGet, "/task-queue", "", http.StatusOK, &queue)
		if queue.QueuedCount != wantQueued {
			t.Fatalf("got %d queued tasks, want %d", queue.QueuedCount, wantQueued)
		}
		return queue.Current
	}
	type stopReceipt struct {
		Scope                         string
		StoppingCount, CancelledCount int
	}
	stop := func(body string, expected stopReceipt) {
		t.Helper()
		var got struct {
			Scope                         string
			StoppingCount, CancelledCount *int
		}
		response := request(http.MethodPost, "/task-queue/stop", body, http.StatusOK, &got)
		if got.Scope != expected.Scope || got.StoppingCount == nil || *got.StoppingCount != expected.StoppingCount ||
			got.CancelledCount == nil || *got.CancelledCount != expected.CancelledCount {
			t.Fatalf("stop receipt = %s, want %+v", response, expected)
		}
	}
	conflict := func(body, wantCode string) {
		t.Helper()
		var problem struct{ Error apiError }
		request(http.MethodPost, "/task-queue/stop", body, http.StatusConflict, &problem)
		if problem.Error.Code != wantCode {
			t.Fatalf("stop error = %+v, want %s", problem.Error, wantCode)
		}
	}
	currentBody := func(id string, item *queueItem) string {
		return fmt.Sprintf(`{"requestId":%q,"scope":"current","taskId":%q,"expectedRevision":%d}`, id, item.TaskID, item.Revision)
	}

	// Seed a published task without consuming one of the controlled model responses.
	existing := propose("existing", "Prepare a sampling plan for the prior visit.")
	if _, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: projectID, TaskID: existing.TaskID, Action: confirmTask,
		RequestID: "confirm-existing", ExpectedRevision: existing.Revision}, true); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimQueuedTask(ctx, projectID, true)
	if err != nil || claimed.TaskID != existing.TaskID {
		t.Fatalf("claim prior task: task=%+v, err=%v", claimed, err)
	}
	if err := store.finishTask(ctx, projectID, claimed, taskSucceeded, "", taskResult{markdown: existingArtifact}); err != nil {
		t.Fatal(err)
	}
	unconfirmed := propose("unconfirmed", "Leave this sampling plan for later confirmation.")
	var workflow WorkflowRun
	request(http.MethodPost, "/workflow", `{"requestId":"start-workflow","throughStep":"plan"}`, http.StatusCreated, &workflow)
	await(started[0], "the first workflow step")
	second := propose("second", secondInstruction)
	third := propose("third", "Do not dispatch this queued plan.")
	confirm("confirm-second", second)
	confirm("confirm-third", third)
	first := readQueue(2)
	if first == nil || first.TaskID == "" || first.Status != taskRunning || first.Title == "" || first.Kind == "" || first.Revision < 1 {
		t.Fatalf("wrong current queue item: %+v", first)
	}
	firstStop := currentBody("stop-current", first)
	currentReceipt := stopReceipt{Scope: "current", StoppingCount: 1}
	stop(firstStop, currentReceipt)
	await(cancelled[0], "cancellation of the first model call")
	await(started[1], "the second queued plan")
	next := readQueue(1)
	if next == nil || next.TaskID != second.TaskID || next.Status != taskRunning {
		t.Fatalf("the queue did not advance to the second task: %+v", next)
	}
	var snapshot struct{ Run WorkflowRun }
	request(http.MethodGet, "/workflow", "", http.StatusOK, &snapshot)
	if snapshot.Run.RunID != workflow.RunID || snapshot.Run.Status != workflowPaused {
		t.Fatalf("stopping a current workflow step should pause the run: %+v", snapshot.Run)
	}
	conflict(currentBody("old-snapshot", first), "TASK_QUEUE_CHANGED")
	staleRevision := *next
	staleRevision.Revision--
	conflict(currentBody("old-revision", &staleRevision), "TASK_QUEUE_CHANGED")
	conflict(currentBody("stop-current", next), "REQUEST_ID_CONFLICT")
	stop(firstStop, currentReceipt)

	const all = `{"requestId":"stop-all","scope":"all"}`
	allReceipt := stopReceipt{Scope: "all", StoppingCount: 1, CancelledCount: 1}
	stop(all, allReceipt)
	await(cancelled[1], "cancellation of the second model call")
	fresh := propose("fresh", newInstruction)
	confirm("confirm-fresh", fresh)
	await(started[2], "the task submitted after stopping the queue")
	active := readQueue(0)
	if active == nil || active.TaskID != fresh.TaskID || active.Status != taskRunning {
		t.Fatalf("wrong task active after stopping all: %+v", active)
	}
	stop(all, allReceipt)
	conflict(currentBody("stop-all", active), "REQUEST_ID_CONFLICT")
	if current := readQueue(0); current == nil || *current != *active {
		t.Fatalf("replaying stop all changed the new task: %+v", current)
	}
	close(release)

	for {
		var page taskPage
		request(http.MethodGet, "/tasks", "", http.StatusOK, &page)
		states := make(map[string]string, len(page.Items))
		for _, task := range page.Items {
			states[task.TaskID] = task.Status
		}
		if states[existing.TaskID] != taskSucceeded || states[unconfirmed.TaskID] != taskProposed ||
			states[second.TaskID] != taskCancelled || states[third.TaskID] != taskCancelled {
			t.Fatalf("stop all did not preserve or cancel the expected tasks: %v", states)
		}
		if states[fresh.TaskID] == taskSucceeded {
			break
		}
		if states[fresh.TaskID] != taskRunning || ctx.Err() != nil {
			t.Fatalf("the new task did not complete: %v", states)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := request(http.MethodGet, "/tasks/"+existing.TaskID+"/artifact", "", http.StatusOK, nil); got != existingArtifact {
		t.Fatalf("stop all changed a published artifact: %q", got)
	}
	if got := request(http.MethodGet, "/tasks/"+fresh.TaskID+"/artifact", "", http.StatusOK, nil); got != "# Partial output\n"+newArtifact || calls.Load() != 3 {
		t.Fatalf("wrong new artifact %q, calls=%d", got, calls.Load())
	}
	if current := readQueue(0); current != nil {
		t.Fatalf("queue still has a current task: %+v", current)
	}
}
