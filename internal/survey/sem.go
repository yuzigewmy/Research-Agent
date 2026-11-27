package survey

import (
	"errors"
	"strings"
)

const (
	minSEMPaths = 1
	maxSEMPaths = 10
)

var (
	errSEMConstructs = errors.New("结构方程路径模型请定义 2 至 6 个潜变量，每个潜变量包含 2 至 10 个互异的可统计字段。")
	errSEMNames      = errors.New("请为每个潜变量填写不重复的名称（最多 128 个 UTF-8 字节）。")
	errSEMPaths      = errors.New("结构方程路径模型请定义 1 至 10 条互异的有向路径，起止潜变量须已定义且不可相同。")
	errSEMCycle      = errors.New("结构方程路径不能包含环路或自环，请确保路径构成有向无环图（DAG）。")
)

// SEMPathOptions defines one directed structural path from one construct to another.
type SEMPathOptions struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SEMOptions configures a latent-variable structural equation path model.
type SEMOptions struct {
	Constructs []CFAConstructOptions `json:"constructs"`
	Paths      []SEMPathOptions      `json:"paths"`
}

// SEMPathResult holds one estimated standardized structural path coefficient.
type SEMPathResult struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Beta     float64  `json:"beta"`
	StdError float64  `json:"stdError"`
	TStat    *float64 `json:"tStat,omitempty"`
}

// SEMEndogenousResult summarizes the structural OLS regression for one endogenous construct.
type SEMEndogenousResult struct {
	Construct        string   `json:"construct"`
	Predictors       []string `json:"predictors"`
	Included         int      `json:"included"`
	RSquared         *float64 `json:"rSquared,omitempty"`
	AdjustedRSquared *float64 `json:"adjustedRSquared,omitempty"`
}

// SEMResult holds measurement model validity and structural path regression results.
type SEMResult struct {
	Constructs        []CFAConstructResult  `json:"constructs"`
	Paths             []SEMPathResult       `json:"paths"`
	Endogenous        []SEMEndogenousResult `json:"endogenous"`
	CorrelationMatrix [][]float64           `json:"correlationMatrix"`
	DiscriminantValid bool                  `json:"discriminantValid"`
}

// SEM reports latent path analysis over complete numeric valid rows.
type SEM struct {
	SEMOptions
	Included          int        `json:"included"`
	Missing           int        `json:"missing"`
	NonNumeric        int        `json:"nonNumeric"`
	Result            *SEMResult `json:"result,omitempty"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
}

type semEdge struct {
	fromIdx int
	toIdx   int
}

type semAccumulator struct {
	SEM
	allIndices      []int
	constructSlices [][2]int
	edges           []semEdge
	rows            [][]float64
}

func newSEM(options SEMOptions, header []string, byName map[string]int) (*semAccumulator, error) {
	normalized, allIndices, slicesRange, err := normalizeConstructs(
		options.Constructs, "sem.constructs.columns", header, byName, errSEMConstructs, errSEMNames,
	)
	if err != nil {
		return nil, err
	}
	if len(options.Paths) < minSEMPaths || len(options.Paths) > maxSEMPaths {
		return nil, errSEMPaths
	}

	nameToIdx := make(map[string]int, len(normalized))
	for i, c := range normalized {
		nameToIdx[strings.ToLower(c.Name)] = i
	}

	nConstructs := len(normalized)
	edges := make([]semEdge, 0, len(options.Paths))
	normalizedPaths := make([]SEMPathOptions, 0, len(options.Paths))
	seenEdge := make(map[[2]int]bool, len(options.Paths))
	adj := make([][]int, nConstructs)
	inDegree := make([]int, nConstructs)

	for _, p := range options.Paths {
		fromIdx, okFrom := nameToIdx[strings.ToLower(strings.TrimSpace(p.From))]
		toIdx, okTo := nameToIdx[strings.ToLower(strings.TrimSpace(p.To))]
		if !okFrom || !okTo {
			return nil, errSEMPaths
		}
		if fromIdx == toIdx {
			return nil, errSEMCycle
		}
		key := [2]int{fromIdx, toIdx}
		if seenEdge[key] {
			return nil, errSEMPaths
		}
		seenEdge[key] = true
		edges = append(edges, semEdge{fromIdx: fromIdx, toIdx: toIdx})
		normalizedPaths = append(normalizedPaths, SEMPathOptions{
			From: normalized[fromIdx].Name,
			To:   normalized[toIdx].Name,
		})
		adj[fromIdx] = append(adj[fromIdx], toIdx)
		inDegree[toIdx]++
	}

	// Kahn's topological sort to guarantee DAG (acyclic directed graph).
	queue := make([]int, 0, nConstructs)
	for i := range nConstructs {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}
	visited := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[cur] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != nConstructs {
		return nil, errSEMCycle
	}

	return &semAccumulator{
		SEM: SEM{
			SEMOptions: SEMOptions{
				Constructs: normalized,
				Paths:      normalizedPaths,
			},
		},
		allIndices:      allIndices,
		constructSlices: slicesRange,
		edges:           edges,
	}, nil
}

func (a *semAccumulator) add(record []string) {
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

func (a *semAccumulator) finish() *SEM {
	result := &a.SEM
	measurement, stdScores, unavailable := computeMeasurementModel(a.Constructs, a.constructSlices, a.rows, len(a.allIndices))
	if unavailable != "" {
		result.UnavailableReason = unavailable
		return result
	}

	n := len(stdScores)
	// Group incoming edges by endogenous construct while keeping first-seen order.
	predsByTarget := make(map[int][]int)
	edgeIdxByTarget := make(map[int][]int)
	var targetOrder []int
	for idx, e := range a.edges {
		if _, exists := predsByTarget[e.toIdx]; !exists {
			targetOrder = append(targetOrder, e.toIdx)
		}
		predsByTarget[e.toIdx] = append(predsByTarget[e.toIdx], e.fromIdx)
		edgeIdxByTarget[e.toIdx] = append(edgeIdxByTarget[e.toIdx], idx)
	}

	pathResults := make([]SEMPathResult, len(a.edges))
	endogenousResults := make([]SEMEndogenousResult, 0, len(targetOrder))

	for _, toIdx := range targetOrder {
		predIdxs := predsByTarget[toIdx]
		edgeIdxs := edgeIdxByTarget[toIdx]
		m := len(predIdxs)
		predNames := make([]string, m)
		for j, pIdx := range predIdxs {
			predNames[j] = a.Constructs[pIdx].Name
		}

		xRows := make([][]float64, n)
		yVals := make([]float64, n)
		for i := range n {
			row := make([]float64, m)
			for j, pIdx := range predIdxs {
				row[j] = stdScores[i][pIdx]
			}
			xRows[i] = row
			yVals[i] = stdScores[i][toIdx]
		}

		fit, reason := fitLinearRegression(xRows, yVals, nil, predNames)
		if reason != "" {
			result.UnavailableReason = reason
			return result
		}

		for j, eIdx := range edgeIdxs {
			coef := fit.Predictors[j]
			pathResults[eIdx] = SEMPathResult{
				From:     a.Paths[eIdx].From,
				To:       a.Paths[eIdx].To,
				Beta:     coef.Coefficient,
				StdError: coef.StdError,
				TStat:    coef.TStat,
			}
		}
		endogenousResults = append(endogenousResults, SEMEndogenousResult{
			Construct:        a.Constructs[toIdx].Name,
			Predictors:       predNames,
			Included:         n,
			RSquared:         fit.RSquared,
			AdjustedRSquared: fit.AdjustedRSquared,
		})
	}

	result.Result = &SEMResult{
		Constructs:        measurement.Constructs,
		Paths:             pathResults,
		Endogenous:        endogenousResults,
		CorrelationMatrix: measurement.CorrelationMatrix,
		DiscriminantValid: measurement.DiscriminantValid,
	}
	return result
}
