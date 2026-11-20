package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
	"research-agent/internal/literature"
)

func TestTopicWorkflowRetriesAndUsesSelectedCandidateWithFrozenSources(t *testing.T) {
	const requirements = "Use paired measurements at existing monitoring sites."
	const originalDOI, laterDOI = "10.5555/stream-baselines", "10.5555/later-stream-study"
	const recommendedTitle, selectedTitle = "Bank shade comparison", "Upstream temperature differences"
	originalKey, laterKey := citationKey(originalDOI), citationKey(laterDOI)
	const selectedSummary = "Compare upstream sites with paired measurements."
	contract := "# Research agreement\n\n" + selectedTitle + " [@" + originalKey + "]"

	brainstorm := `{"ideas":[
		{"title":"Bank shade comparison","summary":"Record shade and temperature at paired sites."},
		{"title":"Upstream temperature differences","summary":"Compare upstream sites with paired measurements."},
		{"title":"Canopy variation","summary":"Compare temperature across canopy types."},
		{"title":"Rainfall and flow","summary":"Pair rainfall and water level records."},
		{"title":"Bank restoration","summary":"Compare restored and unrestored sites."},
		{"title":"Seasonal range","summary":"Compare seasonal temperature ranges."},
		{"title":"Site access","summary":"Assess coverage gaps by site access."},
		{"title":"Water clarity","summary":"Compare clarity before and after rainfall."}
	]}`
	shortlist := func(evidenceKey string) string {
		return fmt.Sprintf(`{"candidates":[
			{"number":1,"noveltyScore":8,"strength":"Reliable baseline","risk":"Variable shade","effort":"Low","evidenceKeys":[]},
			{"number":2,"noveltyScore":7,"strength":"Paired observations","risk":"Limited field access","effort":"Moderate","evidenceKeys":[%q]},
			{"number":3,"noveltyScore":6,"strength":"Repeatable measures","risk":"Seasonal imbalance","effort":"Moderate","evidenceKeys":[]}
		],"recommendedNumber":1,"recommendationReason":"Site access has already been established."}`, evidenceKey)
	}
	var calls atomic.Int32
	shortlistStarted := make(chan struct{})
	laterSourceSaved := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("model did not receive the workflow prompt")
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		prompt := input.Messages[len(input.Messages)-1].Content
		var output string
		switch call {
		case 1:
			output = brainstorm
		case 2, 3:
			if !strings.Contains(prompt, recommendedTitle) || !strings.Contains(prompt, selectedTitle) ||
				!strings.Contains(prompt, originalKey) || strings.Contains(prompt, laterDOI) || strings.Contains(prompt, laterKey) {
				t.Errorf("shortlist call %d did not use the saved ideas and frozen sources", call)
			}
			output = shortlist(originalKey)
			if call == 2 {
				close(shortlistStarted)
				select {
				case <-laterSourceSaved:
				case <-r.Context().Done():
					return
				}
				output = shortlist(laterKey)
			}
		case 4:
			for _, required := range []string{selectedTitle, selectedSummary, requirements, originalDOI} {
				if !strings.Contains(prompt, required) {
					t.Errorf("contract prompt omitted %q", required)
				}
			}
			for _, unexpected := range []string{recommendedTitle, "Canopy variation", "Water clarity", laterDOI, laterKey} {
				if strings.Contains(prompt, unexpected) {
					t.Errorf("contract prompt included %q", unexpected)
				}
			}
			output = contract
		default:
			t.Errorf("unexpected model call %d", call)
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, body string, want int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res.Body.String()
	}
	decode := func(body string, out any) {
		t.Helper()
		if err := json.Unmarshal([]byte(body), out); err != nil {
			t.Fatalf("invalid response %s: %v", body, err)
		}
	}
	var project Project
	decode(request(http.MethodPost, projectsPath, `{"requestId":"topic-project","title":"River monitoring","templateId":"topic"}`, http.StatusCreated), &project)
	base := projectsPath + "/" + project.ProjectID + "/workflow"
	addSource := func(doi, title string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		if _, _, err := store.saveCitation(ctx, project.ProjectID, literature.Work{DOI: doi, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	type observedRun struct {
		WorkflowRun
		TopicChoice *struct {
			Candidates []struct {
				Number       int      `json:"number"`
				Title        string   `json:"title"`
				Summary      string   `json:"summary"`
				NoveltyScore int      `json:"noveltyScore"`
				Strength     string   `json:"strength"`
				Risk         string   `json:"risk"`
				Effort       string   `json:"effort"`
				EvidenceKeys []string `json:"evidenceKeys"`
			} `json:"candidates"`
			RecommendedNumber    int    `json:"recommendedNumber"`
			RecommendationReason string `json:"recommendationReason"`
			SelectedNumber       int    `json:"selectedNumber"`
		} `json:"topicChoice"`
	}
	waitFor := func(status string) observedRun {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var snapshot struct{ Run *observedRun }
			decode(request(http.MethodGet, base, "", http.StatusOK), &snapshot)
			if snapshot.Run != nil && snapshot.Run.Status == status {
				return *snapshot.Run
			}
			if snapshot.Run == nil || (snapshot.Run.Status != taskQueued && snapshot.Run.Status != taskRunning) || time.Now().After(deadline) {
				t.Fatalf("want %s, got %+v", status, snapshot.Run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	changeBody := func(requestID string, run observedRun, candidate int) string {
		if candidate == 0 {
			return fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d}`, requestID, run.RunID, run.Revision)
		}
		return fmt.Sprintf(`{"requestId":%q,"runId":%q,"expectedRevision":%d,"candidateNumber":%d}`, requestID, run.RunID, run.Revision, candidate)
	}

	addSource(originalDOI, "Stream temperature baselines")
	request(http.MethodPost, base, fmt.Sprintf(`{"requestId":"start","checkpoints":true,"topic":{"requirements":%q}}`, requirements), http.StatusCreated)
	select {
	case <-shortlistStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("the model did not reach the shortlist")
	}
	addSource(laterDOI, "Later temperature study")
	close(laterSourceSaved)
	failed := waitFor(taskFailed)
	if failed.WorkflowKey != "topic-v1" || len(failed.Steps) != 4 || failed.Steps[2].Key != "shortlist" ||
		failed.Steps[2].Status != taskFailed || failed.TopicChoice != nil || calls.Load() != 2 {
		t.Fatalf("unknown citation should fail the shortlist: %+v, calls=%d", failed, calls.Load())
	}
	stepPath := base + "/" + failed.RunID + "/steps/"
	request(http.MethodGet, stepPath+"shortlist/artifact", "", http.StatusNotFound)
	request(http.MethodPost, base+"/continue", changeBody("retry-shortlist", failed, 0), http.StatusAccepted)
	waiting := waitFor("WAITING_TOPIC_CHOICE")
	choice := waiting.TopicChoice
	if choice == nil || choice.RecommendedNumber != 1 || choice.RecommendationReason == "" || choice.SelectedNumber != 0 ||
		len(choice.Candidates) != 3 || waiting.RunID != failed.RunID || waiting.SourceCount != 1 || calls.Load() != 3 {
		t.Fatalf("unexpected topic checkpoint: %+v, choice=%+v, calls=%d", waiting, choice, calls.Load())
	}
	var found bool
	for _, candidate := range choice.Candidates {
		if candidate.Number != 2 {
			continue
		}
		found = candidate.Title == selectedTitle && candidate.Summary == selectedSummary && candidate.NoveltyScore == 7 &&
			candidate.Strength == "Paired observations" && candidate.Risk == "Limited field access" && candidate.Effort == "Moderate" &&
			len(candidate.EvidenceKeys) == 1 && candidate.EvidenceKeys[0] == originalKey
	}
	if !found {
		t.Fatalf("selected candidate was not assembled from the saved ideas and sources: %+v", choice.Candidates)
	}
	request(http.MethodPost, base+"/continue", changeBody("cannot-continue", waiting, 0), http.StatusConflict)
	request(http.MethodPost, base+"/select-topic", changeBody("stale-choice", failed, 2), http.StatusConflict)
	const selectionID = "select-second-topic"
	selection := changeBody(selectionID, waiting, 2)
	request(http.MethodPost, base+"/select-topic", selection, http.StatusOK)
	completed := waitFor(workflowDone)
	request(http.MethodPost, base+"/select-topic", selection, http.StatusOK)
	request(http.MethodPost, base+"/select-topic", changeBody(selectionID, waiting, 1), http.StatusConflict)
	replayed := waitFor(workflowDone)
	if replayed.RunID != failed.RunID || replayed.Revision != completed.Revision || replayed.TopicChoice == nil ||
		replayed.TopicChoice.SelectedNumber != 2 || calls.Load() != 4 {
		t.Fatalf("topic selection or replay changed the run unexpectedly: %+v, calls=%d", replayed, calls.Load())
	}
	if got := request(http.MethodGet, stepPath+"contract/artifact", "", http.StatusOK); got != contract {
		t.Fatalf("wrong research agreement: %q", got)
	}
	if got := request(http.MethodGet, stepPath+"sources/artifact", "", http.StatusOK); !strings.Contains(got, originalDOI) || strings.Contains(got, laterDOI) {
		t.Fatalf("wrong frozen source report: %s", got)
	}
}
