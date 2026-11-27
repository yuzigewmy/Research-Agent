package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRemoteComparisonRecoversOriginalJobAndVerifiesResult(t *testing.T) {
	ctx := t.Context()
	const token = "compute-fixture"
	worker, err := OpenComputeWorker(t.TempDir(), "配对节点", token)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	var submissions, results, stops atomic.Int32
	workerHandler := worker.Handler()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && submissions.Add(1) > 1 {
			http.Error(w, "fixture disconnected before submission", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodPut || (strings.HasSuffix(r.URL.Path, "/cancel") && stops.Add(1) == 1) {
			// The node commits the receipt before the connection drops.
			saved := httptest.NewRecorder()
			workerHandler.ServeHTTP(saved, r)
			want := http.StatusCreated
			if r.Method == http.MethodPost {
				want = http.StatusOK
			}
			if saved.Code != want {
				t.Errorf("worker response: %d, want %d", saved.Code, want)
			}
			connection, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		if strings.HasSuffix(r.URL.Path, "/result") && results.Add(1) == 1 {
			tampered := httptest.NewRecorder()
			workerHandler.ServeHTTP(tampered, r)
			w.Header().Set("X-Result-SHA256", tampered.Header().Get("X-Result-SHA256"))
			_, _ = w.Write(append(tampered.Body.Bytes(), ' '))
			return
		}
		workerHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	node, err := NewComputeNode(upstream.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	open := func() *Store {
		t.Helper()
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	makeProject := func(name string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: name, Title: name, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := makeProject("观测"), makeProject("另一项目")
	source, err := store.saveWorkspaceDocument(ctx, project, "data/observations.csv", "", "baseline,variant\n2,8\n4,10\n")
	if err != nil {
		t.Fatal(err)
	}
	completeComparison := func(requestID string) string {
		t.Helper()
		task, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: project, RequestID: requestID, Action: proposeTask,
			Kind: experimentComparisonTask, Instruction: "核对观察差异", InputVersionIDs: []string{source.VersionID}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: project, TaskID: task.TaskID,
			RequestID: "confirm-" + requestID, Action: confirmTask, ExpectedRevision: task.Revision}, false); err != nil {
			t.Fatal(err)
		}
		claim, err := store.claimQueuedTask(ctx, project, false)
		if err != nil {
			t.Fatal(err)
		}
		calculated, err := store.runExperimentComparison(ctx, project, claim.Inputs[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTask(ctx, project, claim, taskSucceeded, "", calculated); err != nil {
			t.Fatal(err)
		}
		return projectsPath + "/" + project + "/tasks/" + task.TaskID + "/remote-comparison"
	}
	path := completeComparison("pair")
	stopPath := completeComparison("stop-pair")
	if _, err := store.saveWorkspaceDocument(ctx, project, source.Path, source.SHA256, "baseline,variant\n1,99\n"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{ComputeNode: node})
	request := func(method, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	read := func(method, path string) computeJob {
		t.Helper()
		var snapshot struct{ Job computeJob }
		if err := json.Unmarshal(request(method, path, http.StatusOK).Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot.Job
	}
	request(http.MethodPost, strings.Replace(path, project, other, 1)+"/submit", http.StatusNotFound)
	if job := read(http.MethodGet, path); job.JobID != "" || submissions.Load() != 0 {
		t.Fatal("reading the task dispatched a job")
	}
	request(http.MethodPost, path+"/submit", http.StatusBadGateway)
	pending := read(http.MethodGet, path)
	if !validComputeJobID(pending.JobID) || pending.Status != remotePending || pending.InputSHA256 != source.SHA256 {
		t.Fatalf("wrong pending receipt: %+v", pending)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{ComputeNode: node})
	request(http.MethodPost, path+"/refresh", http.StatusBadGateway)
	request(http.MethodGet, path+"/result", http.StatusNotFound)
	job := read(http.MethodPost, path+"/refresh")
	if job.JobID != pending.JobID || job.Status != taskSucceeded || job.InputSHA256 != source.SHA256 || submissions.Load() != 1 {
		t.Fatalf("wrong recovered receipt: %+v, submissions %d", job, submissions.Load())
	}
	request(http.MethodPost, stopPath+"/submit", http.StatusBadGateway)
	reserved := read(http.MethodGet, stopPath)
	stopping := read(http.MethodPost, stopPath+"/cancel")
	if reserved.Status != remotePending || stopping.JobID != reserved.JobID || stopping.Status != taskCancelling {
		t.Fatalf("lost stop response did not preserve intent: %+v, %+v", reserved, stopping)
	}
	stopped := read(http.MethodPost, stopPath+"/refresh")
	if stopped.Status != taskCancelled || stopped.JobID != reserved.JobID || stopped.ResultSHA256 != "" {
		t.Fatalf("wrong refreshed stop receipt: %+v", stopped)
	}
	if replay := read(http.MethodPost, stopPath+"/submit"); replay != stopped || submissions.Load() != 2 || stops.Load() != 1 {
		t.Fatalf("submitting a stopped job contacted the node: %+v", replay)
	}
	request(http.MethodGet, stopPath+"/result", http.StatusNotFound)
	handler = NewHandler(store, nil, HandlerConfig{})
	result := request(http.MethodGet, path+"/result", http.StatusOK)
	if result.Header().Get("X-Result-SHA256") != job.ResultSHA256 || computeDigest(result.Body.Bytes()) != job.ResultSHA256 {
		t.Fatal("downloaded bytes do not match the recovered receipt")
	}
}
