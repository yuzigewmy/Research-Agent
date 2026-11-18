package wordexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPythonEnvironmentKeepsOnlyStartupVariables(t *testing.T) {
	parent := []string{
		"Path=interpreter-path",
		"SystemRoot=windows-path",
		"TEMP=temp-path",
		"HOME=home-path",
		"LANG=locale",
		"OPENAI_API_KEY=example",
		"RESEARCH_MODEL_TOKEN=example",
		"PROVIDER_CUSTOM_CREDENTIAL=example",
		"PYTHONPATH=injected-path",
		"PYTHONHOME=injected-path",
		"=C:=working-directory",
	}
	want := parent[:5]
	if got := pythonEnvironment(parent); !reflect.DeepEqual(got, want) {
		t.Fatalf("worker environment = %v, want %v", got, want)
	}
	if got := pythonEnvironment(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty environment must be non-nil to avoid implicit inheritance, got %v", got)
	}
}

func TestExportReturnsErrorWithoutBytes(t *testing.T) {
	if output, err := Export(context.Background(), "", "manuscript"); err == nil || output != nil {
		t.Fatalf("unconfigured Python returned %d bytes and error %v", len(output), err)
	}
	missing := filepath.Join(t.TempDir(), "missing-python.exe")
	if output, err := Export(context.Background(), missing, "manuscript"); err == nil || output != nil {
		t.Fatalf("missing Python returned %d bytes and error %v", len(output), err)
	}
}

func TestExportReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := Export(ctx, "unused-python", "manuscript")
	if !errors.Is(err, context.Canceled) || output != nil {
		t.Fatalf("canceled export returned %d bytes and error %v", len(output), err)
	}
}

func TestExportReopensAsEditableWordDocument(t *testing.T) {
	python := os.Getenv("WORDEXPORT_TEST_PYTHON")
	if python == "" {
		t.Skip("set WORDEXPORT_TEST_PYTHON to a Python executable with python-docx")
	}
	source := "# 中文标题\r\n\r\nA **bold** and _italic_ paragraph with `a * b`.\r\nContinued text.\r\n\r\n" +
		"- 第一项\n- Second item\n\n3. Third step\n4. Fourth step\n\n" +
		"| 指标 | Value |\n| :--- | ---: |\n| **样本** | 10 |\n| escaped \\| pipe | `a\\|b` |\n## Notes | next steps\n\n" +
		"[local](file:///missing.txt) ![remote](https://example.invalid/image.png) <script>plain</script>\n\n" +
		"```text\n# literal heading\n- literal item\n```\n\nTrailing text with a control: \x00\n"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := Export(ctx, python, source)
	if err != nil {
		t.Fatal(err)
	}
	const reopen = `
from io import BytesIO
import sys
from docx import Document
from docx.oxml.ns import qn
from docx.enum.text import WD_ALIGN_PARAGRAPH

doc = Document(BytesIO(sys.stdin.buffer.read()))
heading, body, first, second, third, fourth, notes, inert, fenced, trailing = doc.paragraphs
assert notes.text == 'Notes | next steps' and notes.style.name == 'Heading 2'
assert heading.text == '中文标题' and heading.style.name == 'Heading 1'
fonts = heading.style.element.rPr.rFonts
assert fonts.get(qn('w:eastAsia')) == 'Microsoft YaHei'
assert body.text == 'A bold and italic paragraph with a * b. Continued text.'
assert any(run.text == 'bold' and run.bold for run in body.runs)
assert any(run.text == 'italic' and run.italic for run in body.runs)
assert any(run.text == 'a * b' and run.font.name == 'Consolas' for run in body.runs)
assert first.text == '第一项' and first.style.name == second.style.name == 'List Bullet'
assert third.text == 'Third step' and third.style.name == fourth.style.name == 'List Number'
num_id = third._p.pPr.numPr.numId.val
assert num_id == fourth._p.pPr.numPr.numId.val
numbering = doc.part.numbering_part.element
assert numbering.xpath(f'./w:num[@w:numId={num_id}]/w:lvlOverride/w:startOverride')[0].val == 3
assert len(doc.tables) == 1
table = doc.tables[0]
assert [[cell.text for cell in row.cells] for row in table.rows] == [
    ['指标', 'Value'], ['样本', '10'], ['escaped | pipe', 'a|b'],
]
assert table.cell(1, 0).paragraphs[0].runs[0].bold
assert table.cell(1, 1).paragraphs[0].alignment == WD_ALIGN_PARAGRAPH.RIGHT
assert inert.text == '[local](file:///missing.txt) ![remote](https://example.invalid/image.png) <script>plain</script>'
assert not any(rel.is_external for rel in doc.part.rels.values())
assert fenced.text == '# literal heading\n- literal item'
assert trailing.text == 'Trailing text with a control: \ufffd'
`
	check := exec.CommandContext(ctx, python, "-I", "-B", "-c", reopen)
	check.Env = pythonEnvironment(os.Environ())
	check.Stdin = bytes.NewReader(output)
	if log, err := check.CombinedOutput(); err != nil {
		t.Fatalf("reopen Word document: %v\n%s", err, log)
	}
}
