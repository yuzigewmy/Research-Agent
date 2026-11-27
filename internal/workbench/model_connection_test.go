package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"research-agent/internal/inference"
)

func TestModelConnectionChecksOnlyConfiguredService(t *testing.T) {
	const credential = "fixture-credential"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model           string              `json:"model"`
			ReasoningEffort string              `json:"reasoning_effort"`
			Messages        []inference.Message `json:"messages"`
			Stream          bool                `json:"stream"`
			Tools           json.RawMessage     `json:"tools"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+credential ||
			json.NewDecoder(r.Body).Decode(&request) != nil ||
			!request.Stream || len(request.Tools) != 0 || len(request.Messages) != 1 ||
			request.Messages[0] != (inference.Message{Role: "user", Content: "这是一条连接测试，请只回复：正常。"}) {
			t.Error("unexpected model connection request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		wantModel, wantEffort := "fixture-chat", "max"
		if call > 1 {
			wantModel, wantEffort = "fixture-review", "low"
		}
		if request.Model != wantModel || request.ReasoningEffort != wantEffort {
			t.Errorf("probe %d used %q / %q, want %q / %q", call, request.Model, request.ReasoningEffort, wantModel, wantEffort)
		}
		if call == 2 {
			http.Error(w, credential, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"正常\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model, err := inference.New(inference.Config{
		BaseURL: upstream.URL + "/v1", Model: "fixture-chat", APIKey: credential, ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatal(err)
	}
	const route = "/api/v1/model-connection"
	probe := route + "/test"
	request := func(handler http.Handler, method, path, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		output := strings.TrimSpace(res.Body.String())
		if strings.Contains(output, credential) || strings.Contains(output, upstream.URL) {
			t.Fatal("model connection response exposed private configuration")
		}
		return output
	}

	offline := NewHandler(store, nil, HandlerConfig{})
	var offlineInfo modelConnectionInfo
	if body := request(offline, http.MethodGet, route, "", http.StatusOK); json.Unmarshal([]byte(body), &offlineInfo) != nil ||
		offlineInfo.Configured || len(offlineInfo.Catalog.Roles) != 7 || len(offlineInfo.ModelOptions) != 0 {
		t.Fatalf("unexpected unconfigured catalog: %s", body)
	}
	for _, role := range offlineInfo.Catalog.Roles {
		if role.Status != "unconfigured" || role.Model != "" {
			t.Fatalf("unconfigured service advertised a model: %+v", role)
		}
	}
	request(offline, http.MethodPost, probe, `{}`, http.StatusServiceUnavailable)

	handler := NewHandler(store, nil, HandlerConfig{
		Model:                 model,
		ConversationModelName: "fixture-conversation", ConversationReasoningEffort: "high",
		WorkflowModelName: "fixture-workflow", WorkflowReasoningEffort: "medium",
		WorkflowModelTemplates: "experiment,unknown-template",
		ReviewModelName:        "fixture-review", ReviewReasoningEffort: "low",
	})
	body := request(handler, http.MethodGet, route, "", http.StatusOK)
	var info modelConnectionInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil || !info.Configured || info.Model != "fixture-chat" || info.ReasoningEffort != "max" {
		t.Fatalf("unexpected default connection: %s (%v)", body, err)
	}
	want := []modelOption{
		{ID: "default", modelSnapshot: modelSnapshot{Model: "fixture-chat", ReasoningEffort: "max"}},
		{ID: "conversation", modelSnapshot: modelSnapshot{Model: "fixture-conversation", ReasoningEffort: "high"}},
		{ID: "workflow", modelSnapshot: modelSnapshot{Model: "fixture-workflow", ReasoningEffort: "medium"}},
		{ID: "review", modelSnapshot: modelSnapshot{Model: "fixture-review", ReasoningEffort: "low"}},
		{ID: "edit", modelSnapshot: modelSnapshot{Model: "fixture-chat", ReasoningEffort: "max"}},
	}
	if !slices.Equal(info.ModelOptions, want) {
		t.Fatalf("unexpected model options: %+v", info.ModelOptions)
	}
	roles := make(map[string]modelCapabilityRole)
	for _, role := range info.Catalog.Roles {
		roles[role.ID] = role
	}
	if len(roles) != 7 || roles["review"].Status != "configured" || roles["review"].Model != "fixture-review" ||
		roles["edit"].Status != "inherited" || roles["edit"].Model != info.Model ||
		roles["vision"].Status != "unconfigured" || roles["vision"].Capability != "vision" ||
		roles["image"].TestSeconds != 120 || roles["default"].TestSeconds != 30 {
		t.Fatalf("unexpected capability roles: %+v", roles)
	}
	templateDefaults := make(map[string]string)
	for _, template := range info.Catalog.TemplateDefaults {
		templateDefaults[template.TemplateID] = template.ModelID
	}
	if templateDefaults["experiment"] != "workflow" || templateDefaults["paper"] != "default" || templateDefaults["unknown-template"] != "" {
		t.Fatalf("unexpected template defaults: %+v", templateDefaults)
	}
	if !slices.Contains(info.Catalog.Limits, modelTechnicalLimit{"conversationOutput", maxTurnOutputBytes, "bytes"}) ||
		!slices.Contains(info.Catalog.Limits, modelTechnicalLimit{"automaticReviewRounds", maxAutomaticReviewRounds, "count"}) {
		t.Fatalf("catalog missing runtime limits: %+v", info.Catalog.Limits)
	}
	request(handler, http.MethodPost, probe, `{"baseUrl":"https://other.invalid"}`, http.StatusBadRequest)
	request(handler, http.MethodPost, probe, `{"modelId":"unsupported"}`, http.StatusBadRequest)
	if calls.Load() != 0 {
		t.Fatal("reading configuration or sending invalid input contacted the model")
	}
	if got := request(handler, http.MethodPost, probe, `{}`, http.StatusOK); got != `{"ok":true}` {
		t.Fatalf("connection check: %s", got)
	}
	const reviewProbe = `{"modelId":"review"}`
	if got := request(handler, http.MethodPost, probe, reviewProbe, http.StatusBadGateway); !strings.Contains(got, `"code":"MODEL_CONNECTION_FAILED"`) {
		t.Fatalf("connection failure: %s", got)
	}
	if got := request(handler, http.MethodPost, probe, reviewProbe, http.StatusOK); got != `{"ok":true}` {
		t.Fatalf("connection retry: %s", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("model calls = %d, want 3", calls.Load())
	}
}
