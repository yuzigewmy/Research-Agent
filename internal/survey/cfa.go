package survey

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	minCFAConstructs = 2
	maxCFAConstructs = 6
	minCFAItems      = 2
	maxCFAItems      = 10
)

var (
	errCFAConstructs = errors.New("基础 CFA 分析请定义 2 至 6 个潜变量，每个潜变量包含 2 至 10 个互异的可统计字段。")
	errCFANames      = errors.New("请为每个潜变量填写不重复的名称（最多 128 个 UTF-8 字节）。")
)

// CFAConstructOptions defines one latent construct and its observed indicator columns.
type CFAConstructOptions struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// CFAOptions configures basic first-order CFA convergent and discriminant validity.
type CFAOptions struct {
	Constructs []CFAConstructOptions `json:"constructs"`
}

// CFAItemResult holds the standardized factor loading for one indicator item.
type CFAItemResult struct {
	Column  string  `json:"column"`
	Loading float64 `json:"loading"`
}

// CFAConstructResult reports loadings, CR, AVE, sqrt(AVE), and validity flags for one construct.
type CFAConstructResult struct {
	Name                     string          `json:"name"`
	Items                    []CFAItemResult `json:"items"`
	CR                       float64         `json:"cr"`
	AVE                      float64         `json:"ave"`
	SqrtAVE                  float64         `json:"sqrtAve"`
	MaxInterConstructAbsCorr float64         `json:"maxInterConstructAbsCorr"`
	ConvergentValid          bool            `json:"convergentValid"`
	DiscriminantValid        bool            `json:"discriminantValid"`
}

// CFAResult reports per-construct convergent validity and Fornell-Larcker discriminant validity.
type CFAResult struct {
	Constructs        []CFAConstructResult `json:"constructs"`
	CorrelationMatrix [][]float64          `json:"correlationMatrix"`
	DiscriminantValid bool                 `json:"discriminantValid"`
}

// CFA reports basic confirmatory factor validity metrics over complete valid rows.
type CFA struct {
	CFAOptions
	Included          int        `json:"included"`
	Missing           int        `json:"missing"`
	NonNumeric        int        `json:"nonNumeric"`
	Result            *CFAResult `json:"result,omitempty"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
}

type cfaAccumulator struct {
	CFA
	allIndices      []int
	constructSlices [][2]int // [start, end) in allIndices
	rows            [][]float64
}

func newCFA(options CFAOptions, header []string, byName map[string]int) (*cfaAccumulator, error) {
	normalized, allIndices, slicesRange, err := normalizeConstructs(
		options.Constructs, "cfa.constructs.columns", header, byName, errCFAConstructs, errCFANames,
	)
	if err != nil {
		return nil, err
	}
	return &cfaAccumulator{
		CFA: CFA{
			CFAOptions: CFAOptions{Constructs: normalized},
		},
		allIndices:      allIndices,
		constructSlices: slicesRange,
	}, nil
}

func (a *cfaAccumulator) add(record []string) {
	for _, idx := range a.allIndices {
		if fieldAt(record, idx) == "" {
			a.Missing++
			return
		}
	}
	vals := make([]float64, len(a.allIndices))
	for i, idx := range a.allIndices {
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

func (a *cfaAccumulator) finish() *CFA {
	result := &a.CFA
	cfaRes, _, unavailable := computeMeasurementModel(a.Constructs, a.constructSlices, a.rows, len(a.allIndices))
	if unavailable != "" {
		result.UnavailableReason = unavailable
		return result
	}
	result.Result = cfaRes
	return result
}

func normalizeConstructs(constructs []CFAConstructOptions, optionPath string, header []string, byName map[string]int, errConstructs, errNames error) ([]CFAConstructOptions, []int, [][2]int, error) {
	if len(constructs) < minCFAConstructs || len(constructs) > maxCFAConstructs {
		return nil, nil, nil, errConstructs
	}
	usedNames := make(map[string]bool, len(constructs))
	usedColumns := make(map[int]bool)
	normalized := make([]CFAConstructOptions, len(constructs))
	var allIndices []int
	slicesRange := make([][2]int, len(constructs))

	for i, c := range constructs {
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > maxValueBytes || !utf8.ValidString(name) {
			return nil, nil, nil, errNames
		}
		nameKey := strings.ToLower(name)
		if usedNames[nameKey] {
			return nil, nil, nil, errNames
		}
		usedNames[nameKey] = true

		if len(c.Columns) < minCFAItems || len(c.Columns) > maxCFAItems {
			return nil, nil, nil, errConstructs
		}
		indices, err := selectColumns(byName, optionPath, c.Columns)
		if err != nil || len(indices) != len(c.Columns) {
			return nil, nil, nil, errConstructs
		}
		start := len(allIndices)
		cols := make([]string, len(indices))
		for j, idx := range indices {
			if usedColumns[idx] || excludeSummary(strings.ToLower(header[idx])) {
				return nil, nil, nil, errConstructs
			}
			usedColumns[idx] = true
			cols[j] = header[idx]
			allIndices = append(allIndices, idx)
		}
		slicesRange[i] = [2]int{start, len(allIndices)}
		normalized[i] = CFAConstructOptions{Name: name, Columns: cols}
	}
	return normalized, allIndices, slicesRange, nil
}

// computeMeasurementModel estimates first-order standardized loadings, CR, AVE,
// standardized latent scores (mean 0, sample std 1), and Fornell-Larcker correlation matrix.
func computeMeasurementModel(constructs []CFAConstructOptions, constructSlices [][2]int, rows [][]float64, totalItems int) (*CFAResult, [][]float64, string) {
	n := len(rows)
	if n < 3 {
		return nil, nil, "INSUFFICIENT_ROWS"
	}

	fullCorr, means, stds, ok := pearsonCorrelationMatrix(rows, totalItems)
	if !ok {
		return nil, nil, "CONSTANT_COLUMN"
	}

	numConstructs := len(constructs)
	constructScores := make([][]float64, n)
	for i := range n {
		constructScores[i] = make([]float64, numConstructs)
	}

	constructResults := make([]CFAConstructResult, numConstructs)
	for cIdx, c := range constructs {
		start, end := constructSlices[cIdx][0], constructSlices[cIdx][1]
		k := end - start
		subCorr := make([][]float64, k)
		for i := range k {
			subCorr[i] = make([]float64, k)
			for j := range k {
				subCorr[i][j] = fullCorr[start+i][start+j]
			}
		}

		eigenvalues, eigenvecs := jacobiEigen(subCorr)
		ev1 := max(0.0, eigenvalues[0])
		sqrtEv1 := math.Sqrt(ev1)

		// Align first eigenvector sign so sum of loadings is non-negative.
		var vecSum float64
		for j := range k {
			vecSum += eigenvecs[j][0]
		}
		sign := 1.0
		if vecSum < 0 {
			sign = -1.0
		}

		items := make([]CFAItemResult, k)
		var sumLoadings, sumLoadingsSq, sumErrorVar float64
		weights := make([]float64, k)
		for j := range k {
			w := sign * eigenvecs[j][0]
			weights[j] = w
			loading := max(-1.0, min(1.0, w*sqrtEv1))
			items[j] = CFAItemResult{
				Column:  c.Columns[j],
				Loading: loading,
			}
			sumLoadings += loading
			l2 := loading * loading
			sumLoadingsSq += l2
			sumErrorVar += max(0.0, 1.0-l2)
		}

		ave := sumLoadingsSq / float64(k)
		sqrtAve := math.Sqrt(max(0.0, ave))
		var cr float64
		num := sumLoadings * sumLoadings
		if num+sumErrorVar > 0 {
			cr = num / (num + sumErrorVar)
		}

		// Compute first principal component latent score for respondent i.
		for i := range n {
			var score float64
			for j := range k {
				z := (rows[i][start+j] - means[start+j]) / stds[start+j]
				score += weights[j] * z
			}
			constructScores[i][cIdx] = score
		}

		constructResults[cIdx] = CFAConstructResult{
			Name:            c.Name,
			Items:           items,
			CR:              cr,
			AVE:             ave,
			SqrtAVE:         sqrtAve,
			ConvergentValid: cr >= 0.7 && ave >= 0.5,
		}
	}

	scoreCorr, scoreMeans, scoreStds, ok := pearsonCorrelationMatrix(constructScores, numConstructs)
	if !ok {
		return nil, nil, "CONSTANT_CONSTRUCT"
	}

	stdScores := make([][]float64, n)
	for i := range n {
		stdScores[i] = make([]float64, numConstructs)
		for cIdx := range numConstructs {
			stdScores[i][cIdx] = (constructScores[i][cIdx] - scoreMeans[cIdx]) / scoreStds[cIdx]
		}
	}

	allDiscriminant := true
	for i := range numConstructs {
		var maxAbs float64
		for j := range numConstructs {
			if i == j {
				continue
			}
			if abs := math.Abs(scoreCorr[i][j]); abs > maxAbs {
				maxAbs = abs
			}
		}
		constructResults[i].MaxInterConstructAbsCorr = maxAbs
		constructResults[i].DiscriminantValid = constructResults[i].SqrtAVE > maxAbs
		if !constructResults[i].DiscriminantValid {
			allDiscriminant = false
		}
	}

	return &CFAResult{
		Constructs:        constructResults,
		CorrelationMatrix: scoreCorr,
		DiscriminantValid: allDiscriminant,
	}, stdScores, ""
}
