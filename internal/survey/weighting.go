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

func (w *weightAccumulator) add(record []string) {
	value := fieldAt(record, w.index)
	if value == "" {
		w.Missing++
		return
	}
	if !weightDecimal.MatchString(value) {
		w.Invalid++
		return
	}
	whole, fraction, _ := strings.Cut(value, ".")
	units, err := strconv.ParseUint(whole+fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	if err != nil || units == 0 || units > maxWeight {
		w.Invalid++
		return
	}
	w.Included++
	w.units += units
}

func (w *weightAccumulator) finish() *Weighting {
	w.TotalWeight = strconv.FormatUint(w.units/weightScale, 10)
	if fraction := w.units % weightScale; fraction != 0 {
		w.TotalWeight += strings.TrimRight(fmt.Sprintf(".%06d", fraction), "0")
	}
	return &w.Weighting
}
