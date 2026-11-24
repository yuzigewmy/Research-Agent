package survey

import (
	"archive/zip"
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestAnalyzeWorkbook(t *testing.T) {
	workbook := makeSurveyWorkbook(t, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
		<row r="1"><c r="A1" s="1"/></row>
		<row r="3">
			<c r="A3" t="s"><v>0</v></c><c r="B3" t="inlineStr"><is><r><t>Se</t></r><r><t>lected</t></r></is></c>
			<c r="C3" t="inlineStr"><is><t>Answer</t></is></c><c r="D3" t="s"><v>1</v></c>
		</row>
		<row r="5"><c r="A5"><f>5+7</f><v>12</v></c><c r="B5" t="b"><v>1</v></c><c r="C5" t="s"><v>2</v></c></row>
		<row r="6"><c r="A6"><f>1+1</f></c><c r="B6" t="inlineStr"><is><t xml:space="preserve"> </t></is></c></row>
		<row r="10"><c r="A10"><v>4</v></c><c r="C10" t="str"><f>"Saved"</f><v>Saved</v></c><c r="D10" t="inlineStr"><is><t>held</t></is></c></row>
	</sheetData></worksheet>`)
	open := func() *bytes.Reader { return bytes.NewReader(workbook) }
	size := int64(len(workbook))
	names, err := WorkbookSheets(open(), size)
	if err != nil || !slices.Equal(names, []string{"说明", "Samples"}) {
		t.Fatalf("sheets = %q, %v", names, err)
	}
	report, err := AnalyzeWorkbook(open(), size, Options{Sheet: "Samples", Reviews: []Review{{Row: 2, Accept: true, Reason: "Confirmed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Columns, []string{"Score", "Selected", "Answer", "_quality_status"}) || report.Total != 2 || report.Valid != 2 ||
		report.Rows[0].Row != 1 || report.Rows[0].SourceRow != 5 || report.Rows[1].Row != 2 || report.Rows[1].SourceRow != 10 {
		t.Fatalf("unexpected headers, counts or original row numbers: %+v", report)
	}
	score, selected, answer := report.Summaries[0], report.Summaries[1], report.Summaries[2]
	if score.Numeric != 2 || score.Mean == nil || *score.Mean != 8 || selected.Missing != 1 ||
		!slices.Equal(selected.Frequencies, []Frequency{{Value: "TRUE", Count: 1}}) ||
		!slices.Equal(answer.Frequencies, []Frequency{{Value: "Saved", Count: 1}, {Value: "Yes & yes", Count: 1}}) {
		t.Fatalf("unexpected sheet values: %+v", report.Summaries)
	}
	other, err := AnalyzeWorkbook(open(), size, Options{Sheet: "说明"})
	if err != nil || !slices.Equal(other.Columns, []string{"Notes"}) || other.Total != 1 || other.Rows[0].SourceRow != 2 {
		t.Fatalf("unexpected first-sheet result: %+v, %v", other, err)
	}
	for _, name := range []string{"", "samples", " Samples", "Chart", "External"} {
		if _, err := AnalyzeWorkbook(open(), size, Options{Sheet: name}); err == nil {
			t.Errorf("accepted unsupported sheet %q", name)
		}
	}
}

func TestWorkbookSheetAndTextLimits(t *testing.T) {
	const worksheetStart = `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`
	const worksheetEnd = `</sheetData></worksheet>`
	empty := makeSurveyWorkbook(t, worksheetStart+worksheetEnd,
		`<sheet name="说明" sheetId="2" r:id="notes"/>`, "",
		`<sheet name="Samples" sheetId="6" r:id="sample"/>`, "",
	)
	if sheets, err := WorkbookSheets(bytes.NewReader(empty), int64(len(empty))); err == nil || len(sheets) != 0 || !strings.Contains(err.Error(), "没有可选择的数据工作表") {
		t.Fatalf("chart-only workbook sheets = %v, %v", sheets, err)
	}

	const header = `<row><c t="s"><v>0</v></c></row>`
	rows := strings.Repeat(`<row><c t="s"><v>2</v></c></row>`, maxCSVBytes/maxXLSXCellBytes+1)
	workbook := makeSurveyWorkbook(t, worksheetStart+header+rows+worksheetEnd,
		`Yes &amp; yes`, strings.Repeat("x", maxXLSXCellBytes),
	)
	if _, err := AnalyzeWorkbook(bytes.NewReader(workbook), int64(len(workbook)), Options{Sheet: "Samples"}); err == nil || !strings.Contains(err.Error(), "解码后的答卷文本") {
		t.Fatalf("repeated references to an oversized shared string total: %v", err)
	}
}

func makeSurveyWorkbook(t *testing.T, worksheet string, replacements ...string) []byte {
	t.Helper()
	replace := strings.NewReplacer(replacements...)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, part := range []struct{ name, text string }{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/content/book.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/response.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/content/tabs/notes.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/content/strings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="root" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="content/book.xml"/></Relationships>`},
		{"content/book.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>
			<sheet name="说明" sheetId="2" r:id="notes"/><sheet name="Chart" sheetId="3" r:id="chart"/>
			<sheet name="Samples" sheetId="6" r:id="sample"/><sheet name="External" sheetId="7" r:id="external"/>
		</sheets></workbook>`},
		{"content/_rels/book.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
			<Relationship Id="sample" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/response.xml"/>
			<Relationship Id="chart" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chartsheet" Target="tabs/chart.xml"/>
			<Relationship Id="notes" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="tabs/notes.xml"/>
			<Relationship Id="external" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="https://example.invalid/external.xml" TargetMode="External"/>
			<Relationship Id="strings" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="strings.xml"/>
		</Relationships>`},
		{"content/strings.xml", `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
			<si><r><t>Sco</t></r><r><t>re</t></r><rPh sb="0" eb="3"><t>Hidden phonetics</t></rPh></si><si><t>_quality_status</t></si><si><t>Yes &amp; yes</t></si>
		</sst>`},
		{"content/tabs/notes.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c t="inlineStr"><is><t>Notes</t></is></c></row><row><c t="inlineStr"><is><t>Ready</t></is></c></row></sheetData></worksheet>`},
		{"response.xml", worksheet},
	} {
		entry, err := writer.Create(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("\ufeff" + replace.Replace(part.text))); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
