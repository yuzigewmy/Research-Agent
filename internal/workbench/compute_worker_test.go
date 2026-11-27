package workbench

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	var replay computeJob
	if err := json.Unmarshal(request(http.MethodPut, statusPath, source, digest, true, http.StatusOK).Body.Bytes(), &replay); err != nil || replay != interrupted {
		t.Fatalf("interruption replay differs: %+v (%v)", replay, err)
	}
	replay = computeJob{}
	if err := json.Unmarshal(request(http.MethodPut, job, source, digest, true, http.StatusOK).Body.Bytes(), &replay); err != nil || replay != first {
		t.Fatalf("replayed receipt differs: %+v (%v)", replay, err)
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
}
