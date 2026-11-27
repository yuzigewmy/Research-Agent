// Package wordread extracts plain text from the main part of Word documents.
package wordread

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxDocumentXML = 16 << 20
	maxXMLDepth    = 256
	transitionalNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	strictNS       = "http://purl.oclc.org/ooxml/wordprocessingml/main"
)

// ErrDocument marks a damaged, unsupported, or oversized Word document.
var ErrDocument = errors.New("invalid or unsupported Word document")

// Excerpt reads the UTF-8 body of a DOCX or DOTX archive and returns up to limit
// bytes of UTF-8 text. Paragraphs, including those in table cells, are separated
// by newlines. It validates the main XML part even after the limit is reached.
func Excerpt(ctx context.Context, source io.ReaderAt, size int64, limit int) (text string, truncated bool, err error) {
	result, err := readExcerpt(ctx, source, size, limit, false)
	return result.text, result.truncated, err
}

// Preview also returns UTF-8 byte offsets after explicit page break markers.
func Preview(ctx context.Context, source io.ReaderAt, size int64, limit int) (text string, truncated bool, pageBreaks []int, err error) {
	result, err := readExcerpt(ctx, source, size, limit, true)
	return result.text, result.truncated, result.pageBreaks, err
}

type excerptResult struct {
	text       string
	truncated  bool
	pageBreaks []int
}

func readExcerpt(ctx context.Context, source io.ReaderAt, size int64, limit int, recordBreaks bool) (excerptResult, error) {
	if err := ctx.Err(); err != nil {
		return excerptResult{}, err
	}
	if limit <= 0 {
		return excerptResult{}, errors.New("Word excerpt limit must be positive")
	}
	if source == nil || size <= 0 {
		return excerptResult{}, ErrDocument
	}
	archive, err := zip.NewReader(contextReaderAt{ctx, source}, size)
	if ctx.Err() != nil {
		return excerptResult{}, ctx.Err()
	}
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return excerptResult{}, fmt.Errorf("%w: open archive: %w", ErrDocument, err)
	}
	var part *zip.File
	for _, entry := range archive.File {
		if entry.Name == "word/document.xml" {
			if part != nil {
				return excerptResult{}, fmt.Errorf("%w: duplicate main XML part", ErrDocument)
			}
			part = entry
		}
	}
	if part == nil || !part.Mode().IsRegular() || part.Flags&1 != 0 {
		return excerptResult{}, fmt.Errorf("%w: missing or unsupported main XML part", ErrDocument)
	}
	if part.UncompressedSize64 > maxDocumentXML {
		return excerptResult{}, fmt.Errorf("%w: main XML part exceeds 16 MiB", ErrDocument)
	}
	raw, err := part.Open()
	if err != nil {
		if ctx.Err() != nil {
			return excerptResult{}, ctx.Err()
		}
		return excerptResult{}, fmt.Errorf("%w: open main XML part: %w", ErrDocument, err)
	}
	defer raw.Close()

	limited := &io.LimitedReader{R: contextReader{ctx, raw}, N: maxDocumentXML + 1}
	reader := bufio.NewReader(limited)
	if prefix, _ := reader.Peek(3); string(prefix) == "\ufeff" {
		_, _ = reader.Discard(3)
	}
	result := excerptBuffer{limit: limit, recordBreaks: recordBreaks}
	err = readBody(ctx, xml.NewDecoder(reader), &result)
	if ctx.Err() != nil {
		return excerptResult{}, ctx.Err()
	}
	if limited.N == 0 {
		return excerptResult{}, fmt.Errorf("%w: main XML part exceeds 16 MiB", ErrDocument)
	}
	if err != nil {
		return excerptResult{}, fmt.Errorf("%w: %w", ErrDocument, err)
	}
	text := result.text.String()
	for len(result.pageBreaks) > 0 && result.pageBreaks[len(result.pageBreaks)-1] >= len(text) {
		result.pageBreaks = result.pageBreaks[:len(result.pageBreaks)-1]
	}
	return excerptResult{text: text, truncated: result.truncated, pageBreaks: result.pageBreaks}, nil
}

func readBody(ctx context.Context, decoder *xml.Decoder, result *excerptBuffer) error {
	var namespace string
	var elements [maxXMLDepth + 1]xml.Name
	var depth, bodyDepth, paragraphDepth, textDepth, skipDepth int
	var sawBody, sawParagraph bool
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			if !sawBody {
				return errors.New("missing Word body")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return errors.New("main XML nesting is too deep")
			}
			if depth == 1 {
				if namespace != "" || token.Name.Local != "document" || (token.Name.Space != transitionalNS && token.Name.Space != strictNS) {
					return errors.New("expected a Word document root")
				}
				namespace = token.Name.Space
			}
			elements[depth] = token.Name
			if token.Name.Space != namespace || skipDepth != 0 {
				continue
			}
			if depth == 2 && token.Name.Local == "body" {
				if sawBody {
					return errors.New("duplicate Word body")
				}
				bodyDepth, sawBody = depth, true
			}
			if bodyDepth == 0 {
				continue
			}
			switch token.Name.Local {
			case "del", "cellDel":
				skipDepth = depth
				// Row and cell properties precede their content. A deletion
				// marker there hides the enclosing row or cell.
				if depth >= 5 {
					parent := elements[depth-1]
					container := elements[depth-2]
					if parent.Space == namespace && container.Space == namespace &&
						((token.Name.Local == "del" && parent.Local == "trPr" && container.Local == "tr") ||
							(token.Name.Local == "cellDel" && parent.Local == "tcPr" && container.Local == "tc")) {
						skipDepth = depth - 2
					}
				}
			case "moveFrom", "drawing", "pict", "object", "txbxContent", "altChunk", "sectPr":
				skipDepth = depth
			case "p":
				if paragraphDepth == 0 {
					if sawParagraph {
						result.append([]byte("\n"))
					}
					paragraphDepth, sawParagraph = depth, true
				}
			case "t", "tab", "ptab", "br", "cr":
				if paragraphDepth == 0 {
					continue
				}
				switch token.Name.Local {
				case "t":
					textDepth = depth
				case "tab", "ptab":
					result.append([]byte("\t"))
				case "br", "cr":
					result.append([]byte("\n"))
					if token.Name.Local == "br" && wordAttribute(token, "type") == "page" {
						result.markPageBreak()
					}
				}
			case "pageBreakBefore":
				if paragraphDepth != 0 && depth == paragraphDepth+2 && elements[depth-1] == (xml.Name{Space: namespace, Local: "pPr"}) {
					switch wordAttribute(token, "val") {
					case "", "true", "1", "on":
						result.markPageBreak()
					}
				}
			}
		case xml.EndElement:
			switch depth {
			case bodyDepth:
				bodyDepth = 0
			case paragraphDepth:
				paragraphDepth = 0
			case textDepth:
				textDepth = 0
			case skipDepth:
				skipDepth = 0
			}
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.Trim(token, " \t\r\n")) != 0 {
				return errors.New("unexpected text outside Word document root")
			}
			if textDepth != 0 && textDepth == depth && skipDepth == 0 {
				result.append(token)
			}
		case xml.Directive:
			return errors.New("unsupported XML directive")
		}
	}
}

func wordAttribute(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if attr.Name.Space == element.Name.Space && attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

type excerptBuffer struct {
	text         strings.Builder
	limit        int
	truncated    bool
	recordBreaks bool
	pageBreaks   []int
}

func (e *excerptBuffer) markPageBreak() {
	offset := e.text.Len()
	if e.recordBreaks && !e.truncated && offset > 0 &&
		(len(e.pageBreaks) == 0 || e.pageBreaks[len(e.pageBreaks)-1] < offset) {
		e.pageBreaks = append(e.pageBreaks, offset)
	}
}

func (e *excerptBuffer) append(value []byte) {
	if e.truncated {
		return
	}
	end := e.limit - e.text.Len()
	if len(value) > end {
		e.truncated = true
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	_, _ = e.text.Write(value)
}

type contextReaderAt struct {
	ctx context.Context
	io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReaderAt.ReadAt(p, offset)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
