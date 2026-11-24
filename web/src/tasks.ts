import { ProjectApiError, request } from './projects'

export const taskStatusLabels = {
  PROPOSED: '待确认',
  QUEUED: '排队中',
  RUNNING: '运行中',
  CANCELLING: '正在取消',
  CANCELLED: '已取消',
  SUCCEEDED: '已完成',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

export type TaskStatus = keyof typeof taskStatusLabels
export type TaskAction = 'confirm' | 'cancel'
export type TaskLink = { taskId: string }

export interface TaskInputVersion {
  versionId: string
  path: string
  sha256: string
}

export interface Task {
  taskId: string
  instruction: string
  revision: number
  status: TaskStatus
  createdAt: string
  kind?: 'manuscript-check' | 'selection-edit' | 'focused-research' | 'experiment-comparison'
  message?: string
  inputs?: TaskInputVersion[]
  sourceTurnId?: string
}

export interface TaskPage {
  items: Task[]
  nextCursor?: string
  modelConfigured: boolean
}

export interface ProposalInput {
  requestId: string
  instruction: string
  inputVersionIds?: string[]
  sourceTurnId?: string
  kind?: 'experiment-comparison'
}

interface ActionInput {
  requestId: string
  expectedRevision: number
}

export const taskInstructionLimit = 8_192
export const taskInputLimit = 4

export function isMarkdownTaskPath(path: string) {
  const lower = path.toLowerCase()
  return lower.endsWith('.md') || lower.endsWith('.markdown')
}

export function isCSVTaskPath(path: string) {
  return /\.csv$/i.test(path)
}

export function isTaskActive(status: TaskStatus) {
  return status === 'QUEUED' || status === 'RUNNING' || status === 'CANCELLING'
}

function isTaskInputVersion(value: unknown): value is TaskInputVersion {
  const input = value as Partial<TaskInputVersion> | null
  return !!input && typeof input.versionId === 'string' && !!input.versionId
    && input.versionId !== '.' && input.versionId !== '..'
    && typeof input.path === 'string' && !!input.path
    && typeof input.sha256 === 'string' && /^[a-f\d]{64}$/i.test(input.sha256)
}

function isTask(value: unknown): value is Task {
  const task = value as Partial<Task> | null
  return !!task && typeof task.taskId === 'string' && !!task.taskId && task.taskId !== '.' && task.taskId !== '..'
    && typeof task.instruction === 'string'
    && typeof task.revision === 'number' && Number.isSafeInteger(task.revision) && task.revision >= 1
    && typeof task.status === 'string' && Object.hasOwn(taskStatusLabels, task.status)
    && typeof task.createdAt === 'string' && Number.isFinite(Date.parse(task.createdAt))
    && (task.kind === undefined || task.kind === 'manuscript-check' || task.kind === 'selection-edit'
      || task.kind === 'focused-research' || task.kind === 'experiment-comparison')
    && (task.message === undefined || typeof task.message === 'string')
    && (task.sourceTurnId === undefined || (typeof task.sourceTurnId === 'string' && task.sourceTurnId.length > 0))
    && (task.inputs === undefined || (Array.isArray(task.inputs) && task.inputs.length <= taskInputLimit
      && task.inputs.every(isTaskInputVersion)))
    && (task.kind === undefined || (task.inputs !== undefined && task.inputs.length >= 1
      && (task.kind === 'experiment-comparison'
        ? task.inputs.length === 1 && isCSVTaskPath(task.inputs[0].path)
        : (task.kind !== 'manuscript-check' || task.inputs.length === 1) && task.inputs.every(({ path }) => isMarkdownTaskPath(path)))))
    && (task.kind !== 'selection-edit' || (task.inputs !== undefined
      && new Set(task.inputs.map(({ path }) => path)).size === task.inputs.length
      && new Set(task.inputs.map(({ versionId }) => versionId)).size === task.inputs.length))
}

function isTaskPage(value: unknown): value is TaskPage {
  const page = value as Partial<TaskPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isTask)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
    && typeof page.modelConfigured === 'boolean'
}

function tasksPath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/tasks`
}

function taskPath(projectId: string, taskId: string) {
  return `${tasksPath(projectId)}/${encodeURIComponent(taskId)}`
}

export function conversationTurnAnchor(turnId: string) {
  return `conversation-turn-${turnId}`
}

export function listTasks(projectId: string, signal: AbortSignal, cursor?: string, sourceTurnId?: string) {
  const query = new URLSearchParams()
  if (cursor) query.set('cursor', cursor)
  if (sourceTurnId) query.set('sourceTurnId', sourceTurnId)
  const suffix = query.size ? `?${query}` : ''
  return request(tasksPath(projectId) + suffix, (value): value is TaskPage =>
    isTaskPage(value) && (!sourceTurnId || value.items.every((task) => task.sourceTurnId === sourceTurnId)), { signal })
}

export async function getTask(projectId: string, taskId: string, signal: AbortSignal) {
  // URL parsing collapses dot segments before the request is sent.
  if ([projectId, taskId].some((id) => !id || id === '.' || id === '..')) {
    throw new ProjectApiError('任务链接无效。', 400)
  }
  return request(taskPath(projectId, taskId), (value): value is Task =>
    isTask(value) && value.taskId === taskId, { cache: 'no-store', signal })
}

// Poll the latest page, then look up known active tasks that have moved into history.
export async function refreshTasks(projectId: string, taskIds: string[], signal: AbortSignal, sourceTurnId?: string) {
  const first = await listTasks(projectId, signal, undefined, sourceTurnId)
  const pending = new Set(taskIds)
  const items = [...first.items]
  const visited = new Set<string>()
  for (const task of items) pending.delete(task.taskId)

  let cursor = first.nextCursor
  while (pending.size && cursor && !visited.has(cursor)) {
    visited.add(cursor)
    const page = await listTasks(projectId, signal, cursor, sourceTurnId)
    for (const task of page.items) {
      if (pending.delete(task.taskId)) items.push(task)
    }
    cursor = page.nextCursor
  }
  return { ...first, items }
}

function sendTask(path: string, input: ProposalInput | ActionInput, signal: AbortSignal, method = 'POST') {
  return request(path, isTask, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}

export function proposeTask(projectId: string, input: ProposalInput, signal: AbortSignal) {
  return sendTask(tasksPath(projectId), input, signal)
}

export function changeTask(projectId: string, taskId: string, action: TaskAction, input: ActionInput, signal: AbortSignal) {
  return sendTask(`${taskPath(projectId, taskId)}/${action}`, input, signal)
}

export function editTask(projectId: string, taskId: string, input: ActionInput & Pick<ProposalInput, 'instruction' | 'inputVersionIds'>, signal: AbortSignal) {
  return sendTask(taskPath(projectId, taskId), input, signal, 'PATCH')
}

export function taskArtifactPath(projectId: string, taskId: string) {
  return `${taskPath(projectId, taskId)}/artifact`
}
