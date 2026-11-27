package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestComputeNodeChecksConfiguredCapabilities(t *testing.T) {
	const token = "sample-compute-credential"
	var calls atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" || r.Header.Get("Authorization") != "Bearer "+token || r.ContentLength != 0 {
			t.Error("unexpected node check request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		switch calls.Add(1) {
		case 1:
			http.Error(w, token, http.StatusServiceUnavailable)
		case 2:
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		default:
			fmt.Fprint(w, `{"protocol":"research-compute-v1","name":"河流计算节点","capabilities":[{"id":"paired-mean-v1","maxInputBytes":524288,"maxRows":10000,"timeoutSeconds":5}]}`)
		}
	}))
	defer node.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const path = "/api/v1/compute-node"
	request := func(handler http.Handler, method, route, body string, status int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://127.0.0.1"+route, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		text := strings.TrimSpace(res.Body.String())
		if res.Code != status || strings.Contains(text, token) || strings.Contains(text, node.URL) {
			t.Errorf("unexpected response status or private configuration exposure for %s %s (%d)", method, route, res.Code)
		}
		return text
	}
	unconfigured := NewHandler(store, nil, HandlerConfig{})
	if got := request(unconfigured, http.MethodGet, path, "", http.StatusOK); got != `{"configured":false}` {
		t.Fatal("unconfigured node was not reported")
	}
	request(unconfigured, http.MethodPost, path+"/test", `{}`, http.StatusServiceUnavailable)
	configured, err := NewComputeNode(node.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, nil, HandlerConfig{ComputeNode: configured})
	if got := request(handler, http.MethodGet, path, "", http.StatusOK); got != `{"configured":true}` {
		t.Fatal("configured node was not reported")
	}
	request(handler, http.MethodPost, path+"/test", `{"url":"https://example.invalid"}`, http.StatusBadRequest)
	if calls.Load() != 0 {
		t.Fatal("reading status or sending invalid input contacted the node")
	}
	request(handler, http.MethodPost, path+"/test", `{}`, http.StatusBadGateway)
	request(handler, http.MethodPost, path+"/test", `{}`, http.StatusBadGateway)
	body := request(handler, http.MethodPost, path+"/test", `{}`, http.StatusOK)
	var result struct {
		Name         string
		Capabilities []struct {
			ID                                     string
			MaxInputBytes, MaxRows, TimeoutSeconds int
		}
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil || result.Name != "河流计算节点" || len(result.Capabilities) != 1 ||
		result.Capabilities[0].ID != experimentComparisonMethod || result.Capabilities[0].MaxInputBytes != 524288 ||
		result.Capabilities[0].MaxRows != 10000 || result.Capabilities[0].TimeoutSeconds != 5 || calls.Load() != 3 {
		t.Fatalf("unexpected node capabilities or call count (%d)", calls.Load())
	}
}
