package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const computeNodePath = "/api/v1/compute-node"
const computeProtocol = "research-compute-v1"
const computeProbeTimeout = 5 * time.Second

// ComputeNode checks the capabilities of a deployment-configured compute service.
type ComputeNode struct {
	capabilitiesURL string
	token           string
	client          *http.Client
}

func NewComputeNode(address, token string) (*ComputeNode, error) {
	base, err := url.Parse(strings.TrimSpace(address))
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("计算节点地址无效")
	}
	if base.Scheme != "https" && (base.Scheme != "http" || !net.ParseIP(base.Hostname()).IsLoopback()) {
		return nil, errors.New("计算节点须使用 HTTPS 或回环 IP 的 HTTP 地址")
	}
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0 {
		return nil, errors.New("请为计算节点设置有效的访问凭据")
	}
	return &ComputeNode{
		capabilitiesURL: base.JoinPath("api", "v1", "capabilities").String(),
		token:           token,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type computeCapability struct {
	ID             string `json:"id"`
	MaxInputBytes  int    `json:"maxInputBytes"`
	MaxRows        int    `json:"maxRows"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type computeNodeInfo struct {
	Name         string              `json:"name"`
	Capabilities []computeCapability `json:"capabilities"`
}

var errComputeCapabilities = errors.New("invalid compute node capabilities")

func (node *ComputeNode) probe(ctx context.Context) (computeNodeInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.capabilitiesURL, nil)
	if err != nil {
		return computeNodeInfo{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+node.token)
	response, err := node.client.Do(request)
	if err != nil {
		return computeNodeInfo{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return computeNodeInfo{}, errComputeCapabilities
	}
	const maxBody = 8 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return computeNodeInfo{}, err
	}
	var payload struct {
		Protocol string `json:"protocol"`
		computeNodeInfo
	}
	if len(body) > maxBody || json.Unmarshal(body, &payload) != nil || payload.Protocol != computeProtocol ||
		len(payload.Name) > 160 || strings.TrimSpace(payload.Name) == "" || strings.ContainsFunc(payload.Name, unicode.IsControl) ||
		strings.Contains(payload.Name, node.token) || len(payload.Capabilities) != 1 {
		return computeNodeInfo{}, errComputeCapabilities
	}
	capability := payload.Capabilities[0]
	if capability.ID != experimentComparisonMethod || capability.MaxInputBytes < 1 || capability.MaxRows < 1 || capability.TimeoutSeconds < 1 ||
		capability.MaxInputBytes > 1<<31-1 || capability.MaxRows > 1<<31-1 || capability.TimeoutSeconds > 1<<31-1 {
		return computeNodeInfo{}, errComputeCapabilities
	}
	return payload.computeNodeInfo, nil
}

func registerComputeNode(mux *http.ServeMux, node *ComputeNode) {
	mux.HandleFunc("GET "+computeNodePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			Configured bool `json:"configured"`
		}{node != nil})
	})
	mux.HandleFunc("POST "+computeNodePath+"/test", func(w http.ResponseWriter, r *http.Request) {
		var input *struct{}
		if !readJSON(w, r, &input, 1024) {
			return
		}
		if input == nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交空 JSON 对象以检查节点。")
			return
		}
		if node == nil {
			writeError(w, http.StatusServiceUnavailable, "COMPUTE_NODE_UNAVAILABLE", "请先在服务端配置计算节点。")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), computeProbeTimeout)
		defer cancel()
		info, err := node.probe(ctx)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "COMPUTE_NODE_TIMEOUT", "计算节点检查超时，请稍后重试。")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "COMPUTE_NODE_CHECK_FAILED", "无法读取计算节点能力。请核对连接、凭据与协议版本后重试。")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc(computeNodePath, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(computeNodePath+"/test", methodNotAllowed("POST"))
}
