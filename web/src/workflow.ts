import { request } from './projects'

export const runStatusLabels = {
  QUEUED: '排队中',
  RUNNING: '运行中',
  WAITING_CHECKPOINT: '等待确认',
  COMPLETED: '已完成',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

export const stepStatusLabels = {
  PENDING: '未开始',
  QUEUED: '排队中',
  RUNNING: '运行中',
  SUCCEEDED: '已完成',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

export interface ExperimentStep {
  key: 'scope' | 'plan' | 'checklist'
  title: string
  status: keyof typeof stepStatusLabels
  message?: string
}

export interface ExperimentRun {
  runId: string
  revision: number
  status: keyof typeof runStatusLabels
  checkpoints: boolean
  createdAt: string
  steps: ExperimentStep[]
}

export interface WorkflowSnapshot {
  run: ExperimentRun | null
  modelConfigured: boolean
}

export interface StartExperimentInput {
  requestId: string
  checkpoints: boolean
}

export interface ContinueExperimentInput {
  requestId: string
  runId: string
  expectedRevision: number
}

export function isWorkflowActive(run: ExperimentRun | null) {
  return run?.status === 'QUEUED' || run?.status === 'RUNNING'
}

function isStep(value: unknown): value is ExperimentStep {
  const step = value as Partial<ExperimentStep> | null
  return !!step && (step.key === 'scope' || step.key === 'plan' || step.key === 'checklist')
    && typeof step.title === 'string' && step.title.trim().length > 0
    && typeof step.status === 'string' && Object.hasOwn(stepStatusLabels, step.status)
    && (step.message === undefined || typeof step.message === 'string')
}

function isRun(value: unknown): value is ExperimentRun {
  const run = value as Partial<ExperimentRun> | null
  return !!run && typeof run.runId === 'string' && !!run.runId && run.runId !== '.' && run.runId !== '..'
    && typeof run.revision === 'number' && Number.isSafeInteger(run.revision) && run.revision >= 1
    && typeof run.status === 'string' && Object.hasOwn(runStatusLabels, run.status)
    && typeof run.checkpoints === 'boolean'
    && typeof run.createdAt === 'string' && Number.isFinite(Date.parse(run.createdAt))
    && Array.isArray(run.steps) && run.steps.every(isStep)
}

function isSnapshot(value: unknown): value is WorkflowSnapshot {
  const snapshot = value as Partial<WorkflowSnapshot> | null
  return !!snapshot && typeof snapshot.modelConfigured === 'boolean'
    && (snapshot.run === null || isRun(snapshot.run))
}

function workflowPath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/workflow`
}

export function getWorkflow(projectId: string, signal: AbortSignal) {
  return request(workflowPath(projectId), isSnapshot, { cache: 'no-store', signal })
}

function postRun(path: string, input: StartExperimentInput | ContinueExperimentInput, signal: AbortSignal) {
  return request(path, isRun, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}

export function startWorkflow(projectId: string, input: StartExperimentInput, signal: AbortSignal) {
  return postRun(workflowPath(projectId), input, signal)
}

export function continueWorkflow(projectId: string, input: ContinueExperimentInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/continue`, input, signal)
}

export function workflowArtifactPath(projectId: string, runId: string, key: ExperimentStep['key']) {
  return `${workflowPath(projectId)}/${encodeURIComponent(runId)}/steps/${key}/artifact`
}
