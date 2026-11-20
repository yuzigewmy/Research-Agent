package wordread

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerptReadsWordBodyInOrder(t *testing.T) {
	for _, ns := range []string{
		"http://schemas.openxmlformats.org/wordprocessingml/2006/main",
		"http://purl.oclc.org/ooxml/wordprocessingml/main",
	} {
		t.Run(ns, func(t *testing.T) {
			xml := `<w:document xmlns:w="` + ns + `" xmlns:r="urn:relationship"><w:body>
<w:p><w:r><w:t xml:space="preserve"> 中文 </w:t><w:tab/><w:t>正文</w:t><w:br/><w:t>续行</w:t><w:cr/><w:t>末行</w:t></w:r>
<w:del><w:r><w:delText>已删除</w:delText><w:t>删除的文字</w:t></w:r></w:del>
<w:r><w:drawing><w:p><w:r><w:t>图片内文字</w:t></w:r></w:p></w:drawing></w:r>
<w:hyperlink r:id="external"><w:r><w:t>链接</w:t></w:r></w:hyperlink></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>左列</w:t></w:r></w:p></w:tc>
<w:tc><w:tcPr><w:cellDel w:id="1"/></w:tcPr><w:p><w:r><w:t>已删除单元格</w:t></w:r></w:p></w:tc>
<w:tc><w:p><w:r><w:t>右列</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:trPr><w:del w:id="2"/></w:trPr><w:tc><w:p><w:r><w:t>已删除行</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:ins><w:r><w:t>结语</w:t></w:r></w:ins></w:p></w:body></w:document>`
			source := wordZIP(t, xml)
			const want = " 中文 \t正文\n续行\n末行链接\n左列\n右列\n结语"
			for _, tc := range []struct {
				limit     int
				want      string
				truncated bool
			}{
				{len(want) + 10, want, false},
				{len(want), want, false},
				{6, " 中", true}, // The byte limit falls within the second Chinese rune.
			} {
				text, truncated, err := Excerpt(context.Background(), source, source.Size(), tc.limit)
				if err != nil || text != tc.want || truncated != tc.truncated || !utf8.ValidString(text) {
					t.Errorf("limit %d: got %q, %t, %v, want %q, %t, nil", tc.limit, text, truncated, err, tc.want, tc.truncated)
				}
			}
		})
	}
}

func TestExcerptReportsUnsupportedDamagedAndOversizeDocuments(t *testing.T) {
	const prefix = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`
	const suffix = `</w:t></w:r></w:p></w:body></w:document>`
	for _, tc := range []struct {
		name   string
		source *bytes.Reader
	}{
		{"not a ZIP", bytes.NewReader([]byte("ordinary text"))},
		{"wrong XML root", wordZIP(t, `<body>text</body>`)},
		{"malformed tail past the excerpt limit", wordZIP(t, prefix+"valid prefix"+`</broken>`)},
		{"oversize XML", wordZIP(t, prefix+strings.Repeat("a", 16<<20)+suffix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, truncated, err := Excerpt(context.Background(), tc.source, tc.source.Size(), 4)
			if !errors.Is(err, ErrDocument) || text != "" || truncated {
				t.Errorf("got %q, %t, %v, want ErrDocument without partial text", text, truncated, err)
			}
		})
	}
}

func TestExcerptHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Excerpt(ctx, bytes.NewReader(nil), 0, 4); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func wordZIP(t *testing.T, document string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	add := func(name, content string) {
		member, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("word/document.xml", document)
	add("word/header1.xml", "unrelated header")
	add("word/footer1.xml", "unrelated footer")
	add("word/_rels/document.xml.rels", `<Relationship Id="external" TargetMode="External" Target="https://example.invalid/"/>`)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}
