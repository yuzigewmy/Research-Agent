package survey

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const categoryMappingLimit = 64

// CategoryMapping assigns a raw answer to a category for frequencies and cross-tabs.
type CategoryMapping struct {
	Column string `json:"column"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type categoryMappings map[int]map[string]string

func compileCategoryMappings(input []CategoryMapping, byName map[string]int) (categoryMappings, error) {
	if len(input) > categoryMappingLimit {
		return nil, fmt.Errorf("分类编码最多支持 %d 条规则。", categoryMappingLimit)
	}
	mappings := make(categoryMappings)
	for i, rule := range input {
		column := strings.ToLower(strings.TrimSpace(rule.Column))
		index, exists := byName[column]
		if !exists || excludeSummary(column) {
			return nil, fmt.Errorf("第 %d 条分类编码须选择可统计的字段。", i+1)
		}
		from, to := strings.TrimSpace(rule.From), strings.TrimSpace(rule.To)
		if !validCategoryLabel(from) || !validCategoryLabel(to) {
			return nil, fmt.Errorf("第 %d 条分类编码须填写原值和目标类别，每项最多 %d 个 UTF-8 字节，且不能含控制字符。", i+1, maxValueBytes)
		}
		if mappings[index] == nil {
			mappings[index] = make(map[string]string)
		}
		if _, duplicate := mappings[index][from]; duplicate {
			return nil, fmt.Errorf("第 %d 条分类编码与同字段的原值规则重复。", i+1)
		}
		mappings[index][from] = to
	}
	return mappings, nil
}

func validCategoryLabel(value string) bool {
	return value != "" && len(value) <= maxValueBytes && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func (m categoryMappings) label(index int, value string) string {
	if category, ok := m[index][value]; ok {
		return category
	}
	return value
}
