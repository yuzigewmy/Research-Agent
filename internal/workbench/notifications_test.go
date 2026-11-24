package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskNotificationsKeepHistoryAndUnreadStateAfterRestart(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
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
	create := func(key, title string) Project {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: key, Title: title, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	first := create("river", "河流观测")
	other := create("forest", "林地观测")
	handler := NewHandler(store, nil, HandlerConfig{})
	send := func(method, route, body string, status int, output any) {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, response.Code, status, response.Body)
		}
		if output != nil {
			if err := json.Unmarshal(response.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
	}
	taskRoute := projectsPath + "/" + first.ProjectID + "/tasks"
	const inbox = "/api/v1/notifications"
	type notice struct {
		NotificationID, ProjectID, ProjectName, TaskID, Instruction, Status, CreatedAt string
		Read                                                                           bool
	}
	list := func(suffix string) (page struct {
		Items       []notice
		UnreadCount int
		NextCursor  string
	}) {
		t.Helper()
		send(http.MethodGet, inbox+suffix, "", http.StatusOK, &page)
		return page
	}
	run := func(task Task, status string) {
		t.Helper()
		_, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: first.ProjectID, TaskID: task.TaskID,
			RequestID: "confirm-" + task.TaskID, Action: confirmTask, ExpectedRevision: task.Revision}, true)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.claimQueuedTask(ctx, first.ProjectID, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTask(ctx, first.ProjectID, claimed, status, "", taskResult{}); err != nil {
			t.Fatal(err)
		}
	}
	const request = `{"requestId":"draft-plan","instruction":"整理河流观测计划。"}`
	var proposed Task
	send(http.MethodPost, taskRoute, request, http.StatusCreated, &proposed)
	send(http.MethodPost, taskRoute, request, http.StatusOK, nil)
	page := list("")
	if len(page.Items) != 1 || page.UnreadCount != 1 || page.Items[0].Status != taskProposed || page.Items[0].TaskID != proposed.TaskID {
		t.Fatalf("wrong proposal notification: %+v", page)
	}
	readRoute := inbox + "/" + page.Items[0].NotificationID
	send(http.MethodPatch, readRoute, `{"read":true}`, http.StatusNoContent, nil)
	send(http.MethodPatch, readRoute, `{"read":true}`, http.StatusNoContent, nil)
	run(proposed, taskSucceeded)
	var second Task
	send(http.MethodPost, taskRoute, `{"requestId":"failed-plan","instruction":"复核观测记录。"}`, http.StatusCreated, &second)
	run(second, taskFailed)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{})
	latest := list("?limit=1")
	if latest.UnreadCount != 3 || latest.NextCursor == "" || len(latest.Items) != 1 || latest.Items[0].Status != taskFailed || latest.Items[0].TaskID != second.TaskID {
		t.Fatalf("wrong newest notification after reopen: %+v", latest)
	}
	older := list("?cursor=" + latest.NextCursor)
	if older.UnreadCount != 3 || len(older.Items) != 3 || older.Items[0].TaskID != second.TaskID ||
		older.Items[1].Status != taskSucceeded || older.Items[1].TaskID != proposed.TaskID ||
		older.Items[2].Status != taskProposed || !older.Items[2].Read || older.Items[2].ProjectID != first.ProjectID ||
		older.Items[2].ProjectName != first.Title || older.Items[2].Instruction != proposed.Instruction || older.Items[2].CreatedAt == "" {
		t.Fatalf("wrong notification history after reopen: %+v", older)
	}
	var linked Task
	send(http.MethodGet, taskRoute+"/"+proposed.TaskID, "", http.StatusOK, &linked)
	if linked.TaskID != proposed.TaskID || linked.Status != taskSucceeded {
		t.Fatalf("notification target did not return its current task: %+v", linked)
	}
	send(http.MethodGet, projectsPath+"/"+other.ProjectID+"/tasks/"+proposed.TaskID, "", http.StatusNotFound, nil)
}
