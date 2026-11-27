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
	Included     int               `json:"included"`
	Missing      int               `json:"missing"`
	RowLabels    []string          `json:"rowLabels"`
	ColumnLabels []string          `json:"columnLabels"`
	Counts       [][]int           `json:"counts"`
	RowTotals    []int             `json:"rowTotals"`
	ColumnTotals []int             `json:"columnTotals"`
	Weighted     *WeightedCrossTab `json:"weighted,omitempty"`
}

// WeightedCrossTab uses the same labels as CrossTab and sums valid weights for complete pairs.
type WeightedCrossTab struct {
	Included     int        `json:"included"`
	Weights      [][]string `json:"weights"`
	RowTotals    []string   `json:"rowTotals"`
	ColumnTotals []string   `json:"columnTotals"`
	TotalWeight  string     `json:"totalWeight"`
}

type crossTabCell struct {
	count int
	units uint64
}

type crossTabAccumulator struct {
	CrossTab
	rowIndex, columnIndex int
	rows, columns         map[string]struct{}
	pairs                 map[[2]string]crossTabCell
}

func newCrossTab(options CrossTabOptions, header []string, byName map[string]int, weighted bool) (*crossTabAccumulator, error) {
	row := strings.ToLower(strings.TrimSpace(options.RowColumn))
	column := strings.ToLower(strings.TrimSpace(options.ColumnColumn))
	rowIndex, hasRow := byName[row]
	columnIndex, hasColumn := byName[column]
	if !hasRow || !hasColumn || rowIndex == columnIndex || excludeSummary(row) || excludeSummary(column) {
		return nil, errors.New("请选择两个不同的可统计字段。")
	}
	acc := &crossTabAccumulator{
		CrossTab: CrossTab{CrossTabOptions: CrossTabOptions{RowColumn: header[rowIndex], ColumnColumn: header[columnIndex]}},
		rowIndex: rowIndex, columnIndex: columnIndex,
		rows: make(map[string]struct{}), columns: make(map[string]struct{}), pairs: make(map[[2]string]crossTabCell),
	}
	if weighted {
		acc.Weighted = &WeightedCrossTab{}
	}
	return acc, nil
}

func (c *crossTabAccumulator) add(record []string, weight uint64) error {
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
	key := [2]string{row, column}
	cell := c.pairs[key]
	cell.count++
	cell.units += weight
	c.pairs[key] = cell
	if weight != 0 {
		c.Weighted.Included++
	}
	c.Included++
	return nil
}

func (c *crossTabAccumulator) finish() *CrossTab {
	c.RowLabels = sortedLabels(c.rows)
	c.ColumnLabels = sortedLabels(c.columns)
	c.Counts = make([][]int, len(c.RowLabels))
	c.RowTotals = make([]int, len(c.RowLabels))
	c.ColumnTotals = make([]int, len(c.ColumnLabels))
	var rowWeights, columnWeights []uint64
	if c.Weighted != nil {
		c.Weighted.Weights = make([][]string, len(c.RowLabels))
		rowWeights = make([]uint64, len(c.RowLabels))
		columnWeights = make([]uint64, len(c.ColumnLabels))
	}
	for i, row := range c.RowLabels {
		c.Counts[i] = make([]int, len(c.ColumnLabels))
		if c.Weighted != nil {
			c.Weighted.Weights[i] = make([]string, len(c.ColumnLabels))
		}
		for j, column := range c.ColumnLabels {
			cell := c.pairs[[2]string{row, column}]
			c.Counts[i][j] = cell.count
			c.RowTotals[i] += cell.count
			c.ColumnTotals[j] += cell.count
			if c.Weighted != nil {
				c.Weighted.Weights[i][j] = formatWeight(cell.units)
				rowWeights[i] += cell.units
				columnWeights[j] += cell.units
			}
		}
	}
	if c.Weighted != nil {
		c.Weighted.RowTotals = make([]string, len(rowWeights))
		c.Weighted.ColumnTotals = make([]string, len(columnWeights))
		var total uint64
		for i, units := range rowWeights {
			c.Weighted.RowTotals[i] = formatWeight(units)
			total += units
		}
		for i, units := range columnWeights {
			c.Weighted.ColumnTotals[i] = formatWeight(units)
		}
		c.Weighted.TotalWeight = formatWeight(total)
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
