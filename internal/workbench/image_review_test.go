package workbench

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"research-agent/internal/inference"
)

func TestImageReviewAcceptsCompleteImages(t *testing.T) {
	picture := image.NewGray(image.Rect(0, 0, 8, 8))
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var file bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&file, picture)
			} else {
				err = jpeg.Encode(&file, picture, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateImageReviewFile(t.Context(), format, file.Bytes()); err != nil {
				t.Fatalf("valid image was rejected: %v", err)
			}
			file.WriteString("an extra note")
			if err := validateImageReviewFile(t.Context(), format, file.Bytes()); !errors.Is(err, errImageReviewInput) {
				t.Fatalf("expected trailing data to be rejected: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := validateImageReviewFile(ctx, format, file.Bytes()); !errors.Is(err, context.Canceled) {
				t.Fatalf("image validation lost cancellation: %v", err)
			}
		})
	}
}

func TestImageReviewBoundsJPEGScanCount(t *testing.T) {
	envelope := []byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x04, 0xff, 0xd9} // An end marker inside metadata.
	scan := []byte{0xff, 0xda, 0x00, 0x02, 0xff, 0x00, 0xff, 0xd0}     // Stuffed data and a restart marker.
	for _, count := range []int{32, 33} {
		body := append(bytes.Clone(envelope), bytes.Repeat(scan, count)...)
		body = append(body, 0xff, 0xd9)
		err := checkImageReviewJPEG(t.Context(), body)
		if (count == 32 && err != nil) || (count == 33 && !errors.Is(err, errImageReviewInput)) {
			t.Fatalf("JPEG envelope with %d scans: %v", count, err)
		}
	}
}

func TestImageReviewVerdictsRequireEvidence(t *testing.T) {
	for _, test := range []struct {
		name, output string
		want         error
	}{
		{"pass with observation", `{"verdict":"PASS","observedLabels":["测站 A"],"relations":[],"issues":[]}`, nil},
		{"pass without evidence", `{"verdict":"PASS","observedLabels":[],"relations":[],"issues":[]}`, errImageReviewOutput},
		{"pass with an issue", `{"verdict":"PASS","observedLabels":["测站 A"],"relations":[],"issues":["图例被遮挡"]}`, errImageReviewOutput},
		{"unverifiable image", `{"verdict":"UNVERIFIABLE","observedLabels":[],"relations":[],"issues":[]}`, errImageReviewUnverifiable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseImageReview(test.output); !errors.Is(err, test.want) {
				t.Fatalf("parseImageReview returned %v, want %v", err, test.want)
			}
		})
	}
}

func TestImageReviewResumesWithOriginalImageAndModel(t *testing.T) {
	ctx := t.Context()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	makeProject := func(requestID string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: "图表核对", TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	project, other := makeProject("diagram"), makeProject("other")
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewGray(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	material, _, err := store.saveMaterial(ctx, project, "观测图.png", bytes.NewReader(original.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
			Messages        []struct {
				Content []struct {
					Text     string
					ImageURL struct{ URL string } `json:"image_url"`
				}
			}
		}
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "fixture-vision-original" || input.ReasoningEffort != "low" ||
			len(input.Messages) != 1 || len(input.Messages[0].Content) != 2 ||
			!strings.Contains(input.Messages[0].Content[0].Text, "observedLabels") ||
			input.Messages[0].Content[1].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(original.Bytes()) {
			t.Error("worker did not send the pinned model and image")
			http.Error(w, "unexpected test image", http.StatusBadRequest)
			return
		}
		const result = `{"verdict":"ISSUES","observedLabels":["观测区"],"relations":[],"issues":["图例文字不清楚"]}`
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", result)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL + "/v1", Model: "fixture-text"})
	if err != nil {
		t.Fatal(err)
	}
	config := HandlerConfig{Model: model, VisionModelName: "fixture-vision-original", VisionReasoningEffort: "low"}
	request := func(handler http.Handler, method, target, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s returned %d, expected %d: %s", method, target, res.Code, want, res.Body)
		}
		return res
	}
	pathFor := func(projectID, materialID string) string {
		return projectsPath + "/" + projectID + "/materials/" + materialID + "/image-review"
	}
	path := pathFor(project, material.MaterialID)
	const start = `{"requestId":"check-original"}`
	response := func(res *httptest.ResponseRecorder) imageReviewResponse {
		t.Helper()
		var body imageReviewResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	unconfigured := NewHandler(store, nil, HandlerConfig{Model: model})
	if received := response(request(unconfigured, http.MethodGet, path, "", http.StatusOK)); received.Configured || received.Review != nil {
		t.Fatalf("unexpected unconfigured response: %+v", received)
	}
	request(unconfigured, http.MethodPost, path, start, http.StatusServiceUnavailable)
	firstHandler := NewHandler(store, nil, config)
	request(firstHandler, http.MethodGet, pathFor(other, material.MaterialID), "", http.StatusNotFound)

	// Keep the accepted row queued so a new service instance has to claim it.
	store.tasks.mu.Lock()
	store.tasks.closing = true
	store.tasks.mu.Unlock()
	first := response(request(firstHandler, http.MethodPost, path, start, http.StatusAccepted)).Review
	if first == nil || first.MaterialID != material.MaterialID || first.SourceSHA256 != material.SHA256 || first.Status != taskQueued {
		t.Fatalf("unexpected queued image receipt: %+v", first)
	}
	replay := response(request(firstHandler, http.MethodPost, path, `{"requestId":"concurrent-start"}`, http.StatusOK)).Review
	if replay == nil || replay.ReviewID != first.ReviewID || calls.Load() != 0 {
		t.Fatalf("repeat submit replaced the queued receipt: %+v", replay)
	}
	var newer bytes.Buffer
	if err := png.Encode(&newer, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	replacement, _, err := store.saveMaterial(ctx, project, "观测图.png", bytes.NewReader(newer.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request(firstHandler, http.MethodPost, pathFor(project, replacement.MaterialID), start, http.StatusConflict)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	config.VisionModelName, config.VisionReasoningEffort = "fixture-vision-new", "high"
	secondHandler := NewHandler(store, nil, config)
	if err := store.ResumeQueuedTasks(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	store.tasks.wg.Wait()
	completed := response(request(secondHandler, http.MethodGet, path+"?reviewId="+first.ReviewID, "", http.StatusOK)).Review
	if completed == nil || completed.Status != taskSucceeded || completed.Model != "fixture-vision-original" || completed.ReasoningEffort != "low" ||
		completed.Result == nil || completed.Result.Verdict != "ISSUES" || len(completed.Result.Issues) != 1 || completed.SourceSHA256 != material.SHA256 {
		t.Fatalf("incorrect recovered review: %+v", completed)
	}
	if received := response(request(secondHandler, http.MethodGet, pathFor(project, replacement.MaterialID), "", http.StatusOK)); received.Review != nil {
		t.Fatalf("same-name replacement reused an unrelated review: %+v", received)
	}
	request(secondHandler, http.MethodGet, pathFor(project, replacement.MaterialID)+"?reviewId="+first.ReviewID, "", http.StatusNotFound)
	offline := NewHandler(store, nil, HandlerConfig{})
	receipt := response(request(offline, http.MethodPost, path, start, http.StatusOK))
	if receipt.Configured || receipt.Review == nil || receipt.Review.ReviewID != first.ReviewID || receipt.Review.Status != taskSucceeded {
		t.Fatalf("replay with no model connection lost the original result: %+v", receipt)
	}
	artifact := request(offline, http.MethodGet, projectsPath+"/"+project+"/tasks/"+first.ReviewID+"/artifact", "", http.StatusOK)
	var report imageReview
	if err := json.Unmarshal(artifact.Body.Bytes(), &report); err != nil || report.SourceSHA256 != material.SHA256 ||
		artifact.Header().Get("X-Source-SHA256") != material.SHA256 || report.RuleRevision != imageReviewRule ||
		report.Result == nil || report.Result.Verdict != "ISSUES" || report.Model != completed.Model {
		t.Fatalf("download does not match the source and saved review: %+v (%v)", report, err)
	}
	request(offline, http.MethodGet, projectsPath+"/"+other+"/tasks/"+first.ReviewID+"/artifact", "", http.StatusNotFound)
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, expected one", calls.Load())
	}
}
