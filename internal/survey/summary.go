package survey

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxFrequencies = 20
	maxValueBytes  = 128
)

// Summary describes the values in a non-identifier column among VALID rows.
// Numeric counts finite numbers. Mean, Min and Max are omitted when Numeric is zero.
// Frequencies includes up to 20 values of at most 128 bytes, most frequent first,
// with ties sorted by value. Longer answers still count in the other statistics.
type Summary struct {
	Column      string           `json:"column"`
	Answered    int              `json:"answered"`
	Missing     int              `json:"missing"`
	Numeric     int              `json:"numeric"`
	Mean        *float64         `json:"mean,omitempty"`
	Min         *float64         `json:"min,omitempty"`
	Max         *float64         `json:"max,omitempty"`
	Frequencies []Frequency      `json:"frequencies"`
	Weighted    *WeightedSummary `json:"weighted,omitempty"`
}

// WeightedSummary describes the numeric responses with a valid weight.
type WeightedSummary struct {
	Included    int     `json:"included"`
	TotalWeight string  `json:"totalWeight"`
	Mean        float64 `json:"mean"`
}

// Frequency is the count for one distinct trimmed answer.
type Frequency struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type columnSummary struct {
	Summary
	index           int
	mean, low, high float64
	counts          map[string]int
	weightedCount   int
	weightUnits     uint64
	weightedSum     big.Rat
}

func (s *columnSummary) add(record []string, weight uint64, mappings categoryMappings) {
	value := fieldAt(record, s.index)
	if value == "" {
		return
	}
	s.Answered++
	category := mappings.label(s.index, value)
	if len(category) <= maxValueBytes {
		if s.counts == nil {
			s.counts = make(map[string]int)
		}
		s.counts[category]++
	}
	number, ok := parseFiniteNumber(value)
	if !ok {
		return
	}
	if weight != 0 {
		// Accumulate the parsed values exactly so opposite large answers can cancel.
		var term, factor big.Rat
		term.SetFloat64(number)
		factor.SetUint64(weight)
		term.Mul(&term, &factor)
		s.weightedSum.Add(&s.weightedSum, &term)
		s.weightUnits += weight
		s.weightedCount++
	}
	s.Numeric++
	if s.Numeric == 1 {
		s.mean, s.low, s.high = number, number, number
		return
	}
	s.low, s.high = min(s.low, number), max(s.high, number)
	n := float64(s.Numeric)
	delta := number - s.mean
	if math.IsInf(delta, 0) {
		// Divide first when subtracting numbers of opposite signs would overflow.
		delta = number/n - s.mean/n
	} else {
		delta /= n
	}
	s.mean = min(s.high, max(s.low, s.mean+delta))
}

func parseFiniteNumber(value string) (float64, bool) {
	number, err := strconv.ParseFloat(value, 64)
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func (s *columnSummary) finish(valid int) Summary {
	s.Missing = valid - s.Answered
	if s.Numeric > 0 {
		s.Mean, s.Min, s.Max = &s.mean, &s.low, &s.high
	}
	if s.weightedCount > 0 {
		var denominator big.Rat
		denominator.SetUint64(s.weightUnits)
		mean, _ := s.weightedSum.Quo(&s.weightedSum, &denominator).Float64()
		s.Weighted = &WeightedSummary{Included: s.weightedCount, TotalWeight: formatWeight(s.weightUnits), Mean: mean}
	}
	frequencies := make([]Frequency, 0, len(s.counts))
	for value, count := range s.counts {
		frequencies = append(frequencies, Frequency{Value: value, Count: count})
	}
	sort.Slice(frequencies, func(i, j int) bool {
		if frequencies[i].Count != frequencies[j].Count {
			return frequencies[i].Count > frequencies[j].Count
		}
		return frequencies[i].Value < frequencies[j].Value
	})
	s.Frequencies = make([]Frequency, min(maxFrequencies, len(frequencies)))
	for i := range s.Frequencies {
		s.Frequencies[i] = Frequency{Value: strings.Clone(frequencies[i].Value), Count: frequencies[i].Count}
	}
	return s.Summary
}

func excludeSummary(name string) bool {
	if strings.HasPrefix(name, "_") {
		return true
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	switch strings.Join(parts, "") {
	case "name", "names", "fullname", "firstname", "middlename", "lastname", "preferredname", "givenname", "surname", "familyname", "forename", "username", "legalname", "realname", "respondentname", "participantname", "contactname",
		"email", "emails", "emailaddress", "contactemail", "workemail", "personalemail", "respondentemail", "participantemail",
		"phone", "phones", "phonenumber", "telephone", "telephonenumber", "cellphone", "cellphonenumber", "mobile", "mobilenumber", "mobilephone", "contactphone",
		"id", "ids", "idnumber", "respondentid", "participantid", "userid", "studentid", "employeeid", "recordid", "responseid", "surveyid", "nationalid", "passportnumber", "ssn", "socialsecuritynumber", "ipaddress",
		"姓名", "名字", "真实姓名", "用户名", "手机号", "手机号码", "手机", "电话号码", "电话", "邮箱", "电子邮箱", "电子邮件", "邮件地址", "身份证", "身份证号", "身份证号码", "学号", "工号":
		return true
	}
	for _, part := range parts {
		if part == "id" || part == "email" {
			return true
		}
	}
	return false
}
