import { request } from './projects'

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

export interface Task {
  taskId: string
  instruction: string
  revision: number
  status: TaskStatus
  createdAt: string
  message?: string
}

export interface TaskPage {
  items: Task[]
  nextCursor?: string
  modelConfigured: boolean
}

interface ProposalInput {
  requestId: string
  instruction: string
}

interface ActionInput {
  requestId: string
  expectedRevision: number
}

export const taskInstructionLimit = 8_192

export function isTaskActive(status: TaskStatus) {
  return status === 'QUEUED' || status === 'RUNNING' || status === 'CANCELLING'
}

function isTask(value: unknown): value is Task {
  const task = value as Partial<Task> | null
  return !!task && typeof task.taskId === 'string' && !!task.taskId && task.taskId !== '.' && task.taskId !== '..'
    && typeof task.instruction === 'string'
    && typeof task.revision === 'number' && Number.isSafeInteger(task.revision) && task.revision >= 1
    && typeof task.status === 'string' && Object.hasOwn(taskStatusLabels, task.status)
    && typeof task.createdAt === 'string' && Number.isFinite(Date.parse(task.createdAt))
    && (task.message === undefined || typeof task.message === 'string')
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

export function listTasks(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(tasksPath(projectId) + query, isTaskPage, { signal })
}

// Poll the latest page, then look up known active tasks that have moved into history.
export async function refreshTasks(projectId: string, taskIds: string[], signal: AbortSignal) {
  const first = await listTasks(projectId, signal)
  const pending = new Set(taskIds)
  const items = [...first.items]
  const visited = new Set<string>()
  for (const task of items) pending.delete(task.taskId)

  let cursor = first.nextCursor
  while (pending.size && cursor && !visited.has(cursor)) {
    visited.add(cursor)
    const page = await listTasks(projectId, signal, cursor)
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

export function editTask(projectId: string, taskId: string, input: ActionInput & { instruction: string }, signal: AbortSignal) {
  return sendTask(taskPath(projectId, taskId), input, signal, 'PATCH')
}

export function taskArtifactPath(projectId: string, taskId: string) {
  return `${taskPath(projectId, taskId)}/artifact`
}
