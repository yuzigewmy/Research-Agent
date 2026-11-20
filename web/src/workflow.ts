import { request } from './projects'
import { isFileRecord } from './workspace'

export const runStatusLabels = {
  QUEUED: '排队中',
  RUNNING: '运行中',
  PAUSING: '正在暂停',
  PAUSED: '已暂停',
  CANCELLING: '正在取消本轮',
  CANCELLED: '本轮已取消',
  WAITING_CHECKPOINT: '等待确认',
  RANGE_COMPLETED: '本次范围已完成',
  COMPLETED: '全部步骤已完成',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

export const stepStatusLabels = {
  PENDING: '未开始',
  QUEUED: '排队中',
  RUNNING: '运行中',
  CANCELLING: '正在停止',
  CANCELLED: '已停止',
  SUCCEEDED: '已完成',
  SKIPPED: '已跳过',
  STALE: '待重算',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

interface WorkflowStepDefinition {
  key: string
  title: string
}

interface WorkflowArtifactSource {
  versionId: string
  path: string
  sha256: string
}

interface WorkflowStep extends WorkflowStepDefinition {
  status: keyof typeof stepStatusLabels
  message?: string
  artifactId?: string
  artifactCount?: number
  source?: WorkflowArtifactSource
}

interface WorkflowArtifact {
  artifactId: string
  createdAt: string
  current: boolean
  source?: WorkflowArtifactSource
}

export interface WorkflowArtifactPage {
  items: WorkflowArtifact[]
  nextCursor?: string
}

interface WorkflowRunSummary {
  runId: string
  status: keyof typeof runStatusLabels
  createdAt: string
}

export interface WorkflowRunPage {
  items: WorkflowRunSummary[]
  nextCursor?: string
}

export interface WorkflowRun extends WorkflowRunSummary {
  workflowKey: string
  revision: number
  checkpoints: boolean
  targetPaperCount?: number
  sourceCount?: number
  language?: 'zh' | 'en'
  acceptStepKey?: string
  skipStepKey?: string
  acceptedArtifactId?: string
  scope: { startStep: string; endStep: string }
  steps: WorkflowStep[]
}

export interface WorkflowSnapshot {
  run: WorkflowRun | null
  modelConfigured: boolean
  steps: WorkflowStepDefinition[]
}

export interface StartWorkflowInput {
  requestId: string
  checkpoints: boolean
  targetPaperCount?: number
  paper?: { language: 'zh' | 'en'; outline?: string }
  throughStep?: string
}

export interface WorkflowRunActionInput {
  requestId: string
  runId: string
  expectedRevision: number
}

export interface ContinueWorkflowInput extends WorkflowRunActionInput {
  throughStep?: string
}

export interface RerunWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
}

export interface AcceptWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
  sourceVersionId: string
}

export interface SkipWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
  reason: string
}

export function isWorkflowActive(run: WorkflowRun | null) {
  const status = run?.status
  return status === 'QUEUED' || status === 'RUNNING' || status === 'PAUSING' || status === 'CANCELLING'
}

function isStepDefinition(value: unknown): value is WorkflowStepDefinition {
  const step = value as Partial<WorkflowStepDefinition> | null
  return !!step && typeof step.key === 'string' && /^[a-z][a-z0-9-]*$/.test(step.key) && typeof step.title === 'string'
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isArtifactSource(value: unknown): value is WorkflowArtifactSource {
  const source = value as Partial<WorkflowArtifactSource> | null
  return !!source && isPathId(source.versionId) && typeof source.path === 'string' && source.path.length > 0
    && typeof source.sha256 === 'string' && /^[a-f\d]{64}$/i.test(source.sha256)
}

function isStep(value: unknown): value is WorkflowStep {
  const step = value as Partial<WorkflowStep> | null
  return !!step && typeof step.status === 'string' && Object.hasOwn(stepStatusLabels, step.status)
    && (step.message === undefined || typeof step.message === 'string')
    && (step.artifactId === undefined || isPathId(step.artifactId))
    && (step.artifactCount === undefined || (Number.isSafeInteger(step.artifactCount) && step.artifactCount >= 0))
    && (step.source === undefined || isArtifactSource(step.source))
    && isStepDefinition(step)
}

function isWorkflowArtifact(value: unknown): value is WorkflowArtifact {
  const artifact = value as Partial<WorkflowArtifact> | null
  return !!artifact && isPathId(artifact.artifactId) && typeof artifact.current === 'boolean'
    && typeof artifact.createdAt === 'string' && Number.isFinite(Date.parse(artifact.createdAt))
    && (artifact.source === undefined || isArtifactSource(artifact.source))
}

function isWorkflowArtifactPage(value: unknown): value is WorkflowArtifactPage {
  const page = value as Partial<WorkflowArtifactPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isWorkflowArtifact)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function isScope(value: unknown, steps: WorkflowStep[]) {
  const scope = value as Partial<WorkflowRun['scope']> | null
  if (!scope) return false
  const start = steps.findIndex(({ key }) => key === scope.startStep)
  const end = steps.findIndex(({ key }) => key === scope.endStep)
  return start >= 0 && end >= start
}

function isRunSummary(value: unknown): value is WorkflowRunSummary {
  const run = value as Partial<WorkflowRunSummary> | null
  return !!run && isPathId(run.runId)
    && typeof run.status === 'string' && Object.hasOwn(runStatusLabels, run.status)
    && typeof run.createdAt === 'string' && Number.isFinite(Date.parse(run.createdAt))
}

function isRunPage(value: unknown): value is WorkflowRunPage {
  const page = value as Partial<WorkflowRunPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isRunSummary)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function isRun(value: unknown): value is WorkflowRun {
  const run = value as Partial<WorkflowRun> | null
  return !!run && typeof run.workflowKey === 'string' && typeof run.revision === 'number' && Number.isSafeInteger(run.revision)
    && typeof run.checkpoints === 'boolean'
    && (run.sourceCount === undefined || Number.isSafeInteger(run.sourceCount))
    && (run.targetPaperCount === undefined || Number.isSafeInteger(run.targetPaperCount))
    && (run.language === undefined || run.language === 'zh' || run.language === 'en')
    && (run.acceptedArtifactId === undefined || isPathId(run.acceptedArtifactId))
    && Array.isArray(run.steps) && run.steps.every(isStep)
    && (run.acceptStepKey === undefined || run.steps.some((step) => step.key === run.acceptStepKey))
    && (run.skipStepKey === undefined || run.steps.some((step) => step.key === run.skipStepKey))
    && isScope(run.scope, run.steps) && isRunSummary(run)
}

function isSnapshot(value: unknown): value is WorkflowSnapshot {
  const snapshot = value as Partial<WorkflowSnapshot> | null
  return !!snapshot && typeof snapshot.modelConfigured === 'boolean'
    && (snapshot.run === null || isRun(snapshot.run))
    && Array.isArray(snapshot.steps) && snapshot.steps.every(isStepDefinition)
}

function workflowPath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/workflow`
}

export function getWorkflow(projectId: string, signal: AbortSignal) {
  return request(workflowPath(projectId), isSnapshot, { cache: 'no-store', signal })
}

export function listWorkflowRuns(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(`${workflowPath(projectId)}/runs${query}`, isRunPage, { cache: 'no-store', signal })
}

export function getWorkflowRun(projectId: string, runId: string, signal: AbortSignal) {
  return request(`${workflowPath(projectId)}/runs/${encodeURIComponent(runId)}`,
    (value): value is WorkflowRun => isRun(value) && value.runId === runId, { cache: 'no-store', signal })
}

function postRun(path: string, input: StartWorkflowInput | WorkflowRunActionInput, signal: AbortSignal) {
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

export function pauseWorkflow(projectId: string, input: WorkflowRunActionInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/pause`, input, signal)
}

export function cancelWorkflow(projectId: string, input: WorkflowRunActionInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/cancel`, input, signal)
}

export function rerunWorkflow(projectId: string, input: RerunWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/rerun`, input, signal)
}

export function acceptWorkflow(projectId: string, input: AcceptWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/accept`, input, signal)
}

export function skipWorkflow(projectId: string, input: SkipWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/skip`, input, signal)
}

function workflowStepPath(projectId: string, runId: string, key: string) {
  return `${workflowPath(projectId)}/${encodeURIComponent(runId)}/steps/${encodeURIComponent(key)}`
}

export function workflowArtifactPath(projectId: string, runId: string, key: string, artifactId?: string) {
  const step = workflowStepPath(projectId, runId, key)
  return artifactId === undefined ? `${step}/artifact` : `${step}/artifacts/${encodeURIComponent(artifactId)}`
}

export function listWorkflowArtifacts(projectId: string, runId: string, key: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(`${workflowStepPath(projectId, runId, key)}/artifacts${query}`, isWorkflowArtifactPage, { cache: 'no-store', signal })
}

export function saveManuscript(projectId: string, runId: string, signal: AbortSignal, artifactId?: string) {
  const query = artifactId ? `?${new URLSearchParams({ artifactId })}` : ''
  return request(`${workflowPath(projectId)}/${encodeURIComponent(runId)}/manuscript${query}`, isFileRecord, { method: 'POST', signal })
}
