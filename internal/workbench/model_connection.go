package workbench

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"research-agent/internal/inference"
)

const modelConnectionPath = "/api/v1/model-connection"

type modelConnectionInfo struct {
	Configured      bool   `json:"configured"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

func registerModelConnection(mux *http.ServeMux, model *inference.Client) {
	info := modelConnectionInfo{Configured: model != nil}
	if model != nil {
		info.Model, info.ReasoningEffort = model.ModelInfo()
	}
	mux.HandleFunc("GET "+modelConnectionPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("POST "+modelConnectionPath+"/test", func(w http.ResponseWriter, r *http.Request) {
		testModelConnection(w, r, model)
	})
	mux.HandleFunc(modelConnectionPath, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(modelConnectionPath+"/test", methodNotAllowed("POST"))
}

func testModelConnection(w http.ResponseWriter, r *http.Request, model *inference.Client) {
	var input *struct{}
	if !readJSON(w, r, &input, 1024) {
		return
	}
	if input == nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交一个空 JSON 对象。")
		return
	}
	if model == nil {
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先在服务端配置模型连接。")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var receivedText bool
	err := model.Stream(ctx, []inference.Message{{Role: "user", Content: "这是一条连接测试，请只回复：正常。"}}, func(text string) error {
		receivedText = receivedText || strings.TrimSpace(text) != ""
		return nil
	})
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "MODEL_CONNECTION_TIMEOUT", "模型连接测试超时，请稍后重试。")
	case err != nil || !receivedText:
		writeError(w, http.StatusBadGateway, "MODEL_CONNECTION_FAILED", "模型未完成测试回复。请核对服务地址、模型和凭据后重试。")
	default:
		writeJSON(w, http.StatusOK, struct {
			OK bool `json:"ok"`
		}{OK: true})
	}
}
