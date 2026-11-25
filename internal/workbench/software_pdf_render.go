package workbench

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/signintech/gopdf"
)

const (
	softwarePDFMargin   = 40.0
	softwarePDFLineGap  = 12.0
	softwarePDFPageRows = 50
)

var (
	errSoftwarePDFFont  = errors.New("software PDF font unavailable")
	errSoftwarePDFGlyph = errors.New("software PDF font lacks a required character")
)

var softwarePDFNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ")

var softwarePDFEngineVersion, softwarePDFEngineIdentity = func() (string, string) {
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, dependency := range build.Deps {
			if dependency.Path != "github.com/signintech/gopdf" {
				continue
			}
			if dependency.Replace != nil {
				dependency = dependency.Replace
			}
			if dependency.Version != "" && dependency.Version != "(devel)" {
				return dependency.Version, dependency.Path + "@" + dependency.Version + ":" + dependency.Sum
			}
			break
		}
	}
	// Local renderer builds may only reuse entries within the same process.
	return "", rand.Text()
}()

const maxSoftwarePDFFontBytes = 64 << 20

// Use the same bytes for the cache key and the renderer.
func loadSoftwarePDFFont(ctx context.Context, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSoftwarePDFFontBytes {
		return nil, errSoftwarePDFFont
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errSoftwarePDFFont
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxSoftwarePDFFontBytes+1))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil || len(body) == 0 || len(body) > maxSoftwarePDFFontBytes {
		return nil, errSoftwarePDFFont
	}
	return body, nil
}

func renderSoftwarePDF(ctx context.Context, font []byte, kind, title, version string, sections []softwarePDFSection) ([]byte, int, error) {
	var pdf gopdf.GoPdf
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	// gopdf embeds the used TrueType glyphs and supplies a Unicode text map.
	// https://github.com/signintech/gopdf/blob/v0.38.6/README.md#features
	if err := pdf.AddTTFFontData("material", font); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errSoftwarePDFFont, err)
	}
	if err := pdf.SetFont("material", "", 10); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errSoftwarePDFFont, err)
	}
	widths := make(map[rune]float64)
	page := 0
	for _, section := range sections {
		header := kind + "\n" + strings.Join(strings.Fields(title), " ") + "  " + version
		if section.label != "" {
			header += "\n" + section.label
		}
		heading, err := wrapSoftwarePDFText(ctx, &pdf, widths, header)
		if err != nil {
			return nil, 0, err
		}
		rows, err := wrapSoftwarePDFText(ctx, &pdf, widths, section.text)
		if err != nil {
			return nil, 0, err
		}
		bodyTop := softwarePDFMargin + float64(len(heading)+1)*softwarePDFLineGap
		footer := gopdf.PageSizeA4.H - softwarePDFMargin
		capacity := min(softwarePDFPageRows, int((footer-24-bodyTop)/softwarePDFLineGap)+1)
		if capacity < 1 {
			return nil, 0, errSoftwarePDFFont
		}
		for start := 0; start < len(rows); start += capacity {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			page++
			pdf.AddPage()
			if err := writeSoftwarePDFRows(&pdf, heading, softwarePDFMargin); err != nil {
				return nil, 0, err
			}
			pdf.Line(softwarePDFMargin, bodyTop-12, gopdf.PageSizeA4.W-softwarePDFMargin, bodyTop-12)
			if err := writeSoftwarePDFRows(&pdf, rows[start:min(start+capacity, len(rows))], bodyTop); err != nil {
				return nil, 0, err
			}
			pageLabel, err := wrapSoftwarePDFText(ctx, &pdf, widths, fmt.Sprintf("第 %d 页", page))
			if err != nil {
				return nil, 0, err
			}
			if err := writeSoftwarePDFRows(&pdf, pageLabel, footer); err != nil {
				return nil, 0, err
			}
		}
	}
	body, err := pdf.GetBytesPdfReturnErr()
	return body, page, err
}

func wrapSoftwarePDFText(ctx context.Context, pdf *gopdf.GoPdf, widths map[rune]float64, text string) ([]string, error) {
	text = softwarePDFNewlines.Replace(strings.TrimPrefix(text, "\uFEFF"))
	maxWidth := gopdf.PageSizeA4.W - 2*softwarePDFMargin
	var rows []string
	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start, used := 0, 0.0
		for offset, character := range line {
			width, measured := widths[character]
			if !measured {
				available, err := pdf.IsCurrFontContainGlyph(character)
				if err != nil {
					return nil, err
				}
				if !available {
					return nil, fmt.Errorf("%w: %U", errSoftwarePDFGlyph, character)
				}
				width, err = pdf.MeasureTextWidth(string(character))
				if err != nil {
					return nil, err
				}
				widths[character] = width
			}
			if used+width > maxWidth && offset > start {
				rows = append(rows, line[start:offset])
				start, used = offset, 0
			}
			used += width
		}
		rows = append(rows, line[start:])
	}
	return rows, nil
}

func writeSoftwarePDFRows(pdf *gopdf.GoPdf, rows []string, top float64) error {
	for index, row := range rows {
		if row == "" {
			continue
		}
		pdf.SetXY(softwarePDFMargin, top+float64(index)*softwarePDFLineGap)
		if err := pdf.Text(row); err != nil {
			return err
		}
	}
	return nil
}
