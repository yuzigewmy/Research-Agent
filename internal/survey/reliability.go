package survey

import (
	"errors"
	"math"
	"strings"
)

const maxReliabilityColumns = 20

var errReliabilityColumns = errors.New("信度分析请选择 2 至 20 个不同的可统计字段。")

// ReliabilityOptions selects 2 to 20 distinct numeric items from the same
// scale. Reverse-score any opposite-direction items to align score directions.
type ReliabilityOptions struct {
	Columns []string `json:"columns"`
}

// WeightedReliability reports weighted Cronbach alpha over complete numeric
// responses with valid positive weights.
type WeightedReliability struct {
	Included      int      `json:"included"`
	WeightSum     float64  `json:"weightSum"`
	MissingWeight int      `json:"missingWeight"`
	InvalidWeight int      `json:"invalidWeight"`
	Alpha         *float64 `json:"alpha,omitempty"`
}

// Reliability reports raw Cronbach alpha from complete numeric responses among
// valid rows. Missing takes precedence over NonNumeric. Alpha is omitted if
// fewer than two responses are included or the coefficient cannot be computed.
type Reliability struct {
	Columns    []string             `json:"columns"`
	Included   int                  `json:"included"`
	Missing    int                  `json:"missing"`
	NonNumeric int                  `json:"nonNumeric"`
	Alpha      *float64             `json:"alpha,omitempty"`
	Weighted   *WeightedReliability `json:"weighted,omitempty"`
}

type reliabilityAccumulator struct {
	Reliability
	indices          []int
	scores           []float64
	weightedEnabled  bool
	weightedScores   []float64
	weightedWeights  []float64
	weightedUnits    uint64
	weightedIncluded int
	missingWeight    int
	invalidWeight    int
}

func newReliability(options ReliabilityOptions, header []string, byName map[string]int, weighted bool) (*reliabilityAccumulator, error) {
	if len(options.Columns) < 2 || len(options.Columns) > maxReliabilityColumns {
		return nil, errReliabilityColumns
	}
	indices, err := selectColumns(byName, "reliability.columns", options.Columns)
	if err != nil || len(indices) != len(options.Columns) {
		return nil, errReliabilityColumns
	}
	columns := make([]string, len(indices))
	for i, index := range indices {
		columns[i] = header[index]
		if excludeSummary(strings.ToLower(columns[i])) {
			return nil, errReliabilityColumns
		}
	}
	return &reliabilityAccumulator{
		Reliability:     Reliability{Columns: columns},
		indices:         indices,
		weightedEnabled: weighted,
	}, nil
}

func (a *reliabilityAccumulator) add(record []string, weight uint64, wStatus weightStatus) {
	for _, index := range a.indices {
		if fieldAt(record, index) == "" {
			a.Missing++
			return
		}
	}
	var scores [maxReliabilityColumns]float64
	for i, index := range a.indices {
		value, ok := parseFiniteNumber(fieldAt(record, index))
		if !ok {
			a.NonNumeric++
			return
		}
		scores[i] = value
	}
	itemSlice := scores[:len(a.indices)]
	a.scores = append(a.scores, itemSlice...)
	a.Included++
	if a.weightedEnabled {
		switch wStatus {
		case weightValid:
			a.weightedScores = append(a.weightedScores, itemSlice...)
			a.weightedWeights = append(a.weightedWeights, weightUnitsToFloat(weight))
			a.weightedUnits += weight
			a.weightedIncluded++
		case weightMissing:
			a.missingWeight++
		case weightInvalid:
			a.invalidWeight++
		}
	}
}

func (a *reliabilityAccumulator) finish() *Reliability {
	if a.weightedEnabled && a.weightedIncluded >= 2 {
		a.Weighted = a.finishWeighted()
	}
	if a.Included < 2 {
		return &a.Reliability
	}
	itemCount := len(a.indices)
	baseline := a.scores[:itemCount]
	var largestDifference float64
	for i, value := range a.scores[itemCount:] {
		largestDifference = max(largestDifference, math.Abs(value-baseline[i%itemCount]))
	}
	if largestDifference == 0 {
		return &a.Reliability
	}
	_, exponent := math.Frexp(largestDifference)
	if math.IsInf(largestDifference, 1) {
		exponent = 1024 // The binary exponent of MaxFloat64.
	}
	// Center each item at the first complete row and use one binary scale for
	// all items. Both transformations preserve raw alpha and bound row totals.
	items := make([]reliabilityVariance, itemCount)
	var total reliabilityVariance
	for offset := itemCount; offset < len(a.scores); offset += itemCount {
		count := float64(offset/itemCount + 1)
		var rowTotal, correction float64
		for i, value := range a.scores[offset : offset+itemCount] {
			var scaled float64
			if value != baseline[i] {
				// Skip scaling equal values, which may dwarf all the differences.
				// Keep subtraction and addition errors for cancelling row totals.
				var subtractionError, additionError float64
				scaled, subtractionError = sumAndRoundoff(math.Ldexp(value, -exponent), -math.Ldexp(baseline[i], -exponent))
				rowTotal, additionError = sumAndRoundoff(rowTotal, scaled)
				correction += subtractionError + additionError
			}
			items[i].add(scaled, count)
		}
		total.add(rowTotal+correction, count)
	}
	if total.squares <= 0 {
		return &a.Reliability
	}
	var itemSquares float64
	for _, item := range items {
		itemSquares += item.squares
	}
	// The (n-1) divisors in the sample variances cancel in the ratio.
	k := float64(itemCount)
	alpha := k / (k - 1) * (1 - itemSquares/total.squares)
	if !math.IsNaN(alpha) && !math.IsInf(alpha, 0) {
		a.Alpha = &alpha
	}
	return &a.Reliability
}

func (a *reliabilityAccumulator) finishWeighted() *WeightedReliability {
	res := &WeightedReliability{
		Included:      a.weightedIncluded,
		WeightSum:     weightUnitsToFloat(a.weightedUnits),
		MissingWeight: a.missingWeight,
		InvalidWeight: a.invalidWeight,
	}
	if a.weightedIncluded < 2 || res.WeightSum <= 0 {
		return res
	}
	itemCount := len(a.indices)
	baseline := a.weightedScores[:itemCount]
	var largestDifference float64
	for i, value := range a.weightedScores[itemCount:] {
		largestDifference = max(largestDifference, math.Abs(value-baseline[i%itemCount]))
	}
	if largestDifference == 0 {
		return res
	}
	_, exponent := math.Frexp(largestDifference)
	if math.IsInf(largestDifference, 1) {
		exponent = 1024
	}
	n := a.weightedIncluded
	scaledVals := make([]float64, n*itemCount)
	rowTotals := make([]float64, n)
	meanItems := make([]float64, itemCount)
	var meanTotal, wSum float64
	for row := range n {
		w := a.weightedWeights[row]
		wSum += w
		offset := row * itemCount
		var rowTotal, correction float64
		for j := range itemCount {
			value := a.weightedScores[offset+j]
			var scaled float64
			if value != baseline[j] {
				var subErr, addErr float64
				scaled, subErr = sumAndRoundoff(math.Ldexp(value, -exponent), -math.Ldexp(baseline[j], -exponent))
				rowTotal, addErr = sumAndRoundoff(rowTotal, scaled)
				correction += subErr + addErr
			}
			scaledVals[offset+j] = scaled
			meanItems[j] += w * scaled
		}
		rt := rowTotal + correction
		rowTotals[row] = rt
		meanTotal += w * rt
	}
	if wSum <= 0 {
		return res
	}
	for j := range itemCount {
		meanItems[j] /= wSum
	}
	meanTotal /= wSum

	var itemWeightedSS, totalWeightedSS float64
	for row := range n {
		w := a.weightedWeights[row]
		offset := row * itemCount
		for j := range itemCount {
			d := scaledVals[offset+j] - meanItems[j]
			itemWeightedSS += w * d * d
		}
		dt := rowTotals[row] - meanTotal
		totalWeightedSS += w * dt * dt
	}
	if totalWeightedSS <= 0 {
		return res
	}
	k := float64(itemCount)
	alpha := k / (k - 1) * (1 - itemWeightedSS/totalWeightedSS)
	if !math.IsNaN(alpha) && !math.IsInf(alpha, 0) {
		res.Alpha = &alpha
	}
	return res
}

// sumAndRoundoff returns a rounded sum and its lost low bits (TwoSum).
func sumAndRoundoff(a, b float64) (sum, roundoff float64) {
	sum = a + b
	bHigh := sum - a
	return sum, (a - (sum - bHigh)) + (b - bHigh)
}

type reliabilityVariance struct {
	mean, squares float64
}

func (v *reliabilityVariance) add(value, count float64) {
	delta := value - v.mean
	v.mean += delta / count
	v.squares += delta * (value - v.mean)
}
