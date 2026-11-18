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

interface WorkflowStep {
  key: string
  title: string
  status: keyof typeof stepStatusLabels
  message?: string
}

export interface WorkflowRun {
  runId: string
  workflowKey: string
  revision: number
  status: keyof typeof runStatusLabels
  checkpoints: boolean
  createdAt: string
  targetPaperCount?: number
  sourceCount?: number
  steps: WorkflowStep[]
}

export interface WorkflowSnapshot {
  run: WorkflowRun | null
  modelConfigured: boolean
}

export interface StartWorkflowInput {
  requestId: string
  checkpoints: boolean
  targetPaperCount?: number
}

export interface ContinueWorkflowInput {
  requestId: string
  runId: string
  expectedRevision: number
}

export function isWorkflowActive(run: WorkflowRun | null) {
  return run?.status === 'QUEUED' || run?.status === 'RUNNING'
}

function isStep(value: unknown): value is WorkflowStep {
  const step = value as Partial<WorkflowStep> | null
  return !!step && typeof step.key === 'string' && /^[a-z][a-z0-9-]*$/.test(step.key)
    && typeof step.title === 'string' && typeof step.status === 'string' && Object.hasOwn(stepStatusLabels, step.status)
    && (step.message === undefined || typeof step.message === 'string')
}

function isRun(value: unknown): value is WorkflowRun {
  const run = value as Partial<WorkflowRun> | null
  return !!run && typeof run.runId === 'string' && !!run.runId && run.runId !== '.' && run.runId !== '..'
    && typeof run.workflowKey === 'string' && typeof run.revision === 'number' && Number.isSafeInteger(run.revision)
    && typeof run.status === 'string' && Object.hasOwn(runStatusLabels, run.status)
    && typeof run.checkpoints === 'boolean' && typeof run.createdAt === 'string'
    && (run.sourceCount === undefined || Number.isSafeInteger(run.sourceCount))
    && (run.targetPaperCount === undefined || Number.isSafeInteger(run.targetPaperCount))
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

function postRun(path: string, input: StartWorkflowInput | ContinueWorkflowInput, signal: AbortSignal) {
  return request(path, isRun, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}

export function startWorkflow(projectId: string, input: StartWorkflowInput, signal: AbortSignal) {
  return postRun(workflowPath(projectId), input, signal)
}

export function continueWorkflow(projectId: string, input: ContinueWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/continue`, input, signal)
}

export function workflowArtifactPath(projectId: string, runId: string, key: string) {
  return `${workflowPath(projectId)}/${encodeURIComponent(runId)}/steps/${encodeURIComponent(key)}/artifact`
}
