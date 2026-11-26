package workbench

import (
	"strings"
	"testing"
)

func TestExperimentPlotNormalizesBeforeRounding(t *testing.T) {
	for _, test := range []struct {
		name, data, coordinates string
	}{
		{"subnormal decimals", "1e-400,2e-400", `cx="124.00" cy="72.00"`},
		{"adjacent large integers", "9007199254740992,9007199254740993", `cx="124.00" cy="72.00"`},
		{"constant range", "-2,-2", `cx="304.00" cy="252.00"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			source := "baseline,variant\n" + test.data + "\n"
			metrics, err := compareExperimentMetrics(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			plot, err := renderExperimentPlot(ctx, experimentComparisonReport{Metrics: metrics}, source)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plot, "<circle "+test.coordinates) {
				t.Fatalf("expected coordinates %s in %s", test.coordinates, plot)
			}
		})
	}
}
