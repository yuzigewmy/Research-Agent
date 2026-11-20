import { request } from './projects'

export const queueTaskKindLabels = {
  plan: '研究计划',
  'manuscript-check': '只读稿件检查',
  workflow: '工作流步骤',
  review: '稿件审查',
  'automatic-review': '多轮审稿',
  edit: '稿件修改',
  recheck: '稿件复查',
} as const

interface CurrentTask {
  taskId: string
  title: string
  kind: keyof typeof queueTaskKindLabels
  status: 'RUNNING' | 'CANCELLING'
  revision: number
}

export interface TaskQueue {
  current: CurrentTask | null
  queuedCount: number
}

export type StopTaskInput =
  | { requestId: string; scope: 'current'; taskId: string; expectedRevision: number }
  | { requestId: string; scope: 'all' }

export interface StopTaskReceipt {
  scope: StopTaskInput['scope']
  stoppingCount: number
  cancelledCount: number
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isCurrentTask(value: unknown): value is CurrentTask {
  const task = value as Partial<CurrentTask> | null
  return !!task && typeof task.taskId === 'string' && task.taskId.length > 0
    && typeof task.title === 'string'
    && typeof task.kind === 'string' && Object.hasOwn(queueTaskKindLabels, task.kind)
    && (task.status === 'RUNNING' || task.status === 'CANCELLING')
    && isCount(task.revision) && task.revision > 0
}

function isTaskQueue(value: unknown): value is TaskQueue {
  const queue = value as Partial<TaskQueue> | null
  return !!queue && (queue.current === null || isCurrentTask(queue.current)) && isCount(queue.queuedCount)
}

function queuePath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/task-queue`
}

export function getTaskQueue(projectId: string, signal: AbortSignal) {
  return request(queuePath(projectId), isTaskQueue, { signal, cache: 'no-store' })
}

export function stopTaskQueue(projectId: string, input: StopTaskInput, signal: AbortSignal) {
  return request(`${queuePath(projectId)}/stop`, (value): value is StopTaskReceipt => {
    const receipt = value as Partial<StopTaskReceipt> | null
    return !!receipt && receipt.scope === input.scope && isCount(receipt.stoppingCount) && isCount(receipt.cancelledCount)
  }, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}
