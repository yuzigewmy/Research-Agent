package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"research-agent/internal/wordexport"
)

const workspaceComponentCheckPath = "/api/v1/workspace-components/check"

type workspaceComponent struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Message string `json:"message"`
}

type workspaceComponentReport struct {
	Components []workspaceComponent `json:"components"`
}

func registerWorkspaceComponents(mux *http.ServeMux, assets fs.FS, python, font string) {
	var checks sync.Mutex
	mux.HandleFunc("POST "+workspaceComponentCheckPath, func(w http.ResponseWriter, r *http.Request) {
		if !checks.TryLock() {
			writeError(w, http.StatusConflict, "COMPONENT_CHECK_BUSY", "正在检查本机组件，请稍后重试。")
			return
		}
		defer checks.Unlock()
		start := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		pdf := probePDFResources(ctx, assets)
		word := probeWordExport(ctx, python)
		softwarePDF := probeSoftwarePDF(ctx, font)
		if r.Context().Err() != nil {
			return
		}
		slog.Debug("workspace components checked", "elapsed_ms", time.Since(start).Milliseconds(),
			"pdf_preview", pdf.Status, "word_export", word.Status, "software_pdf", softwarePDF.Status)
		writeJSON(w, http.StatusOK, workspaceComponentReport{Components: []workspaceComponent{pdf, word, softwarePDF}})
	})
	mux.HandleFunc(workspaceComponentCheckPath, methodNotAllowed("POST"))
}

func probePDFResources(ctx context.Context, assets fs.FS) workspaceComponent {
	result := workspaceComponent{ID: "pdf.preview", Status: "unavailable",
		Message: "PDF 查看资源不完整。请重新构建工作台或更新本机包。"}
	if assets == nil {
		return result
	}
	const root = "pdf-resources"
	metadata, err := fs.ReadFile(assets, root+"/version.json")
	var release struct{ Version string }
	if err != nil || json.Unmarshal(metadata, &release) != nil || strings.TrimSpace(release.Version) == "" {
		return result
	}
	workers, err := fs.Glob(assets, "assets/pdf.worker*.mjs")
	if err != nil || len(workers) != 1 {
		return result
	}
	worker, err := fs.Stat(assets, workers[0])
	if err != nil || !worker.Mode().IsRegular() || worker.Size() == 0 {
		return result
	}
	for _, name := range []string{
		"cmaps/Adobe-GB1-UCS2.bcmap", "iccs/CGATS001Compat-v2-micro.icc",
		"standard_fonts/LiberationSans-Regular.ttf", "wasm/openjpeg.wasm",
	} {
		info, err := fs.Stat(assets, root+"/"+name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return result
		}
	}
	bytes := worker.Size()
	if err := fs.WalkDir(assets, root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
		}
		return nil
	}); err != nil {
		return result
	}
	result.Status, result.Version, result.Bytes = "ready", release.Version, bytes
	result.Message = "已找到 PDF 查看器的工作线程和支持资源。"
	return result
}

func probeWordExport(ctx context.Context, python string) workspaceComponent {
	result := workspaceComponent{ID: "docx.export", Status: "unconfigured",
		Message: "请设置 RESEARCH_WORD_PYTHON，指定装有 python-docx 的解释器。"}
	if python == "" {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	version, err := wordexport.Probe(ctx, python)
	if err != nil {
		result.Status, result.Message = "unavailable", "无法生成并读回 Word 样例。请检查解释器和 python-docx。"
		if errors.Is(err, context.DeadlineExceeded) {
			result.Message = "Word 样例检查超时。请检查配置的解释器。"
		}
		return result
	}
	result.Status, result.Version = "ready", version
	result.Message = "已生成并读回包含中文标题的 Word 样例。"
	return result
}

func probeSoftwarePDF(ctx context.Context, font string) workspaceComponent {
	result := workspaceComponent{ID: "pdf.software.export", Status: "unconfigured",
		Message: "请设置 RESEARCH_PDF_FONT，指定可嵌入的 TrueType 字体。"}
	if font == "" {
		return result
	}
	result.Status, result.Message = "unavailable", "无法读取配置的 TrueType 字体文件。"
	fontData, err := loadSoftwarePDFFont(ctx, font)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.Message = "鉴别材料 PDF 样例检查超时，请重试。"
		}
		return result
	}
	result.Bytes = int64(len(fontData))
	body, pages, err := renderSoftwareArtifactPDF(ctx, fontData, "组件检查", "V1.0", "manuscript", "中文和 English 样例。")
	switch {
	case errors.Is(err, errSoftwarePDFGlyph):
		result.Message = "当前字体缺少样例所用的字形，请更换支持中英文的字体。"
	case errors.Is(err, context.DeadlineExceeded):
		result.Message = "鉴别材料 PDF 样例检查超时，请重试。"
	case errors.Is(err, errSoftwarePDFFont):
		result.Message = "无法嵌入配置的字体。请检查字体格式和嵌入权限。"
	case err != nil || pages != 1 || len(body) == 0:
		result.Message = "无法生成鉴别材料 PDF 样例。请检查配置的字体。"
	default:
		result.Status, result.Message = "ready", "已用配置的字体生成一页中英文鉴别材料 PDF。"
		result.Version = softwarePDFEngineVersion
	}
	return result
}
