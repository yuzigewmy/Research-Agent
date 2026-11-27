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
	if len(options.Constructs) < minCFAConstructs || len(options.Constructs) > maxCFAConstructs {
		return nil, errCFAConstructs
	}
	usedNames := make(map[string]bool, len(options.Constructs))
	usedColumns := make(map[int]bool)
	normalized := make([]CFAConstructOptions, len(options.Constructs))
	var allIndices []int
	slicesRange := make([][2]int, len(options.Constructs))

	for i, c := range options.Constructs {
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > maxValueBytes || !utf8.ValidString(name) {
			return nil, errCFANames
		}
		nameKey := strings.ToLower(name)
		if usedNames[nameKey] {
			return nil, errCFANames
		}
		usedNames[nameKey] = true

		if len(c.Columns) < minCFAItems || len(c.Columns) > maxCFAItems {
			return nil, errCFAConstructs
		}
		indices, err := selectColumns(byName, "cfa.constructs.columns", c.Columns)
		if err != nil || len(indices) != len(c.Columns) {
			return nil, errCFAConstructs
		}
		start := len(allIndices)
		cols := make([]string, len(indices))
		for j, idx := range indices {
			if usedColumns[idx] || excludeSummary(strings.ToLower(header[idx])) {
				return nil, errCFAConstructs
			}
			usedColumns[idx] = true
			cols[j] = header[idx]
			allIndices = append(allIndices, idx)
		}
		slicesRange[i] = [2]int{start, len(allIndices)}
		normalized[i] = CFAConstructOptions{Name: name, Columns: cols}
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
	n := a.Included
	if n < 3 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	totalItems := len(a.allIndices)
	fullCorr, means, stds, ok := pearsonCorrelationMatrix(a.rows, totalItems)
	if !ok {
		result.UnavailableReason = "CONSTANT_COLUMN"
		return result
	}

	numConstructs := len(a.Constructs)
	constructScores := make([][]float64, n)
	for i := range n {
		constructScores[i] = make([]float64, numConstructs)
	}

	constructResults := make([]CFAConstructResult, numConstructs)
	for cIdx, c := range a.Constructs {
		start, end := a.constructSlices[cIdx][0], a.constructSlices[cIdx][1]
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
				z := (a.rows[i][start+j] - means[start+j]) / stds[start+j]
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

	scoreCorr, _, _, ok := pearsonCorrelationMatrix(constructScores, numConstructs)
	if !ok {
		result.UnavailableReason = "CONSTANT_CONSTRUCT"
		return result
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

	result.Result = &CFAResult{
		Constructs:        constructResults,
		CorrelationMatrix: scoreCorr,
		DiscriminantValid: allDiscriminant,
	}
	return result
}
