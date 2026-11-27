package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteComparisonRecoversOriginalJobAndVerifiesResult(t *testing.T) {
	ctx := t.Context()
	const token = "compute-fixture"
	worker, err := OpenComputeWorker(t.TempDir(), "配对节点", token)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	var submissions, results, stops, scatterRequests atomic.Int32
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
			if r.Method == http.MethodPut {
				var accepted computeJob
				if err := json.Unmarshal(saved.Body.Bytes(), &accepted); err != nil || saved.Code != http.StatusAccepted ||
					accepted.Status != taskRunning || accepted.ResultSHA256 != "" || saved.Header().Get("Preference-Applied") != "respond-async" {
					t.Errorf("expected an immediate running receipt, got %d: %+v (%v)", saved.Code, accepted, err)
				}
			} else if saved.Code != http.StatusOK {
				t.Errorf("stop response: %d, want %d", saved.Code, http.StatusOK)
			}
			connection, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		if strings.HasSuffix(r.URL.Path, "/artifacts/scatter.svg") && scatterRequests.Add(1) == 1 {
			tampered := httptest.NewRecorder()
			workerHandler.ServeHTTP(tampered, r)
			body := tampered.Body.Bytes()
			if tampered.Code != http.StatusOK || len(body) == 0 {
				t.Errorf("node did not produce the scatter: %d", tampered.Code)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			body[0] = ' '
			w.Header().Set("X-Result-SHA256", tampered.Header().Get("X-Result-SHA256"))
			_, _ = w.Write(body)
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
		return task.TaskID
	}
	taskID := completeComparison("pair")
	stopTaskID := completeComparison("stop-pair")
	taskBase := projectsPath + "/" + project + "/tasks/"
	path := taskBase + taskID + "/remote-comparison"
	stopPath := taskBase + stopTaskID + "/remote-comparison"
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
	// The workbench can lose the accepted response while the node finishes independently.
	worker.active.Wait()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{ComputeNode: node})
	request(http.MethodPost, path+"/retrieve", http.StatusConflict)
	var snapshot struct {
		Job             computeJob
		ReportRetrieved bool
		Scatter         *remoteScatter
	}
	if err := json.Unmarshal(request(http.MethodPost, path+"/refresh", http.StatusOK).Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	job := snapshot.Job
	if job.JobID != pending.JobID || job.Status != taskSucceeded || job.InputSHA256 != source.SHA256 || submissions.Load() != 1 ||
		snapshot.ReportRetrieved || results.Load() != 0 {
		t.Fatalf("wrong recovered receipt or unexpected transfer: %+v, submissions %d, transfers %d", snapshot, submissions.Load(), results.Load())
	}
	request(http.MethodPost, path+"/retrieve-scatter", http.StatusConflict)
	if err := json.Unmarshal(request(http.MethodPost, path+"/artifacts", http.StatusOK).Body.Bytes(), &snapshot); err != nil ||
		snapshot.Scatter == nil || snapshot.Scatter.Retrieved || snapshot.ReportRetrieved || results.Load() != 0 || scatterRequests.Load() != 0 {
		t.Fatalf("listing transferred artifact bytes or returned an invalid manifest: %+v (%v)", snapshot, err)
	}
	request(http.MethodPost, path+"/retrieve-scatter", http.StatusBadGateway)
	request(http.MethodGet, path+"/scatter", http.StatusNotFound)
	request(http.MethodPost, path+"/scatter/material", http.StatusNotFound)
	if err := json.Unmarshal(request(http.MethodPost, path+"/retrieve-scatter", http.StatusOK).Body.Bytes(), &snapshot); err != nil ||
		snapshot.Scatter == nil || !snapshot.Scatter.Retrieved || snapshot.ReportRetrieved || results.Load() != 0 || scatterRequests.Load() != 2 {
		t.Fatalf("unexpected scatter retrieval snapshot: %+v (%v)", snapshot, err)
	}
	request(http.MethodPost, path+"/retrieve-scatter", http.StatusOK)
	if scatterRequests.Load() != 2 {
		t.Fatal("retrieval replay transferred an already saved scatter plot")
	}
	request(http.MethodGet, path+"/result", http.StatusNotFound)
	request(http.MethodPost, path+"/result/workspace", http.StatusNotFound)
	request(http.MethodPost, path+"/retrieve", http.StatusBadGateway)
	request(http.MethodGet, path+"/result", http.StatusNotFound)
	if err := json.Unmarshal(request(http.MethodPost, path+"/retrieve", http.StatusOK).Body.Bytes(), &snapshot); err != nil ||
		!snapshot.ReportRetrieved || snapshot.Job != job || results.Load() != 2 {
		t.Fatalf("wrong report retrieval receipt: %+v (%v)", snapshot, err)
	}
	request(http.MethodPost, path+"/retrieve", http.StatusOK)
	if results.Load() != 2 {
		t.Fatal("retrieval replay transferred an already saved report")
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
	request(http.MethodGet, strings.Replace(path, project, other, 1)+"/scatter", http.StatusNotFound)
	plot := request(http.MethodGet, path+"/scatter", http.StatusOK)
	if plot.Body.Len() != snapshot.Scatter.Size || computeDigest(plot.Body.Bytes()) != snapshot.Scatter.SHA256 ||
		plot.Header().Get("X-Result-SHA256") != snapshot.Scatter.SHA256 || !strings.HasPrefix(plot.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("offline scatter download differs from the manifest")
	}
	savePlot := path + "/scatter/material"
	request(http.MethodPost, strings.Replace(savePlot, project, other, 1), http.StatusNotFound)
	var savedPlot Material
	if err := json.Unmarshal(request(http.MethodPost, savePlot, http.StatusOK).Body.Bytes(), &savedPlot); err != nil ||
		savedPlot.Name != "远程散点图-"+strings.ToLower(taskID)+".svg" || savedPlot.SHA256 != snapshot.Scatter.SHA256 ||
		savedPlot.Size != int64(snapshot.Scatter.Size) {
		t.Fatalf("unexpected saved scatter: %+v (%v)", savedPlot, err)
	}
	var replayPlot Material
	if err := json.Unmarshal(request(http.MethodPost, savePlot, http.StatusOK).Body.Bytes(), &replayPlot); err != nil || replayPlot.MaterialID != savedPlot.MaterialID {
		t.Fatalf("scatter save did not replay the original material: %+v (%v)", replayPlot, err)
	}
	artifactPath := projectsPath + "/" + project + "/artifacts?kind=material&format=image"
	var catalog struct{ Items []projectArtifact }
	if err := json.Unmarshal(request(http.MethodGet, artifactPath, http.StatusOK).Body.Bytes(), &catalog); err != nil ||
		len(catalog.Items) != 1 || catalog.Items[0].ID != savedPlot.MaterialID {
		t.Fatalf("saved scatter missing from the image catalog: %+v (%v)", catalog, err)
	}
	if downloaded := request(http.MethodGet, catalog.Items[0].DownloadURL, http.StatusOK); downloaded.Body.String() != plot.Body.String() {
		t.Fatal("catalog download changed the scatter bytes")
	}
	bundlePath := projectsPath + "/" + project + "/files/bundle"
	bundleInput, err := json.Marshal(map[string][]workspaceBundleItem{"items": {{Kind: "material", ID: savedPlot.MaterialID}}})
	if err != nil {
		t.Fatal(err)
	}
	bundleRequest := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+bundlePath, bytes.NewReader(bundleInput))
	bundleRequest.Header.Set("Content-Type", "application/json")
	bundle := httptest.NewRecorder()
	handler.ServeHTTP(bundle, bundleRequest)
	if bundle.Code != http.StatusOK {
		t.Fatalf("bundle returned %d: %s", bundle.Code, bundle.Body)
	}
	archive, err := zip.NewReader(bytes.NewReader(bundle.Body.Bytes()), int64(bundle.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	member, err := archive.Open("files/01.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	if exported, err := io.ReadAll(member); err != nil || !bytes.Equal(exported, plot.Body.Bytes()) {
		t.Fatalf("ZIP did not retain the original scatter bytes: %v", err)
	}
	result := request(http.MethodGet, path+"/result", http.StatusOK)
	if result.Header().Get("X-Result-SHA256") != job.ResultSHA256 || computeDigest(result.Body.Bytes()) != job.ResultSHA256 {
		t.Fatal("downloaded bytes do not match the recovered receipt")
	}
	publication := path + "/result/workspace"
	request(http.MethodPost, strings.Replace(publication, project, other, 1), http.StatusNotFound)
	var published WorkspaceFile
	if err := json.Unmarshal(request(http.MethodPost, publication, http.StatusOK).Body.Bytes(), &published); err != nil ||
		published.Path != "experiments/"+taskID+"/remote-comparison.json" ||
		published.SHA256 != job.ResultSHA256 || published.Size != result.Body.Len() {
		t.Fatalf("unexpected workspace receipt: %+v (%v)", published, err)
	}
	stored, err := store.readWorkspaceDocument(published)
	if err != nil || stored.Content != result.Body.String() {
		t.Fatalf("saved version differs from remote result: %v", err)
	}
	edited, err := store.saveWorkspaceDocument(ctx, project, published.Path, published.SHA256, stored.Content+"\n")
	if err != nil {
		t.Fatal(err)
	}
	var replay WorkspaceFile
	if err := json.Unmarshal(request(http.MethodPost, publication, http.StatusOK).Body.Bytes(), &replay); err != nil || replay != published {
		t.Fatalf("replay did not return the original report: %+v (%v)", replay, err)
	}
	if latest, err := store.workspaceFile(ctx, project, published.Path); err != nil || latest.VersionID != edited.VersionID {
		t.Fatalf("replay displaced the edited version: %+v (%v)", latest, err)
	}

	var backup bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, t.TempDir(), &backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := restoreWorkspace(ctx, bytes.NewReader(backup.Bytes()), int64(backup.Len()), destination, maxRestoreDatabaseBytes); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	handler = NewHandler(restored, nil, HandlerConfig{})
	var restoredSnapshot struct {
		Configured      bool
		Job             computeJob
		ReportRetrieved bool
		Scatter         *remoteScatter
	}
	if err := json.Unmarshal(request(http.MethodGet, path, http.StatusOK).Body.Bytes(), &restoredSnapshot); err != nil ||
		restoredSnapshot.Configured || restoredSnapshot.Job != job || !restoredSnapshot.ReportRetrieved ||
		restoredSnapshot.Scatter == nil || *restoredSnapshot.Scatter != *snapshot.Scatter {
		t.Fatalf("restore changed the remote receipt: %+v (%v)", restoredSnapshot, err)
	}
	if recovered := read(http.MethodGet, stopPath); recovered != stopped {
		t.Fatalf("restore changed the stopped receipt: %+v", recovered)
	}
	for route, original := range map[string]*httptest.ResponseRecorder{
		path + "/result":             result,
		path + "/scatter":            plot,
		catalog.Items[0].DownloadURL: plot,
	} {
		if got := request(http.MethodGet, route, http.StatusOK); !bytes.Equal(got.Body.Bytes(), original.Body.Bytes()) {
			t.Fatalf("restore changed the downloaded artifact at %s", route)
		}
	}
	if latest, err := restored.workspaceFile(ctx, project, published.Path); err != nil || latest != edited.WorkspaceFile {
		t.Fatalf("restore changed the current workspace version: %+v (%v)", latest, err)
	}
}

func TestRemoteComparisonLongJobReconnectAndStopRecovery(t *testing.T) {
	ctx := t.Context()
	const token = "long-job-fixture"
	worker, err := OpenComputeWorker(t.TempDir(), "长作业节点替身", token)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	var (
		mu             sync.Mutex
		executions     = make(map[string]int)
		putCounts      = make(map[string]int)
		getJobCounts   = make(map[string]int)
		cancelCounts   = make(map[string]int)
		resultCounts   = make(map[string]int)
		scatterCounts  = make(map[string]int)
		disconnected   atomic.Bool
		dropFirstStop  atomic.Bool
		tamperScatter  atomic.Bool
		tamperResult   atomic.Bool
		lateResultMode atomic.Bool
	)
	dropFirstStop.Store(true)
	tamperScatter.Store(true)
	tamperResult.Store(true)

	started := make(chan string, 4)
	release := make(chan struct{})
	worker.runJob = func(runCtx context.Context, job computeJob, source string) (computeJob, string, string) {
		mu.Lock()
		executions[job.JobID]++
		mu.Unlock()
		started <- job.JobID
		if lateResultMode.Load() {
			<-runCtx.Done()
			// Simulate a late valid calculation finishing right after cancellation.
			return runComputeJob(context.Background(), job, source)
		}
		select {
		case <-release:
		case <-runCtx.Done():
			job.Status = taskFailed
			job.FinishedAt = time.Now().UTC().Format(createdAtLayout)
			return job, "", ""
		}
		return runComputeJob(runCtx, job, source)
	}

	workerHandler := worker.Handler()
	hijackDrop := func(w http.ResponseWriter) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack connection: %v", err)
			return
		}
		_ = conn.Close()
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if disconnected.Load() {
			hijackDrop(w)
			return
		}
		trimmed := strings.TrimPrefix(r.URL.Path, computeJobsPath)
		jobID, rest, _ := strings.Cut(trimmed, "/")
		switch {
		case r.Method == http.MethodPut && rest == "":
			mu.Lock()
			putCounts[jobID]++
			mu.Unlock()
		case r.Method == http.MethodGet && rest == "":
			mu.Lock()
			getJobCounts[jobID]++
			mu.Unlock()
		case r.Method == http.MethodPost && rest == "cancel":
			mu.Lock()
			cancelCounts[jobID]++
			mu.Unlock()
			if dropFirstStop.CompareAndSwap(true, false) {
				saved := httptest.NewRecorder()
				workerHandler.ServeHTTP(saved, r)
				if saved.Code != http.StatusOK {
					t.Errorf("cancel on node returned %d: %s", saved.Code, saved.Body)
				}
				hijackDrop(w)
				return
			}
		case r.Method == http.MethodGet && rest == "artifacts/scatter.svg":
			mu.Lock()
			scatterCounts[jobID]++
			mu.Unlock()
			if tamperScatter.CompareAndSwap(true, false) {
				saved := httptest.NewRecorder()
				workerHandler.ServeHTTP(saved, r)
				body := append([]byte(nil), saved.Body.Bytes()...)
				if saved.Code != http.StatusOK || len(body) == 0 {
					t.Errorf("scatter missing before tamper: %d", saved.Code)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				body[0] = ' '
				w.Header().Set("Content-Type", saved.Header().Get("Content-Type"))
				w.Header().Set("X-Result-SHA256", saved.Header().Get("X-Result-SHA256"))
				_, _ = w.Write(body)
				return
			}
		case r.Method == http.MethodGet && rest == "result":
			mu.Lock()
			resultCounts[jobID]++
			mu.Unlock()
			if tamperResult.CompareAndSwap(true, false) {
				saved := httptest.NewRecorder()
				workerHandler.ServeHTTP(saved, r)
				w.Header().Set("Content-Type", saved.Header().Get("Content-Type"))
				w.Header().Set("X-Result-SHA256", saved.Header().Get("X-Result-SHA256"))
				_, _ = w.Write(append(saved.Body.Bytes(), ' '))
				return
			}
		}
		workerHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()

	node, err := NewComputeNode(upstream.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "long-job", Title: "长作业重连", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	const csvSource = "baseline,variant\n2,8\n4,10\n6,12\n"
	source, err := store.saveWorkspaceDocument(ctx, project.ProjectID, "data/long.csv", "", csvSource)
	if err != nil {
		t.Fatal(err)
	}
	completeComparison := func(requestID string) string {
		t.Helper()
		task, _, err := store.applyTaskRequest(ctx, taskRequest{
			ProjectID: project.ProjectID, RequestID: requestID, Action: proposeTask,
			Kind: experimentComparisonTask, Instruction: "长作业配对验证", InputVersionIDs: []string{source.VersionID},
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.applyTaskRequest(ctx, taskRequest{
			ProjectID: project.ProjectID, TaskID: task.TaskID, RequestID: "confirm-" + requestID,
			Action: confirmTask, ExpectedRevision: task.Revision,
		}, false); err != nil {
			t.Fatal(err)
		}
		claim, err := store.claimQueuedTask(ctx, project.ProjectID, false)
		if err != nil {
			t.Fatal(err)
		}
		calculated, err := store.runExperimentComparison(ctx, project.ProjectID, claim.Inputs[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTask(ctx, project.ProjectID, claim, taskSucceeded, "", calculated); err != nil {
			t.Fatal(err)
		}
		return task.TaskID
	}
	taskID := completeComparison("long-run")
	stopTaskID := completeComparison("long-stop")
	handler := NewHandler(store, nil, HandlerConfig{ComputeNode: node})

	call := func(method, route string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+route, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, w.Code, want, w.Body)
		}
		return w
	}
	type remoteSnapshot struct {
		Configured      bool
		Job             *computeJob
		ReportRetrieved bool
		Scatter         *remoteScatter
	}
	readSnap := func(method, route string) remoteSnapshot {
		t.Helper()
		var snap remoteSnapshot
		if err := json.Unmarshal(call(method, route, http.StatusOK).Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}

	base := projectsPath + "/" + project.ProjectID + "/tasks/"
	path := base + taskID + "/remote-comparison"
	stopPath := base + stopTaskID + "/remote-comparison"

	// 1. Submit long-running job: node accepts asynchronously and stays RUNNING.
	accepted := readSnap(http.MethodPost, path+"/submit")
	if accepted.Job == nil || accepted.Job.Status != taskRunning || accepted.Job.InputSHA256 != source.SHA256 {
		t.Fatalf("expected accepted RUNNING receipt, got %+v", accepted)
	}
	jobID := accepted.Job.JobID
	select {
	case gotID := <-started:
		if gotID != jobID {
			t.Fatalf("started job %s, want %s", gotID, jobID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for job execution to start")
	}

	// Verify idempotent PUT replay while the job is still actively RUNNING returns 200 OK without re-executing,
	// while a different job ID receives 429 NODE_BUSY.
	replayReq, err := http.NewRequestWithContext(ctx, http.MethodPut, upstream.URL+computeJobsPath+jobID, strings.NewReader(csvSource))
	if err != nil {
		t.Fatal(err)
	}
	replayReq.Header.Set("Authorization", "Bearer "+token)
	replayReq.Header.Set("Content-Type", "text/csv")
	replayReq.Header.Set("X-Input-SHA256", source.SHA256)
	replayRes, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	var replayed computeJob
	if err := json.NewDecoder(replayRes.Body).Decode(&replayed); err != nil || replayRes.StatusCode != http.StatusOK ||
		replayed.JobID != jobID || replayed.Status != taskRunning {
		_ = replayRes.Body.Close()
		t.Fatalf("running PUT replay: status=%d job=%+v err=%v", replayRes.StatusCode, replayed, err)
	}
	_ = replayRes.Body.Close()

	busyReq, err := http.NewRequestWithContext(ctx, http.MethodPut, upstream.URL+computeJobsPath+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", strings.NewReader(csvSource))
	if err != nil {
		t.Fatal(err)
	}
	busyReq.Header.Set("Authorization", "Bearer "+token)
	busyReq.Header.Set("Content-Type", "text/csv")
	busyReq.Header.Set("X-Input-SHA256", source.SHA256)
	busyRes, err := http.DefaultClient.Do(busyReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = busyRes.Body.Close()
	if busyRes.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("different job while busy: got %d, want 429", busyRes.StatusCode)
	}

	// 2. Drop connection during the running window; refresh fails, local snapshot preserves jobId + RUNNING.
	disconnected.Store(true)
	call(http.MethodPost, path+"/refresh", http.StatusBadGateway)
	offlineSnap := readSnap(http.MethodGet, path)
	if offlineSnap.Job == nil || offlineSnap.Job.JobID != jobID || offlineSnap.Job.Status != taskRunning {
		t.Fatalf("disconnect lost local receipt: %+v", offlineSnap)
	}

	// 3. Let the node finish while disconnected, then reconnect and query the SAME job ID.
	close(release)
	worker.active.Wait()
	disconnected.Store(false)

	recovered := readSnap(http.MethodPost, path+"/refresh")
	if recovered.Job == nil || recovered.Job.JobID != jobID || recovered.Job.Status != taskSucceeded ||
		!validObjectDigest(recovered.Job.ResultSHA256) || recovered.ReportRetrieved || recovered.Scatter != nil {
		t.Fatalf("unexpected recovered terminal receipt: %+v", recovered)
	}
	mu.Lock()
	execCount, getCount, resCount, scatCount := executions[jobID], getJobCounts[jobID], resultCounts[jobID], scatterCounts[jobID]
	mu.Unlock()
	if execCount != 1 || getCount != 1 || resCount != 0 || scatCount != 0 {
		t.Fatalf("unexpected counts after reconnect: executions=%d getJob=%d results=%d scatters=%d", execCount, getCount, resCount, scatCount)
	}

	// 4. List artifacts, reject tampered scatter, retrieve ONLY scatter while keeping report pending.
	listed := readSnap(http.MethodPost, path+"/artifacts")
	if listed.Scatter == nil || listed.Scatter.Retrieved || listed.ReportRetrieved {
		t.Fatalf("unexpected artifact manifest snapshot: %+v", listed)
	}
	call(http.MethodPost, path+"/retrieve-scatter", http.StatusBadGateway)
	call(http.MethodGet, path+"/scatter", http.StatusNotFound)

	scatterOnly := readSnap(http.MethodPost, path+"/retrieve-scatter")
	if scatterOnly.Scatter == nil || !scatterOnly.Scatter.Retrieved || scatterOnly.ReportRetrieved {
		t.Fatalf("expected scatter retrieved while report stays pending: %+v", scatterOnly)
	}
	call(http.MethodGet, path+"/result", http.StatusNotFound)

	// Confirm scatter is downloadable offline while report is still unretrieved.
	disconnected.Store(true)
	offlinePlot := call(http.MethodGet, path+"/scatter", http.StatusOK)
	if computeDigest(offlinePlot.Body.Bytes()) != scatterOnly.Scatter.SHA256 {
		t.Fatal("offline scatter digest mismatch")
	}
	disconnected.Store(false)

	// 5. Reject tampered report, then retrieve report and verify offline download.
	call(http.MethodPost, path+"/retrieve", http.StatusBadGateway)
	call(http.MethodGet, path+"/result", http.StatusNotFound)
	bothDone := readSnap(http.MethodPost, path+"/retrieve")
	if !bothDone.ReportRetrieved || bothDone.Scatter == nil || !bothDone.Scatter.Retrieved {
		t.Fatalf("expected both artifacts retrieved: %+v", bothDone)
	}
	offlineReport := call(http.MethodGet, path+"/result", http.StatusOK)
	if computeDigest(offlineReport.Body.Bytes()) != bothDone.Job.ResultSHA256 {
		t.Fatal("retrieved report digest mismatch")
	}

	// 6. Stop request response loss + late result rejected by node; reconnect recovers CANCELLED.
	lateResultMode.Store(true)
	stopAccepted := readSnap(http.MethodPost, stopPath+"/submit")
	if stopAccepted.Job == nil || stopAccepted.Job.Status != taskRunning {
		t.Fatalf("expected stop job RUNNING, got %+v", stopAccepted)
	}
	stopJobID := stopAccepted.Job.JobID
	select {
	case gotID := <-started:
		if gotID != stopJobID {
			t.Fatalf("started stop job %s, want %s", gotID, stopJobID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stop job to start")
	}

	// Node accepts /cancel and cancels runCtx, then proxy drops the response.
	cancelling := readSnap(http.MethodPost, stopPath+"/cancel")
	if cancelling.Job == nil || cancelling.Job.JobID != stopJobID || cancelling.Job.Status != taskCancelling {
		t.Fatalf("expected CANCELLING receipt after dropped stop response, got %+v", cancelling)
	}
	// Wait for the late result goroutine to finish and hit finishJob.
	worker.active.Wait()

	// Brief disconnect while CANCELLING preserves local receipt.
	disconnected.Store(true)
	call(http.MethodPost, stopPath+"/refresh", http.StatusBadGateway)
	stillCancelling := readSnap(http.MethodGet, stopPath)
	if stillCancelling.Job == nil || stillCancelling.Job.JobID != stopJobID || stillCancelling.Job.Status != taskCancelling {
		t.Fatalf("disconnect lost CANCELLING receipt: %+v", stillCancelling)
	}
	disconnected.Store(false)

	// Reconnect recovers CANCELLED terminal state with no result artifacts.
	cancelled := readSnap(http.MethodPost, stopPath+"/refresh")
	if cancelled.Job == nil || cancelled.Job.JobID != stopJobID || cancelled.Job.Status != taskCancelled ||
		cancelled.Job.ResultSHA256 != "" || cancelled.ReportRetrieved || cancelled.Scatter != nil {
		t.Fatalf("expected CANCELLED without results, got %+v", cancelled)
	}
	call(http.MethodPost, stopPath+"/retrieve", http.StatusConflict)
	call(http.MethodPost, stopPath+"/artifacts", http.StatusConflict)
	call(http.MethodGet, stopPath+"/result", http.StatusNotFound)
	call(http.MethodGet, stopPath+"/scatter", http.StatusNotFound)

	// Re-submitting cancelled job does not re-execute on node.
	replayStopped := readSnap(http.MethodPost, stopPath+"/submit")
	if replayStopped.Job == nil || *replayStopped.Job != *cancelled.Job {
		t.Fatalf("re-submitting cancelled job altered receipt: %+v", replayStopped)
	}
	mu.Lock()
	stopExecs, stopPuts, stopCancels := executions[stopJobID], putCounts[stopJobID], cancelCounts[stopJobID]
	mu.Unlock()
	if stopExecs != 1 || stopPuts != 1 || stopCancels != 1 {
		t.Fatalf("unexpected stop job counts: executions=%d puts=%d cancels=%d", stopExecs, stopPuts, stopCancels)
	}
}
