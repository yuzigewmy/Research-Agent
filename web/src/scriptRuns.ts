import { ProjectApiError, request } from './projects'
import { isFileRecord } from './workspace'
import type { FileRecord } from './workspace'

export const scriptRunStatusLabels = {
  QUEUED: '排队中',
  RUNNING: '运行中',
  SUCCEEDED: '已完成',
  FAILED: '执行失败',
  TIMED_OUT: '已超时',
  CANCELLING: '正在停止',
  CANCELLED: '已取消',
  INTERRUPTED: '已中断',
} as const

export type ScriptRunStatus = keyof typeof scriptRunStatusLabels

export interface ScriptRunLimits {
  runner: string
  networkAccess: boolean
  minTimeoutSeconds: number
  maxTimeoutSeconds: number
  defaultTimeoutSeconds: number
  maxScriptBytes: number
  maxInputBytes: number
  maxOutputFiles: number
  maxOutputFileBytes: number
  maxOutputTotalBytes: number
  maxLogBytes: number
}

export interface ScriptRunArtifact {
  name: string
  size: number
  sha256: string
}

export interface ScriptRun {
  runId: string
  projectId: string
  requestId: string
  scriptVersionId: string
  scriptPath: string
  scriptSha256: string
  inputVersionId?: string
  inputPath?: string
  inputSha256?: string
  timeoutSeconds: number
  maxOutputFiles: number
  maxOutputFileBytes: number
  maxLogBytes: number
  status: ScriptRunStatus
  phase: string
  exitCode?: number
  stdout: string
  stderr: string
  stdoutTruncated: boolean
  stderrTruncated: boolean
  artifacts: ScriptRunArtifact[]
  errorMessage?: string
  createdAt: string
  startedAt?: string
  finishedAt?: string
}

export interface ScriptRunListResponse {
  configured: boolean
  limits: ScriptRunLimits
  items: ScriptRun[]
}

export interface CreateScriptRunInput {
  requestId: string
  scriptVersionId: string
  inputVersionId?: string
  timeoutSeconds: number
  confirmed: boolean
}

const sha256Pattern = /^[a-f0-9]{64}$/i

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isDigest(value: unknown): value is string {
  return typeof value === 'string' && sha256Pattern.test(value)
}

function isPositiveInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isNonNegativeInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isScriptRunLimits(value: unknown): value is ScriptRunLimits {
  return isRecord(value)
    && typeof value.runner === 'string' && value.runner.length > 0
    && typeof value.networkAccess === 'boolean'
    && isPositiveInt(value.minTimeoutSeconds)
    && isPositiveInt(value.maxTimeoutSeconds)
    && isPositiveInt(value.defaultTimeoutSeconds)
    && isPositiveInt(value.maxScriptBytes)
    && isPositiveInt(value.maxInputBytes)
    && isPositiveInt(value.maxOutputFiles)
    && isPositiveInt(value.maxOutputFileBytes)
    && isPositiveInt(value.maxOutputTotalBytes)
    && isPositiveInt(value.maxLogBytes)
}

function isScriptRunArtifact(value: unknown): value is ScriptRunArtifact {
  return isRecord(value)
    && typeof value.name === 'string' && value.name.length > 0
    && isNonNegativeInt(value.size)
    && isDigest(value.sha256)
}

export function isScriptRun(value: unknown): value is ScriptRun {
  return isRecord(value)
    && isPathId(value.runId)
    && isPathId(value.projectId)
    && typeof value.requestId === 'string' && value.requestId.length > 0
    && isPathId(value.scriptVersionId)
    && typeof value.scriptPath === 'string' && value.scriptPath.length > 0
    && isDigest(value.scriptSha256)
    && (value.inputVersionId === undefined || isPathId(value.inputVersionId))
    && (value.inputPath === undefined || typeof value.inputPath === 'string')
    && (value.inputSha256 === undefined || isDigest(value.inputSha256))
    && isPositiveInt(value.timeoutSeconds)
    && isPositiveInt(value.maxOutputFiles)
    && isPositiveInt(value.maxOutputFileBytes)
    && isPositiveInt(value.maxLogBytes)
    && typeof value.status === 'string' && Object.hasOwn(scriptRunStatusLabels, value.status)
    && typeof value.phase === 'string'
    && (value.exitCode === undefined || (typeof value.exitCode === 'number' && Number.isSafeInteger(value.exitCode)))
    && typeof value.stdout === 'string'
    && typeof value.stderr === 'string'
    && typeof value.stdoutTruncated === 'boolean'
    && typeof value.stderrTruncated === 'boolean'
    && Array.isArray(value.artifacts) && value.artifacts.every(isScriptRunArtifact)
    && (value.errorMessage === undefined || typeof value.errorMessage === 'string')
    && typeof value.createdAt === 'string' && Number.isFinite(Date.parse(value.createdAt))
    && (value.startedAt === undefined || (typeof value.startedAt === 'string' && Number.isFinite(Date.parse(value.startedAt))))
    && (value.finishedAt === undefined || (typeof value.finishedAt === 'string' && Number.isFinite(Date.parse(value.finishedAt))))
}

function isScriptRunListResponse(value: unknown): value is ScriptRunListResponse {
  return isRecord(value)
    && typeof value.configured === 'boolean'
    && isScriptRunLimits(value.limits)
    && Array.isArray(value.items) && value.items.every(isScriptRun)
}

export function isScriptRunActive(status: ScriptRunStatus): boolean {
  return status === 'QUEUED' || status === 'RUNNING' || status === 'CANCELLING'
}

function basePath(projectId: string) {
  if (!isPathId(projectId)) throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}/script-runs`
}

function runPath(projectId: string, runId: string) {
  if (!isPathId(runId)) throw new ProjectApiError('运行记录编号无效。', 400)
  return `${basePath(projectId)}/${encodeURIComponent(runId)}`
}

export function listScriptRuns(projectId: string, signal: AbortSignal) {
  return request(basePath(projectId), isScriptRunListResponse, { signal, cache: 'no-store' })
}

export function createScriptRun(projectId: string, input: CreateScriptRunInput, signal: AbortSignal) {
  return request(basePath(projectId), isScriptRun, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    cache: 'no-store',
    signal,
  })
}

export function getScriptRun(projectId: string, runId: string, signal: AbortSignal) {
  return request(runPath(projectId, runId), isScriptRun, { signal, cache: 'no-store' })
}

export function cancelScriptRun(projectId: string, runId: string, signal: AbortSignal) {
  return request(`${runPath(projectId, runId)}/cancel`, isScriptRun, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
    cache: 'no-store',
    signal,
  })
}

export function publishScriptArtifact(
  projectId: string,
  runId: string,
  name: string,
  sha256: string,
  signal: AbortSignal,
): Promise<FileRecord> {
  return request(`${runPath(projectId, runId)}/artifacts/publish`, isFileRecord, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, sha256 }),
    cache: 'no-store',
    signal,
  })
}
