package survey

import (
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
)

var errPSMColumns = errors.New("PSM 价格敏感度分析请选择四个不同的可统计价格字段。")

// PSMOptions selects the four Van Westendorp price threshold columns.
type PSMOptions struct {
	TooCheapColumn     string `json:"tooCheapColumn"`
	CheapColumn        string `json:"cheapColumn"`
	ExpensiveColumn    string `json:"expensiveColumn"`
	TooExpensiveColumn string `json:"tooExpensiveColumn"`
}

// PSMCurvePoint records the six cumulative proportions at one price point.
type PSMCurvePoint struct {
	Price        float64 `json:"price"`
	TooCheap     float64 `json:"tooCheap"`
	Cheap        float64 `json:"cheap"`
	NotCheap     float64 `json:"notCheap"`
	Expensive    float64 `json:"expensive"`
	NotExpensive float64 `json:"notExpensive"`
	TooExpensive float64 `json:"tooExpensive"`
}

// PSMResult holds the four Van Westendorp price intersections and curve points.
type PSMResult struct {
	PMC             float64         `json:"pmc"`
	PME             float64         `json:"pme"`
	OPP             float64         `json:"opp"`
	IDP             float64         `json:"idp"`
	AcceptableRange [2]float64      `json:"acceptableRange"`
	Curve           []PSMCurvePoint `json:"curve"`
}

// PSM reports Van Westendorp Price Sensitivity Meter intersections over valid monotonic rows.
type PSM struct {
	PSMOptions
	Included          int        `json:"included"`
	Missing           int        `json:"missing"`
	NonNumeric        int        `json:"nonNumeric"`
	Inconsistent      int        `json:"inconsistent"`
	Result            *PSMResult `json:"result,omitempty"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
}

type psmAccumulator struct {
	PSM
	indices      [4]int
	tooCheap     []float64
	cheap        []float64
	expensive    []float64
	tooExpensive []float64
}

func newPSM(options PSMOptions, header []string, byName map[string]int) (*psmAccumulator, error) {
	raw := [4]string{
		options.TooCheapColumn,
		options.CheapColumn,
		options.ExpensiveColumn,
		options.TooExpensiveColumn,
	}
	var indices [4]int
	used := make(map[int]bool, 4)
	for i, name := range raw {
		key := strings.ToLower(strings.TrimSpace(name))
		idx, ok := byName[key]
		if !ok || excludeSummary(key) || used[idx] {
			return nil, errPSMColumns
		}
		indices[i] = idx
		used[idx] = true
	}
	return &psmAccumulator{
		PSM: PSM{
			PSMOptions: PSMOptions{
				TooCheapColumn:     header[indices[0]],
				CheapColumn:        header[indices[1]],
				ExpensiveColumn:    header[indices[2]],
				TooExpensiveColumn: header[indices[3]],
			},
		},
		indices: indices,
	}, nil
}

func (a *psmAccumulator) add(record []string) {
	for _, idx := range a.indices {
		if fieldAt(record, idx) == "" {
			a.Missing++
			return
		}
	}
	var vals [4]float64
	for i, idx := range a.indices {
		v, ok := parseFiniteNumber(fieldAt(record, idx))
		if !ok || v < 0 {
			a.NonNumeric++
			return
		}
		vals[i] = v
	}
	if vals[0] > vals[1] || vals[1] > vals[2] || vals[2] > vals[3] {
		a.Inconsistent++
		return
	}
	a.Included++
	a.tooCheap = append(a.tooCheap, vals[0])
	a.cheap = append(a.cheap, vals[1])
	a.expensive = append(a.expensive, vals[2])
	a.tooExpensive = append(a.tooExpensive, vals[3])
}

func (a *psmAccumulator) finish() *PSM {
	result := &a.PSM
	n := a.Included
	if n < 2 {
		result.UnavailableReason = "INSUFFICIENT_ROWS"
		return result
	}

	slices.Sort(a.tooCheap)
	slices.Sort(a.cheap)
	slices.Sort(a.expensive)
	slices.Sort(a.tooExpensive)

	grid := make([]float64, 0, 4*n)
	grid = append(grid, a.tooCheap...)
	grid = append(grid, a.cheap...)
	grid = append(grid, a.expensive...)
	grid = append(grid, a.tooExpensive...)
	slices.Sort(grid)
	grid = slices.Compact(grid)

	nf := float64(n)
	curve := make([]PSMCurvePoint, len(grid))
	for i, price := range grid {
		// Descending cumulative: proportion with threshold >= price.
		tcCount := n - sort.SearchFloat64s(a.tooCheap, price)
		cCount := n - sort.SearchFloat64s(a.cheap, price)
		// Ascending cumulative: proportion with threshold <= price.
		eCount := sort.Search(n, func(k int) bool { return a.expensive[k] > price })
		teCount := sort.Search(n, func(k int) bool { return a.tooExpensive[k] > price })

		tc := float64(tcCount) / nf
		c := float64(cCount) / nf
		e := float64(eCount) / nf
		te := float64(teCount) / nf
		curve[i] = PSMCurvePoint{
			Price:        price,
			TooCheap:     tc,
			Cheap:        c,
			NotCheap:     1 - c,
			Expensive:    e,
			NotExpensive: 1 - e,
			TooExpensive: te,
		}
	}

	pmc := psmIntersection(curve, func(pt PSMCurvePoint) float64 { return pt.TooCheap - pt.NotCheap })
	pme := psmIntersection(curve, func(pt PSMCurvePoint) float64 { return pt.NotExpensive - pt.TooExpensive })
	opp := psmIntersection(curve, func(pt PSMCurvePoint) float64 { return pt.TooCheap - pt.TooExpensive })
	idp := psmIntersection(curve, func(pt PSMCurvePoint) float64 { return pt.Cheap - pt.Expensive })

	if math.IsNaN(pmc) || math.IsNaN(pme) || math.IsNaN(opp) || math.IsNaN(idp) {
		result.UnavailableReason = "OUT_OF_RANGE"
		return result
	}

	low, high := min(pmc, pme), max(pmc, pme)
	result.Result = &PSMResult{
		PMC:             pmc,
		PME:             pme,
		OPP:             opp,
		IDP:             idp,
		AcceptableRange: [2]float64{low, high},
		Curve:           curve,
	}
	return result
}

// psmIntersection finds where a monotone non-increasing difference function
// diff(pt) = f_decreasing(pt) - g_increasing(pt) crosses zero via linear interpolation.
func psmIntersection(curve []PSMCurvePoint, diff func(PSMCurvePoint) float64) float64 {
	if len(curve) == 0 {
		return math.NaN()
	}
	if len(curve) == 1 {
		return curve[0].Price
	}
	// Check if diff is zero on a plateau [firstZero, lastZero].
	firstZero, lastZero := -1, -1
	for i, pt := range curve {
		d := diff(pt)
		if math.Abs(d) < 1e-12 {
			if firstZero == -1 {
				firstZero = i
			}
			lastZero = i
		}
	}
	if firstZero != -1 {
		return 0.5 * (curve[firstZero].Price + curve[lastZero].Price)
	}
	for i := 0; i < len(curve)-1; i++ {
		d0 := diff(curve[i])
		d1 := diff(curve[i+1])
		if d0 > 0 && d1 < 0 {
			t := d0 / (d0 - d1)
			return curve[i].Price + t*(curve[i+1].Price-curve[i].Price)
		}
	}
	if diff(curve[0]) <= 0 {
		return curve[0].Price
	}
	return curve[len(curve)-1].Price
}
