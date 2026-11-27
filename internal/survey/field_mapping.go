package survey

import (
	"errors"
	"fmt"
	"strings"
)

// MapSettings copies analysis settings to a different table using confirmed field pairs.
// Review decisions refer to source rows, so the new analysis starts without them.
func MapSettings(source Options, sourceColumns, targetColumns []string, fields []FieldPair, sheet string) (Options, []FieldPair, error) {
	if len(fields) > maxColumns {
		return Options{}, nil, errors.New("字段映射最多支持 128 列。")
	}
	columnKey := func(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
	index := func(columns []string) map[string]string {
		names := make(map[string]string, len(columns))
		for _, name := range columns {
			names[columnKey(name)] = name
		}
		return names
	}
	fromNames, toNames := index(sourceColumns), index(targetColumns)
	mapping := make(map[string]string, len(fields))
	targets := make(map[string]bool, len(fields))
	for _, field := range fields {
		from, to := columnKey(field.From), columnKey(field.To)
		if fromNames[from] == "" || toNames[to] == "" || mapping[from] != "" || targets[to] {
			return Options{}, nil, errors.New("请选择现有字段，并为每个来源字段指定不同的目标字段。")
		}
		mapping[from], targets[to] = toNames[to], true
	}

	confirmed := make([]FieldPair, 0, len(fields))
	used := make(map[string]bool, len(fields))
	var missing string
	translate := func(name string) string {
		key := columnKey(name)
		to := mapping[key]
		if to == "" {
			missing = name
		} else if !used[key] {
			used[key] = true
			confirmed = append(confirmed, FieldPair{From: fromNames[key], To: to})
		}
		return to
	}
	translateList := func(names []string) []string {
		result := make([]string, len(names))
		for i, name := range names {
			result[i] = translate(name)
		}
		return result
	}

	result := Options{
		RequiredColumns: translateList(source.RequiredColumns), DuplicateColumns: translateList(source.DuplicateColumns), Sheet: sheet,
		PlanID: source.PlanID,
	}
	if source.WeightColumn != "" {
		result.WeightColumn = translate(source.WeightColumn)
	}
	if tab := source.CrossTab; tab != nil {
		result.CrossTab = &CrossTabOptions{RowColumn: translate(tab.RowColumn), ColumnColumn: translate(tab.ColumnColumn)}
	}
	if reliability := source.Reliability; reliability != nil {
		result.Reliability = &ReliabilityOptions{Columns: translateList(reliability.Columns)}
	}
	if regression := source.Regression; regression != nil {
		result.Regression = &RegressionOptions{PredictorColumn: translate(regression.PredictorColumn), OutcomeColumn: translate(regression.OutcomeColumn)}
	}
	for _, rule := range source.CategoryMappings {
		rule.Column = translate(rule.Column)
		result.CategoryMappings = append(result.CategoryMappings, rule)
	}
	if missing != "" {
		return Options{}, nil, fmt.Errorf("请为历史报告使用的字段 %q 指定对应列。", missing)
	}
	if len(used) != len(fields) {
		return Options{}, nil, errors.New("请为历史报告实际使用的字段设置对应关系。")
	}
	return result, confirmed, nil
}
