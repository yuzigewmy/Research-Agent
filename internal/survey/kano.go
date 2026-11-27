package survey

import (
	"errors"
	"math"
	"strings"
)

var errKanoColumns = errors.New("Kano 模型分析请选择两个不同的可统计字段（正向题与负向题）。")

// KanoOptions selects the functional (positive) and dysfunctional (negative) columns.
type KanoOptions struct {
	FunctionalColumn    string `json:"functionalColumn"`
	DysfunctionalColumn string `json:"dysfunctionalColumn"`
}

// KanoCategory holds count and proportion for one Kano classification code.
type KanoCategory struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Count int     `json:"count"`
	Share float64 `json:"share"`
}

// KanoResult summarizes the Kano classification distribution and Better-Worse indices.
type KanoResult struct {
	DominantCode  string         `json:"dominantCode"`
	DominantLabel string         `json:"dominantLabel"`
	Categories    []KanoCategory `json:"categories"`
	Better        *float64       `json:"better,omitempty"`
	Worse         *float64       `json:"worse,omitempty"`
}

// Kano reports Kano model counts, dominant attribute type, and Better/Worse scores.
type Kano struct {
	KanoOptions
	Included          int         `json:"included"`
	Missing           int         `json:"missing"`
	Invalid           int         `json:"invalid"`
	Result            *KanoResult `json:"result,omitempty"`
	UnavailableReason string      `json:"unavailableReason,omitempty"`
}

type kanoAccumulator struct {
	Kano
	funcIndex, dysIndex int
	counts              [6]int // M, O, A, I, R, Q
}

var kanoCodes = [6]struct {
	code  string
	label string
}{
	{"M", "必备属性"},
	{"O", "期望属性"},
	{"A", "魅力属性"},
	{"I", "无差异属性"},
	{"R", "反向属性"},
	{"Q", "可疑结果"},
}

// Classic 5x5 Kano evaluation matrix (rows: functional 1..5, cols: dysfunctional 1..5).
// Indices map into kanoCodes: 0=M, 1=O, 2=A, 3=I, 4=R, 5=Q.
var kanoMatrix = [5][5]int{
	{5, 2, 2, 2, 1}, // Functional: 1 (很喜欢)
	{4, 3, 3, 3, 0}, // Functional: 2 (理所当然)
	{4, 3, 3, 3, 0}, // Functional: 3 (无所谓)
	{4, 3, 3, 3, 0}, // Functional: 4 (可以忍受)
	{4, 4, 4, 4, 5}, // Functional: 5 (不喜欢)
}

func newKano(options KanoOptions, header []string, byName map[string]int) (*kanoAccumulator, error) {
	fKey := strings.ToLower(strings.TrimSpace(options.FunctionalColumn))
	dKey := strings.ToLower(strings.TrimSpace(options.DysfunctionalColumn))
	fIdx, hasF := byName[fKey]
	dIdx, hasD := byName[dKey]
	if !hasF || !hasD || fIdx == dIdx || excludeSummary(fKey) || excludeSummary(dKey) {
		return nil, errKanoColumns
	}
	return &kanoAccumulator{
		Kano: Kano{
			KanoOptions: KanoOptions{
				FunctionalColumn:    header[fIdx],
				DysfunctionalColumn: header[dIdx],
			},
		},
		funcIndex: fIdx,
		dysIndex:  dIdx,
	}, nil
}

func (a *kanoAccumulator) add(record []string, mappings categoryMappings) {
	fText := mappings.label(a.funcIndex, fieldAt(record, a.funcIndex))
	dText := mappings.label(a.dysIndex, fieldAt(record, a.dysIndex))
	if fText == "" || dText == "" {
		a.Missing++
		return
	}
	fLevel, fOK := parseKanoLevel(fText)
	dLevel, dOK := parseKanoLevel(dText)
	if !fOK || !dOK {
		a.Invalid++
		return
	}
	a.Included++
	codeIdx := kanoMatrix[fLevel-1][dLevel-1]
	a.counts[codeIdx]++
}

func parseKanoLevel(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	if num, ok := parseFiniteNumber(trimmed); ok {
		rounded := math.Round(num)
		if math.Abs(num-rounded) < 1e-9 && rounded >= 1 && rounded <= 5 {
			return int(rounded), true
		}
		return 0, false
	}
	switch strings.ToLower(trimmed) {
	case "很喜欢", "喜欢", "非常喜欢", "满意", "很满意", "like", "i like it":
		return 1, true
	case "理所当然", "应该如此", "理应如此", "必须如此", "本该如此", "expect", "must-be", "must be", "i expect it":
		return 2, true
	case "无所谓", "中立", "一般", "不在乎", "neutral", "i am neutral":
		return 3, true
	case "可以忍受", "勉强接受", "可忍受", "能够忍受", "容忍", "live with", "tolerate", "i can live with it":
		return 4, true
	case "不喜欢", "很不喜欢", "非常不喜欢", "不满意", "很不满意", "dislike", "i dislike it":
		return 5, true
	default:
		return 0, false
	}
}

func (a *kanoAccumulator) finish() *Kano {
	result := &a.Kano
	if a.Included == 0 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}
	total := float64(a.Included)
	categories := make([]KanoCategory, 6)
	dominantIdx := 0
	for i, spec := range kanoCodes {
		c := a.counts[i]
		categories[i] = KanoCategory{
			Code:  spec.code,
			Label: spec.label,
			Count: c,
			Share: float64(c) / total,
		}
		// Tie-breaking follows standard Kano priority: M > O > A > I > R > Q.
		if c > a.counts[dominantIdx] {
			dominantIdx = i
		}
	}
	res := &KanoResult{
		DominantCode:  kanoCodes[dominantIdx].code,
		DominantLabel: kanoCodes[dominantIdx].label,
		Categories:    categories,
	}
	m, o, attr, indiff := float64(a.counts[0]), float64(a.counts[1]), float64(a.counts[2]), float64(a.counts[3])
	denom := attr + o + m + indiff
	if denom > 0 {
		better := (attr + o) / denom
		worse := -(o + m) / denom
		res.Better = &better
		res.Worse = &worse
	}
	result.Result = res
	return result
}
