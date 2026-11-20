package workbench

const workflowStepSkipped = "SKIPPED"

const workflowSkipSchema = `
	ALTER TABLE workflow_runs ADD COLUMN skipped_request_id TEXT;
	ALTER TABLE workflow_requests ADD COLUMN skip_reason TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 22;
`

func skippableWorkflowStep(run WorkflowRun, stages []workflowStage) string {
	switch run.Status {
	case workflowRangeCompleted, workflowWaiting, workflowPaused, taskFailed, taskInterrupted:
	default:
		return ""
	}
	// Only an optional final step can be omitted. Earlier outcomes are inputs to later work.
	if run.step != len(stages)-1 || !stages[run.step].optional {
		return ""
	}
	for _, preceding := range run.Steps[:run.step] {
		if preceding.Status != taskSucceeded {
			return ""
		}
	}
	return stages[run.step].key
}
