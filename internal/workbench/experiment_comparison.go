package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	experimentComparisonTask   = "experiment-comparison"
	experimentComparisonMethod = "paired-mean-v1"
	maxExperimentRows          = 10_000
	maxExperimentMetricBytes   = 128
)

const experimentComparisonSchema = `
	ALTER TABLE tasks RENAME COLUMN kind TO previous_kind;
	ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT ''
		CHECK (kind IN ('', 'manuscript-check', 'focused-research', 'experiment-comparison'));
	UPDATE tasks SET kind = previous_kind WHERE previous_kind <> '';
	ALTER TABLE tasks DROP COLUMN previous_kind;
	ALTER TABLE tasks ADD COLUMN result_json TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 51;
`

var (
	errExperimentInput = errors.New("请选择本项目中一份已保存的 CSV 数据。")
	decimalMetric      = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?0*[0-9]{1,3})?$`)
	maxMetric, _       = new(big.Rat).SetString("1e100")
)

type experimentDataError string

func (e experimentDataError) Error() string { return string(e) }

type experimentMetrics struct {
	PairedRows     int         `json:"pairedRows"`
	SkippedRows    int         `json:"skippedRows"`
	BaselineMean   json.Number `json:"baselineMean"`
	VariantMean    json.Number `json:"variantMean"`
	MeanDifference json.Number `json:"meanDifference"`
}

func experimentCSVPath(name string) bool {
	return strings.EqualFold(path.Ext(name), ".csv")
}

func parseExperimentMetric(text string) *big.Rat {
	if len(text) > maxExperimentMetricBytes || !decimalMetric.MatchString(text) {
		return nil
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil
	}
	if new(big.Rat).Abs(value).Cmp(maxMetric) > 0 {
		return nil
	}
	return value
}

func experimentMean(sum, count *big.Rat) json.Number {
	mean := new(big.Rat).Quo(sum, count)
	// Keep 34 significant digits in the JSON, including exponents outside float64.
	return json.Number(new(big.Float).SetPrec(256).SetRat(mean).Text('g', 34))
}

type experimentRow struct {
	Line     int    `json:"line"`
	Baseline string `json:"baseline,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Missing  string `json:"missing,omitempty"`
}

type experimentComparisonReport struct {
	Method  string            `json:"method"`
	Input   taskInput         `json:"input"`
	Metrics experimentMetrics `json:"metrics"`
}

func scanExperimentRows(ctx context.Context, source string, visit func(experimentRow, *big.Rat, *big.Rat)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return experimentDataError("请选择不含空字符的 UTF-8 CSV 文件。")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(source, "\ufeff")))
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return experimentDataError("无法读取 CSV 表头。请核对逗号分隔和引号。")
	}
	baseline, variant := -1, -1
	for index, name := range header {
		switch strings.TrimSpace(name) {
		case "baseline":
			if baseline != -1 {
				return experimentDataError("表头中的 baseline 列重复。")
			}
			baseline = index
		case "variant":
			if variant != -1 {
				return experimentDataError("表头中的 variant 列重复。")
			}
			variant = index
		}
	}
	if baseline == -1 || variant == -1 {
		return experimentDataError("CSV 表头必须包含 baseline 和 variant 两列。")
	}
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				return experimentDataError(fmt.Sprintf("CSV 第 %d 行的分隔格式或列数有误。", parse.StartLine))
			}
			return err
		}
		if index == maxExperimentRows {
			return experimentDataError("数据超过 10,000 行。请缩小输入范围后重新提交。")
		}
		line, _ := reader.FieldPos(0)
		base := strings.TrimSpace(row[baseline])
		alternative := strings.TrimSpace(row[variant])
		if base == "" || alternative == "" {
			missing := "variant"
			if base == "" {
				missing = "baseline"
				if alternative == "" {
					missing = "both"
				}
			}
			visit(experimentRow{Line: line, Missing: missing}, nil, nil)
			continue
		}
		baselineValue := parseExperimentMetric(base)
		variantValue := parseExperimentMetric(alternative)
		if baselineValue == nil || variantValue == nil {
			return experimentDataError(fmt.Sprintf("CSV 第 %d 行的指标须为十进制数。单个值最多 %d 字节，绝对值不超过 1e100，指数的绝对值不超过 999。", line, maxExperimentMetricBytes))
		}
		visit(experimentRow{Line: line, Baseline: base, Variant: alternative}, baselineValue, variantValue)
	}
}

func compareExperimentMetrics(ctx context.Context, source string) (experimentMetrics, error) {
	var metrics experimentMetrics
	var bases, variants big.Rat
	err := scanExperimentRows(ctx, source, func(row experimentRow, baseline, variant *big.Rat) {
		if row.Missing != "" {
			metrics.SkippedRows++
			return
		}
		metrics.PairedRows++
		bases.Add(&bases, baseline)
		variants.Add(&variants, variant)
	})
	if err != nil {
		return experimentMetrics{}, err
	}
	if metrics.PairedRows == 0 {
		return experimentMetrics{}, experimentDataError("CSV 中没有完整的基线与方案配对。")
	}
	count := big.NewRat(int64(metrics.PairedRows), 1)
	metrics.BaselineMean = experimentMean(&bases, count)
	metrics.VariantMean = experimentMean(&variants, count)
	// Subtract the exact totals before rounding the two means to JSON numbers.
	metrics.MeanDifference = experimentMean(new(big.Rat).Sub(&variants, &bases), count)
	return metrics, nil
}

func (s *Store) readExperimentSource(ctx context.Context, projectID string, input taskInput) (string, error) {
	return s.readExperimentSourceFrom(ctx, s.db, projectID, input)
}

func (s *Store) readExperimentSourceFrom(ctx context.Context, db workspaceQuery, projectID string, input taskInput) (string, error) {
	file, err := lookupWorkspaceVersion(ctx, db, projectID, input.VersionID)
	if err != nil {
		return "", err
	}
	if file.SHA256 != input.SHA256 || file.Path != input.Path {
		return "", experimentDataError("计算输入与确认的文件版本不一致。")
	}
	source, err := s.openObject(file.SHA256)
	if err != nil {
		return "", err
	}
	defer source.Close()
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, source}, maxWorkspaceText+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxWorkspaceText {
		return "", experimentDataError("计算输入超过 512 KiB。")
	}
	if fmt.Sprintf("%x", sha256.Sum256(body)) != input.SHA256 {
		return "", experimentDataError("计算输入未通过摘要校验。")
	}
	return string(body), nil
}

func (s *Store) runExperimentComparison(ctx context.Context, projectID string, input taskInput) (taskResult, error) {
	source, err := s.readExperimentSource(ctx, projectID, input)
	if err != nil {
		return taskResult{}, err
	}
	metrics, err := compareExperimentMetrics(ctx, source)
	if err != nil {
		return taskResult{}, err
	}
	report, err := json.MarshalIndent(experimentComparisonReport{experimentComparisonMethod, input, metrics}, "", "  ")
	return taskResult{jsonArtifact: string(report)}, err
}
