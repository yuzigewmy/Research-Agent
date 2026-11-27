package workbench

import (
	"errors"
	"slices"
)

var errWorkflowStepOrder = errors.New("workflow step order violates step definitions or hard dependencies")

// workflowStepDeps returns the hard prerequisite step keys for each step in a workflow template.
// A step listed as depending on prerequisites cannot be ordered before any of those prerequisites.
func workflowStepDeps(workflowKey string) map[string][]string {
	switch workflowKey {
	case experimentWorkflowV1:
		return map[string][]string{
			"plan":      {"scope"},
			"checklist": {"plan"},
		}
	case experimentWorkflow:
		return map[string][]string{
			"plan": {"scope"},
		}
	case reviewWorkflow:
		return map[string][]string{
			"themes":     {"sources"},
			"manuscript": {"themes"},
		}
	case paperWorkflow:
		return map[string][]string{
			"manuscript": {"outline"},
			"structure":  {"manuscript"},
		}
	case topicWorkflow:
		return map[string][]string{
			"brainstorm": {"sources"},
			"shortlist":  {"brainstorm"},
			"contract":   {"shortlist"},
		}
	case proposalWorkflowV1:
		return map[string][]string{
			"outline":    {"sources"},
			"manuscript": {"outline"},
			"defense":    {"outline"},
			"check":      {"manuscript"},
		}
	case proposalWorkflow:
		deps := map[string][]string{
			"outline": {"sources"},
			"defense": {"outline"},
			"check":   {"manuscript"},
		}
		sectionKeys := make([]string, 0, len(proposalSections))
		for _, sec := range proposalSections {
			deps[sec.key] = []string{"outline"}
			sectionKeys = append(sectionKeys, sec.key)
		}
		deps["manuscript"] = sectionKeys
		return deps
	case coursePaperWorkflow:
		deps := map[string][]string{
			"check": {"manuscript"},
		}
		sectionKeys := make([]string, 0, len(courseSections[coursePaperWorkflow]))
		for _, sec := range courseSections[coursePaperWorkflow] {
			deps[sec.key] = []string{"outline"}
			sectionKeys = append(sectionKeys, sec.key)
		}
		deps["manuscript"] = sectionKeys
		return deps
	case courseReportWorkflow:
		deps := map[string][]string{
			"outline": {"facts"},
			"check":   {"manuscript"},
		}
		sectionKeys := make([]string, 0, len(courseSections[courseReportWorkflow]))
		for _, sec := range courseSections[courseReportWorkflow] {
			deps[sec.key] = []string{"outline"}
			sectionKeys = append(sectionKeys, sec.key)
		}
		deps["manuscript"] = sectionKeys
		return deps
	case softwareRegistrationWorkflowV1:
		return map[string][]string{
			"manuscript":  {"sources"},
			"program":     {"sources"},
			"application": {"sources", "manuscript", "program"},
		}
	case softwareRegistrationWorkflow:
		return map[string][]string{
			"manuscript":  {"sources"},
			"program":     {"sources", "manuscript"},
			"application": {"sources", "manuscript", "program"},
		}
	case patentDisclosureWorkflowV1:
		return map[string][]string{
			"outline":    {"sources"},
			"manuscript": {"outline"},
			"check":      {"manuscript"},
		}
	case patentDisclosureWorkflow:
		return map[string][]string{
			"outline":    {"sources"},
			"manuscript": {"outline"},
			"figures":    {"manuscript"},
			"check":      {"manuscript", "figures"},
		}
	case fullResearchWorkflow:
		return map[string][]string{
			"contract":   {"boundary", "literature"},
			"experiment": {"contract"},
			"manuscript": {"experiment"},
			"check":      {"manuscript"},
		}
	case chinesePaperWorkflow:
		return map[string][]string{
			"outline":    {"scope"},
			"manuscript": {"outline"},
			"check":      {"manuscript"},
		}
	case humanitiesPaperWorkflow:
		return map[string][]string{
			"outline":    {"context"},
			"manuscript": {"outline"},
			"check":      {"manuscript"},
		}
	case thesisWorkflow:
		return map[string][]string{
			"outline":    {"spec"},
			"manuscript": {"outline"},
			"check":      {"manuscript"},
		}
	case existingMaterialsWorkflow:
		return map[string][]string{
			"plan":       {"inventory"},
			"manuscript": {"plan"},
			"check":      {"manuscript"},
		}
	case marketResearchWorkflow:
		return map[string][]string{
			"questionnaire": {"design"},
			"analysis":      {"questionnaire"},
			"manuscript":    {"questionnaire"},
		}
	case mathModelingWorkflow:
		return map[string][]string{
			"decomposition": {"analysis"},
			"modeling":      {"decomposition"},
			"manuscript":    {"decomposition"},
		}
	case bigDataContestWorkflow:
		return map[string][]string{
			"baseline":   {"eda"},
			"iteration":  {"baseline"},
			"manuscript": {"baseline"},
		}
	default:
		return nil
	}
}

func validateWorkflowStepOrder(workflowKey string, stepOrder []string) ([]string, error) {
	stages := workflowStages[workflowKey]
	if len(stepOrder) != len(stages) {
		return nil, errWorkflowStepOrder
	}
	pos := make(map[string]int, len(stages))
	for i, stepKey := range stepOrder {
		if _, dup := pos[stepKey]; dup {
			return nil, errWorkflowStepOrder
		}
		if !slices.ContainsFunc(stages, func(s workflowStage) bool { return s.key == stepKey }) {
			return nil, errWorkflowStepOrder
		}
		pos[stepKey] = i
	}
	deps := workflowStepDeps(workflowKey)
	for stepKey, prereqs := range deps {
		stepIdx, ok := pos[stepKey]
		if !ok {
			continue
		}
		for _, prereq := range prereqs {
			prereqIdx, ok := pos[prereq]
			if ok && stepIdx < prereqIdx {
				return nil, errWorkflowStepOrder
			}
		}
	}
	return append([]string(nil), stepOrder...), nil
}

func workflowStagesForRun(workflowKey, optionsJSON string) []workflowStage {
	defaults := workflowStages[workflowKey]
	cfg := parseWorkflowRunConfig(optionsJSON)
	if len(cfg.StepOrder) == 0 || len(cfg.StepOrder) != len(defaults) {
		return defaults
	}
	byKey := make(map[string]workflowStage, len(defaults))
	for _, stage := range defaults {
		byKey[stage.key] = stage
	}
	ordered := make([]workflowStage, 0, len(defaults))
	for _, stepKey := range cfg.StepOrder {
		stage, ok := byKey[stepKey]
		if !ok {
			return defaults
		}
		ordered = append(ordered, stage)
	}
	return ordered
}
