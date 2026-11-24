package survey

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const crossTabLevelLimit = 20

// CrossTabOptions identifies two categorical CSV columns.
type CrossTabOptions struct {
	RowColumn    string `json:"rowColumn"`
	ColumnColumn string `json:"columnColumn"`
}

// CrossTab describes complete pairs among valid responses. Missing counts valid
// responses with an empty answer on either axis. Counts follows the label order.
type CrossTab struct {
	CrossTabOptions
	Included     int      `json:"included"`
	Missing      int      `json:"missing"`
	RowLabels    []string `json:"rowLabels"`
	ColumnLabels []string `json:"columnLabels"`
	Counts       [][]int  `json:"counts"`
	RowTotals    []int    `json:"rowTotals"`
	ColumnTotals []int    `json:"columnTotals"`
}

type crossTabAccumulator struct {
	CrossTab
	rowIndex, columnIndex int
	rows, columns         map[string]struct{}
	pairs                 map[[2]string]int
}

func newCrossTab(options CrossTabOptions, header []string, byName map[string]int) (*crossTabAccumulator, error) {
	row := strings.ToLower(strings.TrimSpace(options.RowColumn))
	column := strings.ToLower(strings.TrimSpace(options.ColumnColumn))
	rowIndex, hasRow := byName[row]
	columnIndex, hasColumn := byName[column]
	if !hasRow || !hasColumn || rowIndex == columnIndex || excludeSummary(row) || excludeSummary(column) {
		return nil, errors.New("请选择两个不同的可统计字段。")
	}
	return &crossTabAccumulator{
		CrossTab: CrossTab{CrossTabOptions: CrossTabOptions{RowColumn: header[rowIndex], ColumnColumn: header[columnIndex]}},
		rowIndex: rowIndex, columnIndex: columnIndex,
		rows: make(map[string]struct{}), columns: make(map[string]struct{}), pairs: make(map[[2]string]int),
	}, nil
}

func (c *crossTabAccumulator) add(record []string) error {
	row, column := fieldAt(record, c.rowIndex), fieldAt(record, c.columnIndex)
	if row == "" || column == "" {
		c.Missing++
		return nil
	}
	if len(row) > maxValueBytes || len(column) > maxValueBytes {
		return fmt.Errorf("交叉统计的类别名称最多 %d 个 UTF-8 字节，请选择分类字段。", maxValueBytes)
	}
	c.rows[row], c.columns[column] = struct{}{}, struct{}{}
	if len(c.rows) > crossTabLevelLimit || len(c.columns) > crossTabLevelLimit {
		return fmt.Errorf("交叉统计每列最多 %d 个类别，请选择分类较少的字段。", crossTabLevelLimit)
	}
	c.pairs[[2]string{row, column}]++
	c.Included++
	return nil
}

func (c *crossTabAccumulator) finish() *CrossTab {
	c.RowLabels = sortedLabels(c.rows)
	c.ColumnLabels = sortedLabels(c.columns)
	c.Counts = make([][]int, len(c.RowLabels))
	c.RowTotals = make([]int, len(c.RowLabels))
	c.ColumnTotals = make([]int, len(c.ColumnLabels))
	for i, row := range c.RowLabels {
		c.Counts[i] = make([]int, len(c.ColumnLabels))
		for j, column := range c.ColumnLabels {
			count := c.pairs[[2]string{row, column}]
			c.Counts[i][j] = count
			c.RowTotals[i] += count
			c.ColumnTotals[j] += count
		}
	}
	return &c.CrossTab
}

func sortedLabels(values map[string]struct{}) []string {
	labels := make([]string, 0, len(values))
	for label := range values {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return labels
}
