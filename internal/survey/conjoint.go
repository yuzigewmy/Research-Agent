package survey

import (
	"errors"
	"math"
	"slices"
	"strings"
)

const (
	minConjointAttributes = 1
	maxConjointAttributes = 6
	minConjointLevels     = 2
	maxConjointLevels     = 8
)

var errConjointColumns = errors.New("联合分析请选择 1 个评分因变量和 1 至 6 个不同的分类属性字段。")

// ConjointOptions selects a numeric rating column and 1 to 6 categorical attribute columns.
type ConjointOptions struct {
	RatingColumn     string   `json:"ratingColumn"`
	AttributeColumns []string `json:"attributeColumns"`
}

// ConjointLevelUtility holds the centered part-worth utility for one attribute level.
type ConjointLevelUtility struct {
	Level     string  `json:"level"`
	Count     int     `json:"count"`
	PartWorth float64 `json:"partWorth"`
}

// ConjointAttributeResult holds utilities, utility range, and relative importance for one attribute.
type ConjointAttributeResult struct {
	Column            string                 `json:"column"`
	Range             float64                `json:"range"`
	ImportancePercent float64                `json:"importancePercent"`
	Levels            []ConjointLevelUtility `json:"levels"`
}

// ConjointResult holds effects-coded OLS part-worth utilities and model fit.
type ConjointResult struct {
	Intercept  float64                   `json:"intercept"`
	RSquared   *float64                  `json:"rSquared,omitempty"`
	Attributes []ConjointAttributeResult `json:"attributes"`
}

// Conjoint reports conjoint analysis part-worth utilities and attribute importance.
type Conjoint struct {
	ConjointOptions
	Included          int             `json:"included"`
	Missing           int             `json:"missing"`
	NonNumeric        int             `json:"nonNumeric"`
	Result            *ConjointResult `json:"result,omitempty"`
	UnavailableReason string          `json:"unavailableReason,omitempty"`
}

type conjointRow struct {
	rating float64
	levels []string
}

type conjointAccumulator struct {
	Conjoint
	ratingIndex int
	attrIndices []int
	rows        []conjointRow
	levelCounts []map[string]int
}

func newConjoint(options ConjointOptions, header []string, byName map[string]int) (*conjointAccumulator, error) {
	if len(options.AttributeColumns) < minConjointAttributes || len(options.AttributeColumns) > maxConjointAttributes {
		return nil, errConjointColumns
	}
	rKey := strings.ToLower(strings.TrimSpace(options.RatingColumn))
	rIdx, hasR := byName[rKey]
	if !hasR || excludeSummary(rKey) {
		return nil, errConjointColumns
	}
	attrIndices, err := selectColumns(byName, "conjoint.attributeColumns", options.AttributeColumns)
	if err != nil || len(attrIndices) != len(options.AttributeColumns) {
		return nil, errConjointColumns
	}
	attrs := make([]string, len(attrIndices))
	levelCounts := make([]map[string]int, len(attrIndices))
	for i, idx := range attrIndices {
		if idx == rIdx || excludeSummary(strings.ToLower(header[idx])) {
			return nil, errConjointColumns
		}
		attrs[i] = header[idx]
		levelCounts[i] = make(map[string]int)
	}
	return &conjointAccumulator{
		Conjoint: Conjoint{
			ConjointOptions: ConjointOptions{
				RatingColumn:     header[rIdx],
				AttributeColumns: attrs,
			},
		},
		ratingIndex: rIdx,
		attrIndices: attrIndices,
		levelCounts: levelCounts,
	}, nil
}

func (a *conjointAccumulator) add(record []string, mappings categoryMappings) {
	ratingText := fieldAt(record, a.ratingIndex)
	if ratingText == "" {
		a.Missing++
		return
	}
	levels := make([]string, len(a.attrIndices))
	for i, idx := range a.attrIndices {
		val := mappings.label(idx, fieldAt(record, idx))
		if val == "" {
			a.Missing++
			return
		}
		levels[i] = val
	}
	rating, ok := parseFiniteNumber(ratingText)
	if !ok {
		a.NonNumeric++
		return
	}
	a.Included++
	for i, lv := range levels {
		a.levelCounts[i][lv]++
	}
	a.rows = append(a.rows, conjointRow{rating: rating, levels: levels})
}

func (a *conjointAccumulator) finish() *Conjoint {
	result := &a.Conjoint
	n := a.Included
	numAttrs := len(a.attrIndices)
	if n < 2 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	attrLevels := make([][]string, numAttrs)
	levelIndex := make([]map[string]int, numAttrs)
	totalCols := 1 // intercept
	for i := range numAttrs {
		if len(a.levelCounts[i]) < minConjointLevels || len(a.levelCounts[i]) > maxConjointLevels {
			result.UnavailableReason = "INVALID_LEVEL_COUNT"
			return result
		}
		levels := make([]string, 0, len(a.levelCounts[i]))
		for lv := range a.levelCounts[i] {
			if len(lv) > maxValueBytes {
				result.UnavailableReason = "INVALID_LEVEL_COUNT"
				return result
			}
			levels = append(levels, lv)
		}
		slices.Sort(levels)
		attrLevels[i] = levels
		levelIndex[i] = make(map[string]int, len(levels))
		for j, lv := range levels {
			levelIndex[i][lv] = j
		}
		totalCols += len(levels) - 1
	}
	if n < totalCols {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	// Build effects-coded design matrix X: [1, z_{0,0..k0-2}, z_{1,0..k1-2}, ...].
	// For each attribute, the last level is coded as -1 on all (k-1) columns so
	// part-worth utilities automatically sum to 0.
	x := make([][]float64, n)
	y := make([]float64, n)
	var meanY float64
	for rowIdx, row := range a.rows {
		design := make([]float64, totalCols)
		design[0] = 1
		offset := 1
		for attrIdx, lv := range row.levels {
			k := len(attrLevels[attrIdx])
			lvPos := levelIndex[attrIdx][lv]
			if lvPos == k-1 {
				for j := range k - 1 {
					design[offset+j] = -1
				}
			} else {
				design[offset+lvPos] = 1
			}
			offset += k - 1
		}
		x[rowIdx] = design
		y[rowIdx] = row.rating
		meanY += row.rating
	}
	meanY /= float64(n)

	r, qty, ok := householderQR(x, y)
	if !ok {
		result.UnavailableReason = "COLLINEAR_DESIGN"
		return result
	}
	betas, ok := solveUpperTriangular(r, qty)
	if !ok {
		result.UnavailableReason = "COLLINEAR_DESIGN"
		return result
	}

	var rss, tss float64
	for i := range n {
		var pred float64
		for j := range totalCols {
			pred += x[i][j] * betas[j]
		}
		resid := y[i] - pred
		rss += resid * resid
		dy := y[i] - meanY
		tss += dy * dy
	}
	if math.IsNaN(rss) || math.IsInf(rss, 0) {
		result.UnavailableReason = "OUT_OF_RANGE"
		return result
	}

	attributes := make([]ConjointAttributeResult, numAttrs)
	var totalRange float64
	offset := 1
	for i := range numAttrs {
		levels := attrLevels[i]
		k := len(levels)
		levelResults := make([]ConjointLevelUtility, k)
		var sumFirst float64
		var minU, maxU float64
		for j := range k {
			var u float64
			if j < k-1 {
				u = betas[offset+j]
				sumFirst += u
			} else {
				u = -sumFirst
			}
			if j == 0 || u < minU {
				minU = u
			}
			if j == 0 || u > maxU {
				maxU = u
			}
			levelResults[j] = ConjointLevelUtility{
				Level:     levels[j],
				Count:     a.levelCounts[i][levels[j]],
				PartWorth: u,
			}
		}
		offset += k - 1
		rng := maxU - minU
		totalRange += rng
		attributes[i] = ConjointAttributeResult{
			Column: a.AttributeColumns[i],
			Range:  rng,
			Levels: levelResults,
		}
	}
	for i := range numAttrs {
		if totalRange > 0 {
			attributes[i].ImportancePercent = attributes[i].Range / totalRange * 100.0
		} else {
			attributes[i].ImportancePercent = 100.0 / float64(numAttrs)
		}
	}

	res := &ConjointResult{
		Intercept:  betas[0],
		Attributes: attributes,
	}
	if tss > 0 {
		r2 := max(0.0, min(1.0, 1.0-rss/tss))
		res.RSquared = &r2
	}
	result.Result = res
	return result
}
