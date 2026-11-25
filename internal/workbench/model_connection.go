package workbench

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"research-agent/internal/inference"
)

const modelConnectionPath = "/api/v1/model-connection"

type modelConnectionInfo struct {
	Configured bool `json:"configured"`
	modelSnapshot
	ModelOptions []modelOption `json:"modelOptions,omitempty"`
}

func registerModelConnection(mux *http.ServeMux, model *inference.Client, options []modelOption) {
	info := modelConnectionInfo{
		Configured: model != nil, modelSnapshot: snapshotModel(model, "", ""), ModelOptions: options,
	}
	mux.HandleFunc("GET "+modelConnectionPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("POST "+modelConnectionPath+"/test", func(w http.ResponseWriter, r *http.Request) {
		testModelConnection(w, r, model, options)
	})
	mux.HandleFunc(modelConnectionPath, methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(modelConnectionPath+"/test", methodNotAllowed("POST"))
}

func testModelConnection(w http.ResponseWriter, r *http.Request, model *inference.Client, options []modelOption) {
	var input *struct {
		ModelID string `json:"modelId"`
	}
	if !readJSON(w, r, &input, 1024) {
		return
	}
	if input == nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交一个 JSON 对象以选择要测试的模型。")
		return
	}
	if model == nil {
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先在服务端配置模型连接。")
		return
	}
	if input.ModelID == "" {
		input.ModelID = defaultModelID
	}
	index := slices.IndexFunc(options, func(option modelOption) bool { return option.ID == input.ModelID })
	if index < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "所选模型当前不可用。请重新打开模型连接。")
		return
	}
	option := options[index]
	model = model.ForModel(option.Model, option.ReasoningEffort)

	timeout := 30 * time.Second
	if option.ID == imageGenerationModelID {
		timeout = imageGenerationTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var passed bool
	var err error
	switch option.ID {
	case visionModelID:
		passed, err = checkVisionModel(ctx, model)
	case imageGenerationModelID:
		const size = "1024x1024"
		var body []byte
		body, err = model.GeneratePNG(ctx, "绘制一张白底测试图，图中有一个蓝色圆和一个灰色方块。图中不写文字。", size, option.ImageQuality)
		if err == nil {
			err = validateGeneratedPNG(ctx, size, body)
		}
		passed = err == nil
	default:
		err = model.Stream(ctx, []inference.Message{{Role: "user", Content: "这是一条连接测试，请只回复：正常。"}}, func(text string) error {
			passed = passed || strings.TrimSpace(text) != ""
			return nil
		})
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "MODEL_CONNECTION_TIMEOUT", "模型连接测试超时，请稍后重试。")
	case err == nil && !passed && option.ID == visionModelID:
		writeError(w, http.StatusBadGateway, "VISION_TEST_MISMATCH", "测试图像未能通过识别。请核对模型的图像输入能力和推理档，再试一次。")
	case err != nil && option.ID == imageGenerationModelID:
		writeError(w, http.StatusBadGateway, "IMAGE_TEST_FAILED", "未收到有效的测试图片。请核对生图模型、质量参数和服务配置。")
	case err != nil || !passed:
		writeError(w, http.StatusBadGateway, "MODEL_CONNECTION_FAILED", "模型未完成测试回复。请核对服务地址、模型和凭据后重试。")
	default:
		writeJSON(w, http.StatusOK, struct {
			OK bool `json:"ok"`
		}{OK: true})
	}
}
