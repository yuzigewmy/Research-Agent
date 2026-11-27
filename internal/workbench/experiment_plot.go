package workbench

import (
	"context"
	"encoding/xml"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const experimentPlotRevision = "paired-scatter-v1"

type experimentPoint struct {
	row               experimentRow
	baseline, variant *big.Rat
}

func renderExperimentPlot(ctx context.Context, report experimentComparisonReport, source string) (string, error) {
	var points []experimentPoint
	skipped := 0
	var low, high *big.Rat
	var lowText, highText string
	include := func(value *big.Rat, text string) {
		if low == nil || value.Cmp(low) < 0 {
			low, lowText = value, text
		}
		if high == nil || value.Cmp(high) > 0 {
			high, highText = value, text
		}
	}
	err := scanExperimentRows(ctx, source, func(row experimentRow, baseline, variant *big.Rat) {
		if row.Missing != "" {
			skipped++
			return
		}
		points = append(points, experimentPoint{row, baseline, variant})
		include(baseline, row.Baseline)
		include(variant, row.Variant)
	})
	if err != nil {
		return "", err
	}
	if len(points) == 0 || len(points) != report.Metrics.PairedRows || skipped != report.Metrics.SkippedRows {
		return "", errExperimentEvidence
	}

	const left, top, size = 124, 72, 360
	span := new(big.Rat).Sub(high, low)
	var normalized big.Rat
	position := func(value *big.Rat) float64 {
		if span.Sign() == 0 {
			return size / 2
		}
		normalized.Sub(value, low)
		normalized.Quo(&normalized, span)
		fraction, _ := normalized.Float64()
		return fraction * size
	}
	var out strings.Builder
	out.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 570 582" width="570" height="582" role="img" aria-labelledby="plot-title plot-desc">`)
	out.WriteString(`<title id="plot-title">基线与方案配对散点图</title><desc id="plot-desc">横轴为基线，纵轴为方案，虚线表示两列数值相等。</desc><metadata>`)
	fmt.Fprintf(&out, "renderer=%s; method=%s", experimentPlotRevision, experimentComparisonMethod)
	if report.Input.VersionID != "" {
		out.WriteString("; version=")
		_ = xml.EscapeText(&out, []byte(report.Input.VersionID))
	}
	fmt.Fprintf(&out, `; sha256=%s</metadata><rect width="570" height="566" fill="#fff"/><g font-family="sans-serif" fill="#172920"><text x="124" y="32" font-size="20" font-weight="bold">基线与方案配对散点图</text><text x="124" y="55" font-size="13">%d 行参与计算 · %d 行因缺失跳过</text>`, report.Input.SHA256, len(points), skipped)
	fmt.Fprintf(&out, `<rect x="%d" y="%d" width="%d" height="%d" fill="#f5f8f6" stroke="#a6bbb2"/><path d="M%d %dL%d %d" stroke="#556c63" stroke-dasharray="6 5"/>`, left, top, size, size, left, top+size, left+size, top)
	shortLabel := func(value *big.Rat, exact string) string {
		if len(exact) <= 14 {
			return exact
		}
		return new(big.Float).SetPrec(64).SetRat(value).Text('g', 6)
	}
	lowLabel, highLabel := shortLabel(low, lowText), shortLabel(high, highText)
	if span.Sign() != 0 && lowLabel == highLabel {
		lowLabel, highLabel = "下界", "上界"
	}
	tick := func(value *big.Rat, label, exact string) {
		pixel := position(value)
		fmt.Fprintf(&out, `<text x="%.2f" y="456" text-anchor="middle" font-size="12"><title>%s</title>%s</text><text x="115" y="%.2f" text-anchor="end" font-size="12"><title>%s</title>%s</text>`, left+pixel, exact, label, top+size-pixel+4, exact, label)
	}
	tick(low, lowLabel, lowText)
	if span.Sign() != 0 {
		tick(high, highLabel, highText)
	}
	out.WriteString(`<text x="304" y="486" text-anchor="middle" font-size="15">基线</text><text transform="translate(24 252) rotate(-90)" text-anchor="middle" font-size="15">方案</text></g><g fill="#08685a" fill-opacity="0.68">`)
	for _, point := range points {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprintf(&out, `<circle cx="%.2f" cy="%.2f" r="3" data-line="%d"><title>CSV 第 %d 行 · 基线 %s · 方案 %s</title></circle>`,
			left+position(point.baseline), top+size-position(point.variant), point.row.Line, point.row.Line, point.row.Baseline, point.row.Variant)
	}
	out.WriteString(`</g>`)
	fmt.Fprintf(&out, `<g font-family="sans-serif" fill="#172920"><text x="124" y="519" font-size="12">虚线表示两值相等。重合的点可在原始行证据中逐条核对。</text><text x="124" y="542" font-size="11">来源 SHA-256</text><text x="124" y="559" font-family="monospace" font-size="10">%s</text></g></svg>`, report.Input.SHA256)
	return out.String(), nil
}

func (api researchTasks) comparisonPlot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	report, source, err := api.store.readExperimentEvidence(ctx, r.PathValue("id"), r.PathValue("taskId"))
	if err != nil {
		writeExperimentEvidenceError(w, err)
		return
	}
	plot, err := renderExperimentPlot(ctx, report, source)
	if err != nil {
		writeExperimentEvidenceError(w, errExperimentEvidence)
		return
	}
	w.Header().Set("X-Source-SHA256", report.Input.SHA256)
	w.Header().Set("X-Source-Version-ID", report.Input.VersionID)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	writeTextAttachment(w, r, "experiment-comparison.svg", "image/svg+xml; charset=utf-8", plot)
}
