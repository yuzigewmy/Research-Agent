package survey

import (
	"math"
	"strings"
	"testing"
)

func TestAnalyzeRegressionUsesCompleteReviewedPairs(t *testing.T) {
	const data = `自变量,因变量,_quality_status
1,2,
2,4,
3,9,held
,非数值,
4,NaN,
99,0,invalid
`
	options := Options{Regression: &RegressionOptions{PredictorColumn: " 自变量 ", OutcomeColumn: "因变量"}}
	initial, err := Analyze(strings.NewReader(data), options)
	if err != nil {
		t.Fatal(err)
	}
	if got := initial.Regression; got.Included != 2 || got.Missing != 1 || got.NonNumeric != 1 || got.Fit == nil || got.Fit.Slope != 2 {
		t.Fatalf("unexpected regression before review: %+v", got)
	}
	options.Reviews = []Review{{Row: 3, Accept: true, Reason: "已核对原表"}}
	report, err := Analyze(strings.NewReader(data), options)
	if err != nil {
		t.Fatal(err)
	}
	got := report.Regression
	if got.PredictorColumn != "自变量" || got.OutcomeColumn != "因变量" || got.Included != 3 || got.Missing != 1 || got.NonNumeric != 1 || got.UnavailableReason != "" {
		t.Fatalf("unexpected regression after review: %+v", got)
	}
	fit := got.Fit
	if fit == nil || math.Abs(fit.Slope-3.5) > 1e-12 || math.Abs(fit.Intercept+2) > 1e-12 || fit.RSquared == nil || math.Abs(*fit.RSquared-49.0/52) > 1e-12 {
		t.Fatalf("unexpected fitted coefficients: %+v", fit)
	}
}

func TestAnalyzeRegressionHandlesDegenerateAndScaledData(t *testing.T) {
	options := Options{Regression: &RegressionOptions{PredictorColumn: "x", OutcomeColumn: "y"}}
	for _, test := range []struct {
		name, rows, reason string
		slope, intercept   float64
		constantOutcome    bool
	}{
		{name: "one pair", rows: "0,1\n", reason: "INSUFFICIENT_ROWS"},
		{name: "constant predictor", rows: "2,1\n2,9\n", reason: "CONSTANT_PREDICTOR"},
		{name: "constant outcome", rows: "1,7\n2,7\n", intercept: 7, constantOutcome: true},
		{name: "large finite inputs", rows: "-1e308,-1e308\n1e308,1e308\n", slope: 1},
		{name: "tiny finite inputs", rows: "1e-310,2e-310\n2e-310,4e-310\n", slope: 2},
		{name: "unrepresentable slope", rows: "0,0\n1e-308,1e308\n", reason: "OUT_OF_RANGE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, err := Analyze(strings.NewReader("X,Y\n"+test.rows), options)
			if err != nil {
				t.Fatal(err)
			}
			got := report.Regression
			if got.UnavailableReason != test.reason || (got.Fit == nil) != (test.reason != "") {
				t.Fatalf("unexpected fit availability: %+v", got)
			}
			if fit := got.Fit; fit != nil && (math.Abs(fit.Slope-test.slope) > 1e-12 || math.Abs(fit.Intercept-test.intercept) > 1e-12 ||
				(fit.RSquared == nil) != test.constantOutcome || (fit.RSquared != nil && math.Abs(*fit.RSquared-1) > 1e-12)) {
				t.Fatalf("unexpected scaled or constant fit: %+v", fit)
			}
		})
	}
	for _, outcome := range []string{" X ", "missing", "Email"} {
		options.Regression.OutcomeColumn = outcome
		if _, err := Analyze(strings.NewReader("X,Y,Email\n1,2,3\n"), options); err == nil {
			t.Errorf("accepted unsupported regression pair x/%q", outcome)
		}
	}
}
