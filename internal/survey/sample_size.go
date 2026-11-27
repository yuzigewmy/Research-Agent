package survey

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// SampleSizeMethodCochran identifies the fixed Cochran proportion sample-size formula with optional FPC and response-rate adjustment.
	SampleSizeMethodCochran = "proportion-cochran-v1"

	zScore90 = 1.6448536269514722
	zScore95 = 1.959963984540054
	zScore99 = 2.5758293035489004

	maxPopulationSize       = 1_000_000_000
	maxCalculatedSampleSize = 1_000_000_000

	maxPlanTitleBytes      = 200
	maxPlanObjectiveBytes  = 1000
	maxPlanPopulationBytes = 500
	maxPlanSamplingBytes   = 200
	maxPlanMethodCount     = 12
	maxPlanMethodBytes     = 64
	maxPlanMissingBytes    = 500
	maxPlanNotesBytes      = 2000
)

// SampleSizeInput holds the deterministic parameters for proportion sample-size estimation.
type SampleSizeInput struct {
	Method             string  `json:"method"`
	ConfidenceLevel    int     `json:"confidenceLevel"`
	ExpectedProportion float64 `json:"expectedProportion"`
	MarginOfError      float64 `json:"marginOfError"`
	PopulationSize     *int    `json:"populationSize,omitempty"`
	ResponseRate       float64 `json:"responseRate"`
}

// SampleSizeResult holds the persisted calculation outputs and human-readable formula summary.
type SampleSizeResult struct {
	Method                   string  `json:"method"`
	ZScore                   float64 `json:"zScore"`
	UnadjustedSampleSize     int     `json:"unadjustedSampleSize"`
	FinitePopulationAdjusted int     `json:"finitePopulationAdjusted"`
	PlannedInvitations       int     `json:"plannedInvitations"`
	FormulaSummary           string  `json:"formulaSummary"`
}

// AnalysisPlanSpec is the normalized, versioned survey sample-size and analysis plan payload.
type AnalysisPlanSpec struct {
	Title            string           `json:"title"`
	Objective        string           `json:"objective"`
	TargetPopulation string           `json:"targetPopulation"`
	SamplingMethod   string           `json:"samplingMethod"`
	PlannedMethods   []string         `json:"plannedMethods"`
	MissingStrategy  string           `json:"missingStrategy"`
	Notes            string           `json:"notes,omitempty"`
	SampleSizeInput  SampleSizeInput  `json:"sampleSizeInput"`
	SampleSizeResult SampleSizeResult `json:"sampleSizeResult"`
}

// AnalysisPlanSource records the fixed versioned plan snapshot bound to a survey report.
type AnalysisPlanSource struct {
	PlanID                   string   `json:"planId"`
	Version                  int      `json:"version"`
	Title                    string   `json:"title"`
	Objective                string   `json:"objective"`
	Method                   string   `json:"method"`
	ConfidenceLevel          int      `json:"confidenceLevel"`
	UnadjustedSampleSize     int      `json:"unadjustedSampleSize"`
	FinitePopulationAdjusted int      `json:"finitePopulationAdjusted"`
	PlannedInvitations       int      `json:"plannedInvitations"`
	FormulaSummary           string   `json:"formulaSummary"`
	PlannedMethods           []string `json:"plannedMethods"`
	CreatedAt                string   `json:"createdAt"`
}

// DefaultSampleSizeInput returns the standard confirmed baseline sample-size parameters.
func DefaultSampleSizeInput() SampleSizeInput {
	return SampleSizeInput{
		Method:             SampleSizeMethodCochran,
		ConfidenceLevel:    95,
		ExpectedProportion: 0.5,
		MarginOfError:      0.05,
		ResponseRate:       1.0,
	}
}

// CalculateSampleSize validates inputs and computes the Cochran proportion sample size
// with optional finite population correction and response-rate inflation.
func CalculateSampleSize(input SampleSizeInput) (SampleSizeInput, SampleSizeResult, error) {
	method := strings.TrimSpace(input.Method)
	if method == "" {
		method = SampleSizeMethodCochran
	}
	if method != SampleSizeMethodCochran {
		return SampleSizeInput{}, SampleSizeResult{}, fmt.Errorf("不支持的样本量计算口径 %q，仅支持 %s。", method, SampleSizeMethodCochran)
	}
	z, ok := confidenceZScore(input.ConfidenceLevel)
	if !ok {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("置信水平仅支持 90%、95% 或 99%。")
	}
	p := input.ExpectedProportion
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p >= 1 {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("预期比例 p 须为大于 0 且小于 1 的有限数值。")
	}
	e := input.MarginOfError
	if math.IsNaN(e) || math.IsInf(e, 0) || e <= 0 || e >= 0.5 {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("允许误差 e 须为大于 0 且小于 0.5 的有限数值。")
	}
	var pop *int
	if input.PopulationSize != nil {
		n := *input.PopulationSize
		if n < 0 || n > maxPopulationSize {
			return SampleSizeInput{}, SampleSizeResult{}, fmt.Errorf("总体规模 N 若指定则须为 1 至 %d 的整数，留空或 0 表示无限总体。", maxPopulationSize)
		}
		if n > 0 {
			value := n
			pop = &value
		}
	}
	r := input.ResponseRate
	if math.IsNaN(r) || math.IsInf(r, 0) || r <= 0 || r > 1 {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("预期有效回收率 r 须为大于 0 且不超过 1 的有限数值。")
	}

	n0Raw := z * z * p * (1 - p) / (e * e)
	if math.IsNaN(n0Raw) || math.IsInf(n0Raw, 0) || n0Raw <= 0 {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("基础样本量计算结果超出有效范围。")
	}
	unadjusted, err := ceilSampleCount(n0Raw)
	if err != nil {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("基础样本量计算结果超出系统上限。")
	}

	fpcAdjusted := unadjusted
	if pop != nil {
		n := float64(*pop)
		nFPCRaw := n0Raw / (1 + (n0Raw-1)/n)
		fpcAdjusted, err = ceilSampleCount(nFPCRaw)
		if err != nil {
			return SampleSizeInput{}, SampleSizeResult{}, errors.New("有限总体校正样本量超出系统上限。")
		}
	}

	planned, err := ceilSampleCount(float64(fpcAdjusted) / r)
	if err != nil {
		return SampleSizeInput{}, SampleSizeResult{}, errors.New("计划发放量超出系统上限。")
	}

	normalized := SampleSizeInput{
		Method:             method,
		ConfidenceLevel:    input.ConfidenceLevel,
		ExpectedProportion: p,
		MarginOfError:      e,
		PopulationSize:     pop,
		ResponseRate:       r,
	}
	result := SampleSizeResult{
		Method:                   method,
		ZScore:                   z,
		UnadjustedSampleSize:     unadjusted,
		FinitePopulationAdjusted: fpcAdjusted,
		PlannedInvitations:       planned,
		FormulaSummary:           formatSampleSizeSummary(normalized, z, unadjusted, fpcAdjusted, planned),
	}
	return normalized, result, nil
}

// BuildAnalysisPlanSpec validates and normalizes plan metadata and computes the sample size.
func BuildAnalysisPlanSpec(input AnalysisPlanSpec) (AnalysisPlanSpec, error) {
	title, err := validatePlanText(input.Title, "计划标题", true, maxPlanTitleBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	objective, err := validatePlanText(input.Objective, "研究目标", true, maxPlanObjectiveBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	targetPopulation, err := validatePlanText(input.TargetPopulation, "目标总体", true, maxPlanPopulationBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	samplingMethod, err := validatePlanText(input.SamplingMethod, "抽样方式", true, maxPlanSamplingBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	missingStrategy, err := validatePlanText(input.MissingStrategy, "缺失值处理策略", true, maxPlanMissingBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	notes, err := validatePlanText(input.Notes, "补充说明", false, maxPlanNotesBytes)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	methods, err := normalizePlannedMethods(input.PlannedMethods)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	sizeInput, sizeResult, err := CalculateSampleSize(input.SampleSizeInput)
	if err != nil {
		return AnalysisPlanSpec{}, err
	}
	return AnalysisPlanSpec{
		Title:            title,
		Objective:        objective,
		TargetPopulation: targetPopulation,
		SamplingMethod:   samplingMethod,
		PlannedMethods:   methods,
		MissingStrategy:  missingStrategy,
		Notes:            notes,
		SampleSizeInput:  sizeInput,
		SampleSizeResult: sizeResult,
	}, nil
}

func confidenceZScore(level int) (float64, bool) {
	switch level {
	case 90:
		return zScore90, true
	case 95:
		return zScore95, true
	case 99:
		return zScore99, true
	default:
		return 0, false
	}
}

func ceilSampleCount(value float64) (int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, errors.New("invalid sample size")
	}
	rounded := math.Round(value)
	var ceiled float64
	if math.Abs(value-rounded) <= 1e-12 {
		ceiled = rounded
	} else {
		ceiled = math.Ceil(value)
	}
	if ceiled < 1 {
		ceiled = 1
	}
	if ceiled > maxCalculatedSampleSize {
		return 0, errors.New("sample size overflow")
	}
	return int(ceiled), nil
}

func formatSampleSizeSummary(input SampleSizeInput, z float64, unadjusted, fpcAdjusted, planned int) string {
	pText := strconv.FormatFloat(input.ExpectedProportion, 'f', -1, 64)
	eText := strconv.FormatFloat(input.MarginOfError, 'f', -1, 64)
	rText := strconv.FormatFloat(input.ResponseRate, 'f', -1, 64)
	rPct := strconv.FormatFloat(math.Round(input.ResponseRate*10000)/100, 'f', -1, 64)
	base := fmt.Sprintf(
		"Cochran 比例抽样口径（%s）：置信水平 %d%%（z = %.4f），预期比例 p = %s，允许误差 e = %s，基础样本量 n₀ = ⌈z²·p·(1−p)/e²⌉ = %d",
		input.Method, input.ConfidenceLevel, z, pText, eText, unadjusted,
	)
	if input.PopulationSize != nil {
		return fmt.Sprintf(
			"%s；有限总体 N = %d，校正样本量 n_fpc = ⌈n₀,raw / (1 + (n₀,raw − 1) / N)⌉ = %d；预期有效回收率 %s%%（r = %s），计划发放量 = ⌈%d / %s⌉ = %d。",
			base, *input.PopulationSize, fpcAdjusted, rPct, rText, fpcAdjusted, rText, planned,
		)
	}
	return fmt.Sprintf(
		"%s；按无限总体计算（n_fpc = %d）；预期有效回收率 %s%%（r = %s），计划发放量 = ⌈%d / %s⌉ = %d。",
		base, fpcAdjusted, rPct, rText, fpcAdjusted, rText, planned,
	)
}

func validatePlanText(raw, label string, required bool, maxBytes int) (string, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("%s须为有效 UTF-8 文本，且不能包含空字符。", label)
	}
	text := strings.TrimSpace(raw)
	if required && text == "" {
		return "", fmt.Errorf("请填写%s。", label)
	}
	if len(text) > maxBytes {
		return "", fmt.Errorf("%s最多支持 %d 个 UTF-8 字节。", label, maxBytes)
	}
	return text, nil
}

func normalizePlannedMethods(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("请至少选择一项计划统计方法。")
	}
	methods := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		text, err := validatePlanText(item, "计划统计方法", true, maxPlanMethodBytes)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[text]; dup {
			continue
		}
		if len(methods) >= maxPlanMethodCount {
			return nil, fmt.Errorf("计划统计方法最多支持 %d 项。", maxPlanMethodCount)
		}
		seen[text] = struct{}{}
		methods = append(methods, text)
	}
	if len(methods) == 0 {
		return nil, errors.New("请至少选择一项计划统计方法。")
	}
	return methods, nil
}
