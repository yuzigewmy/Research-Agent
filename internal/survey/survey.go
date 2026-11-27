// Package survey cleans survey responses and computes statistics from saved tables.
package survey

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxResponses    = 10000
	maxColumns      = 128
	maxColumnBytes  = 256
	maxReviewBytes  = 256
	maxCSVBytes     = 16 << 20
	qualityColumn   = "_quality_status"
	syntheticColumn = "__synthetic__"
	StatusValid     = "VALID"
	StatusInvalid   = "INVALID"
	StatusPending   = "PENDING"
)

// Options selects CSV columns for quality checks and statistics and applies
// pending-row reviews. Empty required and duplicate column lists disable their
// respective checks. Column names are matched case-insensitively after trimming.
type Options struct {
	RequiredColumns    []string                   `json:"requiredColumns,omitempty"`
	DuplicateColumns   []string                   `json:"duplicateColumns,omitempty"`
	Reviews            []Review                   `json:"reviews,omitempty"`
	CrossTab           *CrossTabOptions           `json:"crossTab,omitempty"`
	Reliability        *ReliabilityOptions        `json:"reliability,omitempty"`
	Regression         *RegressionOptions         `json:"regression,omitempty"`
	MultipleRegression *MultipleRegressionOptions `json:"multipleRegression,omitempty"`
	FactorAnalysis     *FactorAnalysisOptions     `json:"factorAnalysis,omitempty"`
	Kano               *KanoOptions               `json:"kano,omitempty"`
	PSM                *PSMOptions                `json:"psm,omitempty"`
	Conjoint           *ConjointOptions           `json:"conjoint,omitempty"`
	CFA                *CFAOptions                `json:"cfa,omitempty"`
	Sheet              string                     `json:"sheet,omitempty"`
	WeightColumn       string                     `json:"weightColumn,omitempty"`
	CategoryMappings   []CategoryMapping          `json:"categoryMappings,omitempty"`
	SettingsSource     *SettingsSource            `json:"settingsSource,omitempty"`
}

// SettingsSource records the historical report and confirmed column pairs used to transfer settings.
type SettingsSource struct {
	AnalysisID string      `json:"analysisId"`
	Fields     []FieldPair `json:"fields"`
}

type FieldPair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Review resolves a pending response by its one-based record number after the header.
// A rejection sets Accept to false. Reason must be nonblank.
type Review struct {
	Row    int    `json:"row"`
	Accept bool   `json:"accept"`
	Reason string `json:"reason"`
}

// Report gives the final quality counts and requested statistics of valid rows.
// Demo marks a CSV with a __synthetic__ column, regardless of its contents.
type Report struct {
	Columns            []string            `json:"columns"`
	Total              int                 `json:"total"`
	Valid              int                 `json:"valid"`
	Invalid            int                 `json:"invalid"`
	Pending            int                 `json:"pending"`
	Rows               []Row               `json:"rows"`
	Summaries          []Summary           `json:"summaries"`
	CrossTab           *CrossTab           `json:"crossTab,omitempty"`
	Reliability        *Reliability        `json:"reliability,omitempty"`
	Regression         *Regression         `json:"regression,omitempty"`
	MultipleRegression *MultipleRegression `json:"multipleRegression,omitempty"`
	FactorAnalysis     *FactorAnalysis     `json:"factorAnalysis,omitempty"`
	Kano               *Kano               `json:"kano,omitempty"`
	PSM                *PSM                `json:"psm,omitempty"`
	Conjoint           *Conjoint           `json:"conjoint,omitempty"`
	CFA                *CFA                `json:"cfa,omitempty"`
	Weighting          *Weighting          `json:"weighting,omitempty"`
	Demo               bool                `json:"demo"`
}

// Row gives one response's quality status and reasons in evaluation order.
type Row struct {
	Row       int      `json:"row"`
	SourceRow int      `json:"sourceRow,omitempty"`
	Status    string   `json:"status"`
	Reasons   []string `json:"reasons"`
}

type readResponse func() (fields []string, sourceRow int, err error)

// Analyze reads UTF-8, comma-separated CSV with a header. Short records have
// blank trailing fields. Blank lines are skipped and quoted newlines belong to
// the same record. Duplicate keys compare trimmed, case-sensitive field values
// in the selected columns, retaining the first physical match before review.
// CSV uploads can contain up to 10,000 responses, 128 columns and 16 MiB.
// An input or review error returns no partial report.
func Analyze(r io.Reader, options Options) (Report, error) {
	if options.Sheet != "" {
		return Report{}, errors.New("CSV 资料没有工作表。")
	}
	limited := &io.LimitedReader{R: r, N: maxCSVBytes + 1}
	buffer := bufio.NewReader(limited)
	prefix, err := buffer.Peek(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("CSV header: %w", err)
	}
	if string(prefix) == "\xef\xbb\xbf" {
		_, _ = buffer.Discard(3)
	}
	reader := csv.NewReader(buffer)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if limited.N == 0 {
		return Report{}, csvSizeError()
	}
	if errors.Is(err, io.EOF) {
		return Report{}, errors.New("CSV header is required")
	}
	if err != nil {
		return Report{}, fmt.Errorf("CSV header: %w", err)
	}
	reader.ReuseRecord = true
	return analyzeResponses(header, func() ([]string, int, error) {
		fields, err := reader.Read()
		if limited.N == 0 {
			return nil, 0, csvSizeError()
		}
		return fields, 0, err
	}, options)
}

func analyzeResponses(header []string, read readResponse, options Options) (Report, error) {
	if len(header) > maxColumns {
		return Report{}, fmt.Errorf("答卷表最多支持 %d 列。", maxColumns)
	}
	byName := make(map[string]int, len(header))
	accumulators := make([]columnSummary, 0, len(header))
	for i, field := range header {
		name := strings.TrimSpace(field)
		if name == "" || len(name) > maxColumnBytes || !utf8.ValidString(name) {
			return Report{}, fmt.Errorf("第 %d 列需要非空列名，最多 %d 个 UTF-8 字节。", i+1, maxColumnBytes)
		}
		key := strings.ToLower(name)
		if _, exists := byName[key]; exists {
			return Report{}, fmt.Errorf("第 %d 列的列名与前面的列重复。", i+1)
		}
		header[i], byName[key] = name, i
		if !excludeSummary(key) {
			accumulators = append(accumulators, columnSummary{index: i, Summary: Summary{Column: name}})
		}
	}
	mappings, err := compileCategoryMappings(options.CategoryMappings, byName)
	if err != nil {
		return Report{}, err
	}
	required, err := selectColumns(byName, "requiredColumns", options.RequiredColumns)
	if err != nil {
		return Report{}, err
	}
	duplicates, err := selectColumns(byName, "duplicateColumns", options.DuplicateColumns)
	if err != nil {
		return Report{}, err
	}
	reviews, err := collectReviews(options.Reviews)
	if err != nil {
		return Report{}, err
	}
	var crossTab *crossTabAccumulator
	if options.CrossTab != nil {
		crossTab, err = newCrossTab(*options.CrossTab, header, byName, options.WeightColumn != "")
		if err != nil {
			return Report{}, err
		}
	}
	var reliability *reliabilityAccumulator
	if options.Reliability != nil {
		reliability, err = newReliability(*options.Reliability, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var regression *regressionAccumulator
	if options.Regression != nil {
		regression, err = newRegression(*options.Regression, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var multipleRegression *multipleRegressionAccumulator
	if options.MultipleRegression != nil {
		multipleRegression, err = newMultipleRegression(*options.MultipleRegression, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var factorAnalysis *factorAnalysisAccumulator
	if options.FactorAnalysis != nil {
		factorAnalysis, err = newFactorAnalysis(*options.FactorAnalysis, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var kano *kanoAccumulator
	if options.Kano != nil {
		kano, err = newKano(*options.Kano, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var psm *psmAccumulator
	if options.PSM != nil {
		psm, err = newPSM(*options.PSM, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var conjoint *conjointAccumulator
	if options.Conjoint != nil {
		conjoint, err = newConjoint(*options.Conjoint, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var cfa *cfaAccumulator
	if options.CFA != nil {
		cfa, err = newCFA(*options.CFA, header, byName)
		if err != nil {
			return Report{}, err
		}
	}
	var weights *weightAccumulator
	if options.WeightColumn != "" {
		key := strings.ToLower(strings.TrimSpace(options.WeightColumn))
		index, found := byName[key]
		if !found || excludeSummary(key) {
			return Report{}, errors.New("请选择答卷表中的有效权重列。")
		}
		weights = &weightAccumulator{index: index, Weighting: Weighting{Column: header[index]}}
	}
	statusIndex, hasStatus := byName[qualityColumn]
	_, demo := byName[syntheticColumn]
	report := Report{Columns: header, Rows: []Row{}, Summaries: make([]Summary, 0, len(accumulators)), Demo: demo}
	seen := make(map[string]int)
	for {
		record, sourceRow, err := read()
		if err == io.EOF {
			break
		}
		row := report.Total + 1
		if err != nil {
			return Report{}, fmt.Errorf("读取第 %d 条答卷失败。%w", row, err)
		}
		if row > maxResponses {
			return Report{}, fmt.Errorf("最多可分析 %d 条答卷。", maxResponses)
		}
		if len(record) > len(header) {
			return Report{}, fmt.Errorf("第 %d 条答卷的列数超过表头。", row)
		}
		for i, value := range record {
			if !utf8.ValidString(value) {
				return Report{}, fmt.Errorf("第 %d 条答卷包含无效的 UTF-8 字符。", row)
			}
			record[i] = strings.TrimSpace(value)
		}

		result := Row{Row: row, SourceRow: sourceRow, Status: StatusValid, Reasons: []string{}}
		if hasStatus {
			switch strings.ToLower(fieldAt(record, statusIndex)) {
			case "held", "hold", "pending":
				result.Status = StatusPending
				result.Reasons = append(result.Reasons, "源数据标记为待复核")
			case "invalid":
				result.Status = StatusInvalid
				result.Reasons = append(result.Reasons, "源数据标记为无效")
			}
		}
		missing := 0
		for _, i := range required {
			if fieldAt(record, i) == "" {
				missing++
			}
		}
		if missing*5 > len(required) {
			result.Status = StatusInvalid
			result.Reasons = append(result.Reasons, fmt.Sprintf("缺失 %d 个必答字段（共 %d 个），缺失率超过 20%%", missing, len(required)))
		}
		if len(duplicates) > 0 {
			key := duplicateKey(record, duplicates)
			if first, found := seen[key]; found {
				result.Status = StatusInvalid
				result.Reasons = append(result.Reasons, fmt.Sprintf("与第 %d 条回答重复", first))
			} else {
				seen[key] = row
			}
		}
		if review, exists := reviews[row]; exists {
			if result.Status != StatusPending {
				return Report{}, fmt.Errorf("review for row %d requires a pending response", row)
			}
			result.Status = StatusInvalid
			if review.Accept {
				result.Status = StatusValid
			}
			result.Reasons = append(result.Reasons, review.Reason)
		}
		switch result.Status {
		case StatusValid:
			report.Valid++
			var weight uint64
			if weights != nil {
				weight = weights.add(record)
			}
			if crossTab != nil {
				if err := crossTab.add(record, weight, mappings); err != nil {
					return Report{}, err
				}
			}
			if reliability != nil {
				reliability.add(record)
			}
			if regression != nil {
				regression.add(record)
			}
			if multipleRegression != nil {
				multipleRegression.add(record)
			}
			if factorAnalysis != nil {
				factorAnalysis.add(record)
			}
			if kano != nil {
				kano.add(record, mappings)
			}
			if psm != nil {
				psm.add(record)
			}
			if conjoint != nil {
				conjoint.add(record, mappings)
			}
			if cfa != nil {
				cfa.add(record)
			}
			for i := range accumulators {
				accumulators[i].add(record, weight, mappings)
			}
		case StatusInvalid:
			report.Invalid++
		case StatusPending:
			report.Pending++
		}
		report.Total = row
		report.Rows = append(report.Rows, result)
	}
	for _, review := range options.Reviews {
		if review.Row > report.Total {
			return Report{}, fmt.Errorf("第 %d 条答卷不存在，无法应用复核决定。", review.Row)
		}
	}
	for i := range accumulators {
		report.Summaries = append(report.Summaries, accumulators[i].finish(report.Valid))
	}
	if crossTab != nil {
		report.CrossTab = crossTab.finish()
	}
	if reliability != nil {
		report.Reliability = reliability.finish()
	}
	if regression != nil {
		report.Regression = regression.finish()
	}
	if multipleRegression != nil {
		report.MultipleRegression = multipleRegression.finish()
	}
	if factorAnalysis != nil {
		report.FactorAnalysis = factorAnalysis.finish()
	}
	if kano != nil {
		report.Kano = kano.finish()
	}
	if psm != nil {
		report.PSM = psm.finish()
	}
	if conjoint != nil {
		report.Conjoint = conjoint.finish()
	}
	if cfa != nil {
		report.CFA = cfa.finish()
	}
	if weights != nil {
		report.Weighting = weights.finish()
	}
	return report, nil
}

func csvSizeError() error {
	return fmt.Errorf("CSV exceeds %d bytes", maxCSVBytes)
}

func selectColumns(byName map[string]int, option string, names []string) ([]int, error) {
	if len(names) > maxColumns {
		return nil, fmt.Errorf("%s exceeds %d columns", option, maxColumns)
	}
	indices := make([]int, 0, len(names))
	used := make(map[int]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if len(name) > maxColumnBytes || !utf8.ValidString(name) {
			return nil, fmt.Errorf("%s contains an invalid column name", option)
		}
		i, exists := byName[strings.ToLower(name)]
		if !exists {
			return nil, fmt.Errorf("%s refers to unknown column %q", option, name)
		}
		if !used[i] {
			indices = append(indices, i)
			used[i] = true
		}
	}
	return indices, nil
}

func collectReviews(input []Review) (map[int]Review, error) {
	if len(input) > maxResponses {
		return nil, fmt.Errorf("reviews exceed %d entries", maxResponses)
	}
	reviews := make(map[int]Review, len(input))
	for _, review := range input {
		if review.Row < 1 || review.Row > maxResponses {
			return nil, fmt.Errorf("review row %d must be between 1 and %d", review.Row, maxResponses)
		}
		if _, exists := reviews[review.Row]; exists {
			return nil, fmt.Errorf("row %d has more than one review", review.Row)
		}
		review.Reason = strings.TrimSpace(review.Reason)
		if review.Reason == "" || len(review.Reason) > maxReviewBytes || !utf8.ValidString(review.Reason) {
			return nil, fmt.Errorf("review for row %d requires a nonblank UTF-8 reason of at most %d bytes", review.Row, maxReviewBytes)
		}
		reviews[review.Row] = review
	}
	return reviews, nil
}

func fieldAt(record []string, i int) string {
	if i >= len(record) {
		return ""
	}
	return record[i]
}

func duplicateKey(record []string, indices []int) string {
	var key strings.Builder
	for _, i := range indices {
		value := fieldAt(record, i)
		// Length prefixes separate fields even when answers contain delimiters.
		key.WriteString(strconv.Itoa(len(value)))
		key.WriteByte(':')
		key.WriteString(value)
	}
	return key.String()
}
