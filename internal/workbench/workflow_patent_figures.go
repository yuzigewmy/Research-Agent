package workbench

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"research-agent/internal/inference"
)

//go:embed prompts/patent_figures.md
var patentFiguresPrompt string

var errPatentFigures = errors.New("附图数据不完整，或节点与连线不符合绘图规格。")

type patentFigureModels struct {
	System  json.RawMessage `json:"system"`
	Process json.RawMessage `json:"process"`
}

type patentFiguresArtifact struct {
	patentFigureModels
	ManuscriptArtifactID string `json:"manuscriptArtifactId"`
	ManuscriptSHA256     string `json:"manuscriptSha256"`
}

func parsePatentDiagram(raw json.RawMessage) (diagramDocument, error) {
	graph, err := parseDiagram(string(raw))
	if err != nil || len(graph.Nodes) < 2 || len(graph.Nodes) > 12 || len(graph.Edges) == 0 {
		return diagramDocument{}, errPatentFigures
	}
	const gap = 12
	for index, left := range graph.Nodes {
		for _, right := range graph.Nodes[index+1:] {
			if *left.X < *right.X+nodeWidth+gap && *right.X < *left.X+nodeWidth+gap &&
				*left.Y < *right.Y+nodeHeight+gap && *right.Y < *left.Y+nodeHeight+gap {
				return diagramDocument{}, errPatentFigures
			}
		}
	}
	return graph, nil
}

func (s *Store) generatePatentFigures(ctx context.Context, model *inference.Client, projectID, runID, prompt, input string) (taskResult, error) {
	manuscript, err := s.workflowArtifactVersion(ctx, projectID, runID, "manuscript", "")
	if err != nil {
		return taskResult{}, err
	}
	result, err := streamTaskMarkdown(ctx, model, prompt, input+"\n\n固定交底稿\n"+manuscript.markdown)
	if err != nil {
		return taskResult{}, err
	}
	var graphs patentFigureModels
	decoder := json.NewDecoder(strings.NewReader(result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&graphs); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return taskResult{}, workflowOutputError{errPatentFigures}
	}
	for _, raw := range []json.RawMessage{graphs.System, graphs.Process} {
		if _, err := parsePatentDiagram(raw); err != nil {
			return taskResult{}, workflowOutputError{err}
		}
	}
	artifact, err := json.Marshal(patentFiguresArtifact{
		patentFigureModels: graphs, ManuscriptArtifactID: manuscript.ArtifactID,
		ManuscriptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(manuscript.markdown))),
	})
	return taskResult{markdown: string(artifact)}, err
}

func (api workflows) patentFigureSVG(w http.ResponseWriter, r *http.Request) {
	figure := r.PathValue("figure")
	if figure != "system" && figure != "process" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有此类技术附图。")
		return
	}
	ctx, projectID, runID := r.Context(), r.PathValue("id"), r.PathValue("runId")
	artifact, err := api.store.workflowArtifactVersion(ctx, projectID, runID, "figures", r.PathValue("artifactId"))
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	var figures patentFiguresArtifact
	if err := json.Unmarshal([]byte(artifact.markdown), &figures); err != nil {
		writeStoreError(w, "read patent figure artifact", err)
		return
	}
	raw := figures.System
	if figure == "process" {
		raw = figures.Process
	}
	graph, err := parsePatentDiagram(raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ARTIFACT", err.Error())
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	w.Header().Set("X-Source-Artifact-ID", artifact.ArtifactID)
	w.Header().Set("X-Manuscript-SHA256", figures.ManuscriptSHA256)
	writeTextAttachment(w, r, "patent-disclosure-"+figure+".svg", "image/svg+xml; charset=utf-8", renderDiagramSVG(graph))
}
