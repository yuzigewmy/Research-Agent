import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import type { Dispatch, MouseEvent, ReactNode, SetStateAction } from 'react'
import type { ProjectTarget } from './App'
import { listNotifications, markNotificationRead, readReviewLink } from './notificationApi'
import type { NotificationPage, TaskNotification } from './notificationApi'
import { ProjectDate } from './ProjectDetails'
import { taskStatusLabels } from './tasks'
import { t, unreadCountLabel, useLocale } from './i18n'
import type { WorkbenchLanguage } from './i18n'
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

function notificationTarget(item: TaskNotification): ProjectTarget {
  const reviewLink = readReviewLink(item.taskId, item.reviewVersionId, item.reviewKind)
  if (reviewLink) return reviewLink
  return item.workflowRunId ? { workflowRunId: item.workflowRunId } : { taskId: item.taskId }
}

export function Notifications({ hrefFor, onOpen, onNavigate, language }: NotificationLinks & {
  onNavigate: (projectId: string, target: ProjectTarget) => void
  language?: WorkbenchLanguage
}) {
  const contextLocale = useLocale()
  const locale = language ?? contextLocale
  const [open, setOpen] = useState(false)
  const [unreadCount, setUnreadCount] = useState(0)
  const [refresh, setRefresh] = useState(0)
  const [history, setHistory] = useState<History>()
  const [headFailed, setHeadFailed] = useState(false)
  const disclosure = useRef<HTMLDetailsElement>(null)
  const trigger = useRef<HTMLElement>(null)
  const headRequest = useRef<AbortController | null>(null)
  const newest = useRef<bigint>(undefined)
  const desktopNotice = useRef<Notification | null>(null)
  const supported = window.isSecureContext && typeof window.Notification === 'function'
  const [permission, setPermission] = useState<NotificationPermission>(() => supported ? Notification.permission : 'default')
  const [desktopEnabled, setDesktopEnabled] = useState(false)
  const [askingPermission, setAskingPermission] = useState(false)
  const [desktopError, setDesktopError] = useState('')

  const showDesktopNotice = useEffectEvent((page: NotificationPage) => {
    if (!desktopEnabled) return
    if (Notification.permission !== 'granted') {
      setPermission(Notification.permission)
      setDesktopEnabled(false)
      desktopNotice.current?.close()
      return
    }
    const top = page.items[0]
    const previous = newest.current
    const id = top && /^\d+$/.test(top.notificationId) ? BigInt(top.notificationId) : 0n
    if (previous !== undefined && id <= previous) return
    newest.current = id
    if (previous === undefined) return
    const item = page.items.find(candidate => !candidate.read && /^\d+$/.test(candidate.notificationId)
      && BigInt(candidate.notificationId) > previous)
    if (!item) return

    const target = notificationTarget(item)
    try {
      desktopNotice.current?.close()
      const notice = new Notification('研究工作台', { body: '收到一条新的站内通知。', tag: `research-notice-${item.notificationId}` })
      desktopNotice.current = notice
      notice.onclick = () => {
        window.focus()
        onNavigate(item.projectId, target)
        close()
        notice.close()
      }
      notice.onerror = () => setDesktopError('桌面通知发送失败。可继续查看站内通知。')
    } catch {
      setDesktopError('当前浏览器无法显示桌面通知。可继续查看站内通知。')
    }
  })

  useEffect(() => () => desktopNotice.current?.close(), [])

  async function toggleDesktop() {
    setDesktopError('')
    if (desktopEnabled) {
      desktopNotice.current?.close()
      setDesktopEnabled(false)
      return
    }
    setAskingPermission(true)
    try {
      const result = Notification.permission === 'default' ? await Notification.requestPermission() : Notification.permission
      setPermission(result)
      setDesktopEnabled(result === 'granted')
      if (result === 'granted') {
        newest.current = undefined
        refreshHead()
      }
    } catch {
      setDesktopError('无法请求浏览器通知权限。可继续查看站内通知。')
    } finally {
      setAskingPermission(false)
    }
  }

  useEffect(() => {
    let timer: number | undefined

    async function poll() {
      if (document.visibilityState !== 'visible' && !desktopEnabled) return
      const controller = new AbortController()
      headRequest.current = controller
      const signal = requestSignal(controller)
      try {
        const page = await listNotifications(signal)
        signal.throwIfAborted()
        setUnreadCount(page.unreadCount)
        showDesktopNotice(page)
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
  }, [open, refresh, desktopEnabled])

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
      {t(locale, 'notifications.title')}{' '}
      <span aria-live="polite">
        {unreadCountLabel(locale, unreadCount)}
      </span>
    </summary>
    {open && <NotificationHistory locale={locale} hrefFor={hrefFor} onOpen={(event, projectId, target) => {
      if (event.defaultPrevented) return
      onOpen(event, projectId, target)
      if (event.defaultPrevented) close()
    }} history={history} setHistory={setHistory} headFailed={headFailed} refreshHead={refreshHead} desktopControls={
      <div className="notification-desktop">
        <p className="notification-meta" role="status">
          {!supported ? t(locale, 'notifications.desktopUnsupported')
            : permission === 'denied' ? t(locale, 'notifications.desktopDenied')
              : desktopEnabled ? t(locale, 'notifications.desktopEnabled') : t(locale, 'notifications.desktopPrompt')}
        </p>
        {supported && <button className="button outline" type="button" disabled={askingPermission} onClick={() => void toggleDesktop()}>
          {desktopEnabled ? t(locale, 'notifications.disableDesktop') : permission === 'denied' ? t(locale, 'notifications.checkPermission') : t(locale, 'notifications.enableDesktop')}
        </button>}
        {desktopError && <p className="form-error" role="alert">{desktopError}</p>}
      </div>
    } />}
  </details>
}

function NotificationHistory({ locale, hrefFor, onOpen, history, setHistory, headFailed, refreshHead, desktopControls }: NotificationLinks & {
  locale: WorkbenchLanguage
  desktopControls: ReactNode
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
    {desktopControls}
    {!!history?.items.length && <ol className="notification-list" aria-label={t(locale, 'notifications.listLabel')} role="list">
      {history.items.map((item) => {
        const target = notificationTarget(item)
        return <li key={item.notificationId}>
          <p className="notification-meta">
            <bdi>{item.projectName}</bdi> · {taskStatusLabels[item.status]} · <ProjectDate value={item.createdAt} withTime />
            {' · '}<span className="notification-read-state" aria-live="polite">{t(locale, item.read ? 'notifications.read' : 'notifications.unread')}</span>
          </p>
          <p className="notification-instruction" dir="auto">{item.instruction}</p>
          <div className="notification-actions">
            <a className="button outline" id={`${listId}-${item.notificationId}`} href={hrefFor(item.projectId, target)}
              onClick={(event) => onOpen(event, item.projectId, target)}>
              {target.reviewId ? t(locale, 'notifications.openReview') : target.workflowRunId ? t(locale, 'notifications.openWorkflow') : t(locale, 'notifications.openTask')}
            </a>
            {!item.read && <button className="button outline" type="button" aria-disabled={pendingRead !== undefined}
              onClick={(event) => void markRead(item.notificationId, event.currentTarget)}>
              {pendingRead === item.notificationId ? t(locale, 'notifications.markingRead') : t(locale, 'notifications.markRead')}
            </button>}
          </div>
          {failedRead === item.notificationId && <p className="form-error" role="alert">{t(locale, 'notifications.markFailed')}</p>}
        </li>
      })}
    </ol>}
    {history && history.items.length === 0 && <p className="notification-meta" role="status">{t(locale, 'notifications.empty')}</p>}
    {!history && !headFailed && <p className="notification-meta" role="status">{t(locale, 'notifications.loading')}</p>}
    {headFailed && <div className="notification-actions">
      <p className="form-error" role="alert">{t(locale, 'notifications.loadFailed')}</p>
      <button className="button outline" type="button" onClick={refreshHead}>{t(locale, 'notifications.retryLoad')}</button>
    </div>}
    {loadFailed && <p className="form-error" role="alert">{t(locale, 'notifications.loadOlderFailed')}</p>}
    {history?.nextCursor && <div className="notification-actions">
      <button className="button outline" type="button" aria-disabled={loading}
        onClick={(event) => {
          if (history.nextCursor) void loadOlder(history.nextCursor, event.currentTarget)
        }}>
        {loading ? t(locale, 'notifications.loadingMore') : loadFailed ? t(locale, 'notifications.retryLoadMore') : t(locale, 'notifications.loadMore')}
      </button>
    </div>}
  </div>
}
