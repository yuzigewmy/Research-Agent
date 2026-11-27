package survey

import (
	"math"
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestSampleSizeBaselineExamples(t *testing.T) {
	cases := []struct {
		name           string
		input          SampleSizeInput
		wantZ          float64
		wantUnadjusted int
		wantFPC        int
		wantPlanned    int
		wantPopNil     bool
	}{
		{
			name:           "95pct infinite population baseline",
			input:          DefaultSampleSizeInput(),
			wantZ:          1.959963984540054,
			wantUnadjusted: 385,
			wantFPC:        385,
			wantPlanned:    385,
			wantPopNil:     true,
		},
		{
			name: "95pct zero population treated as infinite",
			input: SampleSizeInput{
				ConfidenceLevel:    95,
				ExpectedProportion: 0.5,
				MarginOfError:      0.05,
				PopulationSize:     intPtr(0),
				ResponseRate:       1.0,
			},
			wantZ:          1.959963984540054,
			wantUnadjusted: 385,
			wantFPC:        385,
			wantPlanned:    385,
			wantPopNil:     true,
		},
		{
			name: "95pct finite population N=1000 and responseRate=0.8",
			input: SampleSizeInput{
				Method:             SampleSizeMethodCochran,
				ConfidenceLevel:    95,
				ExpectedProportion: 0.5,
				MarginOfError:      0.05,
				PopulationSize:     intPtr(1000),
				ResponseRate:       0.8,
			},
			wantZ:          1.959963984540054,
			wantUnadjusted: 385,
			wantFPC:        278,
			wantPlanned:    348,
		},
		{
			name: "90pct finite population N=1000 and responseRate=0.8",
			input: SampleSizeInput{
				ConfidenceLevel:    90,
				ExpectedProportion: 0.5,
				MarginOfError:      0.05,
				PopulationSize:     intPtr(1000),
				ResponseRate:       0.8,
			},
			wantZ:          1.6448536269514722,
			wantUnadjusted: 271,
			wantFPC:        214,
			wantPlanned:    268,
		},
		{
			name: "99pct finite population N=1000 and responseRate=0.8",
			input: SampleSizeInput{
				ConfidenceLevel:    99,
				ExpectedProportion: 0.5,
				MarginOfError:      0.05,
				PopulationSize:     intPtr(1000),
				ResponseRate:       0.8,
			},
			wantZ:          2.5758293035489004,
			wantUnadjusted: 664,
			wantFPC:        400,
			wantPlanned:    500,
		},
		{
			name: "95pct non-0.5 proportion p=0.3 e=0.04 N=500 r=0.75",
			input: SampleSizeInput{
				ConfidenceLevel:    95,
				ExpectedProportion: 0.3,
				MarginOfError:      0.04,
				PopulationSize:     intPtr(500),
				ResponseRate:       0.75,
			},
			wantZ:          1.959963984540054,
			wantUnadjusted: 505, // 3.84145882 * 0.21 / 0.0016 = 504.19147 -> 505
			wantFPC:        252, // 504.19147 / (1 + 503.19147/500) = 251.2937 -> 252
			wantPlanned:    336, // 252 / 0.75 = 336
		},
		{
			name: "minimum finite population N=1",
			input: SampleSizeInput{
				ConfidenceLevel:    95,
				ExpectedProportion: 0.5,
				MarginOfError:      0.05,
				PopulationSize:     intPtr(1),
				ResponseRate:       1.0,
			},
			wantZ:          1.959963984540054,
			wantUnadjusted: 385,
			wantFPC:        1,
			wantPlanned:    1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, got, err := CalculateSampleSize(tc.input)
			if err != nil {
				t.Fatalf("CalculateSampleSize failed: %v", err)
			}
			if normalized.Method != SampleSizeMethodCochran || got.Method != SampleSizeMethodCochran {
				t.Fatalf("method = %q / %q, want %q", normalized.Method, got.Method, SampleSizeMethodCochran)
			}
			if tc.wantPopNil && normalized.PopulationSize != nil {
				t.Fatalf("expected nil normalized PopulationSize, got %d", *normalized.PopulationSize)
			}
			if math.Abs(got.ZScore-tc.wantZ) > 1e-15 {
				t.Fatalf("zScore = %.16f, want %.16f", got.ZScore, tc.wantZ)
			}
			if got.UnadjustedSampleSize != tc.wantUnadjusted ||
				got.FinitePopulationAdjusted != tc.wantFPC ||
				got.PlannedInvitations != tc.wantPlanned {
				t.Fatalf("got (n0=%d, fpc=%d, planned=%d), want (%d, %d, %d)",
					got.UnadjustedSampleSize, got.FinitePopulationAdjusted, got.PlannedInvitations,
					tc.wantUnadjusted, tc.wantFPC, tc.wantPlanned)
			}
			if !strings.Contains(got.FormulaSummary, SampleSizeMethodCochran) {
				t.Fatalf("formula summary missing method identifier: %s", got.FormulaSummary)
			}
		})
	}
}

func TestSampleSizeRejectsInvalidInputs(t *testing.T) {
	base := DefaultSampleSizeInput()
	cases := []struct {
		name  string
		input SampleSizeInput
	}{
		{"unsupported method", func() SampleSizeInput { c := base; c.Method = "unknown"; return c }()},
		{"invalid confidence 0", func() SampleSizeInput { c := base; c.ConfidenceLevel = 0; return c }()},
		{"invalid confidence 80", func() SampleSizeInput { c := base; c.ConfidenceLevel = 80; return c }()},
		{"proportion 0", func() SampleSizeInput { c := base; c.ExpectedProportion = 0; return c }()},
		{"proportion 1", func() SampleSizeInput { c := base; c.ExpectedProportion = 1; return c }()},
		{"proportion negative", func() SampleSizeInput { c := base; c.ExpectedProportion = -0.2; return c }()},
		{"proportion NaN", func() SampleSizeInput { c := base; c.ExpectedProportion = math.NaN(); return c }()},
		{"margin 0", func() SampleSizeInput { c := base; c.MarginOfError = 0; return c }()},
		{"margin 0.5", func() SampleSizeInput { c := base; c.MarginOfError = 0.5; return c }()},
		{"margin too large", func() SampleSizeInput { c := base; c.MarginOfError = 0.6; return c }()},
		{"margin tiny overflow", func() SampleSizeInput { c := base; c.MarginOfError = 1e-9; return c }()},
		{"population negative", func() SampleSizeInput { c := base; c.PopulationSize = intPtr(-1); return c }()},
		{"population above limit", func() SampleSizeInput { c := base; c.PopulationSize = intPtr(1_000_000_001); return c }()},
		{"response rate 0", func() SampleSizeInput { c := base; c.ResponseRate = 0; return c }()},
		{"response rate above 1", func() SampleSizeInput { c := base; c.ResponseRate = 1.01; return c }()},
		{"response rate Inf", func() SampleSizeInput { c := base; c.ResponseRate = math.Inf(1); return c }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := CalculateSampleSize(tc.input); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestBuildAnalysisPlanSpecValidation(t *testing.T) {
	valid := AnalysisPlanSpec{
		Title:            "  春季校园调研样本与分析计划  ",
		Objective:        "评估学生对图书馆延时的满意度差异",
		TargetPopulation: "在校本科生与研究生",
		SamplingMethod:   "分层随机抽样",
		PlannedMethods:   []string{"描述统计", "交叉统计", "描述统计", "量表信度（Cronbach α）"},
		MissingStrategy:  "必答列缺失超过 20% 判为无效",
		Notes:            "优先覆盖高年级样本",
		SampleSizeInput: SampleSizeInput{
			ConfidenceLevel:    95,
			ExpectedProportion: 0.5,
			MarginOfError:      0.05,
			PopulationSize:     intPtr(1000),
			ResponseRate:       0.8,
		},
	}
	spec, err := BuildAnalysisPlanSpec(valid)
	if err != nil {
		t.Fatalf("BuildAnalysisPlanSpec failed: %v", err)
	}
	if spec.Title != "春季校园调研样本与分析计划" {
		t.Fatalf("title not trimmed: %q", spec.Title)
	}
	if len(spec.PlannedMethods) != 3 {
		t.Fatalf("expected deduplicated 3 methods, got %v", spec.PlannedMethods)
	}
	if spec.SampleSizeResult.UnadjustedSampleSize != 385 ||
		spec.SampleSizeResult.FinitePopulationAdjusted != 278 ||
		spec.SampleSizeResult.PlannedInvitations != 348 {
		t.Fatalf("unexpected sample size result: %+v", spec.SampleSizeResult)
	}

	missingTitle := valid
	missingTitle.Title = "   "
	if _, err := BuildAnalysisPlanSpec(missingTitle); err == nil {
		t.Fatal("expected error for blank title")
	}

	noMethods := valid
	noMethods.PlannedMethods = nil
	if _, err := BuildAnalysisPlanSpec(noMethods); err == nil {
		t.Fatal("expected error for empty planned methods")
	}

	nulObjective := valid
	nulObjective.Objective = "bad\x00text"
	if _, err := BuildAnalysisPlanSpec(nulObjective); err == nil {
		t.Fatal("expected error for NUL byte in objective")
	}
}
