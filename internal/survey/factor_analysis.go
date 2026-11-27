package survey

import (
	"errors"
	"math"
	"sort"
	"strings"
)

const (
	minFactorColumns = 3
	maxFactorColumns = 20
)

var (
	errFactorColumns = errors.New("效度与探索性因子分析请选择 3 至 20 个不同的可统计字段。")
	errFactorCount   = errors.New("指定因子数须为正整数且不超过所选字段数量。")
)

// FactorAnalysisOptions selects 3 to 20 numeric columns and an optional factor count.
// Factors == 0 automatically retains eigenvalues > 1 (at least 1).
type FactorAnalysisOptions struct {
	Columns []string `json:"columns"`
	Factors int      `json:"factors,omitempty"`
}

// FactorSummary describes one extracted factor's eigenvalue and explained variance.
type FactorSummary struct {
	Factor                    int     `json:"factor"`
	Eigenvalue                float64 `json:"eigenvalue"`
	VariancePercent           float64 `json:"variancePercent"`
	CumulativeVariancePercent float64 `json:"cumulativeVariancePercent"`
	RotatedSSLoadings         float64 `json:"rotatedSSLoadings"`
	RotatedVariancePercent    float64 `json:"rotatedVariancePercent"`
}

// FactorVariableResult holds per-variable MSA, communality, and Varimax-rotated loadings.
type FactorVariableResult struct {
	Column      string    `json:"column"`
	MSA         float64   `json:"msa"`
	Communality float64   `json:"communality"`
	Loadings    []float64 `json:"loadings"`
}

// FactorAnalysisResult holds KMO, Bartlett test, eigenvalues, and Varimax-rotated loadings.
type FactorAnalysisResult struct {
	KMO               float64                `json:"kmo"`
	BartlettChiSquare float64                `json:"bartlettChiSquare"`
	BartlettDF        int                    `json:"bartlettDf"`
	BartlettPValue    float64                `json:"bartlettPValue"`
	FactorCount       int                    `json:"factorCount"`
	AutoFactors       bool                   `json:"autoFactors"`
	Eigenvalues       []float64              `json:"eigenvalues"`
	Factors           []FactorSummary        `json:"factors"`
	Variables         []FactorVariableResult `json:"variables"`
	CorrelationMatrix [][]float64            `json:"correlationMatrix"`
}

// FactorAnalysis reports KMO, Bartlett's test, and Varimax EFA on complete valid rows.
type FactorAnalysis struct {
	FactorAnalysisOptions
	Included          int                   `json:"included"`
	Missing           int                   `json:"missing"`
	NonNumeric        int                   `json:"nonNumeric"`
	Result            *FactorAnalysisResult `json:"result,omitempty"`
	UnavailableReason string                `json:"unavailableReason,omitempty"`
}

type factorAnalysisAccumulator struct {
	FactorAnalysis
	indices []int
	rows    [][]float64
}

func newFactorAnalysis(options FactorAnalysisOptions, header []string, byName map[string]int) (*factorAnalysisAccumulator, error) {
	if len(options.Columns) < minFactorColumns || len(options.Columns) > maxFactorColumns {
		return nil, errFactorColumns
	}
	if options.Factors < 0 || options.Factors > len(options.Columns) {
		return nil, errFactorCount
	}
	indices, err := selectColumns(byName, "factorAnalysis.columns", options.Columns)
	if err != nil || len(indices) != len(options.Columns) {
		return nil, errFactorColumns
	}
	columns := make([]string, len(indices))
	for i, idx := range indices {
		columns[i] = header[idx]
		if excludeSummary(strings.ToLower(columns[i])) {
			return nil, errFactorColumns
		}
	}
	return &factorAnalysisAccumulator{
		FactorAnalysis: FactorAnalysis{
			FactorAnalysisOptions: FactorAnalysisOptions{
				Columns: columns,
				Factors: options.Factors,
			},
		},
		indices: indices,
	}, nil
}

func (a *factorAnalysisAccumulator) add(record []string) {
	for _, idx := range a.indices {
		if fieldAt(record, idx) == "" {
			a.Missing++
			return
		}
	}
	vals := make([]float64, len(a.indices))
	for i, idx := range a.indices {
		v, ok := parseFiniteNumber(fieldAt(record, idx))
		if !ok {
			a.NonNumeric++
			return
		}
		vals[i] = v
	}
	a.Included++
	a.rows = append(a.rows, vals)
}

func (a *factorAnalysisAccumulator) finish() *FactorAnalysis {
	result := &a.FactorAnalysis
	p := len(a.indices)
	n := a.Included
	if n < p+1 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	corr, _, _, ok := pearsonCorrelationMatrix(a.rows, p)
	if !ok {
		result.UnavailableReason = "CONSTANT_COLUMN"
		return result
	}

	inv, logDet, ok := choleskyInvertAndLogDet(corr)
	if !ok {
		result.UnavailableReason = "SINGULAR_MATRIX"
		return result
	}

	// KMO overall and per-variable MSA using anti-image partial correlations.
	msa := make([]float64, p)
	var totalR2, totalP2 float64
	for i := range p {
		var r2Sum, p2Sum float64
		for j := range p {
			if i == j {
				continue
			}
			r2 := corr[i][j] * corr[i][j]
			partial := -inv[i][j] / math.Sqrt(inv[i][i]*inv[j][j])
			p2 := partial * partial
			r2Sum += r2
			p2Sum += p2
		}
		totalR2 += r2Sum
		totalP2 += p2Sum
		if r2Sum+p2Sum > 0 {
			msa[i] = r2Sum / (r2Sum + p2Sum)
		}
	}
	var kmo float64
	if totalR2+totalP2 > 0 {
		kmo = totalR2 / (totalR2 + totalP2)
	}

	// Bartlett's test of sphericity.
	bartlettDF := p * (p - 1) / 2
	bartlettChi := max(0.0, -(float64(n-1)-float64(2*p+5)/6.0)*logDet)
	bartlettP := chiSquareUpperTail(bartlettChi, bartlettDF)

	// Eigen-decomposition of R.
	eigenvalues, eigenvecs := jacobiEigen(corr)
	autoFactors := a.Factors == 0
	factorCount := a.Factors
	if autoFactors {
		factorCount = 0
		for _, ev := range eigenvalues {
			if ev > 1.0 {
				factorCount++
			}
		}
		if factorCount == 0 {
			factorCount = 1
		}
	}
	factorCount = min(factorCount, p)

	// Unrotated PCA factor loadings: L[i][j] = v[i][j] * sqrt(max(0, lambda_j)).
	loadings := make([][]float64, p)
	for i := range p {
		loadings[i] = make([]float64, factorCount)
		for j := range factorCount {
			ev := max(0.0, eigenvalues[j])
			loadings[i][j] = eigenvecs[i][j] * math.Sqrt(ev)
		}
	}

	rotated := varimaxRotate(loadings)

	// Order rotated factors by descending sum of squared loadings and canonicalize signs.
	ss := make([]float64, factorCount)
	for j := range factorCount {
		for i := range p {
			ss[j] += rotated[i][j] * rotated[i][j]
		}
	}
	order := make([]int, factorCount)
	for j := range factorCount {
		order[j] = j
	}
	sort.Slice(order, func(i, j int) bool {
		return ss[order[i]] > ss[order[j]]
	})

	orderedRotated := make([][]float64, p)
	for i := range p {
		orderedRotated[i] = make([]float64, factorCount)
	}
	orderedSS := make([]float64, factorCount)
	for newCol, oldCol := range order {
		orderedSS[newCol] = ss[oldCol]
		var colSum float64
		for i := range p {
			colSum += rotated[i][oldCol]
		}
		sign := 1.0
		if colSum < 0 {
			sign = -1.0
		}
		for i := range p {
			orderedRotated[i][newCol] = sign * rotated[i][oldCol]
		}
	}

	factors := make([]FactorSummary, factorCount)
	var cumPct float64
	for j := range factorCount {
		varPct := max(0.0, eigenvalues[j]) / float64(p) * 100.0
		cumPct += varPct
		factors[j] = FactorSummary{
			Factor:                    j + 1,
			Eigenvalue:                eigenvalues[j],
			VariancePercent:           varPct,
			CumulativeVariancePercent: cumPct,
			RotatedSSLoadings:         orderedSS[j],
			RotatedVariancePercent:    orderedSS[j] / float64(p) * 100.0,
		}
	}

	variables := make([]FactorVariableResult, p)
	for i := range p {
		var comm float64
		for j := range factorCount {
			comm += orderedRotated[i][j] * orderedRotated[i][j]
		}
		variables[i] = FactorVariableResult{
			Column:      a.Columns[i],
			MSA:         msa[i],
			Communality: min(1.0, comm),
			Loadings:    orderedRotated[i],
		}
	}

	result.Result = &FactorAnalysisResult{
		KMO:               kmo,
		BartlettChiSquare: bartlettChi,
		BartlettDF:        bartlettDF,
		BartlettPValue:    bartlettP,
		FactorCount:       factorCount,
		AutoFactors:       autoFactors,
		Eigenvalues:       eigenvalues,
		Factors:           factors,
		Variables:         variables,
		CorrelationMatrix: corr,
	}
	return result
}

// varimaxRotate performs Kaiser-normalized orthogonal Varimax rotation using
// pairwise planar rotations (Kaiser 1958).
func varimaxRotate(loadings [][]float64) [][]float64 {
	p := len(loadings)
	if p == 0 {
		return nil
	}
	m := len(loadings[0])
	out := make([][]float64, p)
	for i := range p {
		out[i] = make([]float64, m)
		copy(out[i], loadings[i])
	}
	if m < 2 {
		return out
	}

	// Kaiser normalization: scale each row by h_i = sqrt(sum_j l_{ij}^2).
	h := make([]float64, p)
	for i := range p {
		var sumSq float64
		for j := range m {
			sumSq += out[i][j] * out[i][j]
		}
		h[i] = math.Sqrt(sumSq)
		if h[i] > 1e-12 {
			for j := range m {
				out[i][j] /= h[i]
			}
		}
	}

	pf := float64(p)
	for range 200 {
		var maxAngle float64
		for u := range m {
			for v := u + 1; v < m; v++ {
				var aSum, bSum, cSum, dSum float64
				for i := range p {
					x, y := out[i][u], out[i][v]
					ui := x*x - y*y
					vi := 2 * x * y
					aSum += ui
					bSum += vi
					cSum += ui*ui - vi*vi
					dSum += 2 * ui * vi
				}
				num := dSum - 2*aSum*bSum/pf
				den := cSum - (aSum*aSum-bSum*bSum)/pf
				phi := 0.25 * math.Atan2(num, den)
				if abs := math.Abs(phi); abs > maxAngle {
					maxAngle = abs
				}
				if math.Abs(phi) < 1e-12 {
					continue
				}
				cosP, sinP := math.Cos(phi), math.Sin(phi)
				for i := range p {
					x, y := out[i][u], out[i][v]
					out[i][u] = x*cosP + y*sinP
					out[i][v] = -x*sinP + y*cosP
				}
			}
		}
		if maxAngle < 1e-10 {
			break
		}
	}

	// Undo Kaiser normalization.
	for i := range p {
		if h[i] > 1e-12 {
			for j := range m {
				out[i][j] *= h[i]
			}
		}
	}
	return out
}
