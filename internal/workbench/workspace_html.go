package workbench

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"golang.org/x/net/html"
)

const workspaceHTMLPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox"

func htmlWorkspacePath(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".html" || ext == ".htm"
}

func (s *Store) handleWorkspaceHTML(download bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, err := s.workspaceVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
		if err != nil {
			writeWorkspaceError(w, "read HTML version", err)
			return
		}
		if !htmlWorkspacePath(file.Path) {
			writeError(w, http.StatusUnprocessableEntity, "FORMAT_UNSUPPORTED", "请选择已保存的 HTML 或 HTM 文件版本。")
			return
		}
		doc, err := s.readWorkspaceDocument(file)
		if err != nil {
			writeWorkspaceError(w, "read HTML source", err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Source-Version-ID", file.VersionID)
		w.Header().Set("X-Source-SHA256", file.SHA256)
		if download {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(file.Path)}))
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
			if r.Method != http.MethodHead {
				_, _ = io.WriteString(w, doc.Content)
			}
			return
		}
		tree, err := html.Parse(strings.NewReader(doc.Content))
		if err != nil {
			writeWorkspaceError(w, "parse HTML preview", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", workspaceHTMLPolicy)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, "<!doctype html>")
			_ = html.Render(w, staticHTML(tree))
		}
	}
}

func staticHTML(node *html.Node) *html.Node {
	result := &html.Node{Type: node.Type, Data: node.Data, DataAtom: node.DataAtom}
	switch node.Type {
	case html.DocumentNode, html.TextNode:
	case html.ElementNode:
		if node.Namespace != "" {
			return nil
		}
		switch node.Data {
		case "html", "head", "body", "title", "style", "main", "article", "section", "header", "footer", "aside",
			"h1", "h2", "h3", "h4", "h5", "h6", "div", "span", "p", "br", "hr", "strong", "b", "em", "i",
			"u", "s", "small", "sup", "sub", "mark", "blockquote", "pre", "code", "ul", "ol", "li", "dl", "dt", "dd",
			"figure", "figcaption", "img", "table", "caption", "colgroup", "col", "thead", "tbody", "tfoot", "tr", "th", "td":
		case "a", "font", "center":
			result = &html.Node{Type: html.ElementNode, Data: "span"}
		default:
			return nil
		}
		for _, attr := range node.Attr {
			if attr.Namespace != "" {
				continue
			}
			switch attr.Key {
			case "class", "id", "style", "title", "lang", "dir", "colspan", "rowspan", "scope", "alt", "width", "height":
				result.Attr = append(result.Attr, attr)
			case "src":
				if node.Data == "img" && embeddedRaster(attr.Val) {
					result.Attr = append(result.Attr, attr)
				}
			}
		}
	default:
		return nil
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if safe := staticHTML(child); safe != nil {
			result.AppendChild(safe)
		}
	}
	return result
}

func embeddedRaster(source string) bool {
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/gif;base64,", "data:image/webp;base64,"} {
		if strings.HasPrefix(source, prefix) {
			return true
		}
	}
	return false
}
