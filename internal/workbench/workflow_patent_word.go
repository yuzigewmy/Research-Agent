package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"research-agent/internal/wordexport"
)

func (api workflows) patentWordConfigured() bool {
	return api.wordPython != "" && api.softwarePDFFont != ""
}

func (api workflows) patentWord(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, runID := r.Context(), r.PathValue("id"), r.PathValue("runId")
	artifact, err := api.store.workflowArtifactVersion(ctx, projectID, runID, "figures", r.PathValue("artifactId"))
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	if !api.patentWordConfigured() {
		writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING", "请配置 Word 导出环境和支持中文的 TrueType 字体。")
		return
	}
	var figures patentFiguresArtifact
	if err := json.Unmarshal([]byte(artifact.markdown), &figures); err != nil {
		writeStoreError(w, "read patent figure artifact", err)
		return
	}
	manuscript, err := api.store.workflowArtifactVersion(ctx, projectID, runID, "manuscript", figures.ManuscriptArtifactID)
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript.markdown)))
	if manuscript.ArtifactID != figures.ManuscriptArtifactID || digest != figures.ManuscriptSHA256 {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARTIFACT", "附图与交底稿的版本指纹不一致。")
		return
	}

	pictures := make([]wordexport.Figure, 0, 2)
	for _, picture := range []struct {
		caption string
		graph   json.RawMessage
	}{{"图 1 系统框图", figures.System}, {"图 2 操作流程图", figures.Process}} {
		graph, err := parsePatentDiagram(picture.graph)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "INVALID_ARTIFACT", err.Error())
			return
		}
		pictures = append(pictures, patentWordFigure(picture.caption, graph))
	}

	renderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := wordexport.ExportWithFigures(renderCtx, api.wordPython, api.softwarePDFFont, manuscript.markdown, pictures)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "EXPORT_FAILED", "无法生成带附图的 Word。请检查 Word 导出环境、Pillow 和字体配置。")
		return
	}
	w.Header().Set("Content-Type", wordContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="patent-disclosure-with-figures.docx"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Source-Artifact-ID", artifact.ArtifactID)
	w.Header().Set("X-Source-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte(artifact.markdown))))
	w.Header().Set("X-Manuscript-Artifact-ID", manuscript.ArtifactID)
	w.Header().Set("X-Manuscript-SHA256", digest)
	w.Header().Set("X-Word-SHA256", fmt.Sprintf("%x", sha256.Sum256(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func patentWordFigure(caption string, graph diagramDocument) wordexport.Figure {
	figure := wordexport.Figure{Caption: caption,
		Nodes: make([]wordexport.FigureNode, 0, len(graph.Nodes)),
		Edges: make([]wordexport.FigureEdge, 0, len(graph.Edges))}
	for _, node := range graph.Nodes {
		figure.Nodes = append(figure.Nodes, wordexport.FigureNode{ID: node.ID, Label: node.Label, X: *node.X, Y: *node.Y})
	}
	for _, edge := range graph.Edges {
		figure.Edges = append(figure.Edges, wordexport.FigureEdge{From: edge.From, To: edge.To})
	}
	return figure
}
