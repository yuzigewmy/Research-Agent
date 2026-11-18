"""Convert a small, text-only Markdown subset from UTF-8 stdin to DOCX stdout."""

from io import BytesIO
import re
import sys

try:
    from docx import Document
    from docx.enum.text import WD_ALIGN_PARAGRAPH
    from docx.oxml import OxmlElement
    from docx.oxml.ns import qn
except ImportError:
    sys.exit(3)


HEADING = re.compile(r" {0,3}(#{1,6})[ \t]+(.*)")
LIST_ITEM = re.compile(r" {0,3}([-+*]|[0-9]{1,9}[.)])[ \t]+(.*)")
FENCE = re.compile(r" {0,3}(`{3,}|~{3,})(.*)")
TABLE_SEPARATOR = re.compile(r":?-{3,}:?")
TABLE_PIPE = re.compile(r"\\([\\|])|(\|)")
INLINE = re.compile(
    r"\\(?P<escape>[\\`*_])"
    r"|(?<!`)(?P<ticks>`+)(?P<code>[^`]+)(?P=ticks)(?!`)"
    r"|(?<!\*)(?P<stars>\*{1,3})(?P<star_text>[^\s*](?:[^*]*[^\s*])?)(?P=stars)(?!\*)"
    r"|(?<!\w)(?P<underscores>_{1,3})(?P<under_text>[^\s_](?:[^_]*[^\s_])?)(?P=underscores)(?!\w)"
)
EAST_ASIA_FONT = "Microsoft YaHei"
ALIGNMENTS = {
    (True, False): WD_ALIGN_PARAGRAPH.LEFT,
    (True, True): WD_ALIGN_PARAGRAPH.CENTER,
    (False, True): WD_ALIGN_PARAGRAPH.RIGHT,
}


def add_inline(paragraph, text, *, header=False):
    def add(text):
        run = paragraph.add_run(text)
        if header:
            run.bold = True
        return run

    end = 0
    for match in INLINE.finditer(text):
        if match.start() > end:
            add(text[end:match.start()])
        if match["escape"] is not None:
            add(match["escape"])
        elif match["code"] is not None:
            add(match["code"]).font.name = "Consolas"
        else:
            marker = match["stars"] or match["underscores"]
            run = add(match["star_text"] or match["under_text"])
            if len(marker) >= 2:
                run.bold = True
            if len(marker) % 2:
                run.italic = True
        end = match.end()
    if end < len(text):
        add(text[end:])


def table_cells(line):
    """Split a pipe row while preserving Markdown's escaped pipes."""
    line = line.strip()
    cells, pieces, end = [], [], 0
    for match in TABLE_PIPE.finditer(line):
        pieces.append(line[end:match.start()])
        if match[2] is not None:
            cells.append("".join(pieces).strip())
            pieces = []
        else:
            pieces.append("|" if match[1] == "|" else match[0])
        end = match.end()
    if not cells:
        return []
    if line.startswith("|"):
        cells.pop(0)
    if end < len(line) or pieces:
        pieces.append(line[end:])
        cells.append("".join(pieces).strip())
    return cells


def table_header(lines, index):
    if index + 1 >= len(lines) or "|" not in lines[index]:
        return None
    headers = table_cells(lines[index])
    separator = table_cells(lines[index + 1])
    if 0 < len(headers) <= 63 and len(headers) == len(separator) and all(
        TABLE_SEPARATOR.fullmatch(cell) for cell in separator
    ):
        return headers, [ALIGNMENTS.get((cell.startswith(":"), cell.endswith(":"))) for cell in separator]
    return None


def add_table(document, columns, alignments, lines, index):
    width = len(columns)
    table = document.add_table(rows=1, cols=width, style="Table Grid")
    header = table.rows[0]
    header._tr.get_or_add_trPr().append(OxmlElement("w:tblHeader"))

    def fill(row, values, heading=False):
        for cell, value, alignment in zip(row.cells, values, alignments):
            paragraph = cell.paragraphs[0]
            paragraph.alignment = alignment
            add_inline(paragraph, value, header=heading)

    fill(header, columns, heading=True)
    while index < len(lines):
        line = lines[index]
        if HEADING.fullmatch(line) or FENCE.fullmatch(line) or LIST_ITEM.fullmatch(line):
            break
        values = table_cells(line)
        if not values or len(values) > width:
            break
        fill(table.add_row(), values)
        index += 1
    return index


def list_numbering(document, start):
    # Use Word's built-in decimal definition with a separate counter per list.
    numbering = document.part.numbering_part.element
    num_id = document.styles["List Number"].element.pPr.numPr.numId.val
    template = numbering.xpath(f"./w:num[@w:numId={num_id}]")[0]
    numbers = numbering.add_num(template.abstractNumId.val)
    numbers.add_lvlOverride(0).add_startOverride(start)
    return numbers.numId


def add_list(document, lines, index, first):
    marker = first[1]
    ordered = marker[0].isdigit()
    num_id = list_numbering(document, int(marker[:-1])) if ordered else None
    while index < len(lines):
        item = LIST_ITEM.fullmatch(lines[index])
        if item is None or item[1][-1] != marker[-1]:
            break
        paragraph = document.add_paragraph(style="List Number" if ordered else "List Bullet")
        if num_id is not None:
            paragraph._p.get_or_add_pPr().get_or_add_numPr().get_or_add_numId().val = num_id
        add_inline(paragraph, item[2].rstrip())
        index += 1
    return index


def add_fence(document, lines, index, marker):
    end_pattern = re.compile(rf" {{0,3}}{marker[0]}{{{len(marker)},}}[ \t]*")
    start = index
    while index < len(lines) and not end_pattern.fullmatch(lines[index]):
        index += 1
    document.add_paragraph().add_run("\n".join(lines[start:index])).font.name = "Consolas"
    return min(index + 1, len(lines))


def assemble(source):
    document = Document()
    headings = [f"Heading {level}" for level in range(1, 7)]
    for name in ["Normal", "List Bullet", "List Number", *headings]:
        style = document.styles[name]
        fonts = style.element.get_or_add_rPr().get_or_add_rFonts()
        fonts.attrib.pop(qn("w:eastAsiaTheme"), None)
        fonts.set(qn("w:eastAsia"), EAST_ASIA_FONT)

    # Preserve Unicode line-separator characters as text rather than splitting on them.
    lines = source.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    pending = []

    def flush_paragraph():
        if pending:
            add_inline(document.add_paragraph(), " ".join(pending))
            pending.clear()

    index = 0
    while index < len(lines):
        line = lines[index]
        if not line.strip():
            flush_paragraph()
            index += 1
        elif fence := FENCE.fullmatch(line):
            flush_paragraph()
            index = add_fence(document, lines, index + 1, fence[1])
        elif heading := HEADING.fullmatch(line):
            flush_paragraph()
            text = re.sub(r"[ \t]+#+[ \t]*$", "", heading[2]).strip()
            add_inline(document.add_heading(level=len(heading[1])), text)
            index += 1
        elif item := LIST_ITEM.fullmatch(line):
            flush_paragraph()
            index = add_list(document, lines, index, item)
        elif header := table_header(lines, index):
            flush_paragraph()
            index = add_table(document, *header, lines, index + 2)
        else:
            pending.append(line.strip())
            index += 1
    flush_paragraph()
    return document


def main():
    source = sys.stdin.buffer.read().decode("utf-8-sig", errors="replace")
    # Replace characters forbidden in XML 1.0 before handing text to Word.
    source = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f\ufffe\uffff]", "\ufffd", source)
    output = BytesIO()
    assemble(source).save(output)
    sys.stdout.buffer.write(output.getbuffer())


if __name__ == "__main__":
    main()
