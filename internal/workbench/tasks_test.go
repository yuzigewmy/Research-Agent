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

func TestResearchTaskQueueCancellationAndPublication(t *testing.T) {
	const markdown = "# Study plan\n\nCompare the baseline against the control.\n"
	var calls atomic.Int32
	started, stopped := make(chan struct{}), make(chan struct{})
	releaseStub := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the body so net/http detects the client's disconnect.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Partial plan\"}}]}\n\n")
			w.(http.Flusher).Flush()
			close(started)
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-releaseStub:
			}
		case 2:
			if !strings.Contains(string(body), "Compare both sampling sites") {
				http.Error(w, "expected revised task instruction", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", markdown)
		default:
			http.Error(w, "unexpected model request", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()
	defer close(releaseStub)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: "project", Title: "Field study", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	base := projectsPath + "/" + project.ProjectID + "/tasks"
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res
	}
	type taskResponse struct {
		TaskID      string
		Status      string
		Revision    int
		Instruction string
	}
	post := func(path, body string, want int) taskResponse {
		t.Helper()
		var got taskResponse
		if err := json.Unmarshal(request("POST", path, body, want).Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	list := func() []taskResponse {
		t.Helper()
		var page struct {
			Items           []taskResponse
			ModelConfigured bool
		}
		if err := json.Unmarshal(request("GET", base, "", http.StatusOK).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if !page.ModelConfigured || len(page.Items) != 2 {
			t.Fatalf("unexpected task list: %+v", page)
		}
		return page.Items
	}
	awaitSignal := func(signal <-chan struct{}, event string) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", event)
		}
	}

	const firstInput = `{"requestId":"p1","instruction":"Plan a baseline study"}`
	first := post(base, firstInput, http.StatusCreated)
	if first.TaskID == "" || first.Status != "PROPOSED" || first.Revision != 1 || post(base, firstInput, http.StatusOK) != first {
		t.Fatalf("incorrect proposal or replay: %+v", first)
	}
	const secondInput = `{"requestId":"p2","instruction":"Plan a field comparison"}`
	second := post(base, secondInput, http.StatusCreated)
	firstPath, secondPath := base+"/"+first.TaskID, base+"/"+second.TaskID
	const edit = `{"requestId":"edit","expectedRevision":1,"instruction":"Compare both sampling sites"}`
	patch := func() taskResponse {
		t.Helper()
		var got taskResponse
		if err := json.Unmarshal(request("PATCH", secondPath, edit, http.StatusOK).Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	edited := patch()
	if edited.TaskID != second.TaskID || edited.Revision != 2 || edited.Status != "PROPOSED" ||
		edited.Instruction != "Compare both sampling sites" || patch() != edited || post(base, secondInput, http.StatusOK) != edited {
		t.Fatalf("incorrect task edit or replay: %+v", edited)
	}
	request("PATCH", secondPath, strings.Replace(edit, "both", "three", 1), http.StatusConflict)
	request("POST", secondPath+"/confirm", `{"requestId":"stale","expectedRevision":1}`, http.StatusConflict)
	const confirmFirst = `{"requestId":"c1","expectedRevision":1}`
	post(firstPath+"/confirm", confirmFirst, http.StatusAccepted)
	awaitSignal(started, "the first model call")
	const confirmSecond = `{"requestId":"c2","expectedRevision":2}`
	post(secondPath+"/confirm", confirmSecond, http.StatusAccepted)
	queued := list()
	if queued[0].TaskID != second.TaskID || queued[0].Status != "QUEUED" || queued[1].TaskID != first.TaskID || queued[1].Status != "RUNNING" || calls.Load() != 1 {
		t.Fatalf("expected one running task and one queued task: %+v, calls=%d", queued, calls.Load())
	}
	request("PATCH", secondPath, fmt.Sprintf(`{"requestId":"late-edit","expectedRevision":%d,"instruction":"Change the queued task"}`, queued[0].Revision), http.StatusConflict)
	const cancelFirst = `{"requestId":"x1","expectedRevision":%d}`
	cancelBody := fmt.Sprintf(cancelFirst, queued[1].Revision)
	post(firstPath+"/cancel", cancelBody, http.StatusOK)
	awaitSignal(stopped, "cancellation at the model server")

	deadline := time.Now().Add(5 * time.Second)
	for {
		items := list()
		if items[0].Status == "SUCCEEDED" && items[1].Status == "CANCELLED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not drain after cancellation: %+v", items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := post(firstPath+"/cancel", cancelBody, http.StatusOK); got.Status != "CANCELLED" {
		t.Fatalf("cancel replay changed the terminal result: %+v", got)
	}
	published := post(secondPath+"/confirm", confirmSecond, http.StatusOK)
	if published.Status != "SUCCEEDED" || calls.Load() != 2 {
		t.Fatalf("confirm replay changed the result or issued another model request: %+v, calls=%d", published, calls.Load())
	}
	lateCancel := fmt.Sprintf(`{"requestId":"x2","expectedRevision":%d}`, published.Revision)
	if got := post(secondPath+"/cancel", lateCancel, http.StatusOK); got != published {
		t.Fatalf("late cancellation changed the published task: %+v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handler = NewHandler(reopened, nil, HandlerConfig{})
	request("GET", firstPath+"/artifact", "", http.StatusNotFound)
	artifact := request("GET", secondPath+"/artifact", "", http.StatusOK)
	if artifact.Body.String() != markdown || artifact.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || !strings.HasPrefix(artifact.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("invalid durable Markdown download: %v %q", artifact.Header(), artifact.Body)
	}
}
