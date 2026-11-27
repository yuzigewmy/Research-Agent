package survey

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const weightScale = 1_000_000
const maxWeight = 1_000_000 * weightScale

var weightDecimal = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,6})?$`)

// Weighting describes the weights available among valid responses.
type Weighting struct {
	Column      string `json:"column"`
	Included    int    `json:"included"`
	Missing     int    `json:"missing"`
	Invalid     int    `json:"invalid"`
	TotalWeight string `json:"totalWeight"`
}

type weightAccumulator struct {
	Weighting
	index int
	units uint64
}

type weightStatus uint8

const (
	weightDisabled weightStatus = iota
	weightValid
	weightMissing
	weightInvalid
)

func (w *weightAccumulator) add(record []string) (uint64, weightStatus) {
	value := fieldAt(record, w.index)
	if value == "" {
		w.Missing++
		return 0, weightMissing
	}
	if !weightDecimal.MatchString(value) {
		w.Invalid++
		return 0, weightInvalid
	}
	whole, fraction, _ := strings.Cut(value, ".")
	units, err := strconv.ParseUint(whole+fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	if err != nil || units == 0 || units > maxWeight {
		w.Invalid++
		return 0, weightInvalid
	}
	w.Included++
	w.units += units
	return units, weightValid
}

func weightUnitsToFloat(units uint64) float64 {
	return float64(units) / float64(weightScale)
}

func (w *weightAccumulator) finish() *Weighting {
	w.TotalWeight = formatWeight(w.units)
	return &w.Weighting
}

func formatWeight(units uint64) string {
	result := strconv.FormatUint(units/weightScale, 10)
	if fraction := units % weightScale; fraction != 0 {
		result += strings.TrimRight(fmt.Sprintf(".%06d", fraction), "0")
	}
	return result
}
