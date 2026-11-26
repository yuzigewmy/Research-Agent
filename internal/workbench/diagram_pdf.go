package workbench

import (
	"context"
	"errors"
	"math"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/signintech/gopdf"
)

func renderDiagramPDF(ctx context.Context, diagram diagramDocument, font []byte) ([]byte, error) {
	var pdf gopdf.GoPdf
	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: diagramWidth, H: diagramHeight}})
	if err := pdf.AddTTFFontData("diagram", font); err != nil {
		return nil, errSoftwarePDFFont
	}
	if err := pdf.SetFont("diagram", "", 12); err != nil {
		return nil, errSoftwarePDFFont
	}
	pdf.AddPage()
	pdf.SetLineWidth(2)
	pdf.SetStrokeColor(57, 83, 74)
	pdf.SetFillColor(57, 83, 74)
	nodes := make(map[string]diagramNode, len(diagram.Nodes))
	for _, node := range diagram.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range diagram.Edges {
		x1, y1, x2, y2, visible := diagramEdgePoints(nodes[edge.From], nodes[edge.To])
		if !visible {
			continue
		}
		pdf.Line(x1, y1, x2, y2)
		length := math.Hypot(x2-x1, y2-y1)
		ux, uy := (x2-x1)/length, (y2-y1)/length
		backX, backY := x2-10*ux, y2-10*uy
		pdf.Polygon([]gopdf.Point{{X: x2, Y: y2}, {X: backX - 4*uy, Y: backY + 4*ux}, {X: backX + 4*uy, Y: backY - 4*ux}}, "F")
	}
	for _, node := range diagram.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pdf.SetFillColor(245, 248, 246)
		pdf.RectFromUpperLeftWithStyle(float64(*node.X), float64(*node.Y), nodeWidth, nodeHeight, "DF")
		pdf.SetTextColor(23, 41, 32)
		label := []rune(node.Label)
		for _, character := range label {
			available, err := pdf.IsCurrFontContainGlyph(character)
			if err != nil {
				return nil, err
			}
			if !available {
				return nil, errSoftwarePDFGlyph
			}
		}
		lines := (len(label) + 11) / 12
		y := float64(*node.Y + nodeHeight/2 + 5 - (lines-1)*9)
		for start := 0; start < len(label); start += 12 {
			line := string(label[start:min(start+12, len(label))])
			if err := pdf.SetFontSize(12); err != nil {
				return nil, err
			}
			width, err := pdf.MeasureTextWidth(line)
			if err != nil {
				return nil, err
			}
			if width > nodeWidth-12 {
				if err := pdf.SetFontSize(12 * (nodeWidth - 12) / width); err != nil {
					return nil, err
				}
				width = nodeWidth - 12
			}
			pdf.SetXY(float64(*node.X+nodeWidth/2)-width/2, y)
			if err := pdf.Text(line); err != nil {
				return nil, err
			}
			y += 18
		}
	}
	return pdf.GetBytesPdfReturnErr()
}

func (s *Store) handleDiagramPDF(fontPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		file, err := s.workspaceVersion(ctx, r.PathValue("id"), r.PathValue("versionId"))
		if err != nil {
			writeWorkspaceError(w, "find diagram PDF source", err)
			return
		}
		name := diagramName(file.Path)
		if !hasDiagramPath(file.Path) || !validDiagramName(name) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "该版本不是流程图。")
			return
		}
		if fontPath == "" {
			writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING", "请设置 RESEARCH_PDF_FONT，指定可嵌入的 TrueType 字体。")
			return
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			writeWorkspaceError(w, "read diagram PDF source", err)
			return
		}
		if workspaceTextFile(file.Path, doc.Content).SHA256 != file.SHA256 {
			writeError(w, http.StatusUnprocessableEntity, "SOURCE_DIGEST_MISMATCH", "流程图原件未通过摘要校验。")
			return
		}
		diagram, err := parseDiagram(doc.Content)
		if err != nil {
			writeWorkspaceError(w, "decode diagram PDF source", err)
			return
		}
		font, err := loadSoftwarePDFFont(ctx, fontPath)
		var body []byte
		if err == nil {
			body, err = renderDiagramPDF(ctx, diagram, font)
		}
		if err == nil {
			err = ctx.Err()
		}
		switch {
		case errors.Is(err, errSoftwarePDFFont):
			writeError(w, http.StatusServiceUnavailable, "PDF_FONT_UNAVAILABLE", "无法加载配置的 TrueType 字体。请检查字体文件和嵌入权限。")
		case errors.Is(err, errSoftwarePDFGlyph):
			writeError(w, http.StatusUnprocessableEntity, "PDF_GLYPH_MISSING", "当前字体缺少节点标签所用的字形。请换用包含这些字符的可嵌入字体。")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "EXPORT_TIMEOUT", "流程图 PDF 生成超时，请重试。")
		case err != nil:
			writeStoreError(w, "render diagram PDF", err)
		default:
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".pdf"}))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Source-SHA256", file.SHA256)
			w.Header().Set("X-Source-Version-ID", file.VersionID)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
		}
	}
}
