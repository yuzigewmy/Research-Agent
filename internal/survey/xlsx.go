package survey

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const (
	transitionalSheetXML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	strictSheetXML       = "http://purl.oclc.org/ooxml/spreadsheetml/main"
	transitionalRel      = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	strictRel            = "http://purl.oclc.org/ooxml/officeDocument/relationships"
	packageRelXML        = "http://schemas.openxmlformats.org/package/2006/relationships"

	maxXLSXArchiveBytes = 16 << 20
	maxXLSXEntries      = 4096
	maxXLSXMetadata     = 1 << 20
	maxXLSXSheetXML     = 32 << 20
	maxXLSXStringXML    = 16 << 20
	maxXLSXCellBytes    = 128 << 10
	maxXLSXRowNumber    = 1048576
)

var errXLSXXML = errors.New("工作簿的 XML 内容损坏或格式不受支持。")

type xlsxArchive map[string]*zip.File

type xlsxSheet struct {
	name, part string
}

type xlsxWorkbook struct {
	files         xlsxArchive
	sheets        []xlsxSheet
	sharedStrings string
}

type xlsxRelationship struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

func (r xlsxRelationship) isType(name string) bool {
	return r.Type == transitionalRel+"/"+name || r.Type == strictRel+"/"+name
}

func (r xlsxRelationship) internal() bool {
	return r.Mode == "" || r.Mode == "Internal"
}

// WorkbookSheets returns the names of internal worksheets in workbook order.
func WorkbookSheets(source io.ReaderAt, size int64) ([]string, error) {
	book, err := openXLSX(source, size)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(book.sheets))
	for _, sheet := range book.sheets {
		names = append(names, sheet.name)
	}
	return names, nil
}

// AnalyzeWorkbook analyzes the worksheet named exactly by options.Sheet. It
// reads stored cell values and reports each response's physical worksheet row.
func AnalyzeWorkbook(source io.ReaderAt, size int64, options Options) (Report, error) {
	if options.Sheet == "" {
		return Report{}, errors.New("请选择一个工作表。")
	}
	book, err := openXLSX(source, size)
	if err != nil {
		return Report{}, err
	}
	for _, sheet := range book.sheets {
		if sheet.name == options.Sheet {
			return book.analyze(sheet, options)
		}
	}
	return Report{}, errors.New("找不到所选的数据工作表。")
}

// Locate the office document through the package relationship, then resolve the
// sheet relationship IDs from its workbook part.
// https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/structure-of-a-spreadsheetml-document
func openXLSX(source io.ReaderAt, size int64) (xlsxWorkbook, error) {
	if size <= 0 || size > maxXLSXArchiveBytes {
		return xlsxWorkbook{}, fmt.Errorf("XLSX 工作簿必须非空且不超过 %d MiB。", maxXLSXArchiveBytes>>20)
	}
	archive, err := zip.NewReader(source, size)
	if err != nil {
		return xlsxWorkbook{}, errors.New("无法打开 XLSX 工作簿。文件可能已损坏或未采用 XLSX 格式。")
	}
	if len(archive.File) > maxXLSXEntries {
		return xlsxWorkbook{}, errors.New("XLSX 工作簿包含过多部件。")
	}
	files := make(xlsxArchive, len(archive.File))
	for _, part := range archive.File {
		if part.FileInfo().IsDir() {
			continue
		}
		if _, exists := files[part.Name]; exists {
			return xlsxWorkbook{}, errors.New("XLSX 工作簿包含重复的部件。")
		}
		files[part.Name] = part
	}
	roots, err := files.relationships("_rels/.rels")
	if err != nil {
		return xlsxWorkbook{}, err
	}
	var workbookPart string
	for _, rel := range roots {
		if !rel.internal() || !rel.isType("officeDocument") {
			continue
		}
		if workbookPart != "" {
			return xlsxWorkbook{}, errors.New("XLSX 包含多个工作簿部件。")
		}
		workbookPart, err = xlsxTarget("", rel.Target)
		if err != nil {
			return xlsxWorkbook{}, err
		}
	}
	if workbookPart == "" {
		return xlsxWorkbook{}, errors.New("XLSX 缺少内部工作簿关系。")
	}
	var contents struct {
		XMLName xml.Name
		Sheets  []struct {
			Name  string     `xml:"name,attr"`
			Attrs []xml.Attr `xml:",any,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := files.decode(workbookPart, &contents); err != nil {
		return xlsxWorkbook{}, err
	}
	if !isXLSXElement(contents.XMLName, "workbook") {
		return xlsxWorkbook{}, errXLSXXML
	}
	relsPart := path.Join(path.Dir(workbookPart), "_rels", path.Base(workbookPart)+".rels")
	relations, err := files.relationships(relsPart)
	if err != nil {
		return xlsxWorkbook{}, err
	}
	book := xlsxWorkbook{files: files}
	for _, rel := range relations {
		if !rel.internal() || !rel.isType("sharedStrings") {
			continue
		}
		if book.sharedStrings != "" {
			return xlsxWorkbook{}, errors.New("XLSX 包含多个共享字符串部件。")
		}
		book.sharedStrings, err = xlsxTarget(workbookPart, rel.Target)
		if err != nil {
			return xlsxWorkbook{}, err
		}
	}
	seen := make(map[string]bool)
	for _, sheet := range contents.Sheets {
		var relID string
		for _, attr := range sheet.Attrs {
			if attr.Name.Local == "id" && (attr.Name.Space == transitionalRel || attr.Name.Space == strictRel) {
				relID = attr.Value
				break
			}
		}
		rel, exists := relations[relID]
		if !exists {
			return xlsxWorkbook{}, errors.New("XLSX 缺少工作表关系。")
		}
		if !rel.internal() || !rel.isType("worksheet") {
			continue
		}
		part, err := xlsxTarget(workbookPart, rel.Target)
		if err != nil {
			return xlsxWorkbook{}, err
		}
		if sheet.Name == "" || seen[sheet.Name] || files[part] == nil {
			return xlsxWorkbook{}, errors.New("XLSX 工作表的名称重复、为空或找不到对应部件。")
		}
		seen[sheet.Name] = true
		book.sheets = append(book.sheets, xlsxSheet{name: sheet.Name, part: part})
	}
	if len(book.sheets) == 0 {
		return xlsxWorkbook{}, errors.New("XLSX 工作簿中没有可选择的数据工作表。")
	}
	return book, nil
}

func xlsxTarget(owner, target string) (string, error) {
	u, err := url.Parse(target)
	if err == nil && !u.IsAbs() && u.Host == "" && !u.ForceQuery && u.RawQuery == "" && u.Fragment == "" &&
		u.Path != "" && !strings.HasPrefix(u.Path, "//") && !strings.ContainsAny(u.Path, "\\\x00") {
		part := path.Join(path.Dir(owner), u.Path)
		if strings.HasPrefix(u.Path, "/") {
			part = path.Clean(u.Path[1:])
		}
		if part != "." && fs.ValidPath(part) {
			return part, nil
		}
	}
	return "", errors.New("XLSX 的内部部件路径无效。")
}

func (files xlsxArchive) relationships(part string) (map[string]xlsxRelationship, error) {
	var data struct {
		XMLName xml.Name
		Links   []xlsxRelationship `xml:"Relationship"`
	}
	if err := files.decode(part, &data); err != nil {
		return nil, err
	}
	if data.XMLName != (xml.Name{Space: packageRelXML, Local: "Relationships"}) {
		return nil, errXLSXXML
	}
	links := make(map[string]xlsxRelationship, len(data.Links))
	for _, link := range data.Links {
		if _, exists := links[link.ID]; link.ID == "" || exists {
			return nil, errors.New("XLSX 的部件关系编号无效或重复。")
		}
		links[link.ID] = link
	}
	return links, nil
}

func (files xlsxArchive) open(part string, limit uint64) (io.ReadCloser, error) {
	file := files[part]
	if file == nil {
		return nil, errors.New("XLSX 缺少必要的部件。")
	}
	// archive/zip also enforces the declared uncompressed size while reading.
	if file.UncompressedSize64 > limit {
		return nil, fmt.Errorf("XLSX 部件解压后不能超过 %d MiB。", limit>>20)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, errors.New("无法读取 XLSX 部件。")
	}
	return reader, nil
}

func (files xlsxArchive) decode(part string, data any) error {
	reader, err := files.open(part, maxXLSXMetadata)
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := newXLSXDecoder(reader)
	root, err := nextXLSXChild(decoder)
	if err != nil || decoder.DecodeElement(data, &root) != nil {
		return errXLSXXML
	}
	return finishXLSXXML(decoder)
}

func newXLSXDecoder(reader io.Reader) *xml.Decoder {
	buffer := bufio.NewReader(reader)
	if prefix, _ := buffer.Peek(3); string(prefix) == "\xef\xbb\xbf" {
		_, _ = buffer.Discard(3)
	}
	return xml.NewDecoder(buffer)
}

func isXLSXElement(name xml.Name, local string) bool {
	return name.Local == local && (name.Space == transitionalSheetXML || name.Space == strictSheetXML)
}

// nextXLSXChild is called between elements at the same nesting level. Each
// returned child is then consumed with DecodeElement, Skip or another traversal.
func nextXLSXChild(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, errXLSXXML
		}
		switch value := token.(type) {
		case xml.StartElement:
			return value, nil
		case xml.EndElement:
			return xml.StartElement{}, io.EOF
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return xml.StartElement{}, errXLSXXML
			}
		}
	}
}

func finishXLSXXML(decoder *xml.Decoder) error {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errXLSXXML
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return errXLSXXML
			}
		case xml.Comment, xml.ProcInst:
		default:
			return errXLSXXML
		}
	}
}

func (book xlsxWorkbook) analyze(sheet xlsxSheet, options Options) (Report, error) {
	var shared []string
	if book.sharedStrings != "" {
		var err error
		shared, err = book.files.readSharedStrings(book.sharedStrings)
		if err != nil {
			return Report{}, err
		}
	}
	file, err := book.files.open(sheet.part, maxXLSXSheetXML)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()
	decoder := newXLSXDecoder(file)
	root, err := nextXLSXChild(decoder)
	if err != nil || !isXLSXElement(root.Name, "worksheet") {
		return Report{}, errXLSXXML
	}
	for {
		child, err := nextXLSXChild(decoder)
		if err == io.EOF {
			return Report{}, errors.New("工作表缺少答卷表头。")
		}
		if err != nil {
			return Report{}, err
		}
		if isXLSXElement(child.Name, "sheetData") {
			break
		}
		if err := decoder.Skip(); err != nil {
			return Report{}, errXLSXXML
		}
	}
	rows := xlsxRows{xml: decoder, shared: shared}
	header, _, err := rows.next()
	if err == io.EOF {
		return Report{}, errors.New("工作表缺少答卷表头。")
	}
	if err != nil {
		return Report{}, err
	}
	return analyzeResponses(header, rows.next, options)
}

// The shared string index is zero-based. An item consists of plain text or
// rich-text runs, excluding phonetic annotations and formatting.
// https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/working-with-the-shared-string-table
func (files xlsxArchive) readSharedStrings(part string) ([]string, error) {
	file, err := files.open(part, maxXLSXStringXML)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := newXLSXDecoder(file)
	root, err := nextXLSXChild(decoder)
	if err != nil || !isXLSXElement(root.Name, "sst") {
		return nil, errXLSXXML
	}
	var values []string
	for {
		item, err := nextXLSXChild(decoder)
		if err == io.EOF {
			return values, finishXLSXXML(decoder)
		}
		if err != nil {
			return nil, err
		}
		if !isXLSXElement(item.Name, "si") {
			if err := decoder.Skip(); err != nil {
				return nil, errXLSXXML
			}
			continue
		}
		if len(values) >= maxResponses*maxColumns {
			return nil, errors.New("XLSX 工作簿包含过多共享字符串。")
		}
		value, err := readXLSXText(decoder)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
}

func readXLSXText(decoder *xml.Decoder) (string, error) {
	var text strings.Builder
	for {
		child, err := nextXLSXChild(decoder)
		if err == io.EOF {
			return strings.TrimSpace(text.String()), nil
		}
		if err != nil {
			return "", err
		}
		var value string
		switch {
		case isXLSXElement(child.Name, "t"):
			err = decoder.DecodeElement(&value, &child)
		case isXLSXElement(child.Name, "r"):
			var run struct {
				Text string `xml:"t"`
			}
			err = decoder.DecodeElement(&run, &child)
			value = run.Text
		default:
			err = decoder.Skip()
		}
		if err != nil {
			return "", errXLSXXML
		}
		if text.Len()+len(value) > maxXLSXCellBytes {
			return "", errors.New("XLSX 的单元格内容过长。")
		}
		text.WriteString(value)
	}
}

type xlsxRows struct {
	xml       *xml.Decoder
	shared    []string
	lastRow   int
	nonempty  int
	textBytes int
}

func (rows *xlsxRows) next() ([]string, int, error) {
	for {
		element, err := nextXLSXChild(rows.xml)
		if err == io.EOF {
			// Consume the rest of the worksheet and check that the ZIP part
			// ends cleanly, including its checksum.
			for {
				child, err := nextXLSXChild(rows.xml)
				if err == io.EOF {
					if err := finishXLSXXML(rows.xml); err != nil {
						return nil, 0, err
					}
					return nil, 0, io.EOF
				}
				if err != nil || isXLSXElement(child.Name, "sheetData") || rows.xml.Skip() != nil {
					return nil, 0, errXLSXXML
				}
			}
		}
		if err != nil {
			return nil, 0, err
		}
		if !isXLSXElement(element.Name, "row") {
			if err := rows.xml.Skip(); err != nil {
				return nil, 0, errXLSXXML
			}
			continue
		}
		fields, row, err := rows.readRow(element)
		if err != nil {
			return nil, 0, err
		}
		rows.lastRow = row
		if len(fields) > 0 {
			rows.nonempty++
			return fields, row, nil
		}
	}
}

func (rows *xlsxRows) readRow(element xml.StartElement) ([]string, int, error) {
	var fields []string
	column, row := 0, 0
	if coordinate := xlsxAttr(element, "r"); coordinate != "" {
		var err error
		row, err = strconv.Atoi(coordinate)
		if err != nil || row <= rows.lastRow || row > maxXLSXRowNumber {
			return nil, 0, errors.New("XLSX 的工作表行号无效或未递增。")
		}
	}
	for {
		cell, err := nextXLSXChild(rows.xml)
		if err == io.EOF {
			if row == 0 {
				row = rows.lastRow + 1
				if row > maxXLSXRowNumber {
					return nil, 0, errors.New("XLSX 的工作表行号超出范围。")
				}
			}
			return fields, row, nil
		}
		if err != nil {
			return nil, 0, err
		}
		if !isXLSXElement(cell.Name, "c") {
			if err := rows.xml.Skip(); err != nil {
				return nil, 0, errXLSXXML
			}
			continue
		}
		if ref := xlsxAttr(cell, "r"); ref != "" {
			col, cellRow := xlsxCoordinate(ref)
			if col <= column || cellRow <= rows.lastRow || (row != 0 && row != cellRow) {
				return nil, 0, errors.New("XLSX 的单元格坐标无效或未按顺序排列。")
			}
			column, row = col, cellRow
		} else {
			column++
		}
		value, err := rows.readCell(xlsxAttr(cell, "t"))
		if err != nil {
			return nil, 0, err
		}
		if value == "" {
			continue
		}
		if column > maxColumns {
			return nil, 0, fmt.Errorf("答卷表最多支持 %d 列。", maxColumns)
		}
		if rows.nonempty > maxResponses {
			return nil, 0, fmt.Errorf("工作表最多支持 %d 份答卷。", maxResponses)
		}
		rows.textBytes += len(value)
		if rows.textBytes > maxCSVBytes {
			return nil, 0, fmt.Errorf("工作表解码后的答卷文本不能超过 %d MiB。", maxCSVBytes>>20)
		}
		fields = append(fields, make([]string, column-len(fields))...)
		fields[column-1] = value
	}
}

func xlsxAttr(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if attr.Name.Space == "" && attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func xlsxCoordinate(ref string) (column, row int) {
	if len(ref) < 2 || len(ref) > 10 { // XFD1048576 is the largest worksheet coordinate.
		return 0, 0
	}
	letters := 0
	for letters < len(ref) && ref[letters] >= 'A' && ref[letters] <= 'Z' {
		column = column*26 + int(ref[letters]-'A') + 1
		letters++
	}
	if letters == 0 || letters > 3 || letters == len(ref) || column > 16384 || ref[letters] < '1' || ref[letters] > '9' {
		return 0, 0
	}
	row, err := strconv.Atoi(ref[letters:])
	if err != nil || row > maxXLSXRowNumber {
		return 0, 0
	}
	return column, row
}

// A formula contributes only its saved <v> value. Formula expressions and
// links are never executed or opened.
// https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/working-with-formulas
func (rows *xlsxRows) readCell(kind string) (string, error) {
	var value, inline string
	for {
		child, err := nextXLSXChild(rows.xml)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch {
		case isXLSXElement(child.Name, "v"):
			if err := rows.xml.DecodeElement(&value, &child); err != nil {
				return "", errXLSXXML
			}
			if len(value) > maxXLSXCellBytes {
				return "", errors.New("XLSX 的单元格内容过长。")
			}
		case isXLSXElement(child.Name, "is") && kind == "inlineStr":
			inline, err = readXLSXText(rows.xml)
			if err != nil {
				return "", err
			}
		default:
			if err := rows.xml.Skip(); err != nil {
				return "", errXLSXXML
			}
		}
	}
	if kind == "inlineStr" {
		return inline, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	switch kind {
	case "s":
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(rows.shared) {
			return "", errors.New("XLSX 的共享字符串索引无效。")
		}
		return rows.shared[index], nil
	case "b":
		switch value {
		case "1", "true":
			return "TRUE", nil
		case "0", "false":
			return "FALSE", nil
		default:
			return "", errors.New("XLSX 的布尔单元格值无效。")
		}
	case "", "n", "str", "d", "e":
		return value, nil
	default:
		return "", errors.New("XLSX 包含不支持的单元格类型。")
	}
}
