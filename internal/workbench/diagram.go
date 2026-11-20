package workbench

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	diagramPrefix = "diagrams/"
	diagramSuffix = ".flow.json"
	diagramWidth  = 960
	diagramHeight = 640
	nodeWidth     = 160
	nodeHeight    = 64
)

var (
	errInvalidDiagram = errors.New("invalid diagram document")
	diagramNodeID     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
)

type diagramNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	X     *int   `json:"x"`
	Y     *int   `json:"y"`
}

type diagramEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type diagramDocument struct {
	Version int           `json:"version"`
	Nodes   []diagramNode `json:"nodes"`
	Edges   []diagramEdge `json:"edges"`
}

func hasDiagramPath(path string) bool {
	return strings.HasPrefix(path, diagramPrefix) && strings.HasSuffix(path, diagramSuffix)
}

func diagramName(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, diagramPrefix), diagramSuffix)
}

func validDiagramName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 256 && !strings.ContainsRune(name, '/')
}

func validateDiagramDocument(path, content string) error {
	if !hasDiagramPath(path) {
		return nil
	}
	if !validDiagramName(diagramName(path)) {
		return errInvalidDiagram
	}
	_, err := parseDiagram(content)
	return err
}

func parseDiagram(content string) (diagramDocument, error) {
	var doc diagramDocument
	if len(content) > 64<<10 {
		return doc, errInvalidDiagram
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF ||
		doc.Version != 1 || len(doc.Nodes) < 1 || len(doc.Nodes) > 40 || doc.Edges == nil || len(doc.Edges) > 80 {
		return diagramDocument{}, errInvalidDiagram
	}
	ids := make(map[string]bool, len(doc.Nodes))
	for _, node := range doc.Nodes {
		if !diagramNodeID.MatchString(node.ID) || ids[node.ID] || strings.TrimSpace(node.Label) == "" ||
			len([]rune(node.Label)) > 36 || strings.ContainsFunc(node.Label, unicode.IsControl) ||
			node.X == nil || *node.X < 0 || *node.X > diagramWidth-nodeWidth ||
			node.Y == nil || *node.Y < 0 || *node.Y > diagramHeight-nodeHeight {
			return diagramDocument{}, errInvalidDiagram
		}
		ids[node.ID] = true
	}
	edges := make(map[diagramEdge]bool, len(doc.Edges))
	for _, edge := range doc.Edges {
		if !ids[edge.From] || !ids[edge.To] || edge.From == edge.To || edges[edge] {
			return diagramDocument{}, errInvalidDiagram
		}
		edges[edge] = true
	}
	return doc, nil
}

func renderDiagramSVG(diagram diagramDocument) string {
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">`, diagramWidth, diagramHeight, diagramWidth, diagramHeight)
	out.WriteString(`<defs><marker id="arrow" markerWidth="10" markerHeight="8" refX="10" refY="4" orient="auto" markerUnits="userSpaceOnUse"><path d="M0 0L10 4L0 8Z" fill="#39534a"/></marker></defs>`)
	out.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	nodes := make(map[string]diagramNode, len(diagram.Nodes))
	for _, node := range diagram.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range diagram.Edges {
		from, to := nodes[edge.From], nodes[edge.To]
		dx, dy := float64(*to.X-*from.X), float64(*to.Y-*from.Y)
		fraction := math.Min(nodeWidth/2/math.Abs(dx), nodeHeight/2/math.Abs(dy))
		if fraction >= 0.5 {
			continue
		}
		fmt.Fprintf(&out, `<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" stroke="#39534a" stroke-width="2" marker-end="url(#arrow)"/>`,
			float64(*from.X+nodeWidth/2)+dx*fraction, float64(*from.Y+nodeHeight/2)+dy*fraction,
			float64(*to.X+nodeWidth/2)-dx*fraction, float64(*to.Y+nodeHeight/2)-dy*fraction)
	}
	for _, node := range diagram.Nodes {
		fmt.Fprintf(&out, `<rect x="%d" y="%d" width="%d" height="%d" rx="6" fill="#f5f8f6" stroke="#39534a" stroke-width="2"/>`,
			*node.X, *node.Y, nodeWidth, nodeHeight)
		label := []rune(node.Label)
		lines := (len(label) + 11) / 12
		y := *node.Y + nodeHeight/2 + 5 - (lines-1)*9
		out.WriteString(`<g fill="#172920" font-family="sans-serif" font-size="12" text-anchor="middle">`)
		for offset := 0; offset < len(label); offset += 12 {
			fmt.Fprintf(&out, `<text x="%d" y="%d" xml:space="preserve">`, *node.X+nodeWidth/2, y)
			_ = xml.EscapeText(&out, []byte(string(label[offset:min(offset+12, len(label))])))
			out.WriteString(`</text>`)
			y += 18
		}
		out.WriteString(`</g>`)
	}
	out.WriteString(`</svg>`)
	return out.String()
}

func (s *Store) handleDiagramSVG(w http.ResponseWriter, r *http.Request) {
	file, err := s.workspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
	if err != nil {
		writeWorkspaceError(w, "find SVG source version", err)
		return
	}
	name := diagramName(file.Path)
	if !hasDiagramPath(file.Path) || !validDiagramName(name) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该版本不是流程图。")
		return
	}
	doc, err := s.readWorkspaceDocument(file)
	if err != nil {
		writeWorkspaceError(w, "read SVG source version", err)
		return
	}
	diagram, err := parseDiagram(doc.Content)
	if err != nil {
		writeWorkspaceError(w, "decode SVG source version", err)
		return
	}
	svg := renderDiagramSVG(diagram)
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".svg"}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(svg)))
	w.Header().Set("X-Source-SHA256", file.SHA256)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, svg)
	}
}
