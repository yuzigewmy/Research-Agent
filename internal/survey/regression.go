package survey

import (
	"errors"
	"math"
	"strings"
)

// RegressionOptions selects the two columns for an ordinary least squares fit with an intercept.
type RegressionOptions struct {
	PredictorColumn string `json:"predictorColumn"`
	OutcomeColumn   string `json:"outcomeColumn"`
}

// Regression counts complete finite pairs among valid responses. A blank cell
// takes precedence over a nonnumeric value in the other column.
type Regression struct {
	RegressionOptions
	Included          int            `json:"included"`
	Missing           int            `json:"missing"`
	NonNumeric        int            `json:"nonNumeric"`
	Fit               *RegressionFit `json:"fit,omitempty"`
	UnavailableReason string         `json:"unavailableReason,omitempty"`
}

// RegressionFit puts the coefficients in the original units. R² is undefined
// when the outcome has no variation.
type RegressionFit struct {
	Intercept float64  `json:"intercept"`
	Slope     float64  `json:"slope"`
	RSquared  *float64 `json:"rSquared,omitempty"`
}

type regressionPair struct{ x, y float64 }

type regressionAccumulator struct {
	Regression
	xIndex, yIndex int
	pairs          []regressionPair
	maxX, maxY     float64
}

func newRegression(options RegressionOptions, header []string, byName map[string]int) (*regressionAccumulator, error) {
	x, y := strings.ToLower(strings.TrimSpace(options.PredictorColumn)), strings.ToLower(strings.TrimSpace(options.OutcomeColumn))
	xIndex, hasX := byName[x]
	yIndex, hasY := byName[y]
	if !hasX || !hasY || xIndex == yIndex || excludeSummary(x) || excludeSummary(y) {
		return nil, errors.New("回归分析请选择两个不同的可统计字段。")
	}
	return &regressionAccumulator{
		Regression: Regression{RegressionOptions: RegressionOptions{PredictorColumn: header[xIndex], OutcomeColumn: header[yIndex]}},
		xIndex:     xIndex,
		yIndex:     yIndex,
	}, nil
}

func (a *regressionAccumulator) add(record []string) {
	xText, yText := fieldAt(record, a.xIndex), fieldAt(record, a.yIndex)
	if xText == "" || yText == "" {
		a.Missing++
		return
	}
	x, xOK := parseFiniteNumber(xText)
	y, yOK := parseFiniteNumber(yText)
	if !xOK || !yOK {
		a.NonNumeric++
		return
	}
	a.Included++
	a.maxX = max(a.maxX, math.Abs(x))
	a.maxY = max(a.maxY, math.Abs(y))
	a.pairs = append(a.pairs, regressionPair{x, y})
}

func (a *regressionAccumulator) finish() *Regression {
	result := &a.Regression
	if a.Included < 2 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	// Binary scaling keeps the products finite at either end of the float range.
	// Subtract the first scaled pair before averaging to preserve small differences.
	_, xExponent := math.Frexp(a.maxX)
	_, yExponent := math.Frexp(a.maxY)
	baseX := math.Ldexp(a.pairs[0].x, -xExponent)
	baseY := math.Ldexp(a.pairs[0].y, -yExponent)
	var meanDX, meanDY float64
	for i, pair := range a.pairs {
		dx := math.Ldexp(pair.x, -xExponent) - baseX
		dy := math.Ldexp(pair.y, -yExponent) - baseY
		n := float64(i + 1)
		meanDX += (dx - meanDX) / n
		meanDY += (dy - meanDY) / n
	}
	var xx, yy, xy float64
	for _, pair := range a.pairs {
		dx := (math.Ldexp(pair.x, -xExponent) - baseX) - meanDX
		dy := (math.Ldexp(pair.y, -yExponent) - baseY) - meanDY
		xx += dx * dx
		yy += dy * dy
		xy += dx * dy
	}
	if xx == 0 {
		result.UnavailableReason = "CONSTANT_PREDICTOR"
		return result
	}

	scaledSlope := xy / xx
	scaledIntercept := math.FMA(-scaledSlope, baseX, baseY) + math.FMA(-scaledSlope, meanDX, meanDY)
	slope := math.Ldexp(scaledSlope, yExponent-xExponent)
	intercept := math.Ldexp(scaledIntercept, yExponent)
	if math.IsInf(slope, 0) || math.IsInf(intercept, 0) ||
		(slope == 0 && scaledSlope != 0) || (intercept == 0 && scaledIntercept != 0) {
		result.UnavailableReason = "OUT_OF_RANGE"
		return result
	}
	result.Fit = &RegressionFit{Intercept: intercept, Slope: slope}
	if yy > 0 {
		correlation := xy / math.Sqrt(xx) / math.Sqrt(yy)
		result.Fit.RSquared = new(min(1, correlation*correlation))
	}
	return result
}
