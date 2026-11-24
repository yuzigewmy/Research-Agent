import { request } from './projects'

export interface TaskNotification {
  notificationId: string
  projectId: string
  projectName: string
  taskId: string
  instruction: string
  status: 'PROPOSED' | 'FAILED' | 'INTERRUPTED' | 'SUCCEEDED'
  createdAt: string
  read: boolean
}

export interface NotificationPage {
  items: TaskNotification[]
  unreadCount: number
  nextCursor?: string
}

const endpoint = '/api/v1/notifications'

function isId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isNotification(value: unknown): value is TaskNotification {
  const item = value as Partial<TaskNotification> | null
  return !!item && isId(item.notificationId) && isId(item.projectId) && isId(item.taskId)
    && typeof item.projectName === 'string' && typeof item.instruction === 'string'
    && (item.status === 'PROPOSED' || item.status === 'FAILED' || item.status === 'INTERRUPTED' || item.status === 'SUCCEEDED')
    && typeof item.createdAt === 'string' && Number.isFinite(Date.parse(item.createdAt)) && typeof item.read === 'boolean'
}

function isNotificationPage(value: unknown): value is NotificationPage {
  const page = value as Partial<NotificationPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isNotification)
    && typeof page.unreadCount === 'number' && Number.isSafeInteger(page.unreadCount) && page.unreadCount >= 0
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

export function listNotifications(signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(endpoint + query, isNotificationPage, { cache: 'no-store', signal })
}

export function markNotificationRead(notificationId: string, signal: AbortSignal) {
  return request(`${endpoint}/${encodeURIComponent(notificationId)}`, (value): value is null => value === null, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ read: true }), signal,
  })
}
