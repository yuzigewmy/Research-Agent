package wordexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportWithFiguresReopensAsEditableWordDocument(t *testing.T) {
	python := os.Getenv("WORDEXPORT_TEST_PYTHON")
	font := os.Getenv("SOFTWARE_PDF_TEST_FONT")
	if python == "" || font == "" {
		t.Skip("set WORDEXPORT_TEST_PYTHON and SOFTWARE_PDF_TEST_FONT to verify figure export")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	figures := []Figure{
		{
			Caption: "图一 处理流程",
			Nodes: []FigureNode{
				{ID: "input", Label: "采集原始数据审核并清理缺项记录", X: 64, Y: 64},
				{ID: "output", Label: "生成结果", X: 384, Y: 64},
			},
			Edges: []FigureEdge{{From: "input", To: "output"}},
		},
		{
			Caption: "图二 <输入>与输出",
			Nodes: []FigureNode{
				{ID: "review", Label: "人工复核", X: 100, Y: 110},
				{ID: "store", Label: "入库", X: 100, Y: 360},
			},
			Edges: []FigureEdge{{From: "review", To: "store"}},
		},
	}
	const source = "# 可编辑标题\n\n正文保留 **重点** 和 ![图片](file:///missing.png)。"
	output, err := ExportWithFigures(ctx, python, font, source, figures)
	if err != nil {
		t.Fatal(err)
	}
	const reopen = `
from io import BytesIO
import sys
from docx import Document
from docx.oxml.ns import qn
from PIL import Image

doc = Document(BytesIO(sys.stdin.buffer.read()))
heading, body, picture1, caption1, picture2, caption2 = doc.paragraphs
assert heading.text == '可编辑标题' and heading.style.name == 'Heading 1'
assert body.text == '正文保留 重点 和 ![图片](file:///missing.png)。'
assert any(run.text == '重点' and run.bold for run in body.runs)
assert [paragraph.text for paragraph in (caption1, caption2)] == ['图一 处理流程', '图二 <输入>与输出']
assert all(paragraph.style.name == 'Caption' for paragraph in (caption1, caption2))
assert caption1.style.element.rPr.rFonts.get(qn('w:eastAsia')) == 'Microsoft YaHei'
assert picture1.paragraph_format.keep_with_next and picture2.paragraph_format.keep_with_next
assert not any(rel.is_external for rel in doc.part.rels.values())
assert len(doc.inline_shapes) == 2
section = doc.sections[0]
width = section.page_width - section.left_margin - section.right_margin
for shape, edge_pixel in zip(doc.inline_shapes, [(608, 192), (360, 520)]):
    assert shape.width == width and abs(shape.height - width * 2 / 3) < 1
    blip = shape._inline.graphic.graphicData.pic.blipFill.blip
    with Image.open(BytesIO(doc.part.related_parts[blip.embed].blob)) as image:
        image.load()
        assert image.format == 'PNG' and image.size == (1920, 1280)
        assert image.getpixel(edge_pixel) != (255, 255, 255)
`
	check := exec.CommandContext(ctx, python, "-I", "-B", "-c", reopen)
	check.Env = pythonEnvironment(os.Environ())
	check.Stdin = bytes.NewReader(output)
	if log, err := check.CombinedOutput(); err != nil {
		t.Fatalf("reopen Word document with figures: %v\n%s", err, log)
	}
}

func TestExportWithFiguresRejectsMissingFont(t *testing.T) {
	figures := []Figure{{Caption: "图一", Nodes: []FigureNode{{ID: "node", Label: "步骤"}}}}
	output, err := ExportWithFigures(context.Background(), "unused-python", "", "text", figures)
	if err == nil || !strings.Contains(err.Error(), "font") || output != nil {
		t.Fatalf("blank font returned %d bytes and error %v", len(output), err)
	}
	python := os.Getenv("WORDEXPORT_TEST_PYTHON")
	if python == "" || os.Getenv("SOFTWARE_PDF_TEST_FONT") == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	missing := filepath.Join(t.TempDir(), "missing.ttf")
	output, err = ExportWithFigures(ctx, python, missing, "text", figures)
	if err == nil || !strings.Contains(err.Error(), "could not load") || output != nil {
		t.Fatalf("unreadable font returned %d bytes and error %v", len(output), err)
	}
	output, err = ExportWithFigures(ctx, python, missing, "plain text", nil)
	if err != nil || len(output) == 0 {
		t.Fatalf("text-only fallback returned %d bytes and error %v", len(output), err)
	}
}

func TestWorkerOnlyRequiresPillowForFigures(t *testing.T) {
	python := os.Getenv("WORDEXPORT_TEST_PYTHON")
	if python == "" {
		t.Skip("set WORDEXPORT_TEST_PYTHON to check the optional Pillow import")
	}
	const blockPillow = `
import sys
class WithoutPillow:
    def find_spec(self, fullname, *args):
        if fullname == "PIL" or fullname.startswith("PIL."):
            raise ModuleNotFoundError(fullname)
sys.meta_path.insert(0, WithoutPillow())
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(source string, args ...string) ([]byte, error) {
		check := exec.CommandContext(ctx, python, append([]string{"-I", "-B", "-c", blockPillow + workerScript}, args...)...)
		check.Env = pythonEnvironment(os.Environ())
		check.Stdin = strings.NewReader(source)
		return check.Output()
	}
	if output, err := run("普通正文"); err != nil || !bytes.HasPrefix(output, []byte("PK\x03\x04")) {
		t.Fatalf("plain export without Pillow returned %d bytes and error %v", len(output), err)
	}
	output, err := run(`{"source":"普通正文","figures":[{}],"fontPath":"unused.ttf"}`, "--figures")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 4 || len(output) != 0 {
		t.Fatalf("figure export without Pillow returned %d bytes and error %v", len(output), err)
	}
}

func TestExportWithFiguresReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := ExportWithFigures(ctx, "unused-python", "unused.ttf", "text", []Figure{{}})
	if !errors.Is(err, context.Canceled) || output != nil {
		t.Fatalf("canceled export returned %d bytes and error %v", len(output), err)
	}
}
