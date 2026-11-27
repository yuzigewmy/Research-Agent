package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"research-agent/internal/inference"
)

const (
	fullResearchWorkflow      = "full-research-v1"
	chinesePaperWorkflow      = "chinese-paper-v1"
	humanitiesPaperWorkflow   = "humanities-paper-v1"
	thesisWorkflow            = "thesis-v1"
	existingMaterialsWorkflow = "existing-materials-v1"
	marketResearchWorkflow    = "market-research-v1"
	mathModelingWorkflow      = "math-modeling-v1"
	bigDataContestWorkflow    = "big-data-contest-v1"

	defaultMathModelingContestID = "cumcm"
	defaultMaxStepRetries        = 2
	minMaxStepRetries            = 1
	maxMaxStepRetries            = 5
)

var (
	errWorkflowRunConfig   = errors.New("invalid workflow run configuration")
	errWorkflowRetryBudget = errors.New("workflow step retry budget exceeded")
	errWorkflowContest     = errors.New("invalid math modeling contest")
)

type workflowRunConfig struct {
	SkippedSteps   []string `json:"skippedSteps,omitempty"`
	MaxStepRetries int      `json:"maxStepRetries,omitempty"`
	ContestID      string   `json:"contestId,omitempty"`
	StepOrder      []string `json:"stepOrder,omitempty"`
}

func parseWorkflowRunConfig(optionsJSON string) workflowRunConfig {
	var cfg workflowRunConfig
	if optionsJSON != "" && optionsJSON != "{}" {
		_ = json.Unmarshal([]byte(optionsJSON), &cfg)
	}
	return cfg
}

func applyWorkflowRunConfig(baseOptionsJSON, key, action string, req workflowRequest) (string, error) {
	if action != workflowStart {
		if len(req.SkippedSteps) != 0 || req.MaxStepRetries != 0 || req.ContestID != "" || len(req.StepOrder) != 0 {
			return "", errWorkflowRunConfig
		}
		return baseOptionsJSON, nil
	}
	if req.MaxStepRetries != 0 && (req.MaxStepRetries < minMaxStepRetries || req.MaxStepRetries > maxMaxStepRetries) {
		return "", errWorkflowRunConfig
	}
	stages := workflowStages[key]
	var stepOrder []string
	if len(req.StepOrder) > 0 {
		validated, err := validateWorkflowStepOrder(key, req.StepOrder)
		if err != nil {
			return "", err
		}
		stepOrder = validated
	}
	var skipped []string
	if len(req.SkippedSteps) > 0 {
		seen := make(map[string]bool, len(req.SkippedSteps))
		for _, stepKey := range req.SkippedSteps {
			if seen[stepKey] {
				return "", errWorkflowRunConfig
			}
			idx := slices.IndexFunc(stages, func(s workflowStage) bool { return s.key == stepKey })
			if idx < 0 || !stages[idx].optional {
				return "", errWorkflowRunConfig
			}
			seen[stepKey] = true
		}
		for _, stage := range stages {
			if seen[stage.key] {
				skipped = append(skipped, stage.key)
			}
		}
	}
	var contest *mathModelingContest
	if req.ContestID != "" {
		if key != mathModelingWorkflow {
			return "", errWorkflowContest
		}
		matched, ok := mathModelingContestFor(req.ContestID)
		if !ok {
			return "", errWorkflowContest
		}
		contest = &matched
	} else if key == mathModelingWorkflow {
		matched, _ := mathModelingContestFor(defaultMathModelingContestID)
		contest = &matched
	}
	if len(skipped) == 0 && req.MaxStepRetries == 0 && contest == nil && len(stepOrder) == 0 {
		return baseOptionsJSON, nil
	}
	merged := make(map[string]any)
	if baseOptionsJSON != "" && baseOptionsJSON != "{}" {
		if err := json.Unmarshal([]byte(baseOptionsJSON), &merged); err != nil {
			return "", err
		}
	}
	if len(skipped) > 0 {
		merged["skippedSteps"] = skipped
	}
	if req.MaxStepRetries > 0 {
		merged["maxStepRetries"] = req.MaxStepRetries
	}
	if len(stepOrder) > 0 {
		merged["stepOrder"] = stepOrder
	}
	if contest != nil {
		merged["contestId"] = contest.ID
		merged["language"] = contest.Language
	}
	body, err := json.Marshal(merged)
	return string(body), err
}

//go:embed prompts/expanded_stage.md
var expandedStagePrompt string

//go:embed prompts/expanded_manuscript.md
var expandedManuscriptPrompt string

//go:embed prompts/market_research_questionnaire.md
var marketResearchQuestionnairePrompt string

//go:embed prompts/market_research_analysis.md
var marketResearchAnalysisPrompt string

//go:embed prompts/math_modeling_analysis.md
var mathModelingAnalysisPrompt string

//go:embed prompts/math_modeling_solve.md
var mathModelingSolvePrompt string

//go:embed prompts/math_modeling_paper.md
var mathModelingPaperPrompt string

//go:embed prompts/big_data_contest.md
var bigDataContestPrompt string

type mathModelingContest struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Language   string   `json:"language"`
	RuleYear   int      `json:"ruleYear"`
	VerifiedAt string   `json:"verifiedAt"`
	Reminders  []string `json:"reminders"`
}

var mathModelingContests = []mathModelingContest{
	{
		ID: "cumcm", Name: "全国大学生数学建模竞赛（国赛 CUMCM）", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"独立摘要页原则上不超过 1 页", "需按赛区要求提交承诺书与编号专用页", "正文一般控制在 20 页以内并隐去参赛者身份信息", "附可复现程序代码与结果支撑材料"},
	},
	{
		ID: "mcm_icm", Name: "美国大学生数学建模竞赛（美赛 MCM/ICM）", Language: "en", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"首页为英文 Summary Sheet（不超过 1 页）", "总页数上限 25 页（含摘要、正文、图表与参考文献，不含 AI 使用报告）", "须按当年指南附上 AI Use Report", "严格匿名并核对控制号（Team Control Number）"},
	},
	{
		ID: "huawei_cup", Name: "“华为杯”中国研究生数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"独立中文摘要与关键词", "完整呈现数学推导、算法复杂度与灵敏度分析", "按题号提交正文 PDF 与支撑材料压缩包"},
	},
	{
		ID: "mathorcup", Name: "MathorCup 数学应用挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"摘要简明定量并给出各问核心指标", "突出工程实现与实际业务落地建议", "附求解程序代码与结果数据表"},
	},
	{
		ID: "apmcm_zh", Name: "亚太地区大学生数学建模竞赛（APMCM 中文）", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"独立中文摘要页且不含参赛队身份信息", "正文规范图表编号与模型检验", "附程序源码附录与支撑材料"},
	},
	{
		ID: "apmcm_en", Name: "亚太地区大学生数学建模竞赛（APMCM 英文）", Language: "en", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"英文 Summary Sheet 置首并保持匿名", "核对英文术语一致性、误差分析与灵敏度检验", "附源码附录与支撑数据说明"},
	},
	{
		ID: "stat_modeling", Name: "全国大学生统计建模大赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"明确数据来源、统计口径与方法适用前提", "需附参赛承诺书并关注正文字数及查重要求", "提供原始数据说明与完整统计分析代码"},
	},
	{
		ID: "teddy_cup", Name: "“泰迪杯”数据挖掘挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"完整记录数据预处理、特征构建与交叉验证指标", "按赛题规定格式导出结果文件", "提交可运行代码包、环境说明及报告 PDF"},
	},
	{
		ID: "tipdm_cert", Name: "认证杯数学中国数学建模网络挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"摘要逐问列明建模方法与核心结论", "充分说明开放性假设的合理性与模型推广价值", "匿名排版并附程序代码"},
	},
	{
		ID: "huazhong_cup", Name: "华中杯大学生数学建模挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"匿名格式提交论文", "摘要突出量化结论与检验结果", "附完整算法代码与计算结果支撑文件"},
	},
	{
		ID: "huadong_cup", Name: "华东杯大学生数学建模邀请赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"中文摘要简明准确并含模型优缺点评述", "注重数值求解验证与图表规范", "附程序源码支撑材料"},
	},
	{
		ID: "may_day_cup", Name: "五一数学建模竞赛（五一杯）", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"集中赛制下逐问分节作答、结论清晰可查", "摘要控制在 1 页内并给出关键数值结果", "附可执行源码与附件结果表"},
	},
	{
		ID: "shuwei_cup", Name: "数维杯大学生数学建模挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"中文匿名论文，核对参赛编号位置", "摘要涵盖各问模型、算法与量化结果", "打包提交支撑材料与程序代码"},
	},
	{
		ID: "zhongqing_cup", Name: "中青杯全国大学生数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"摘要完整概括建模思路与结果", "规范符号说明、图表题注与参考文献", "附求解代码附录"},
	},
	{
		ID: "yangtze_delta", Name: "长三角高校数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"结合区域实际背景论证假设合理性", "强化误差检验与方案对比分析", "匿名提交论文正文与支撑附件"},
	},
	{
		ID: "diangong_cup", Name: "“电工杯”全国大学生电工数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"贴合电力能源与工程优化约束条件", "给出定量求解指标与参数灵敏度检验", "附可复现求解程序源码"},
	},
	{
		ID: "shenzhen_cup", Name: "深圳杯数学建模挑战赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"面向开放式行业课题，充分说明调研与数据依据", "突出方案可行性评估与多阶段答辩材料准备", "附代码与计算复现说明"},
	},
	{
		ID: "huashu_cup", Name: "华数杯全国大学生数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"摘要突出核心量化结论", "严格匿名排版并核对页码与目录", "附完整算法代码与运行结果附件"},
	},
	{
		ID: "tianfu_cup", Name: "天府杯全国大学生数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"规范摘要、模型假设与符号表", "验证模型稳健性与边界情形", "按赛题要求提交论文与支撑附件"},
	},
	{
		ID: "northeast_liaoning", Name: "东三省/辽宁省大学生数学建模竞赛", Language: "zh", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"省赛匿名排版，摘要页不超过 1 页", "包含清晰符号说明、算法步骤与误差检验", "附程序代码支撑材料"},
	},
	{
		ID: "mini_mcm", Name: "小美赛（美赛模拟赛 / 地区英文数模模拟赛）", Language: "en", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"参照美赛英文结构，英文 Summary Sheet 置首", "控制正文总页数并规范英文图表与引用", "附求解代码与灵敏度分析"},
	},
	{
		ID: "shuwei_intl", Name: "数维杯国际大学生数学建模挑战赛", Language: "en", RuleYear: 2026, VerifiedAt: "2026-01",
		Reminders: []string{"英文论文匿名排版并附 Summary Sheet", "规范英文符号表、模型检验与灵敏度分析", "附英文图表说明与程序源码"},
	},
}

func mathModelingContestFor(id string) (mathModelingContest, bool) {
	for _, contest := range mathModelingContests {
		if contest.ID == id {
			copied := contest
			copied.Reminders = append([]string(nil), contest.Reminders...)
			return copied, true
		}
	}
	return mathModelingContest{}, false
}

func allMathModelingContests() []mathModelingContest {
	items := make([]mathModelingContest, len(mathModelingContests))
	for i, contest := range mathModelingContests {
		contest.Reminders = append([]string(nil), contest.Reminders...)
		items[i] = contest
	}
	return items
}

func isExpandedWorkflow(key string) bool {
	switch key {
	case fullResearchWorkflow, chinesePaperWorkflow, humanitiesPaperWorkflow, thesisWorkflow,
		existingMaterialsWorkflow, marketResearchWorkflow, mathModelingWorkflow, bigDataContestWorkflow:
		return true
	default:
		return false
	}
}

func expandedWorkflowUsesLiterature(key string) bool {
	switch key {
	case fullResearchWorkflow, chinesePaperWorkflow, humanitiesPaperWorkflow, thesisWorkflow, existingMaterialsWorkflow:
		return true
	default:
		return false
	}
}

func expandedWorkflowStages(key string) []workflowStage {
	switch key {
	case fullResearchWorkflow:
		return []workflowStage{
			{key: "boundary", title: "研究边界", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "literature", title: "文献与候选", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "contract", title: "选题与研究契约", prompt: expandedStagePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "experiment", title: "实验与验证计划", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "manuscript", title: "结果整理与论文撰写", prompt: expandedManuscriptPrompt, acceptMarkdown: true},
			{key: "check", title: "交付核对", optional: true},
		}
	case chinesePaperWorkflow:
		return []workflowStage{
			{key: "scope", title: "文体与研究范围", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "outline", title: "中文大纲", prompt: expandedStagePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "manuscript", title: "正文撰写", prompt: expandedManuscriptPrompt, acceptMarkdown: true},
			{key: "check", title: "稿件结构核对", optional: true},
		}
	case humanitiesPaperWorkflow:
		return []workflowStage{
			{key: "context", title: "议题与文献脉络", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "outline", title: "论证大纲", prompt: expandedStagePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "manuscript", title: "章节写作", prompt: expandedManuscriptPrompt, acceptMarkdown: true},
			{key: "check", title: "引注与结构核对", optional: true},
		}
	case thesisWorkflow:
		return []workflowStage{
			{key: "spec", title: "学位规格与框架", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "outline", title: "目录与文献规划", prompt: expandedStagePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "manuscript", title: "主体章节撰写", prompt: expandedManuscriptPrompt, acceptMarkdown: true},
			{key: "check", title: "摘要与交付核对", optional: true},
		}
	case existingMaterialsWorkflow:
		return []workflowStage{
			{key: "inventory", title: "材料清点与缺口识别", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "plan", title: "成稿规划", prompt: expandedStagePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "manuscript", title: "整合撰写", prompt: expandedManuscriptPrompt, acceptMarkdown: true},
			{key: "check", title: "交付核对", optional: true},
		}
	case marketResearchWorkflow:
		return []workflowStage{
			{key: "design", title: "调研目标与样本计划", prompt: expandedStagePrompt, acceptMarkdown: true},
			{key: "questionnaire", title: "问卷与编码设计", prompt: marketResearchQuestionnairePrompt, checkpoint: true, acceptMarkdown: true},
			{key: "analysis", title: "数据清洗与统计解读", prompt: marketResearchAnalysisPrompt, acceptMarkdown: true},
			{key: "manuscript", title: "调研报告与附录", prompt: expandedManuscriptPrompt, acceptMarkdown: true, optional: true},
		}
	case mathModelingWorkflow:
		return []workflowStage{
			{key: "analysis", title: "题意与数据剖析", prompt: mathModelingAnalysisPrompt, acceptMarkdown: true},
			{key: "decomposition", title: "问题拆解与假设", prompt: mathModelingAnalysisPrompt, checkpoint: true, acceptMarkdown: true},
			{key: "modeling", title: "模型构建与求解验证", prompt: mathModelingSolvePrompt, acceptMarkdown: true},
			{key: "manuscript", title: "竞赛论文与摘要核对", prompt: mathModelingPaperPrompt, acceptMarkdown: true, optional: true},
		}
	case bigDataContestWorkflow:
		return []workflowStage{
			{key: "eda", title: "赛题与数据探索", prompt: bigDataContestPrompt, acceptMarkdown: true},
			{key: "baseline", title: "特征与基线方案", prompt: bigDataContestPrompt, checkpoint: true, acceptMarkdown: true},
			{key: "iteration", title: "模型迭代与消融验证", prompt: bigDataContestPrompt, acceptMarkdown: true},
			{key: "manuscript", title: "竞赛报告与代码核对", prompt: bigDataContestPrompt, acceptMarkdown: true, optional: true},
		}
	default:
		return nil
	}
}

func (s *Store) generateExpandedWorkflowStep(ctx context.Context, model *inference.Client, project Project, task Task, key, input, evidence, options string) (taskResult, error) {
	stages := workflowStagesForRun(key, options)
	stage := stages[task.workflowStep]
	var contest *mathModelingContest
	if key == mathModelingWorkflow {
		contestID := defaultMathModelingContestID
		var cfg struct {
			ContestID string `json:"contestId"`
		}
		if err := json.Unmarshal([]byte(options), &cfg); err == nil && cfg.ContestID != "" {
			contestID = cfg.ContestID
		}
		if matched, ok := mathModelingContestFor(contestID); ok {
			contest = &matched
		}
	}
	if stage.key == "check" {
		_, manuscript, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "manuscript")
		if err != nil {
			return taskResult{}, err
		}
		return taskResult{markdown: expandedDeliveryCheck(key, manuscript)}, nil
	}

	var sources []reviewSource
	usesLiterature := expandedWorkflowUsesLiterature(key)
	if usesLiterature {
		if err := json.Unmarshal([]byte(evidence), &sources); err != nil {
			return taskResult{}, err
		}
		if len(sources) > 0 {
			input += "\n\n本轮固定的文献元数据（JSON）：\n" + evidence
		}
	}
	if contest != nil {
		if encoded, err := json.Marshal(contest); err == nil {
			input += "\n\n本轮数学建模竞赛规格（JSON）：\n" + string(encoded)
		}
	}
	if options != "" && options != "{}" {
		input += "\n\n本轮工作流配置（JSON）：\n" + options
	}
	previous, err := s.workflowContext(ctx, task, stages, 0)
	if err != nil {
		return taskResult{}, err
	}
	markdown, err := streamTaskMarkdown(ctx, model, stage.prompt, input+previous)
	if err != nil {
		return taskResult{}, err
	}
	if usesLiterature && len(sources) > 0 {
		if invalid := checkWorkflowCitations(markdown, sources, false); invalid != nil {
			return taskResult{}, workflowOutputError{invalid}
		}
	}
	if key == mathModelingWorkflow && stage.key == "manuscript" && contest != nil {
		var checklist strings.Builder
		checklist.WriteString("\n\n## 赛事交付核对清单\n\n")
		fmt.Fprintf(&checklist, "- 目标赛事：%s（规则年份 %d，校验日期 %s）\n", contest.Name, contest.RuleYear, contest.VerifiedAt)
		langLabel := "中文"
		if contest.Language == "en" {
			langLabel = "英文"
		}
		fmt.Fprintf(&checklist, "- 默认写作语言：%s\n", langLabel)
		for _, reminder := range contest.Reminders {
			fmt.Fprintf(&checklist, "- 交付提醒：%s\n", reminder)
		}
		markdown += checklist.String()
	}
	return taskResult{markdown: markdown}, nil
}

func expandedDeliveryCheck(key, manuscript string) string {
	report := paperCheck(manuscript)
	var extra string
	switch key {
	case fullResearchWorkflow:
		extra = "\n## 科研全流程交付要点\n\n- 请复核研究契约与实验验证计划中的关键假设、指标及限制条件是否已完整回流至正文与局限章节。\n"
	case chinesePaperWorkflow:
		extra = "\n## 中文论文结构要点\n\n- 请核对中文摘要、关键词、各级标题编号及参考文献格式是否符合目标期刊要求。\n"
	case humanitiesPaperWorkflow:
		extra = "\n## 人文社科论文核对要点\n\n- 请核对核心论点脉络、史料或文本引注规范与脚注/尾注体例一致性。\n"
	case thesisWorkflow:
		extra = "\n## 学位论文交付要点\n\n- 请核对中英文摘要、目录层级、致谢与学校学位论文格式规范。\n"
	case existingMaterialsWorkflow:
		extra = "\n## 已有材料成稿核对要点\n\n- 请核对原始材料出处标注及清点出的缺口项是否均已补齐或明确标记。\n"
	}
	return report + extra
}
