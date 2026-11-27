package workbench

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	materialTablePreviewMaxRows  = 50
	materialTablePreviewMaxBytes = 2 << 20
)

var (
	errMaterialNotTable = errors.New("material is not a UTF-8 CSV or TSV table")

	// Order matters: match 18-digit CN ID card before bank card and mobile phone.
	reCNIDCard = regexp.MustCompile(`(?i)(^|[^0-9A-Za-z])([1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx])($|[^0-9A-Za-z])`)
	reBankCard = regexp.MustCompile(`(^|[^0-9A-Za-z])(\d{4})(\d{8,11})(\d{4})($|[^0-9A-Za-z])`)
	reCNPhone  = regexp.MustCompile(`(^|[^0-9A-Za-z])(1[3-9]\d)(\d{4})(\d{4})($|[^0-9A-Za-z])`)
	reEmail    = regexp.MustCompile(`([A-Za-z0-9._%+\-]+)@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)

	sensitiveHeaderKeywords = []string{
		"手机", "电话", "身份证", "证件号", "邮箱", "电子邮件", "银行卡", "卡号",
		"email", "e-mail", "phone", "mobile", "tel", "id_card", "idcard", "id_number", "bank_card", "bankcard",
	}
)

type MaterialTablePreview struct {
	Headers          []string   `json:"headers"`
	Rows             [][]string `json:"rows"`
	TotalPreviewRows int        `json:"totalPreviewRows"`
	Truncated        bool       `json:"truncated"`
	MaskedColumns    []string   `json:"maskedColumns"`
	MaskedCellCount  int        `json:"maskedCellCount"`
}

func isTableMaterialName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".csv" || ext == ".tsv"
}

func isSensitiveColumnHeader(header string) bool {
	lower := strings.ToLower(strings.TrimSpace(header))
	if lower == "" {
		return false
	}
	for _, kw := range sensitiveHeaderKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// maskPIIText detects and masks Chinese mobile numbers, 18-digit ID cards,
// emails, and 16-19 digit bank card numbers. Returns masked text and replacement count.
func maskPIIText(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	count := 0
	masked := text
	replaceRepeated := func(re *regexp.Regexp, fn func(string) string) {
		for {
			prev := count
			masked = re.ReplaceAllStringFunc(masked, fn)
			if count == prev {
				break
			}
		}
	}
	replaceRepeated(reCNIDCard, func(match string) string {
		sub := reCNIDCard.FindStringSubmatch(match)
		if len(sub) != 4 {
			return match
		}
		id := sub[2]
		count++
		return sub[1] + id[:6] + "********" + id[len(id)-4:] + sub[3]
	})
	replaceRepeated(reBankCard, func(match string) string {
		sub := reBankCard.FindStringSubmatch(match)
		if len(sub) != 6 {
			return match
		}
		count++
		return sub[1] + sub[2] + strings.Repeat("*", len(sub[3])) + sub[4] + sub[5]
	})
	replaceRepeated(reCNPhone, func(match string) string {
		sub := reCNPhone.FindStringSubmatch(match)
		if len(sub) != 6 {
			return match
		}
		count++
		return sub[1] + sub[2] + "****" + sub[4] + sub[5]
	})
	masked = reEmail.ReplaceAllStringFunc(masked, func(match string) string {
		sub := reEmail.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		local := sub[1]
		r, _ := utf8.DecodeRuneInString(local)
		count++
		return string(r) + "***@" + sub[2]
	})
	return masked, count
}

func maskSensitiveCellFallback(cell string) string {
	trimmed := strings.TrimSpace(cell)
	if trimmed == "" {
		return cell
	}
	runes := []rune(trimmed)
	switch {
	case len(runes) <= 2:
		return "***"
	case len(runes) <= 6:
		return string(runes[:1]) + "***" + string(runes[len(runes)-1:])
	default:
		return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
	}
}

func maskTableCell(cell string, sensitiveHeader bool) (string, bool) {
	if strings.TrimSpace(cell) == "" {
		return cell, false
	}
	masked, hits := maskPIIText(cell)
	if hits > 0 {
		return masked, true
	}
	if sensitiveHeader {
		return maskSensitiveCellFallback(cell), true
	}
	return cell, false
}

// maskTableText masks PII in CSV/TSV text excerpts before sending them into model context.
func maskTableText(text, filename string) string {
	if !isTableMaterialName(filename) || text == "" {
		return text
	}
	trimmed := strings.TrimPrefix(text, "\ufeff")
	reader := csv.NewReader(strings.NewReader(trimmed))
	reader.FieldsPerRecord = -1
	if strings.EqualFold(filepath.Ext(filename), ".tsv") {
		reader.Comma = '\t'
	}
	records, err := reader.ReadAll()
	if err != nil || len(records) == 0 {
		masked, _ := maskPIIText(text)
		return masked
	}
	headers := records[0]
	sensitiveCols := make([]bool, len(headers))
	for i, h := range headers {
		sensitiveCols[i] = isSensitiveColumnHeader(h)
	}
	for r := 1; r < len(records); r++ {
		for c := range records[r] {
			sensitive := c < len(sensitiveCols) && sensitiveCols[c]
			records[r][c], _ = maskTableCell(records[r][c], sensitive)
		}
	}
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	if strings.EqualFold(filepath.Ext(filename), ".tsv") {
		writer.Comma = '\t'
	}
	_ = writer.WriteAll(records)
	writer.Flush()
	if err := writer.Error(); err != nil {
		masked, _ := maskPIIText(text)
		return masked
	}
	return out.String()
}

func (s *Store) previewMaterialTable(material Material) (MaterialTablePreview, error) {
	if !isTableMaterialName(material.Name) {
		return MaterialTablePreview{}, errMaterialNotTable
	}
	file, err := s.openObject(material.SHA256)
	if err != nil {
		return MaterialTablePreview{}, err
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, materialTablePreviewMaxBytes+1))
	if err != nil {
		return MaterialTablePreview{}, err
	}
	byteTruncated := len(raw) > materialTablePreviewMaxBytes
	if byteTruncated {
		end := materialTablePreviewMaxBytes
		for end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
		raw = raw[:end]
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return MaterialTablePreview{}, errMaterialNotTable
	}
	text := string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	if strings.EqualFold(filepath.Ext(material.Name), ".tsv") {
		reader.Comma = '\t'
	}

	headers, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return MaterialTablePreview{
			Headers:       []string{},
			Rows:          [][]string{},
			MaskedColumns: []string{},
		}, nil
	}
	if err != nil {
		return MaterialTablePreview{}, errMaterialNotTable
	}

	sensitiveCols := make([]bool, len(headers))
	maskedColSet := make(map[int]bool)
	for i, h := range headers {
		if isSensitiveColumnHeader(h) {
			sensitiveCols[i] = true
			maskedColSet[i] = true
		}
	}

	rows := make([][]string, 0, materialTablePreviewMaxRows)
	maskedCells := 0
	rowTruncated := byteTruncated
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if byteTruncated {
				// Partial trailing line caused by byte cap.
				rowTruncated = true
				break
			}
			return MaterialTablePreview{}, errMaterialNotTable
		}
		if len(rows) >= materialTablePreviewMaxRows {
			rowTruncated = true
			break
		}
		maskedRow := make([]string, len(record))
		for c, cell := range record {
			sensitive := c < len(sensitiveCols) && sensitiveCols[c]
			next, wasMasked := maskTableCell(cell, sensitive)
			maskedRow[c] = next
			if wasMasked {
				maskedCells++
				maskedColSet[c] = true
			}
		}
		rows = append(rows, maskedRow)
	}

	maskedColumns := make([]string, 0, len(maskedColSet))
	for i, h := range headers {
		if maskedColSet[i] {
			label := strings.TrimSpace(h)
			if label == "" {
				label = "第 " + strconv.Itoa(i+1) + " 列"
			}
			maskedColumns = append(maskedColumns, label)
		}
	}

	return MaterialTablePreview{
		Headers:          headers,
		Rows:             rows,
		TotalPreviewRows: len(rows),
		Truncated:        rowTruncated,
		MaskedColumns:    maskedColumns,
		MaskedCellCount:  maskedCells,
	}, nil
}

func (s *Store) deleteMaterial(ctx context.Context, projectID, materialID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := lookupMaterial(ctx, tx, projectID, materialID); err != nil {
		return err
	}
	statements := []string{
		`DELETE FROM manuscript_pdf_citations WHERE project_id = ? AND link_id IN (
			SELECT link_id FROM manuscript_pdf_links WHERE project_id = ? AND material_id = ?
		)`,
		`DELETE FROM manuscript_pdf_links WHERE project_id = ? AND material_id = ?`,
		`DELETE FROM image_reviews WHERE project_id = ? AND material_id = ?`,
		`UPDATE image_generations SET material_id = NULL WHERE project_id = ? AND material_id = ?`,
		`DELETE FROM survey_runs WHERE project_id = ? AND material_id = ?`,
		`DELETE FROM materials WHERE project_id = ? AND material_id = ?`,
	}
	if _, err := tx.ExecContext(ctx, statements[0], projectID, projectID, materialID); err != nil {
		return err
	}
	for _, stmt := range statements[1:] {
		if _, err := tx.ExecContext(ctx, stmt, projectID, materialID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) handleDeleteMaterial(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	materialID := r.PathValue("materialId")
	err := s.deleteMaterial(r.Context(), projectID, materialID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "materialId": materialID})
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目或资料不存在。")
	default:
		writeStoreError(w, "delete material", err)
	}
}

func (s *Store) handlePreviewMaterialTable(w http.ResponseWriter, r *http.Request) {
	material, err := s.getMaterial(r.Context(), r.PathValue("id"), r.PathValue("materialId"))
	if err != nil {
		writeMaterialError(w, "read material for table preview", err)
		return
	}
	preview, err := s.previewMaterialTable(material)
	if errors.Is(err, errMaterialNotTable) {
		writeError(w, http.StatusUnprocessableEntity, "PREVIEW_UNAVAILABLE", "仅支持 UTF-8 编码的 CSV/TSV 表格脱敏预览。")
		return
	}
	if err != nil {
		writeStoreError(w, "preview material table", err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
