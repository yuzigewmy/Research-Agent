package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"research-agent/internal/inference"
)

func TestConversationSyncsOnlyItsCompletedTaskResultsOnce(t *testing.T) {
	const prefix = "# 已保存的研究计划\n\n"
	const keptEnd = "节选在此结束"
	const omitted = "Outside the excerpt."
	const unrelated = "Independent task without a source."
	const failedTask = "Task that failed to publish."
	const foreign = "Task from the neighboring project."
	excerptEnd := strings.Repeat("测", 1200-utf8.RuneCountInString(prefix+keptEnd)) + keptEnd
	markdown := prefix + excerptEnd + omitted

	var calls atomic.Int32
	prompts := make(chan []inference.Message, 5)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		call := calls.Add(1)
		if call > 5 || json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) == 0 {
			http.Error(w, "unexpected conversation request", http.StatusBadRequest)
			return
		}
		prompts <- input.Messages
		finish := "stop"
		if call == 1 {
			finish = "length"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Acknowledged.\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	dir := t.TempDir()
	open := func() *Store {
		t.Helper()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	store := open()
	prepareProject := func(id string) (string, string) {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: "Field observations", TemplateID: "experiment"})
		if err != nil {
			t.Fatal(err)
		}
		turn, _, _, err := store.beginTurn(ctx, project.ProjectID, sendTurnInput{RequestID: "source", Text: "Plan a river survey.", Mode: turnModeChat}, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.appendTurnEvent(ctx, turn.TurnID, &TurnEvent{Kind: "delta", Text: "Compare the observations."}); err != nil {
			t.Fatal(err)
		}
		if err := store.appendTurnEvent(ctx, turn.TurnID, &TurnEvent{Kind: "done", Status: "COMPLETED"}); err != nil {
			t.Fatal(err)
		}
		return project.ProjectID, turn.TurnID
	}
	type receipt struct {
		TaskID       string `json:"taskId"`
		SourceTurnID string `json:"sourceTurnId"`
		Instruction  string `json:"instruction"`
	}
	settleTask := func(projectID, sourceID, instruction, status string) receipt {
		t.Helper()
		proposed, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: projectID, RequestID: "propose-" + instruction,
			Action: proposeTask, SourceTurnID: sourceID, Instruction: instruction}, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.applyTaskRequest(ctx, taskRequest{ProjectID: projectID, TaskID: proposed.TaskID,
			RequestID: "confirm-" + instruction, Action: confirmTask, ExpectedRevision: proposed.Revision}, true); err != nil {
			t.Fatal(err)
		}
		task, err := store.claimQueuedTask(ctx, projectID, true)
		if err != nil || task.TaskID != proposed.TaskID {
			t.Fatalf("claim task %q returned %+v, %v", instruction, task, err)
		}
		if err := store.finishTask(ctx, projectID, task, status, "", taskResult{markdown: markdown}); err != nil {
			t.Fatal(err)
		}
		return receipt{task.TaskID, sourceID, instruction}
	}
	projectID, sourceID := prepareProject("main")
	foreignID, foreignSourceID := prepareProject("foreign")
	foreignResult := settleTask(foreignID, foreignSourceID, foreign, taskSucceeded)
	allResults := []receipt{
		foreignResult,
		settleTask(projectID, "", unrelated, taskSucceeded),
		settleTask(projectID, sourceID, failedTask, taskFailed),
	}
	pending := make(map[string]receipt)
	for n := 1; n <= 5; n++ {
		result := settleTask(projectID, sourceID, fmt.Sprintf("Analyze field group %d.", n), taskSucceeded)
		allResults = append(allResults, result)
		pending[result.TaskID] = result
	}

	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	send := func(method, projectID, suffix, body string, want int) []byte {
		t.Helper()
		path := "http://127.0.0.1" + projectsPath + "/" + projectID + "/turns" + suffix
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	type observedTurn struct {
		Turn
		TaskResults []receipt `json:"taskResults"`
	}
	post := func(projectID, requestID, text string, want int) observedTurn {
		t.Helper()
		body := fmt.Sprintf(`{"requestId":%q,"text":%q}`, requestID, text)
		var turn observedTurn
		if err := json.Unmarshal(send(http.MethodPost, projectID, "", body, want), &turn); err != nil || turn.TurnID == "" {
			t.Fatalf("invalid turn receipt %+v, %v", turn, err)
		}
		return turn
	}
	settleTurn := func(projectID, requestID string, started observedTurn, status string) {
		t.Helper()
		stream := send(http.MethodGet, projectID, "/"+started.TurnID+"/stream", "", http.StatusOK)
		if !strings.Contains(string(stream), `"status":"`+status+`"`) {
			t.Fatalf("turn did not finish as %s: %s", status, stream)
		}
		replay := post(projectID, requestID, started.Text, http.StatusOK)
		if replay.TurnID != started.TurnID || replay.Status != status || !slices.Equal(replay.TaskResults, started.TaskResults) {
			t.Fatalf("replay changed the saved turn: %+v", replay)
		}
	}
	checkPrompt := func(turn observedTurn) {
		t.Helper()
		var messages []inference.Message
		select {
		case messages = <-prompts:
		default:
			t.Fatal("the conversation did not call the model")
		}
		if len(messages) < 2 || messages[len(messages)-1].Content != turn.Text {
			t.Fatal("the current user message must follow the separate task context")
		}
		wantCount := len(turn.TaskResults)
		fullPrompt := strings.Builder{}
		for _, message := range messages {
			fullPrompt.WriteString(message.Content)
			fullPrompt.WriteByte('\n')
		}
		prompt := fullPrompt.String()
		if strings.Count(prompt, excerptEnd) != wantCount || strings.Count(prompt, strings.TrimSpace(prefix)) != wantCount ||
			strings.Contains(prompt, excerptEnd+omitted[:1]) {
			t.Fatal("the model did not receive the 1200-character excerpt for each fixed result")
		}
		for _, result := range allResults {
			if want := slices.Contains(turn.TaskResults, result); strings.Contains(prompt, result.Instruction) != want {
				t.Fatalf("task %q in model context, want present=%t", result.Instruction, want)
			}
		}
		if wantCount > 0 {
			resultContext := messages[len(messages)-2].Content
			if strings.Count(resultContext, excerptEnd) != wantCount {
				t.Fatal("task results must share one context message before the user message")
			}
			for _, result := range turn.TaskResults {
				if !strings.Contains(resultContext, result.Instruction) {
					t.Fatalf("missing task %q in the task context message", result.Instruction)
				}
			}
		}
	}

	failed := post(projectID, "failed", "Read the available results.", http.StatusAccepted)
	if len(failed.TaskResults) != 4 {
		t.Fatalf("a new turn must snapshot up to four results: %+v", failed.TaskResults)
	}
	for _, result := range failed.TaskResults {
		if want, ok := pending[result.TaskID]; !ok || result != want {
			t.Fatalf("unexpected task in the snapshot: %+v", result)
		}
		delete(pending, result.TaskID)
	}
	settleTurn(projectID, "failed", failed, "FAILED")
	checkPrompt(failed)

	const interruptedText = "Read the results before a restart."
	started, _, _, err := store.beginTurn(ctx, projectID, sendTurnInput{RequestID: "interrupted", Text: interruptedText, Mode: turnModeChat}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	interrupted := post(projectID, "interrupted", interruptedText, http.StatusOK)
	if interrupted.TurnID != started.TurnID || interrupted.Status != "INTERRUPTED" ||
		!slices.Equal(interrupted.TaskResults, failed.TaskResults) || calls.Load() != 1 {
		t.Fatalf("failure or restart consumed pending results: %+v, calls=%d", interrupted, calls.Load())
	}

	succeeded := post(projectID, "retry", failed.Text, http.StatusAccepted)
	if succeeded.TurnID == failed.TurnID || !slices.Equal(succeeded.TaskResults, failed.TaskResults) {
		t.Fatalf("a new request after failure should include the same results: %+v", succeeded)
	}
	settleTurn(projectID, "retry", succeeded, "COMPLETED")
	checkPrompt(succeeded)

	remainder := post(projectID, "remainder", "Send the remaining result.", http.StatusAccepted)
	if len(remainder.TaskResults) != 1 || pending[remainder.TaskResults[0].TaskID] != remainder.TaskResults[0] {
		t.Fatalf("completion must consume only the snapshotted results: %+v", remainder.TaskResults)
	}
	settleTurn(projectID, "remainder", remainder, "COMPLETED")
	checkPrompt(remainder)
	empty := post(projectID, "empty", "Is any other result pending?", http.StatusAccepted)
	if len(empty.TaskResults) != 0 {
		t.Fatalf("consumed results reappeared: %+v", empty.TaskResults)
	}
	settleTurn(projectID, "empty", empty, "COMPLETED")
	checkPrompt(empty)

	isolated := post(foreignID, "isolated", "Read this project's results.", http.StatusAccepted)
	if !slices.Equal(isolated.TaskResults, []receipt{foreignResult}) {
		t.Fatalf("the other project lost or received extra results: %+v", isolated.TaskResults)
	}
	settleTurn(foreignID, "isolated", isolated, "COMPLETED")
	checkPrompt(isolated)
	if calls.Load() != 5 {
		t.Fatalf("turn replay dispatched another model request, calls=%d", calls.Load())
	}
}
