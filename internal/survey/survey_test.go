package survey

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

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
