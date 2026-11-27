package survey

import (
	"errors"
	"math"
	"strings"
)

const (
	minMultipleRegressionPredictors = 2
	maxMultipleRegressionPredictors = 8
)

var errMultipleRegressionColumns = errors.New("多元线性回归请选择 1 个因变量和 2 至 8 个不同的自变量字段。")

// MultipleRegressionOptions selects an outcome column and 2 to 8 distinct predictor columns.
type MultipleRegressionOptions struct {
	OutcomeColumn    string   `json:"outcomeColumn"`
	PredictorColumns []string `json:"predictorColumns"`
}

// MultipleRegressionCoefficient holds one fitted OLS coefficient and its t-statistic.
type MultipleRegressionCoefficient struct {
	Name        string   `json:"name"`
	Coefficient float64  `json:"coefficient"`
	StdError    float64  `json:"stdError"`
	TStat       *float64 `json:"tStat,omitempty"`
}

// MultipleRegressionFit holds the full OLS regression result with intercept.
type MultipleRegressionFit struct {
	Intercept        MultipleRegressionCoefficient   `json:"intercept"`
	Predictors       []MultipleRegressionCoefficient `json:"predictors"`
	RSquared         *float64                        `json:"rSquared,omitempty"`
	AdjustedRSquared *float64                        `json:"adjustedRSquared,omitempty"`
	FStat            *float64                        `json:"fStat,omitempty"`
	DFModel          int                             `json:"dfModel"`
	DFResidual       int                             `json:"dfResidual"`
}

// MultipleRegression reports OLS regression over complete finite rows among valid responses.
type MultipleRegression struct {
	MultipleRegressionOptions
	Included          int                    `json:"included"`
	Missing           int                    `json:"missing"`
	NonNumeric        int                    `json:"nonNumeric"`
	Fit               *MultipleRegressionFit `json:"fit,omitempty"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
}

type multipleRegressionAccumulator struct {
	MultipleRegression
	yIndex   int
	xIndices []int
	rows     [][]float64
	yValues  []float64
}

func newMultipleRegression(options MultipleRegressionOptions, header []string, byName map[string]int) (*multipleRegressionAccumulator, error) {
	if len(options.PredictorColumns) < minMultipleRegressionPredictors || len(options.PredictorColumns) > maxMultipleRegressionPredictors {
		return nil, errMultipleRegressionColumns
	}
	yKey := strings.ToLower(strings.TrimSpace(options.OutcomeColumn))
	yIndex, hasY := byName[yKey]
	if !hasY || excludeSummary(yKey) {
		return nil, errMultipleRegressionColumns
	}
	xIndices, err := selectColumns(byName, "multipleRegression.predictorColumns", options.PredictorColumns)
	if err != nil || len(xIndices) != len(options.PredictorColumns) {
		return nil, errMultipleRegressionColumns
	}
	predictors := make([]string, len(xIndices))
	for i, idx := range xIndices {
		if idx == yIndex || excludeSummary(strings.ToLower(header[idx])) {
			return nil, errMultipleRegressionColumns
		}
		predictors[i] = header[idx]
	}
	return &multipleRegressionAccumulator{
		MultipleRegression: MultipleRegression{
			MultipleRegressionOptions: MultipleRegressionOptions{
				OutcomeColumn:    header[yIndex],
				PredictorColumns: predictors,
			},
		},
		yIndex:   yIndex,
		xIndices: xIndices,
	}, nil
}

func (a *multipleRegressionAccumulator) add(record []string) {
	if fieldAt(record, a.yIndex) == "" {
		a.Missing++
		return
	}
	for _, idx := range a.xIndices {
		if fieldAt(record, idx) == "" {
			a.Missing++
			return
		}
	}
	y, ok := parseFiniteNumber(fieldAt(record, a.yIndex))
	if !ok {
		a.NonNumeric++
		return
	}
	xs := make([]float64, len(a.xIndices))
	for i, idx := range a.xIndices {
		v, vOK := parseFiniteNumber(fieldAt(record, idx))
		if !vOK {
			a.NonNumeric++
			return
		}
		xs[i] = v
	}
	a.Included++
	a.yValues = append(a.yValues, y)
	a.rows = append(a.rows, xs)
}

func (a *multipleRegressionAccumulator) finish() *MultipleRegression {
	result := &a.MultipleRegression
	n := a.Included
	k := len(a.xIndices)
	if n < k+2 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	// Center and scale predictors for numerical stability, then transform back.
	meanX := make([]float64, k)
	var meanY float64
	for i := range n {
		meanY += a.yValues[i]
		for j := range k {
			meanX[j] += a.rows[i][j]
		}
	}
	meanY /= float64(n)
	for j := range k {
		meanX[j] /= float64(n)
	}

	scaleX := make([]float64, k)
	for i := range n {
		for j := range k {
			d := a.rows[i][j] - meanX[j]
			scaleX[j] += d * d
		}
	}
	for j := range k {
		if scaleX[j] <= 0 || math.IsNaN(scaleX[j]) || math.IsInf(scaleX[j], 0) {
			result.UnavailableReason = "COLLINEAR_PREDICTORS"
			return result
		}
		scaleX[j] = math.Sqrt(scaleX[j])
	}

	z := make([][]float64, n)
	dy := make([]float64, n)
	var tss float64
	for i := range n {
		z[i] = make([]float64, k)
		for j := range k {
			z[i][j] = (a.rows[i][j] - meanX[j]) / scaleX[j]
		}
		dy[i] = a.yValues[i] - meanY
		tss += dy[i] * dy[i]
	}

	r, qty, ok := householderQR(z, dy)
	if !ok {
		result.UnavailableReason = "COLLINEAR_PREDICTORS"
		return result
	}
	gamma, ok := solveUpperTriangular(r, qty)
	if !ok {
		result.UnavailableReason = "COLLINEAR_PREDICTORS"
		return result
	}
	rInv, ok := invertUpperTriangular(r)
	if !ok {
		result.UnavailableReason = "COLLINEAR_PREDICTORS"
		return result
	}

	betas := make([]float64, k)
	intercept := meanY
	for j := range k {
		betas[j] = gamma[j] / scaleX[j]
		intercept -= betas[j] * meanX[j]
		if math.IsNaN(betas[j]) || math.IsInf(betas[j], 0) {
			result.UnavailableReason = "OUT_OF_RANGE"
			return result
		}
	}
	if math.IsNaN(intercept) || math.IsInf(intercept, 0) {
		result.UnavailableReason = "OUT_OF_RANGE"
		return result
	}

	var rss float64
	for i := range n {
		pred := intercept
		for j := range k {
			pred += betas[j] * a.rows[i][j]
		}
		resid := a.yValues[i] - pred
		rss += resid * resid
	}
	if math.IsNaN(rss) || math.IsInf(rss, 0) {
		result.UnavailableReason = "OUT_OF_RANGE"
		return result
	}

	dfModel := k
	dfResidual := n - k - 1
	sigmaSq := rss / float64(dfResidual)

	// Unscaled (Z^T Z)^{-1} = R^{-1} R^{-T}; C = S^{-1} (Z^T Z)^{-1} S^{-1}.
	cov := make([][]float64, k)
	for i := range k {
		cov[i] = make([]float64, k)
	}
	for i := range k {
		for j := i; j < k; j++ {
			var dot float64
			for m := j; m < k; m++ {
				dot += rInv[i][m] * rInv[j][m]
			}
			val := dot / (scaleX[i] * scaleX[j])
			cov[i][j] = val
			cov[j][i] = val
		}
	}

	// Var(intercept) = sigma^2 * (1/n + meanX^T * C * meanX).
	var quadForm float64
	for i := range k {
		for j := range k {
			quadForm += meanX[i] * cov[i][j] * meanX[j]
		}
	}
	interceptSE := math.Sqrt(max(0, sigmaSq*(1.0/float64(n)+quadForm)))
	interceptCoef := MultipleRegressionCoefficient{
		Name:        "截距",
		Coefficient: intercept,
		StdError:    interceptSE,
	}
	if interceptSE > 0 {
		t := intercept / interceptSE
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			interceptCoef.TStat = &t
		}
	}

	predictors := make([]MultipleRegressionCoefficient, k)
	for j := range k {
		se := math.Sqrt(max(0, sigmaSq*cov[j][j]))
		entry := MultipleRegressionCoefficient{
			Name:        a.PredictorColumns[j],
			Coefficient: betas[j],
			StdError:    se,
		}
		if se > 0 {
			t := betas[j] / se
			if !math.IsNaN(t) && !math.IsInf(t, 0) {
				entry.TStat = &t
			}
		}
		predictors[j] = entry
	}

	fit := &MultipleRegressionFit{
		Intercept:  interceptCoef,
		Predictors: predictors,
		DFModel:    dfModel,
		DFResidual: dfResidual,
	}
	if tss > 0 {
		r2 := max(0.0, min(1.0, 1.0-rss/tss))
		adjR2 := 1.0 - (1.0-r2)*float64(n-1)/float64(dfResidual)
		fit.RSquared = &r2
		if !math.IsNaN(adjR2) && !math.IsInf(adjR2, 0) {
			fit.AdjustedRSquared = &adjR2
		}
		if rss > 0 {
			fVal := (max(0.0, tss-rss) / float64(dfModel)) / sigmaSq
			if !math.IsNaN(fVal) && !math.IsInf(fVal, 0) {
				fit.FStat = &fVal
			}
		}
	}
	result.Fit = fit
	return result
}
