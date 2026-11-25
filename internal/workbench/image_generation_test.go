package workbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"research-agent/internal/inference"
)

func TestImageGenerationKeepsRequestAndMaterialAcrossRestart(t *testing.T) {
	ctx, directory := t.Context(), t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	create := func(requestID string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: "河流采样图", TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	projectID, otherID := create("project"), create("other")
	path := projectsPath + "/" + projectID + "/image-generations"
	otherPath := projectsPath + "/" + otherID + "/image-generations"
	const prompt = "绘制河流上游和下游的两站观测示意图。"
	const start = `{"requestId":"generation-1","size":"1536x1024","prompt":"  ` + prompt + `  "}`
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewGray(image.Rect(0, 0, 1536, 1024))); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model, Prompt, Size, Quality string
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" || json.NewDecoder(r.Body).Decode(&request) != nil ||
			request.Model != "image-before" || request.Prompt != prompt || request.Size != "1536x1024" || request.Quality != "low" {
			t.Errorf("generation request did not use the saved configuration: %+v", request)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString(picture.Bytes()))
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL + "/v1", Model: "fixture-text"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(handler http.Handler, method, target, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, target, res.Code, status, res.Body)
		}
		return res
	}
	receipt := func(res *httptest.ResponseRecorder) imageGenerationResponse {
		t.Helper()
		var body imageGenerationResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	config := HandlerConfig{Model: model, ImageModelName: "image-before", ImageModelQuality: "low"}
	handler := NewHandler(store, nil, config)
	if view := receipt(request(handler, http.MethodGet, path, "", http.StatusOK)); !view.Configured || view.Generation != nil || view.Quality != "low" {
		t.Fatalf("unexpected generation panel configuration: %+v", view)
	}
	request(NewHandler(store, nil, HandlerConfig{}), http.MethodPost, path, start, http.StatusServiceUnavailable)

	// Preserve both a waiting task and a claimed task as a service exit would.
	store.tasks.mu.Lock()
	store.tasks.closing = true
	store.tasks.mu.Unlock()
	queued := receipt(request(handler, http.MethodPost, path, start, http.StatusAccepted)).Generation
	if queued == nil || queued.Status != taskQueued || queued.Material != nil || queued.Prompt != prompt || queued.Model != "image-before" {
		t.Fatalf("unexpected queued receipt: %+v", queued)
	}
	if replay := receipt(request(handler, http.MethodPost, path, start, http.StatusOK)).Generation; replay == nil || replay.GenerationID != queued.GenerationID {
		t.Fatalf("replay changed the task: %+v", replay)
	}
	request(handler, http.MethodPost, path, strings.Replace(start, "1536x1024", "1024x1024", 1), http.StatusConflict)
	if res := request(handler, http.MethodPost, path, strings.Replace(start, "generation-1", "another-generation", 1), http.StatusConflict); !strings.Contains(res.Body.String(), "IMAGE_GENERATION_BUSY") {
		t.Fatalf("concurrent generation was not rejected: %s", res.Body)
	}
	interrupted := receipt(request(handler, http.MethodPost, otherPath, start, http.StatusAccepted)).Generation
	claimed, err := store.claimQueuedTask(ctx, otherID, true)
	if err != nil || claimed.Status != taskRunning || claimed.TaskID != interrupted.GenerationID || calls.Load() != 0 {
		t.Fatalf("could not set up a claimed generation: %+v (%v)", claimed, err)
	}
	// The backup observes a queued task before the original workspace dispatches it.
	var archive bytes.Buffer
	if err := store.writeWorkspaceBackup(ctx, t.TempDir(), &archive); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	config.ImageModelName, config.ImageModelQuality = "image-after", "high"
	handler = NewHandler(store, nil, config)
	if err := store.ResumeQueuedTasks(ctx, model); err != nil {
		t.Fatal(err)
	}
	store.tasks.wg.Wait()
	completed := receipt(request(handler, http.MethodGet, path+"?generationId="+queued.GenerationID, "", http.StatusOK)).Generation
	digest := fmt.Sprintf("%x", sha256.Sum256(picture.Bytes()))
	if completed == nil || completed.Status != taskSucceeded || completed.Model != queued.Model || completed.Quality != "low" ||
		completed.Material == nil || completed.Material.SHA256 != digest {
		t.Fatalf("generation did not publish a material from the saved request: %+v", completed)
	}
	interrupted = receipt(request(handler, http.MethodPost, otherPath, start, http.StatusOK)).Generation
	if interrupted == nil || interrupted.GenerationID != claimed.TaskID || interrupted.Status != taskInterrupted || interrupted.Material != nil {
		t.Fatalf("in-flight generation was not preserved as interrupted: %+v", interrupted)
	}
	request(handler, http.MethodGet, otherPath+"?generationId="+queued.GenerationID, "", http.StatusNotFound)
	offline := NewHandler(store, nil, HandlerConfig{})
	if saved := receipt(request(offline, http.MethodPost, path, start, http.StatusOK)).Generation; saved == nil || saved.Material == nil ||
		saved.Material.MaterialID != completed.Material.MaterialID || calls.Load() != 1 {
		t.Fatalf("replay with no model changed the original output: %+v, provider calls %d", saved, calls.Load())
	}
	project := projectsPath + "/" + projectID
	materialPath := project + "/materials/" + completed.Material.MaterialID + "/download"
	taskPath := project + "/tasks/" + queued.GenerationID + "/artifact"
	for _, target := range []string{materialPath, taskPath} {
		if res := request(offline, http.MethodGet, target, "", http.StatusOK); !bytes.Equal(res.Body.Bytes(), picture.Bytes()) {
			t.Fatalf("download does not match published image: %s", target)
		}
	}
	request(offline, http.MethodGet, strings.Replace(taskPath, projectID, otherID, 1), "", http.StatusNotFound)

	destination := filepath.Join(t.TempDir(), "restored")
	if err := restoreWorkspace(ctx, bytes.NewReader(archive.Bytes()), int64(archive.Len()), destination, maxRestoreDatabaseBytes); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredHandler := NewHandler(restored, nil, config)
	copied := receipt(request(restoredHandler, http.MethodGet, path+"?generationId="+queued.GenerationID, "", http.StatusOK)).Generation
	if copied == nil || copied.Status != taskInterrupted || copied.Material != nil || copied.Model != queued.Model {
		t.Fatalf("restored task may dispatch a generation already processed in the original workspace: %+v", copied)
	}
	if err := restored.ResumeQueuedTasks(ctx, model); err != nil {
		t.Fatal(err)
	}
	replayed := receipt(request(restoredHandler, http.MethodPost, path, start, http.StatusOK)).Generation
	restored.tasks.wg.Wait()
	if replayed == nil || replayed.GenerationID != queued.GenerationID || replayed.Status != taskInterrupted || calls.Load() != 1 {
		t.Fatalf("restored replay changed the original receipt or dispatched again: %+v, calls %d", replayed, calls.Load())
	}

	role := imageGenerationRole{modelSnapshot: snapshotModel(model, "image-after", ""), quality: "high"}
	stopped, _, err := store.queueImageGeneration(ctx, otherID, imageGenerationInput{
		RequestID: "stop-generation", Prompt: prompt, Size: "1536x1024",
	}, role)
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.claimQueuedTask(ctx, otherID, true)
	if err != nil || running.TaskID != stopped.GenerationID {
		t.Fatalf("claim stop test: %+v (%v)", running, err)
	}
	stop, err := store.stopProjectTaskQueue(ctx, otherID, taskQueueStopInput{
		RequestID: "stop", Scope: "current", TaskID: running.TaskID, ExpectedRevision: running.Revision,
	})
	if err != nil || stop.StoppingCount != 1 {
		t.Fatalf("stop generation: %+v (%v)", stop, err)
	}
	if err := store.finishTask(ctx, otherID, running, taskSucceeded, "", taskResult{imageMaterial: *completed.Material}); err != nil {
		t.Fatal(err)
	}
	final := receipt(request(handler, http.MethodGet, otherPath+"?generationId="+running.TaskID, "", http.StatusOK)).Generation
	if final == nil || final.Status != taskCancelled || final.Material != nil {
		t.Fatalf("late generation output changed a stopped task: %+v", final)
	}
	if materials, err := store.listMaterials(ctx, otherID, "", 0, 1); err != nil || len(materials) != 0 {
		t.Fatalf("late generation output was published: %+v (%v)", materials, err)
	}
}
