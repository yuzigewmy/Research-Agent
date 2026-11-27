import { useEffect, useId, useRef, useState } from 'react'
import type { Dispatch, MouseEvent, SetStateAction } from 'react'
import type { ProjectTarget } from './App'
import { listNotifications, markNotificationRead } from './notificationApi'
import type { NotificationPage } from './notificationApi'
import { ProjectDate } from './ProjectDetails'
import { taskStatusLabels } from './tasks'
import './notifications.css'

const pollInterval = 5_000
const requestTimeout = 10_000

type NotificationLinks = {
  hrefFor: (projectId: string, target: ProjectTarget) => string
  onOpen: (event: MouseEvent<HTMLAnchorElement>, projectId: string, target: ProjectTarget) => void
}

type History = Pick<NotificationPage, 'items' | 'nextCursor'>

function prependLatest(history: History | undefined, page: History): History {
  if (!history) return page
  const previous = new Map(history.items.map((item) => [item.notificationId, item]))
  const latestIds = new Set(page.items.map(({ notificationId }) => notificationId))
  // If a burst moved the whole previous head out of the latest page, start at the new cursor to cover the gap.
  if (!page.items.some(({ notificationId }) => previous.has(notificationId))) return page
  return {
    items: [
      ...page.items.map((item) => previous.get(item.notificationId)?.read ? { ...item, read: true } : item),
      ...history.items.filter(({ notificationId }) => !latestIds.has(notificationId)),
    ],
    nextCursor: history.nextCursor,
  }
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
}

export function Notifications({ hrefFor, onOpen }: NotificationLinks) {
  const [open, setOpen] = useState(false)
  const [unreadCount, setUnreadCount] = useState(0)
  const [refresh, setRefresh] = useState(0)
  const [history, setHistory] = useState<History>()
  const [headFailed, setHeadFailed] = useState(false)
  const disclosure = useRef<HTMLDetailsElement>(null)
  const trigger = useRef<HTMLElement>(null)
  const headRequest = useRef<AbortController | null>(null)

  useEffect(() => {
    let timer: number | undefined

    async function poll() {
      if (document.visibilityState !== 'visible') return
      const controller = new AbortController()
      headRequest.current = controller
      const signal = requestSignal(controller)
      try {
        const page = await listNotifications(signal)
        signal.throwIfAborted()
        setUnreadCount(page.unreadCount)
        if (open) {
          setHistory((current) => prependLatest(current, page))
          setHeadFailed(false)
        }
      } catch {
        if (open && !controller.signal.aborted) setHeadFailed(true)
      } finally {
        if (!controller.signal.aborted) timer = window.setTimeout(poll, pollInterval)
      }
    }

    function stop() {
      window.clearTimeout(timer)
      headRequest.current?.abort()
    }

    function resumeIfVisible() {
      stop()
      void poll()
    }

    void poll()
    document.addEventListener('visibilitychange', resumeIfVisible)
    return () => {
      document.removeEventListener('visibilitychange', resumeIfVisible)
      stop()
    }
  }, [open, refresh])

  function refreshHead() {
    headRequest.current?.abort()
    setRefresh((value) => value + 1)
  }

  function setDisclosureOpen(next: boolean) {
    headRequest.current?.abort()
    setOpen(next)
    if (!next) {
      setHistory(undefined)
      setHeadFailed(false)
    }
  }

  function close() {
    if (disclosure.current) disclosure.current.open = false
    setDisclosureOpen(false)
  }

  return <details className="notifications" ref={disclosure}
    onToggle={(event) => {
      if (event.currentTarget.open !== open) setDisclosureOpen(event.currentTarget.open)
    }}
    onKeyDown={(event) => {
      if (event.key !== 'Escape' || !event.currentTarget.open) return
      event.preventDefault()
      close()
      trigger.current?.focus()
    }}>
    <summary className="button outline" ref={trigger}>
      通知 <span aria-live="polite">{unreadCount > 0 ? `${unreadCount} 条未读` : ''}</span>
    </summary>
    {open && <NotificationHistory hrefFor={hrefFor} onOpen={(event, projectId, target) => {
      if (event.defaultPrevented) return
      onOpen(event, projectId, target)
      if (event.defaultPrevented) close()
    }} history={history} setHistory={setHistory} headFailed={headFailed} refreshHead={refreshHead} />}
  </details>
}

function NotificationHistory({ hrefFor, onOpen, history, setHistory, headFailed, refreshHead }: NotificationLinks & {
  history?: History
  setHistory: Dispatch<SetStateAction<History | undefined>>
  headFailed: boolean
  refreshHead: () => void
}) {
  const [loading, setLoading] = useState(false)
  const [loadFailed, setLoadFailed] = useState(false)
  const [pendingRead, setPendingRead] = useState<string>()
  const [failedRead, setFailedRead] = useState<string>()
  const listRequest = useRef<AbortController | null>(null)
  const readRequest = useRef<AbortController | null>(null)
  const focusAfterPage = useRef<string>(undefined)
  const listId = useId()

  async function loadOlder(cursor: string, control: HTMLButtonElement) {
    if (listRequest.current) return
    const controller = new AbortController()
    listRequest.current = controller
    const signal = requestSignal(controller)
    setLoading(true)
    setLoadFailed(false)
    try {
      const page = await listNotifications(signal, cursor)
      signal.throwIfAborted()
      if (document.activeElement === control) focusAfterPage.current = page.items[0]?.notificationId
      setHistory((current) => {
        // The first page can move past this cursor while the request is in flight.
        if (current?.nextCursor !== cursor) return current
        const seen = new Set(current.items.map(({ notificationId }) => notificationId))
        const added = page.items.filter(({ notificationId }) => !seen.has(notificationId))
        return { items: [...current.items, ...added], nextCursor: page.nextCursor !== cursor ? page.nextCursor : undefined }
      })
    } catch {
      if (!controller.signal.aborted) setLoadFailed(true)
    } finally {
      listRequest.current = null
      if (!controller.signal.aborted) setLoading(false)
    }
  }

  useEffect(() => () => {
    listRequest.current?.abort()
    readRequest.current?.abort()
  }, [])

  useEffect(() => {
    const notificationId = focusAfterPage.current
    focusAfterPage.current = undefined
    if (notificationId) document.getElementById(`${listId}-${notificationId}`)?.focus()
  }, [history, listId])

  async function markRead(notificationId: string, control: HTMLButtonElement) {
    if (readRequest.current) return
    const controller = new AbortController()
    readRequest.current = controller
    const signal = requestSignal(controller)
    setPendingRead(notificationId)
    setFailedRead(undefined)
    try {
      await markNotificationRead(notificationId, signal)
      signal.throwIfAborted()
      if (document.activeElement === control) document.getElementById(`${listId}-${notificationId}`)?.focus()
      setHistory((current) => current && ({
        ...current,
        items: current.items.map((item) => item.notificationId === notificationId ? { ...item, read: true } : item),
      }))
      refreshHead()
    } catch {
      if (!controller.signal.aborted) setFailedRead(notificationId)
    } finally {
      readRequest.current = null
      if (!controller.signal.aborted) setPendingRead(undefined)
    }
  }

  return <div className="notification-history">
    {!!history?.items.length && <ol className="notification-list" aria-label="站内通知" role="list">
      {history.items.map((item) => {
        const target = item.workflowRunId ? { workflowRunId: item.workflowRunId } : { taskId: item.taskId }
        return <li key={item.notificationId}>
          <p className="notification-meta">
            <bdi>{item.projectName}</bdi> · {taskStatusLabels[item.status]} · <ProjectDate value={item.createdAt} withTime />
            {' · '}<span className="notification-read-state" aria-live="polite">{item.read ? '已读' : '未读'}</span>
          </p>
          <p className="notification-instruction" dir="auto">{item.instruction}</p>
          <div className="notification-actions">
            <a className="button outline" id={`${listId}-${item.notificationId}`} href={hrefFor(item.projectId, target)}
              onClick={(event) => onOpen(event, item.projectId, target)}>
              {item.workflowRunId ? '打开工作流' : '打开任务'}
            </a>
            {!item.read && <button className="button outline" type="button" aria-disabled={pendingRead !== undefined}
              onClick={(event) => void markRead(item.notificationId, event.currentTarget)}>
              {pendingRead === item.notificationId ? '正在标记…' : '标为已读'}
            </button>}
          </div>
          {failedRead === item.notificationId && <p className="form-error" role="alert">标记失败，请重试。</p>}
        </li>
      })}
    </ol>}
    {history && history.items.length === 0 && <p className="notification-meta" role="status">暂无通知。</p>}
    {!history && !headFailed && <p className="notification-meta" role="status">正在读取通知…</p>}
    {headFailed && <div className="notification-actions">
      <p className="form-error" role="alert">读取通知失败，请重试。</p>
      <button className="button outline" type="button" onClick={refreshHead}>重试读取通知</button>
    </div>}
    {loadFailed && <p className="form-error" role="alert">读取较早通知失败，请重试。</p>}
    {history?.nextCursor && <div className="notification-actions">
      <button className="button outline" type="button" aria-disabled={loading}
        onClick={(event) => {
          if (history.nextCursor) void loadOlder(history.nextCursor, event.currentTarget)
        }}>
        {loading ? '正在读取…' : loadFailed ? '重试加载更多' : '加载更多'}
      </button>
    </div>}
  </div>
}
