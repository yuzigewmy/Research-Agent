package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeWorkerUpgradesExistingReceipts(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", workspaceSQLiteURI(filepath.Join(dir, "compute.sqlite3"), ""))
	if err != nil {
		t.Fatal(err)
	}
	const id = "4123456789abcdef0123456789abcdef"
	const started = "2025-01-01T00:00:00.000000000Z"
	const result = `{"method":"paired-mean-v1"}`
	digest, resultDigest := computeDigest([]byte("fixture")), computeDigest([]byte(result))
	legacySchema := strings.Replace(computeWorkerSchema, "'CANCELLING', 'CANCELLED', ", "", 1)
	if _, err := db.Exec(legacySchema); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO jobs VALUES (?, ?, 'SUCCEEDED', ?, ?, ?, ?, '')`, id, digest, started, started, resultDigest, result)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("save legacy receipt: %v, %v", err, closeErr)
	}
	worker, err := OpenComputeWorker(dir, "河流节点", "worker-fixture-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	job, body, err := worker.lookupJob(t.Context(), id)
	if err != nil || job != (computeJob{JobID: id, InputSHA256: digest, Status: taskSucceeded, StartedAt: started, FinishedAt: started, ResultSHA256: resultDigest}) || body != result {
		t.Fatalf("upgrade changed the old result: %+v (%v)", job, err)
	}
	manifestRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, computeJobsPath+id+"/artifacts", nil)
	manifestRequest.Header.Set("Authorization", "Bearer worker-fixture-credential")
	response := httptest.NewRecorder()
	worker.Handler().ServeHTTP(response, manifestRequest)
	var manifest computeArtifactManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil || response.Code != http.StatusOK ||
		manifest.JobID != id || manifest.InputSHA256 != digest || len(manifest.Items) != 1 ||
		manifest.Items[0] != (computeArtifact{computeReportName, len(result), resultDigest}) {
		t.Fatalf("old job did not advertise its original report: %d %+v (%v)", response.Code, manifest, err)
	}
}

func TestComputeWorkerKeepsJobAndResultAcrossRestart(t *testing.T) {
	const token = "worker-fixture-credential"
	const source = "baseline,variant\n2,8\n4,10\n"
	const job = "/api/v1/jobs/0123456789abcdef0123456789abcdef"
	const other = "/api/v1/jobs/1123456789abcdef0123456789abcdef"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	dir := t.TempDir()
	worker, err := OpenComputeWorker(dir, "河流节点", token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.db.Close() })
	request := func(method, path, body, hash string, authenticated bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if hash != "" {
			r.Header.Set("Content-Type", "text/csv")
			r.Header.Set("X-Input-SHA256", hash)
		}
		w := httptest.NewRecorder()
		worker.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w
	}
	request(http.MethodGet, "/api/v1/capabilities", "", "", false, http.StatusUnauthorized)
	request(http.MethodGet, "/api/v1/capabilities", "", "", true, http.StatusOK)
	request(http.MethodPut, job, source+"1,2\n", digest, true, http.StatusUnprocessableEntity)
	request(http.MethodGet, job, "", "", true, http.StatusNotFound)
	var first computeJob
	if err := json.Unmarshal(request(http.MethodPut, job, source, digest, true, http.StatusCreated).Body.Bytes(), &first); err != nil ||
		first.Status != taskSucceeded || first.InputSHA256 != digest || first.ResultSHA256 == "" {
		t.Fatalf("wrong first receipt: %+v (%v)", first, err)
	}
	// Simulate a process exit after the running receipt was committed.
	const interruptedID = "2123456789abcdef0123456789abcdef"
	const startedAt = "2025-01-01T00:00:00.000000000Z"
	if _, err := worker.db.Exec(`INSERT INTO jobs (job_id, input_sha256, status, started_at) VALUES (?, ?, 'RUNNING', ?)`, interruptedID, digest, startedAt); err != nil {
		t.Fatal(err)
	}
	const stoppingID = "5123456789abcdef0123456789abcdef"
	if _, err := worker.db.Exec(`INSERT INTO jobs (job_id, input_sha256, status, started_at) VALUES (?, ?, 'CANCELLING', ?)`, stoppingID, digest, startedAt); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	worker, err = OpenComputeWorker(dir, "河流节点", token)
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, other, "", "", true, http.StatusNotFound)
	var interrupted computeJob
	statusPath := computeJobsPath + interruptedID
	if err := json.Unmarshal(request(http.MethodGet, statusPath, "", "", true, http.StatusOK).Body.Bytes(), &interrupted); err != nil ||
		interrupted.Status != taskInterrupted || interrupted.StartedAt != startedAt || interrupted.FinishedAt < startedAt || interrupted.InputSHA256 != digest {
		t.Fatalf("wrong interruption receipt: %+v (%v)", interrupted, err)
	}
	request(http.MethodGet, statusPath+"/result", "", "", true, http.StatusNotFound)
	var stoppedAfterRestart computeJob
	if err := json.Unmarshal(request(http.MethodGet, computeJobsPath+stoppingID, "", "", true, http.StatusOK).Body.Bytes(), &stoppedAfterRestart); err != nil ||
		stoppedAfterRestart.Status != taskCancelled || stoppedAfterRestart.StartedAt != startedAt || stoppedAfterRestart.FinishedAt < startedAt ||
		stoppedAfterRestart.InputSHA256 != digest || stoppedAfterRestart.ResultSHA256 != "" {
		t.Fatalf("wrong recovered stop receipt: %+v (%v)", stoppedAfterRestart, err)
	}
	var stopped computeJob
	if err := json.Unmarshal(request(http.MethodPost, other+"/cancel", "", digest, true, http.StatusOK).Body.Bytes(), &stopped); err != nil ||
		stopped.Status != taskCancelled || stopped.StartedAt != "" || stopped.InputSHA256 != digest {
		t.Fatalf("stop before submission did not reserve the job ID: %+v (%v)", stopped, err)
	}
	var delayed computeJob
	if err := json.Unmarshal(request(http.MethodPut, other, source, digest, true, http.StatusOK).Body.Bytes(), &delayed); err != nil || delayed != stopped {
		t.Fatalf("late submission changed the stop receipt: %+v (%v)", delayed, err)
	}
	var completed computeJob
	if err := json.Unmarshal(request(http.MethodPost, job+"/cancel", "", digest, true, http.StatusOK).Body.Bytes(), &completed); err != nil || completed != first {
		t.Fatalf("stop changed a completed job: %+v (%v)", completed, err)
	}
	var replay computeJob
	if err := json.Unmarshal(request(http.MethodPut, statusPath, source, digest, true, http.StatusOK).Body.Bytes(), &replay); err != nil || replay != interrupted {
		t.Fatalf("interruption replay differs: %+v (%v)", replay, err)
	}
	replay = computeJob{}
	if err := json.Unmarshal(request(http.MethodPut, job, source, digest, true, http.StatusOK).Body.Bytes(), &replay); err != nil || replay != first {
		t.Fatalf("replayed receipt differs: %+v (%v)", replay, err)
	}
	const runningID = "3123456789abcdef0123456789abcdef"
	if _, err := worker.db.Exec(`INSERT INTO jobs (job_id, input_sha256, status, started_at) VALUES (?, ?, 'RUNNING', ?)`, runningID, digest, startedAt); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	worker.activeID, worker.cancel = runningID, cancel
	runningPath := computeJobsPath + runningID
	var stopping computeJob
	if err := json.Unmarshal(request(http.MethodPost, runningPath+"/cancel", "", digest, true, http.StatusOK).Body.Bytes(), &stopping); err != nil || stopping.Status != taskCancelling || runCtx.Err() == nil {
		t.Fatalf("stop did not cancel the active calculation: %+v (%v)", stopping, err)
	}
	lateResult := first
	lateResult.JobID = runningID
	cancelled, err := worker.finishJob(t.Context(), lateResult, `{"late":true}`, "<svg/>")
	if err != nil || cancelled.Status != taskCancelled || cancelled.ResultSHA256 != "" {
		t.Fatalf("late success overrode cancellation: %+v (%v)", cancelled, err)
	}
	request(http.MethodGet, runningPath+"/result", "", "", true, http.StatusNotFound)
	request(http.MethodGet, runningPath+"/artifacts", "", "", true, http.StatusNotFound)
	request(http.MethodGet, runningPath+"/artifacts/scatter.svg", "", "", true, http.StatusNotFound)
	var repeatedStop computeJob
	if err := json.Unmarshal(request(http.MethodPost, runningPath+"/cancel", "", digest, true, http.StatusOK).Body.Bytes(), &repeatedStop); err != nil || repeatedStop != cancelled {
		t.Fatalf("repeated stop did not return the saved receipt: %+v (%v)", repeatedStop, err)
	}
	changed := source + "5,8\n"
	request(http.MethodPut, job, changed, fmt.Sprintf("%x", sha256.Sum256([]byte(changed))), true, http.StatusConflict)
	result := request(http.MethodGet, job+"/result", "", "", true, http.StatusOK)
	if hash := fmt.Sprintf("%x", sha256.Sum256(result.Body.Bytes())); hash != first.ResultSHA256 || result.Header().Get("X-Result-SHA256") != hash {
		t.Fatal("retrieved result does not match the receipt")
	}
	var report computeJobResult
	if err := json.Unmarshal(result.Body.Bytes(), &report); err != nil || report.Method != experimentComparisonMethod || report.InputSHA256 != digest ||
		report.Metrics != (experimentMetrics{PairedRows: 2, BaselineMean: "3", VariantMean: "9", MeanDifference: "6"}) {
		t.Fatalf("wrong result: %+v (%v)", report, err)
	}
	var manifest computeArtifactManifest
	if err := json.Unmarshal(request(http.MethodGet, job+"/artifacts", "", "", true, http.StatusOK).Body.Bytes(), &manifest); err != nil ||
		manifest.JobID != first.JobID || manifest.InputSHA256 != digest || len(manifest.Items) != 2 {
		t.Fatalf("wrong artifact manifest: %+v (%v)", manifest, err)
	}
	if manifest.Items[0] != (computeArtifact{computeReportName, result.Body.Len(), first.ResultSHA256}) {
		t.Fatalf("unexpected report entry: %+v", manifest.Items[0])
	}
	scatter := request(http.MethodGet, job+"/artifacts/scatter.svg", "", "", true, http.StatusOK)
	wantPlot, err := renderExperimentPlot(t.Context(), experimentComparisonReport{Method: report.Method,
		Input: taskInput{SHA256: digest}, Metrics: report.Metrics}, source)
	if err != nil || scatter.Body.String() != wantPlot || manifest.Items[1] != (computeArtifact{computeScatterName, len(wantPlot), computeDigest([]byte(wantPlot))}) ||
		scatter.Header().Get("X-Result-SHA256") != manifest.Items[1].SHA256 {
		t.Fatalf("unexpected scatter artifact: %+v (%v)", manifest.Items[1], err)
	}
}
