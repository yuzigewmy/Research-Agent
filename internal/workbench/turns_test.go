package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"research-agent/internal/inference"
)

func TestConversationReceiptAndReplaySurviveReopening(t *testing.T) {
	const cited = "第一组样本水温为 18°C。"
	const uncited = "未选中版本的温度为 22°C。"
	const credential = "fixture-credential"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model           string              `json:"model"`
			ReasoningEffort string              `json:"reasoning_effort"`
			Messages        []inference.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid model input", http.StatusBadRequest)
			return
		}
		last := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(last, cited) || strings.Contains(last, uncited) {
			http.Error(w, "wrong material version", http.StatusBadRequest)
			return
		}
		wantModel, wantEffort := "fixture-before", "max"
		if strings.Contains(last, "核对对照组") {
			wantModel, wantEffort = "fixture-after", "low"
		}
		if input.Model != wantModel || input.ReasoningEffort != wantEffort {
			t.Errorf("model = %q / %q, want %q / %q", input.Model, input.ReasoningEffort, wantModel, wantEffort)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"基线\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"与对照组\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	configured := func(name, effort string) *inference.Client {
		t.Helper()
		client, err := inference.New(inference.Config{
			BaseURL: upstream.URL, Model: name, APIKey: credential, ReasoningEffort: effort,
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	model := configured("fixture-before", "max")

	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: "project", Title: "实验记录", TemplateID: "experiment"})
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := store.saveMaterial(context.Background(), project.ProjectID, "采样记录.txt", strings.NewReader(cited))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := store.saveMaterial(context.Background(), project.ProjectID, material.Name, strings.NewReader(uncited))
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project.ProjectID + "/turns"

	request := func(handler http.Handler, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d (%s)", method, path, res.Code, res.Body.String())
		}
		return res
	}
	assertModel := func(body []byte, name, effort string) {
		t.Helper()
		var receipt struct{ Model, ReasoningEffort string }
		if err := json.Unmarshal(body, &receipt); err != nil || receipt.Model != name || receipt.ReasoningEffort != effort {
			t.Fatalf("model receipt = %+v (%v), want %s / %s", receipt, err, name, effort)
		}
		if strings.Contains(string(body), credential) || strings.Contains(string(body), upstream.URL) {
			t.Fatal("turn receipt exposed private model configuration")
		}
	}

	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	input := fmt.Sprintf(`{"requestId":"turn-1","text":"设计实验","referenceIds":[%q]}`, material.MaterialID)
	request(handler, "POST", base, `{"requestId":"bad-reference","text":"设计实验","referenceIds":["missing"]}`, http.StatusUnprocessableEntity)
	started := request(handler, "POST", base, input, http.StatusAccepted)
	assertModel(started.Body.Bytes(), "fixture-before", "max")
	var turn Turn
	if err := json.Unmarshal(started.Body.Bytes(), &turn); err != nil || turn.TurnID == "" {
		t.Fatalf("invalid receipt: %v, %s", err, started.Body.String())
	}
	request(handler, "POST", base, fmt.Sprintf(`{"requestId":"turn-1","text":"设计实验","referenceIds":[%q]}`, other.MaterialID), http.StatusConflict)

	streamPath := base + "/" + turn.TurnID + "/stream"
	stream := request(handler, "GET", streamPath, "", http.StatusOK)
	var firstDelta int64
	for _, line := range strings.Split(stream.Body.String(), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var event TurnEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			if event.Text == "基线" {
				firstDelta = event.Seq
			}
		}
	}
	if firstDelta == 0 || !strings.Contains(stream.Body.String(), `"status":"COMPLETED"`) {
		t.Fatalf("incomplete stream: %s", stream.Body.String())
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler = NewHandler(reopened, nil, HandlerConfig{})
	stored := request(handler, "POST", base, input, http.StatusOK)
	assertModel(stored.Body.Bytes(), "fixture-before", "max")
	if err := json.Unmarshal(stored.Body.Bytes(), &turn); err != nil || turn.Answer != "基线与对照组" || turn.Status != "COMPLETED" {
		t.Fatalf("invalid saved turn: %v, %s", err, stored.Body.String())
	}
	if len(turn.References) != 1 || turn.References[0].MaterialID != material.MaterialID || turn.References[0].SHA256 != material.SHA256 {
		t.Fatalf("saved turn lost its original material version: %+v", turn.References)
	}
	replay := request(handler, "GET", streamPath+"?after="+strconv.FormatInt(firstDelta, 10), "", http.StatusOK)
	if strings.Contains(replay.Body.String(), `"text":"基线"`) || !strings.Contains(replay.Body.String(), `"text":"与对照组"`) {
		t.Fatalf("cursor was not respected: %s", replay.Body.String())
	}

	handler = NewHandler(reopened, nil, HandlerConfig{Model: configured("fixture-after", "low")})
	oldReceipt := request(handler, "POST", base, input, http.StatusOK)
	assertModel(oldReceipt.Body.Bytes(), "fixture-before", "max")
	nextInput := fmt.Sprintf(`{"requestId":"turn-2","text":"核对对照组","referenceIds":[%q]}`, material.MaterialID)
	nextReceipt := request(handler, "POST", base, nextInput, http.StatusAccepted)
	assertModel(nextReceipt.Body.Bytes(), "fixture-after", "low")
	var next Turn
	if err := json.Unmarshal(nextReceipt.Body.Bytes(), &next); err != nil || next.TurnID == turn.TurnID {
		t.Fatalf("invalid new turn: %v, %s", err, nextReceipt.Body)
	}
	nextStream := request(handler, "GET", base+"/"+next.TurnID+"/stream", "", http.StatusOK)
	if !strings.Contains(nextStream.Body.String(), `"status":"COMPLETED"`) {
		t.Fatalf("new turn did not finish: %s", nextStream.Body)
	}
	page := request(handler, "GET", base, "", http.StatusOK)
	var history struct{ Items []json.RawMessage }
	if err := json.Unmarshal(page.Body.Bytes(), &history); err != nil || len(history.Items) != 2 {
		t.Fatalf("invalid turn history: %v, %s", err, page.Body)
	}
	assertModel(history.Items[0], "fixture-after", "low")
	assertModel(history.Items[1], "fixture-before", "max")
}

func TestConversationHistorySurvivesModelReceiptMigration(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	project, _, err := store.createProject(ctx, createProjectInput{RequestID: "legacy", Title: "采样记录", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		ALTER TABLE turns DROP COLUMN model_name;
		ALTER TABLE turns DROP COLUMN reasoning_effort;
		INSERT INTO turns(turn_id, project_id, request_id, user_text, answer, status, created_at)
		VALUES ('old-turn', ?, 'old-request', '原问题', '原答复', 'COMPLETED', '2026-11-23T00:00:00.000000000Z');
		PRAGMA user_version = 42`, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	turn, err := reopened.getTurn(ctx, project.ProjectID, "old-turn")
	if err != nil || turn.Text != "原问题" || turn.Answer != "原答复" || turn.Status != "COMPLETED" || turn.Model != "" || turn.ReasoningEffort != "" {
		t.Fatalf("legacy turn after migration = %+v, error = %v", turn, err)
	}
}
