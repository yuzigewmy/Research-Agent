package survey

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestAnalyzeWeightedMeanUsesNumericResponsesAndValidWeights(t *testing.T) {
	csv := "Score,Weight,_quality_status\n1e308,0.1,\n1,0.1,\n-1e308,0.1,\n,2,\nNaN,3,\n99,0,\n99,1,held\n"
	report, err := Analyze(strings.NewReader(csv), Options{WeightColumn: "Weight"})
	if err != nil {
		t.Fatal(err)
	}
	mean := report.Summaries[0].Weighted
	if mean == nil || *mean != (WeightedSummary{Included: 3, TotalWeight: "0.3", Mean: 1.0 / 3}) {
		t.Fatalf("weighted summary: %+v", mean)
	}
	if report.Summaries[0].Numeric != 4 || report.Weighting.TotalWeight != "5.3" {
		t.Fatalf("incorrect sample counts: %+v", report)
	}
}

func TestAnalyzeMapsCategoriesForFrequenciesAndCrossTab(t *testing.T) {
	csv := "站点,得分,权重,_quality_status\n北岸,2,0.1,\n北岸站,5,0.2,held\n南岸,5,1,\n北岸站,9,5,invalid\n北岸,2,9,\n"
	options := Options{
		DuplicateColumns: []string{"站点", "得分"},
		Reviews:          []Review{{Row: 2, Accept: true, Reason: "核对原件"}},
		WeightColumn:     "权重",
		CrossTab:         &CrossTabOptions{RowColumn: "站点", ColumnColumn: "得分"},
		CategoryMappings: []CategoryMapping{{Column: "站点", From: " 北岸站 ", To: "北岸"}, {Column: "得分", From: "5", To: "五分"}},
	}
	report, err := Analyze(strings.NewReader(csv), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid != 3 || report.Invalid != 2 || !slices.Equal(report.Summaries[0].Frequencies, []Frequency{{Value: "北岸", Count: 2}, {Value: "南岸", Count: 1}}) {
		t.Fatalf("unexpected quality counts or frequencies: %+v", report)
	}
	if summary := report.Summaries[1]; summary.Numeric != 3 || *summary.Mean != 4 || summary.Weighted.Included != 3 {
		t.Fatalf("unexpected numeric summary: %+v", summary)
	}
	tab := report.CrossTab
	if !slices.Equal(tab.RowLabels, []string{"北岸", "南岸"}) || !slices.Equal(tab.ColumnLabels, []string{"2", "五分"}) ||
		!slices.Equal(tab.RowTotals, []int{2, 1}) || !slices.Equal(tab.Weighted.RowTotals, []string{"0.3", "1"}) {
		t.Fatalf("unexpected mapped cross-tab: %+v", tab)
	}
	options.CategoryMappings = append(options.CategoryMappings, CategoryMapping{Column: "站点", From: "北岸站", To: "上游"})
	if _, err := Analyze(strings.NewReader(csv), options); err == nil {
		t.Fatal("accepted duplicate mappings after trimming")
	}
}

func TestAnalyze(t *testing.T) {
	csv := "\ufeff\"Score\",Choice,Hours,Scale,Notes,FullName,e-mail,Respondent ID,手机号码,_quality_status,__synthetic__\r\n" +
		"10,Beta,1,4,\"two\nlines\",Example A,a@example.test,1,123,,true\n" +
		"10,Beta,2,4,copy,Example B,b@example.test,2,234,,true\n" +
		"6,Alpha,3,5,,Example C,c@example.test,3,345,held,true\n" +
		"2,Gamma,,,other,Example D,d@example.test,4,456,held,true\n" +
		"8,Beta,4,5,other,Example E,e@example.test,5,567,invalid,true\n" +
		"4,Alpha,2,6,other,Example F,f@example.test,6,678,held,true\n" +
		"7,Beta,3,6,hold,Example G,g@example.test,7,789,PENDING,true\n" +
		"2,Beta,NaN,0,one\n" +
		"0, Alpha ,5,1,other\n"
	options := Options{
		RequiredColumns:  []string{"Score", "Choice", "Hours", "Scale", "Notes"},
		DuplicateColumns: []string{"Score", "Choice"},
		Reviews:          []Review{{Row: 3, Accept: true, Reason: "Confirmed"}, {Row: 6, Reason: "Rejected"}},
	}
	got, err := Analyze(strings.NewReader(csv), options)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 9 || got.Valid != 4 || got.Invalid != 4 || got.Pending != 1 || !got.Demo || got.Columns[0] != "Score" {
		t.Fatalf("unexpected report counts or metadata: %+v", got)
	}
	for i, want := range []string{"VALID", "INVALID", "VALID", "INVALID", "INVALID", "INVALID", "PENDING", "VALID", "VALID"} {
		row := got.Rows[i]
		if row.Row != i+1 || row.Status != want || row.Reasons == nil {
			t.Fatalf("row %d = %+v, want %s with initialized reasons", i+1, row, want)
		}
	}
	if !slices.Contains(got.Rows[2].Reasons, "Confirmed") || !slices.Contains(got.Rows[5].Reasons, "Rejected") {
		t.Fatalf("review reasons were lost: %+v", got.Rows)
	}
	var columns []string
	for _, summary := range got.Summaries {
		columns = append(columns, summary.Column)
	}
	if !slices.Equal(columns, []string{"Score", "Choice", "Hours", "Scale", "Notes"}) {
		t.Fatalf("summary columns = %v", columns)
	}
	score, choice, hours, notes := got.Summaries[0], got.Summaries[1], got.Summaries[2], got.Summaries[4]
	if score.Numeric != 4 || score.Mean == nil || *score.Mean != 4.5 || score.Min == nil || *score.Min != 0 || score.Max == nil || *score.Max != 10 {
		t.Fatalf("unexpected score statistics: %+v", score)
	}
	if hours.Numeric != 3 || hours.Answered != 4 || notes.Answered != 3 || notes.Missing != 1 || choice.Mean != nil {
		t.Fatalf("unexpected answered, missing or numeric counts: %+v", got.Summaries)
	}
	if !slices.Equal(choice.Frequencies, []Frequency{{Value: "Alpha", Count: 2}, {Value: "Beta", Count: 2}}) {
		t.Fatalf("unexpected choice frequencies: %+v", choice.Frequencies)
	}
	if body, err := json.Marshal(got); err != nil || strings.Contains(string(body), "@example.test") {
		t.Fatalf("report cannot be safely encoded: %v, %s", err, body)
	}

	for _, review := range []Review{{Row: 1, Accept: true, Reason: "Already valid"}, {Row: 4, Accept: true, Reason: "Missing"}, {Row: 7, Accept: true, Reason: " "}, {Row: 10, Reason: "Unknown"}} {
		options.Reviews = []Review{review}
		if _, err := Analyze(strings.NewReader(csv), options); err == nil {
			t.Errorf("accepted invalid review: %+v", review)
		}
	}
	if _, err := Analyze(strings.NewReader("Answer\n"+strings.Repeat("x\n", 10001)), Options{}); err == nil {
		t.Error("accepted 10001 responses")
	}
	manyValues := "Answer\n" + strings.Join(strings.Split("zyxwvutsrqponmlkjihgfedcba", ""), "\n") + "\nz\n" + strings.Repeat("a", 129) + "\n"
	got, err = Analyze(strings.NewReader(manyValues), Options{})
	if err != nil {
		t.Fatal(err)
	}
	freq := got.Summaries[0].Frequencies
	if len(freq) != 20 || freq[0] != (Frequency{Value: "z", Count: 2}) || freq[1].Value != "a" || freq[19].Value != "s" || got.Summaries[0].Answered != 28 {
		t.Fatalf("unexpected capped frequencies or answer count: %+v", got.Summaries[0])
	}
}

func TestAnalyzeReliabilityEdges(t *testing.T) {
	options := Options{Reliability: &ReliabilityOptions{Columns: []string{"A", "B", "C"}}}
	for _, tt := range []struct {
		name, rows string
		alpha      bool
		negative   bool
	}{
		{name: "one response", rows: "1,2,3\n"},
		{name: "constant total", rows: "1,3,0\n2,2,0\n3,1,0\n"},
		{name: "constant total with cancellation", rows: "0,1,-1\n1e30,-1e30,0\n"},
		{name: "finite items with overflowing raw totals", rows: "1e308,1e308,1e308\n1.5e308,1.5e308,1.5e308\n", alpha: true},
		{name: "finite items with negative alpha", rows: "-1e308,1.2e308,0\n1e308,-1e308,0\n", alpha: true, negative: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := Analyze(strings.NewReader("A,B,C\n"+tt.rows), options)
			if err != nil {
				t.Fatal(err)
			}
			got := report.Reliability
			if got == nil || got.Included != report.Valid || got.Missing != 0 || got.NonNumeric != 0 ||
				(got.Alpha != nil) != tt.alpha || (got.Alpha != nil && (*got.Alpha < 0) != tt.negative) {
				t.Fatalf("unexpected reliability: %+v", got)
			}
			if _, err := json.Marshal(report); err != nil {
				t.Fatalf("report cannot be encoded: %v", err)
			}
		})
	}
	for _, columns := range [][]string{
		{}, {"A"}, {"A", " a "}, {"A", "Email"}, {"A", "_quality_status"}, {"A", "unknown"},
	} {
		opts := Options{Reliability: &ReliabilityOptions{Columns: columns}}
		if _, err := Analyze(strings.NewReader("A,B,Email,_quality_status\n"), opts); err == nil {
			t.Errorf("accepted invalid reliability columns: %q", columns)
		}
	}
	columns := make([]string, 21)
	for i := range columns {
		columns[i] = fmt.Sprintf("Item%d", i+1)
	}
	header := strings.Join(columns, ",") + "\n"
	if _, err := Analyze(strings.NewReader(header), Options{Reliability: &ReliabilityOptions{Columns: columns[:20]}}); err != nil {
		t.Errorf("rejected 20 distinct reliability columns: %v", err)
	}
	if _, err := Analyze(strings.NewReader(header), Options{Reliability: &ReliabilityOptions{Columns: columns}}); err == nil {
		t.Error("accepted 21 distinct reliability columns")
	}
}

func TestAnalyzeCrossTabCategories(t *testing.T) {
	axis := CrossTabOptions{RowColumn: "Station", ColumnColumn: "Choice"}
	options := Options{CrossTab: &axis}
	report, err := Analyze(strings.NewReader("Station,Choice\nNorth,\n,Yes\n"), options)
	if err != nil || report.CrossTab == nil || report.CrossTab.Included != 0 || report.CrossTab.Missing != 2 ||
		report.CrossTab.RowLabels == nil || report.CrossTab.ColumnLabels == nil || report.CrossTab.Counts == nil {
		t.Fatalf("unexpected report without complete pairs: %+v (%v)", report.CrossTab, err)
	}

	var many strings.Builder
	many.WriteString("Station,Choice\n")
	for n := 0; n <= crossTabLevelLimit; n++ {
		fmt.Fprintf(&many, "%d,Yes\n", n)
	}
	for _, tt := range []struct {
		name, csv string
		axes      CrossTabOptions
	}{
		{"repeated column", "Station,Choice\nNorth,Yes\n", CrossTabOptions{"Station", " station "}},
		{"identifier column", "Station,Email\nNorth,example@example.test\n", CrossTabOptions{"Station", "Email"}},
		{"too many categories", many.String(), axis},
		{"long category", "Station,Choice\n" + strings.Repeat("北", 43) + ",Yes\n", axis},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Analyze(strings.NewReader(tt.csv), Options{CrossTab: &tt.axes}); err == nil {
				t.Fatal("accepted an unsupported cross-tab")
			}
		})
	}
}
