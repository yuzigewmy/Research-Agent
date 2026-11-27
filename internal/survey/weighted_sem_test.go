package survey

import (
	"math"
	"strings"
	"testing"
)

const weightedSEMTestCSV = `id,w,x1,x2,x3,m1,m2,m3,y1,y2,y3
1,1.0,2.0,2.5,1.8,3.0,2.8,3.2,2.4,2.6,2.2
2,2.5,4.5,4.2,4.8,4.0,4.3,3.9,4.6,4.4,4.7
3,0.5,1.5,2.0,1.2,2.5,2.2,2.8,1.8,2.0,1.6
4,3.0,3.8,4.1,3.5,4.5,4.2,4.7,4.8,4.6,4.9
5,1.5,2.8,2.4,3.1,2.0,2.3,1.9,2.5,2.8,2.3
6,2.0,4.2,4.6,4.0,3.5,3.8,3.3,3.9,4.1,3.7
7,0.75,1.8,1.5,2.2,3.2,3.0,3.5,2.9,2.7,3.1
8,2.25,3.5,3.2,3.8,3.8,4.0,3.6,4.1,3.9,4.3
9,1.25,2.2,2.6,2.0,1.8,2.1,1.6,2.0,1.9,2.2
10,3.5,4.9,4.7,5.0,4.8,4.6,4.9,5.0,4.8,4.9
11,1.0,3.1,3.4,2.9,2.6,2.9,2.4,3.2,3.0,3.4
12,2.0,2.5,2.1,2.9,3.6,3.4,3.9,3.5,3.7,3.3
13,1.5,4.0,3.7,4.3,3.1,3.4,2.9,3.6,3.4,3.8
14,0.5,1.2,1.8,1.5,1.4,1.7,1.2,1.5,1.8,1.3
15,2.75,3.6,3.9,3.4,4.2,4.0,4.4,4.3,4.5,4.1
`

func assertWeightedNear(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Fatalf("%s: got %.8f, want %.8f (tol %.1e)", label, got, want, tol)
	}
}

func TestWeightedReliabilityAndRegressionMatchNumPy(t *testing.T) {
	// Add 2 extra rows with missing and invalid weights to verify exclusion counts in weighted stats.
	csvWithExtraWeights := weightedSEMTestCSV +
		"16,,3.0,3.0,3.0,3.0,3.0,3.0,3.0,3.0,3.0\n" +
		"17,-1,2.5,2.5,2.5,2.5,2.5,2.5,2.5,2.5,2.5\n"

	report, err := Analyze(strings.NewReader(csvWithExtraWeights), Options{
		WeightColumn: "w",
		Reliability:  &ReliabilityOptions{Columns: []string{"x1", "x2", "x3", "m1"}},
		Regression:   &RegressionOptions{PredictorColumn: "x1", OutcomeColumn: "y1"},
		MultipleRegression: &MultipleRegressionOptions{
			OutcomeColumn:    "y1",
			PredictorColumns: []string{"x1", "m1", "x3"},
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// 1. Weighted Reliability
	if report.Reliability == nil || report.Reliability.Weighted == nil || report.Reliability.Weighted.Alpha == nil {
		t.Fatalf("expected weighted reliability alpha, got %+v", report.Reliability)
	}
	wr := report.Reliability.Weighted
	if wr.Included != 15 || wr.MissingWeight != 1 || wr.InvalidWeight != 1 {
		t.Fatalf("unexpected weighted reliability counts: %+v", wr)
	}
	assertWeightedNear(t, "weighted reliability weightSum", wr.WeightSum, 26.0, 1e-6)
	assertWeightedNear(t, "weighted Cronbach alpha", *wr.Alpha, 0.9461370334, 1e-4)

	// 2. WLS Simple Linear Regression
	if report.Regression == nil || report.Regression.Weighted == nil || report.Regression.Weighted.Fit == nil || report.Regression.Weighted.Fit.RSquared == nil {
		t.Fatalf("expected weighted simple regression fit, got %+v", report.Regression)
	}
	wreg := report.Regression.Weighted
	if wreg.Included != 15 || wreg.MissingWeight != 1 || wreg.InvalidWeight != 1 {
		t.Fatalf("unexpected weighted regression counts: %+v", wreg)
	}
	assertWeightedNear(t, "WLS simple weightSum", wreg.WeightSum, 26.0, 1e-6)
	assertWeightedNear(t, "WLS simple intercept", wreg.Fit.Intercept, 0.7776700547, 1e-4)
	assertWeightedNear(t, "WLS simple slope", wreg.Fit.Slope, 0.8733232878, 1e-4)
	assertWeightedNear(t, "WLS simple R2", *wreg.Fit.RSquared, 0.7689597636, 1e-4)

	// 3. WLS Multiple Linear Regression
	if report.MultipleRegression == nil || report.MultipleRegression.Weighted == nil || report.MultipleRegression.Weighted.Fit == nil {
		t.Fatalf("expected weighted multiple regression fit, got %+v", report.MultipleRegression)
	}
	wmr := report.MultipleRegression.Weighted
	if wmr.Included != 15 || wmr.MissingWeight != 1 || wmr.InvalidWeight != 1 {
		t.Fatalf("unexpected weighted multiple regression counts: %+v", wmr)
	}
	assertWeightedNear(t, "WLS multi weightSum", wmr.WeightSum, 26.0, 1e-6)
	fit := wmr.Fit
	assertWeightedNear(t, "WLS multi intercept", fit.Intercept.Coefficient, -0.08461026, 1e-4)
	assertWeightedNear(t, "WLS multi intercept SE", fit.Intercept.StdError, 0.22124787, 1e-4)
	assertWeightedNear(t, "WLS multi intercept t", *fit.Intercept.TStat, -0.38242294, 1e-4)
	wantBetas := []float64{0.29438736, 0.72067538, 0.0801548}
	wantSEs := []float64{0.22384997, 0.08641789, 0.20106602}
	wantTs := []float64{1.31511013, 8.33942365, 0.39864916}
	for i := range wantBetas {
		assertWeightedNear(t, "WLS multi beta", fit.Predictors[i].Coefficient, wantBetas[i], 1e-4)
		assertWeightedNear(t, "WLS multi SE", fit.Predictors[i].StdError, wantSEs[i], 1e-4)
		assertWeightedNear(t, "WLS multi t", *fit.Predictors[i].TStat, wantTs[i], 1e-4)
	}
	assertWeightedNear(t, "WLS multi R2", *fit.RSquared, 0.9687707170, 1e-4)
	assertWeightedNear(t, "WLS multi adj R2", *fit.AdjustedRSquared, 0.9602536399, 1e-4)
	assertWeightedNear(t, "WLS multi F", *fit.FStat, 113.7445038482, 1e-4)

	// 4. Without weightColumn, weighted fields must be omitted for full backward compatibility.
	unweighted, err := Analyze(strings.NewReader(weightedSEMTestCSV), Options{
		Reliability: &ReliabilityOptions{Columns: []string{"x1", "x2", "x3", "m1"}},
		Regression:  &RegressionOptions{PredictorColumn: "x1", OutcomeColumn: "y1"},
		MultipleRegression: &MultipleRegressionOptions{
			OutcomeColumn:    "y1",
			PredictorColumns: []string{"x1", "m1", "x3"},
		},
	})
	if err != nil {
		t.Fatalf("unweighted Analyze failed: %v", err)
	}
	if unweighted.Reliability.Weighted != nil || unweighted.Regression.Weighted != nil || unweighted.MultipleRegression.Weighted != nil {
		t.Fatalf("expected nil weighted fields when weightColumn is empty")
	}
	assertWeightedNear(t, "unweighted Cronbach alpha", *unweighted.Reliability.Alpha, 0.9482248240, 1e-4)

	// 5. Fewer than 2 valid weighted rows omits reliability.weighted.
	sparseWeightCSV := "w,x1,x2\n1.5,1,2\n,3,4\n0,5,6\n"
	sparse, err := Analyze(strings.NewReader(sparseWeightCSV), Options{
		WeightColumn: "w",
		Reliability:  &ReliabilityOptions{Columns: []string{"x1", "x2"}},
	})
	if err != nil {
		t.Fatalf("sparse Analyze failed: %v", err)
	}
	if sparse.Reliability.Weighted != nil {
		t.Fatalf("expected nil reliability.weighted when valid weighted rows < 2, got %+v", sparse.Reliability.Weighted)
	}
}

func TestSEMPathModelMatchNumPyAndValidation(t *testing.T) {
	semOpts := &SEMOptions{
		Constructs: []CFAConstructOptions{
			{Name: "服务感知", Columns: []string{"x1", "x2", "x3"}},
			{Name: "品牌信任", Columns: []string{"m1", "m2", "m3"}},
			{Name: "复购意愿", Columns: []string{"y1", "y2", "y3"}},
		},
		Paths: []SEMPathOptions{
			{From: "服务感知", To: "品牌信任"},
			{From: "服务感知", To: "复购意愿"},
			{From: "品牌信任", To: "复购意愿"},
		},
	}

	report, err := Analyze(strings.NewReader(weightedSEMTestCSV), Options{SEM: semOpts})
	if err != nil {
		t.Fatalf("SEM Analyze failed: %v", err)
	}
	if report.SEM == nil || report.SEM.Result == nil {
		t.Fatalf("expected SEM result, got %+v", report.SEM)
	}
	if report.SEM.Included != 15 || report.SEM.Missing != 0 || report.SEM.NonNumeric != 0 {
		t.Fatalf("unexpected SEM counts: %+v", report.SEM)
	}
	res := report.SEM.Result
	if len(res.Constructs) != 3 || len(res.Paths) != 3 || len(res.Endogenous) != 2 {
		t.Fatalf("unexpected SEM shape: constructs=%d paths=%d endo=%d", len(res.Constructs), len(res.Paths), len(res.Endogenous))
	}

	// Measurement model CR/AVE match NumPy
	assertWeightedNear(t, "X CR", res.Constructs[0].CR, 0.98155840, 1e-4)
	assertWeightedNear(t, "X AVE", res.Constructs[0].AVE, 0.94666128, 1e-4)
	assertWeightedNear(t, "M CR", res.Constructs[1].CR, 0.98881851, 1e-4)
	assertWeightedNear(t, "M AVE", res.Constructs[1].AVE, 0.96719371, 1e-4)
	assertWeightedNear(t, "Y CR", res.Constructs[2].CR, 0.99366203, 1e-4)
	assertWeightedNear(t, "Y AVE", res.Constructs[2].AVE, 0.98122488, 1e-4)

	// Structural path betas, SE, t-stats match NumPy
	assertWeightedNear(t, "SEM X->M beta", res.Paths[0].Beta, 0.75217220, 1e-4)
	assertWeightedNear(t, "SEM X->M SE", res.Paths[0].StdError, 0.18276445, 1e-4)
	assertWeightedNear(t, "SEM X->M t", *res.Paths[0].TStat, 4.11552788, 1e-4)

	assertWeightedNear(t, "SEM X->Y beta", res.Paths[1].Beta, 0.43746957, 1e-4)
	assertWeightedNear(t, "SEM X->Y SE", res.Paths[1].StdError, 0.08132350, 1e-4)
	assertWeightedNear(t, "SEM X->Y t", *res.Paths[1].TStat, 5.37937474, 1e-4)

	assertWeightedNear(t, "SEM M->Y beta", res.Paths[2].Beta, 0.61032709, 1e-4)
	assertWeightedNear(t, "SEM M->Y SE", res.Paths[2].StdError, 0.08132350, 1e-4)
	assertWeightedNear(t, "SEM M->Y t", *res.Paths[2].TStat, 7.50492922, 1e-4)

	// Endogenous R² and Adjusted R² match NumPy
	assertWeightedNear(t, "SEM M R2", *res.Endogenous[0].RSquared, 0.56576302, 1e-4)
	assertWeightedNear(t, "SEM M adj R2", *res.Endogenous[0].AdjustedRSquared, 0.53236017, 1e-4)
	assertWeightedNear(t, "SEM Y R2", *res.Endogenous[1].RSquared, 0.96553803, 1e-4)
	assertWeightedNear(t, "SEM Y adj R2", *res.Endogenous[1].AdjustedRSquared, 0.95979436, 1e-4)
	if res.Endogenous[0].Included != 15 || res.Endogenous[1].Included != 15 {
		t.Fatalf("unexpected endogenous included counts: %+v", res.Endogenous)
	}

	// Reject cycles, self-loops, duplicate paths, and unknown constructs
	badCases := []struct {
		name  string
		paths []SEMPathOptions
	}{
		{"self_loop", []SEMPathOptions{{From: "服务感知", To: "服务感知"}}},
		{"two_cycle", []SEMPathOptions{{From: "服务感知", To: "品牌信任"}, {From: "品牌信任", To: "服务感知"}}},
		{"three_cycle", []SEMPathOptions{{From: "服务感知", To: "品牌信任"}, {From: "品牌信任", To: "复购意愿"}, {From: "复购意愿", To: "服务感知"}}},
		{"duplicate_edge", []SEMPathOptions{{From: "服务感知", To: "品牌信任"}, {From: "服务感知", To: "品牌信任"}}},
		{"unknown_target", []SEMPathOptions{{From: "服务感知", To: "不存在"}}},
		{"empty_paths", nil},
	}
	for _, tc := range badCases {
		_, err := Analyze(strings.NewReader(weightedSEMTestCSV), Options{
			SEM: &SEMOptions{Constructs: semOpts.Constructs, Paths: tc.paths},
		})
		if err == nil {
			t.Fatalf("expected SEM validation error for %s, got nil", tc.name)
		}
	}

	// Field mapping translates SEM construct columns while preserving path topology.
	mapped, _, err := MapSettings(
		Options{SEM: semOpts},
		[]string{"x1", "x2", "x3", "m1", "m2", "m3", "y1", "y2", "y3"},
		[]string{"nx1", "nx2", "nx3", "nm1", "nm2", "nm3", "ny1", "ny2", "ny3"},
		[]FieldPair{
			{From: "x1", To: "nx1"}, {From: "x2", To: "nx2"}, {From: "x3", To: "nx3"},
			{From: "m1", To: "nm1"}, {From: "m2", To: "nm2"}, {From: "m3", To: "nm3"},
			{From: "y1", To: "ny1"}, {From: "y2", To: "ny2"}, {From: "y3", To: "ny3"},
		},
		"",
	)
	if err != nil {
		t.Fatalf("MapSettings for SEM failed: %v", err)
	}
	if mapped.SEM == nil || len(mapped.SEM.Paths) != 3 || mapped.SEM.Constructs[0].Columns[0] != "nx1" {
		t.Fatalf("unexpected mapped SEM options: %+v", mapped.SEM)
	}
}
