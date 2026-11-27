package survey

import (
	"math"
	"strings"
	"testing"
)

func assertNear(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > tol {
		t.Fatalf("%s: got %.8f, want %.8f (tol %.1e)", name, got, want, tol)
	}
}

func TestMultipleRegressionMatchesNumPyBenchmark(t *testing.T) {
	// Independent NumPy OLS benchmark (10 valid complete rows + 1 missing + 1 non-numeric + 1 invalid row):
	// beta = [1.21929379, 1.58016949, 1.20353107]
	// se   = [0.36070837, 0.15523350, 0.14668187]
	// t    = [3.38027582, 10.17930713, 8.20504306]
	// R2   = 0.99701669, adjR2 = 0.99616432, F = 1169.69363355, df = (2, 7)
	csvData := strings.Join([]string{
		"Y,X1,X2,_quality_status",
		"5.2,1,2,",
		"6.1,2,1,",
		"10.4,3,4,",
		"11.2,4,3,",
		"16.5,5,6,",
		"16.8,6,5,",
		"22.7,7,9,",
		"21.5,8,7,",
		"25.0,9,8,",
		"31.1,10,11,",
		",5,5,",
		"12.0,bad,3,",
		"99.0,1,1,invalid",
	}, "\n")

	report, err := Analyze(strings.NewReader(csvData), Options{
		MultipleRegression: &MultipleRegressionOptions{
			OutcomeColumn:    "y",
			PredictorColumns: []string{" x1 ", "X2"},
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	mr := report.MultipleRegression
	if mr == nil || mr.Fit == nil {
		t.Fatalf("expected multiple regression fit, got %+v", mr)
	}
	if mr.Included != 10 || mr.Missing != 1 || mr.NonNumeric != 1 {
		t.Fatalf("counts got (%d,%d,%d), want (10,1,1)", mr.Included, mr.Missing, mr.NonNumeric)
	}
	if mr.OutcomeColumn != "Y" || len(mr.PredictorColumns) != 2 || mr.PredictorColumns[0] != "X1" || mr.PredictorColumns[1] != "X2" {
		t.Fatalf("unexpected canonical column names: %+v", mr.MultipleRegressionOptions)
	}
	fit := mr.Fit
	assertNear(t, "intercept", fit.Intercept.Coefficient, 1.21929379, 1e-4)
	assertNear(t, "intercept.se", fit.Intercept.StdError, 0.36070837, 1e-4)
	if fit.Intercept.TStat == nil {
		t.Fatalf("expected intercept tStat")
	}
	assertNear(t, "intercept.t", *fit.Intercept.TStat, 3.38027582, 1e-4)

	assertNear(t, "x1.coef", fit.Predictors[0].Coefficient, 1.58016949, 1e-4)
	assertNear(t, "x1.se", fit.Predictors[0].StdError, 0.15523350, 1e-4)
	assertNear(t, "x1.t", *fit.Predictors[0].TStat, 10.17930713, 1e-4)

	assertNear(t, "x2.coef", fit.Predictors[1].Coefficient, 1.20353107, 1e-4)
	assertNear(t, "x2.se", fit.Predictors[1].StdError, 0.14668187, 1e-4)
	assertNear(t, "x2.t", *fit.Predictors[1].TStat, 8.20504306, 1e-4)

	if fit.RSquared == nil || fit.AdjustedRSquared == nil || fit.FStat == nil {
		t.Fatalf("expected R2, adjR2, FStat: %+v", fit)
	}
	assertNear(t, "R2", *fit.RSquared, 0.99701669, 1e-4)
	assertNear(t, "adjR2", *fit.AdjustedRSquared, 0.99616432, 1e-4)
	assertNear(t, "FStat", *fit.FStat, 1169.69363355, 1e-3)
	if fit.DFModel != 2 || fit.DFResidual != 7 {
		t.Fatalf("df got (%d,%d), want (2,7)", fit.DFModel, fit.DFResidual)
	}
}

func TestMultipleRegressionHandlesCollinearityAndSmallSample(t *testing.T) {
	collinearCSV := "Y,X1,X2\n1,1,2\n2,2,4\n3,3,6\n4,4,8\n5,5,10\n"
	report, err := Analyze(strings.NewReader(collinearCSV), Options{
		MultipleRegression: &MultipleRegressionOptions{OutcomeColumn: "Y", PredictorColumns: []string{"X1", "X2"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.MultipleRegression == nil || report.MultipleRegression.Fit != nil || report.MultipleRegression.UnavailableReason != "COLLINEAR_PREDICTORS" {
		t.Fatalf("expected COLLINEAR_PREDICTORS, got %+v", report.MultipleRegression)
	}

	smallCSV := "Y,X1,X2\n1,1,2\n2,2,1\n3,3,4\n"
	report, err = Analyze(strings.NewReader(smallCSV), Options{
		MultipleRegression: &MultipleRegressionOptions{OutcomeColumn: "Y", PredictorColumns: []string{"X1", "X2"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.MultipleRegression.UnavailableReason != "INSUFFICIENT_ROWS" {
		t.Fatalf("expected INSUFFICIENT_ROWS, got %+v", report.MultipleRegression)
	}
}

const factorBenchmarkCSV = `Q1,Q2,Q3,Q4,Q5,Q6
5,5,4,2,1,2
4,5,5,1,2,2
5,4,5,3,2,3
3,4,3,2,3,2
2,2,1,5,5,4
1,2,2,4,5,5
2,1,2,5,4,5
3,2,3,4,4,3
5,5,5,5,4,5
4,4,5,4,5,4
1,1,2,1,2,1
2,1,1,2,1,2
4,5,4,3,3,2
3,3,4,5,4,5
5,4,4,2,2,1
2,3,2,4,5,4
4,3,4,1,2,1
3,4,4,5,5,4
1,2,1,3,4,3
5,4,5,4,3,4
,5,5,4,3,4
4,bad,5,4,3,4
`

func TestFactorAnalysisMatchesNumPySciPyBenchmark(t *testing.T) {
	// Independent NumPy/SciPy reference benchmark on 20 complete rows:
	// KMO = 0.51996367
	// MSA = [0.45004225, 0.68891588, 0.56651276, 0.43773815, 0.51157817, 0.55701645]
	// Bartlett chi2 = 107.77518303, df = 15, p = 4.30611241e-16
	// Eigenvalues = [3.07388199, 2.29726179, 0.32137577, 0.15467604, 0.12342562, 0.02937880] (m=2)
	// Rotated Varimax loadings:
	//   Q1: [0.94562360, -0.16050232], comm: 0.91996498
	//   Q2: [0.92502291, -0.04076503], comm: 0.85732917
	//   Q3: [0.95451182, -0.00873579], comm: 0.91116913
	//   Q4: [-0.00038819, 0.96489441], comm: 0.93102137
	//   Q5: [-0.19112775, 0.89741024], comm: 0.84187496
	//   Q6: [-0.01929372, 0.95363092], comm: 0.90978418
	report, err := Analyze(strings.NewReader(factorBenchmarkCSV), Options{
		FactorAnalysis: &FactorAnalysisOptions{
			Columns: []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6"},
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	fa := report.FactorAnalysis
	if fa == nil || fa.Result == nil {
		t.Fatalf("expected factor analysis result, got %+v", fa)
	}
	if fa.Included != 20 || fa.Missing != 1 || fa.NonNumeric != 1 {
		t.Fatalf("counts got (%d,%d,%d), want (20,1,1)", fa.Included, fa.Missing, fa.NonNumeric)
	}
	res := fa.Result
	assertNear(t, "KMO", res.KMO, 0.51996367, 1e-4)
	assertNear(t, "BartlettChi2", res.BartlettChiSquare, 107.77518303, 1e-4)
	if res.BartlettDF != 15 {
		t.Fatalf("BartlettDF got %d, want 15", res.BartlettDF)
	}
	assertNear(t, "BartlettP", res.BartlettPValue, 4.30611241e-16, 1e-12)
	if res.FactorCount != 2 || !res.AutoFactors {
		t.Fatalf("expected 2 auto factors, got %d (auto=%v)", res.FactorCount, res.AutoFactors)
	}

	wantMSA := []float64{0.45004225, 0.68891588, 0.56651276, 0.43773815, 0.51157817, 0.55701645}
	wantComm := []float64{0.91996498, 0.85732917, 0.91116913, 0.93102137, 0.84187496, 0.90978418}
	wantLoadings := [][2]float64{
		{0.94562360, -0.16050232},
		{0.92502291, -0.04076503},
		{0.95451182, -0.00873579},
		{-0.00038819, 0.96489441},
		{-0.19112775, 0.89741024},
		{-0.01929372, 0.95363092},
	}
	wantEig := []float64{3.07388199, 2.29726179, 0.32137577, 0.15467604, 0.12342562, 0.02937880}
	for i, w := range wantEig {
		assertNear(t, "eigenvalue", res.Eigenvalues[i], w, 1e-4)
	}
	for i, v := range res.Variables {
		assertNear(t, v.Column+".MSA", v.MSA, wantMSA[i], 1e-4)
		assertNear(t, v.Column+".Comm", v.Communality, wantComm[i], 1e-4)
		assertNear(t, v.Column+".L1", v.Loadings[0], wantLoadings[i][0], 1e-4)
		assertNear(t, v.Column+".L2", v.Loadings[1], wantLoadings[i][1], 1e-4)
	}
}

func TestKanoModelClassificationAndBetterWorse(t *testing.T) {
	// 10 valid responses:
	// 1: 很喜欢, 无所谓 -> A
	// 2: 1, 3 -> A
	// 3: 喜欢, 不喜欢 -> O
	// 4: 1, 5 -> O
	// 5: 1, 5 -> O
	// 6: 理所当然, 很不喜欢 -> M
	// 7: 2, 5 -> M
	// 8: 无所谓, 无所谓 -> I
	// 9: 不喜欢, 很喜欢 -> R
	// 10: 1, 1 -> Q
	// + 1 missing, + 1 invalid text
	// A=2, O=3, M=2, I=1, R=1, Q=1 -> Dominant = O
	// Better = (2+3)/(2+3+2+1) = 5/8 = 0.625
	// Worse  = -(3+2)/(2+3+2+1) = -5/8 = -0.625
	csvData := strings.Join([]string{
		"Pos,Neg",
		"很喜欢,无所谓",
		"1,3",
		"喜欢,不喜欢",
		"1,5",
		"1,5",
		"理所当然,很不喜欢",
		"2,5",
		"无所谓,无所谓",
		"不喜欢,很喜欢",
		"1,1",
		",无所谓",
		"未知选项,5",
	}, "\n")

	report, err := Analyze(strings.NewReader(csvData), Options{
		Kano: &KanoOptions{FunctionalColumn: "Pos", DysfunctionalColumn: "Neg"},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	kano := report.Kano
	if kano == nil || kano.Result == nil {
		t.Fatalf("expected kano result, got %+v", kano)
	}
	if kano.Included != 10 || kano.Missing != 1 || kano.Invalid != 1 {
		t.Fatalf("counts got (%d,%d,%d), want (10,1,1)", kano.Included, kano.Missing, kano.Invalid)
	}
	if kano.Result.DominantCode != "O" {
		t.Fatalf("dominant got %q, want O", kano.Result.DominantCode)
	}
	wantCounts := map[string]int{"M": 2, "O": 3, "A": 2, "I": 1, "R": 1, "Q": 1}
	for _, cat := range kano.Result.Categories {
		if cat.Count != wantCounts[cat.Code] {
			t.Fatalf("category %s count got %d, want %d", cat.Code, cat.Count, wantCounts[cat.Code])
		}
	}
	if kano.Result.Better == nil || kano.Result.Worse == nil {
		t.Fatalf("expected Better and Worse")
	}
	assertNear(t, "Better", *kano.Result.Better, 0.625, 1e-6)
	assertNear(t, "Worse", *kano.Result.Worse, -0.625, 1e-6)
}

func TestPSMPriceSensitivityIntersections(t *testing.T) {
	// Symmetric 4-row dataset around 50:
	// TC=[20,30,20,30], C=[35,45,35,45], E=[55,65,55,65], TE=[70,80,70,80]
	// Plus 1 missing, 1 negative (nonNumeric), 1 non-monotonic (inconsistent).
	// Exact intersections: PMC=35, PME=65, OPP=50, IDP=50, range=[35, 65].
	csvData := strings.Join([]string{
		"TC,C,E,TE",
		"20,35,55,70",
		"30,45,65,80",
		"20,35,55,70",
		"30,45,65,80",
		",35,55,70",
		"-5,35,55,70",
		"50,40,60,70",
	}, "\n")

	report, err := Analyze(strings.NewReader(csvData), Options{
		PSM: &PSMOptions{
			TooCheapColumn:     "TC",
			CheapColumn:        "C",
			ExpensiveColumn:    "E",
			TooExpensiveColumn: "TE",
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	psm := report.PSM
	if psm == nil || psm.Result == nil {
		t.Fatalf("expected PSM result, got %+v", psm)
	}
	if psm.Included != 4 || psm.Missing != 1 || psm.NonNumeric != 1 || psm.Inconsistent != 1 {
		t.Fatalf("counts got (%d,%d,%d,%d), want (4,1,1,1)", psm.Included, psm.Missing, psm.NonNumeric, psm.Inconsistent)
	}
	assertNear(t, "PMC", psm.Result.PMC, 35.0, 1e-4)
	assertNear(t, "PME", psm.Result.PME, 65.0, 1e-4)
	assertNear(t, "OPP", psm.Result.OPP, 50.0, 1e-4)
	assertNear(t, "IDP", psm.Result.IDP, 50.0, 1e-4)
	assertNear(t, "RangeLow", psm.Result.AcceptableRange[0], 35.0, 1e-4)
	assertNear(t, "RangeHigh", psm.Result.AcceptableRange[1], 65.0, 1e-4)
}

func TestConjointAnalysisMatchesNumPyBenchmark(t *testing.T) {
	// Independent NumPy benchmark (12 valid rows + 1 missing + 1 non-numeric):
	// intercept = 5.91666667
	// Brand part-worths (A,B,C) = [1.58333333, 0.08333333, -1.66666667], range = 3.25, imp = 53.42465753%
	// Price part-worths (High,Low) = [-1.41666667, 1.41666667], range = 2.83333333, imp = 46.57534247%
	// R2 = 0.93459552
	csvData := strings.Join([]string{
		"Brand,Price,Rating",
		"A,Low,8.5",
		"A,Low,9.5",
		"A,High,5.5",
		"A,High,6.5",
		"B,Low,7.0",
		"B,Low,8.0",
		"B,High,4.0",
		"B,High,5.0",
		"C,Low,6.0",
		"C,Low,5.0",
		"C,High,2.5",
		"C,High,3.5",
		"A,,7.0",
		"B,Low,bad",
	}, "\n")

	report, err := Analyze(strings.NewReader(csvData), Options{
		Conjoint: &ConjointOptions{
			RatingColumn:     "Rating",
			AttributeColumns: []string{"Brand", "Price"},
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	cj := report.Conjoint
	if cj == nil || cj.Result == nil {
		t.Fatalf("expected conjoint result, got %+v", cj)
	}
	if cj.Included != 12 || cj.Missing != 1 || cj.NonNumeric != 1 {
		t.Fatalf("counts got (%d,%d,%d), want (12,1,1)", cj.Included, cj.Missing, cj.NonNumeric)
	}
	res := cj.Result
	assertNear(t, "intercept", res.Intercept, 5.91666667, 1e-4)
	if res.RSquared == nil {
		t.Fatalf("expected R2")
	}
	assertNear(t, "R2", *res.RSquared, 0.93459552, 1e-4)

	brand := res.Attributes[0]
	assertNear(t, "Brand.A", brand.Levels[0].PartWorth, 1.58333333, 1e-4)
	assertNear(t, "Brand.B", brand.Levels[1].PartWorth, 0.08333333, 1e-4)
	assertNear(t, "Brand.C", brand.Levels[2].PartWorth, -1.66666667, 1e-4)
	assertNear(t, "Brand.Range", brand.Range, 3.25, 1e-4)
	assertNear(t, "Brand.Imp", brand.ImportancePercent, 53.42465753, 1e-4)

	price := res.Attributes[1]
	assertNear(t, "Price.High", price.Levels[0].PartWorth, -1.41666667, 1e-4)
	assertNear(t, "Price.Low", price.Levels[1].PartWorth, 1.41666667, 1e-4)
	assertNear(t, "Price.Range", price.Range, 2.83333333, 1e-4)
	assertNear(t, "Price.Imp", price.ImportancePercent, 46.57534247, 1e-4)
}

func TestCFAConvergentAndDiscriminantValidityMatchesNumPyBenchmark(t *testing.T) {
	// Independent NumPy benchmark on factorBenchmarkCSV (20 valid complete rows):
	// C1 (Q1,Q2,Q3): loadings=[0.95096297, 0.93021410, 0.95288928], CR=0.96141233, AVE=0.89254227, sqrtAVE=0.94474455
	// C2 (Q4,Q5,Q6): loadings=[0.96464877, 0.90744595, 0.95186370], CR=0.95911452, AVE=0.88668330, sqrtAVE=0.94163862
	// Inter-construct corr = -0.14618759 -> DiscriminantValid = true
	report, err := Analyze(strings.NewReader(factorBenchmarkCSV), Options{
		CFA: &CFAOptions{
			Constructs: []CFAConstructOptions{
				{Name: "易用性", Columns: []string{"Q1", "Q2", "Q3"}},
				{Name: "价值感", Columns: []string{"Q4", "Q5", "Q6"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	cfa := report.CFA
	if cfa == nil || cfa.Result == nil {
		t.Fatalf("expected CFA result, got %+v", cfa)
	}
	if cfa.Included != 20 || cfa.Missing != 1 || cfa.NonNumeric != 1 {
		t.Fatalf("counts got (%d,%d,%d), want (20,1,1)", cfa.Included, cfa.Missing, cfa.NonNumeric)
	}
	res := cfa.Result
	c1, c2 := res.Constructs[0], res.Constructs[1]
	assertNear(t, "C1.Q1", c1.Items[0].Loading, 0.95096297, 1e-4)
	assertNear(t, "C1.Q2", c1.Items[1].Loading, 0.93021410, 1e-4)
	assertNear(t, "C1.Q3", c1.Items[2].Loading, 0.95288928, 1e-4)
	assertNear(t, "C1.CR", c1.CR, 0.96141233, 1e-4)
	assertNear(t, "C1.AVE", c1.AVE, 0.89254227, 1e-4)
	assertNear(t, "C1.SqrtAVE", c1.SqrtAVE, 0.94474455, 1e-4)
	if !c1.ConvergentValid || !c1.DiscriminantValid {
		t.Fatalf("expected C1 valid, got %+v", c1)
	}

	assertNear(t, "C2.Q4", c2.Items[0].Loading, 0.96464877, 1e-4)
	assertNear(t, "C2.Q5", c2.Items[1].Loading, 0.90744595, 1e-4)
	assertNear(t, "C2.Q6", c2.Items[2].Loading, 0.95186370, 1e-4)
	assertNear(t, "C2.CR", c2.CR, 0.95911452, 1e-4)
	assertNear(t, "C2.AVE", c2.AVE, 0.88668330, 1e-4)
	assertNear(t, "C2.SqrtAVE", c2.SqrtAVE, 0.94163862, 1e-4)
	if !c2.ConvergentValid || !c2.DiscriminantValid || !res.DiscriminantValid {
		t.Fatalf("expected C2 and overall discriminant valid, got %+v", res)
	}
	assertNear(t, "Corr(C1,C2)", res.CorrelationMatrix[0][1], -0.14618759, 1e-4)
}

func TestMapSettingsCoversAllAdvancedStats(t *testing.T) {
	source := Options{
		MultipleRegression: &MultipleRegressionOptions{OutcomeColumn: "Y", PredictorColumns: []string{"X1", "X2"}},
		FactorAnalysis:     &FactorAnalysisOptions{Columns: []string{"Q1", "Q2", "Q3"}, Factors: 2},
		Kano:               &KanoOptions{FunctionalColumn: "Pos", DysfunctionalColumn: "Neg"},
		PSM:                &PSMOptions{TooCheapColumn: "TC", CheapColumn: "C", ExpensiveColumn: "E", TooExpensiveColumn: "TE"},
		Conjoint:           &ConjointOptions{RatingColumn: "Rating", AttributeColumns: []string{"Brand", "Price"}},
		CFA: &CFAOptions{Constructs: []CFAConstructOptions{
			{Name: "A", Columns: []string{"Q1", "Q2"}},
			{Name: "B", Columns: []string{"X1", "X2"}},
		}},
	}
	srcCols := []string{"Y", "X1", "X2", "Q1", "Q2", "Q3", "Pos", "Neg", "TC", "C", "E", "TE", "Rating", "Brand", "Price"}
	dstCols := []string{"Y2", "X1_2", "X2_2", "Q1_2", "Q2_2", "Q3_2", "Pos2", "Neg2", "TC2", "C2", "E2", "TE2", "Rating2", "Brand2", "Price2"}
	pairs := make([]FieldPair, len(srcCols))
	for i := range srcCols {
		pairs[i] = FieldPair{From: srcCols[i], To: dstCols[i]}
	}
	mapped, confirmed, err := MapSettings(source, srcCols, dstCols, pairs, "")
	if err != nil {
		t.Fatalf("MapSettings failed: %v", err)
	}
	if len(confirmed) != len(srcCols) {
		t.Fatalf("confirmed len got %d, want %d", len(confirmed), len(srcCols))
	}
	if mapped.MultipleRegression.OutcomeColumn != "Y2" || mapped.FactorAnalysis.Factors != 2 ||
		mapped.Kano.FunctionalColumn != "Pos2" || mapped.PSM.TooExpensiveColumn != "TE2" ||
		mapped.Conjoint.RatingColumn != "Rating2" || mapped.CFA.Constructs[1].Columns[1] != "X2_2" {
		t.Fatalf("unexpected mapped options: %+v", mapped)
	}
}
