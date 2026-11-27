import { useEffect, useId, useRef, useState } from 'react'
import { t, useLocale } from './i18n'
import { errorMessage, ProjectApiError } from './projects'
import { getTaskQueue, queueTaskKindLabels, stopTaskQueue } from './taskQueue'
import type { StopTaskInput, StopTaskReceipt, TaskQueue } from './taskQueue'
import './tasks.css'

const activeInterval = 2_000
const idleInterval = 6_000
const requestTimeout = 10_000

type StopFeedback = { receipt: StopTaskReceipt; taskId?: string; runningTaskStopped?: boolean }

function describeStop({ receipt, runningTaskStopped }: StopFeedback) {
  const messages: string[] = []
  if (receipt.stoppingCount > 0) messages.push(runningTaskStopped ? '运行任务已停止。' : '已请求停止运行任务。')
  if (receipt.cancelledCount > 0) messages.push(`已停止 ${receipt.cancelledCount} 项排队任务。`)
  return messages.join('') || '停止请求已处理。'
}

export function ProjectTaskQueue({ projectId, onStopped }: { projectId: string; onStopped: () => void }) {
  const locale = useLocale()
  const id = useId()
  const [queue, setQueue] = useState<TaskQueue>()
  const [refreshing, setRefreshing] = useState(true)
  const [readError, setReadError] = useState('')
  const [reload, setReload] = useState(0)
  const [confirmingAll, setConfirmingAll] = useState(false)
  const [pending, setPending] = useState<StopTaskInput['scope']>()
  const [retry, setRetry] = useState<StopTaskInput>()
  const [actionError, setActionError] = useState('')
  const [feedback, setFeedback] = useState<StopFeedback>()
  const read = useRef<AbortController | null>(null)
  const submission = useRef<AbortController | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  const stopAllButton = useRef<HTMLButtonElement>(null)
  const current = queue?.current
  const actionsDisabled = queue === undefined || refreshing || !!readError || pending !== undefined || retry !== undefined

  useEffect(() => {
    const controller = new AbortController()
    read.current = controller
    let timer: number | undefined

    async function poll() {
      if (controller.signal.aborted) return
      let interval = idleInterval
      try {
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
        const latest = await getTaskQueue(projectId, signal)
        if (controller.signal.aborted) return
        setQueue(latest)
        setReadError('')
        setFeedback((previous) => {
          if (!previous?.receipt.stoppingCount || previous.runningTaskStopped) return previous
          const stopped = latest.current?.status !== 'CANCELLING'
            || (previous.taskId !== undefined && latest.current.taskId !== previous.taskId)
          return stopped ? { ...previous, runningTaskStopped: true } : previous
        })
        if (latest.current || latest.queuedCount) interval = activeInterval
      } catch (error) {
        if (!controller.signal.aborted) setReadError(errorMessage(error))
      } finally {
        if (!controller.signal.aborted) {
          setRefreshing(false)
          timer = window.setTimeout(poll, interval)
        }
      }
    }

    void poll()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [projectId, reload])

  useEffect(() => () => submission.current?.abort(), [])

  function refresh() {
    // Discard any read started before the stop response.
    read.current?.abort()
    setReadError('')
    setRefreshing(true)
    setReload((version) => version + 1)
  }

  async function stop(input: StopTaskInput) {
    if (submission.current) return
    const controller = new AbortController()
    const control = document.activeElement
    submission.current = controller
    setPending(input.scope)
    setActionError('')
    setFeedback(undefined)
    if (confirmingAll) {
      heading.current?.focus()
      setConfirmingAll(false)
    }

    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
      const receipt = await stopTaskQueue(projectId, input, signal)
      if (controller.signal.aborted) return
      if (document.activeElement === control) heading.current?.focus()
      setRetry(undefined)
      setFeedback({ receipt, taskId: input.scope === 'current' ? input.taskId : undefined })
      refresh()
      onStopped()
    } catch (error) {
      if (controller.signal.aborted) return
      if (document.activeElement === control) heading.current?.focus()
      if (error instanceof ProjectApiError && error.status === 409 && error.code === 'TASK_QUEUE_CHANGED') {
        setRetry(undefined)
        setActionError('当前任务已更新。请核对最新任务后再停止。')
        refresh()
      } else {
        setRetry(input)
        setActionError(errorMessage(error))
      }
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  return (
    <section className="task-panel" aria-labelledby={`${id}-heading`}>
      <h4 id={`${id}-heading`} ref={heading} tabIndex={-1}>{t(locale, 'panel.backgroundTasks')}</h4>
      <div role="status">
        {queue === undefined && refreshing && <p className="task-note">正在读取后台任务…</p>}
        {queue && <>
          {current ? <div className="task-card">
            <div className="task-card-header">
              <p className="task-note">{queueTaskKindLabels[current.kind]}</p>
              <strong className="task-note">{current.status === 'CANCELLING' ? '正在取消' : '运行中'}</strong>
            </div>
            {current.title && current.title !== queueTaskKindLabels[current.kind] && <p className="task-instruction" dir="auto">{current.title}</p>}
          </div> : <p className="task-note">当前没有运行中的后台任务。</p>}
          <p className="task-note">排队任务 {queue.queuedCount} 项</p>
        </>}
      </div>
      {readError && <div className="request-error">
        <p role="alert">{readError}</p>
        <button className="button outline" type="button" onClick={refresh}>刷新后台任务</button>
      </div>}
      <div className="task-actions">
        <button className="button outline" type="button"
          disabled={pending !== 'current' && (actionsDisabled || confirmingAll || current?.status !== 'RUNNING')}
          aria-disabled={pending === 'current'}
          onClick={() => {
            if (actionsDisabled || confirmingAll || current?.status !== 'RUNNING') return
            void stop({ requestId: crypto.randomUUID(), scope: 'current', taskId: current.taskId, expectedRevision: current.revision })
          }}>
          {pending === 'current' ? '正在提交停止…' : '停止当前任务'}
        </button>
        <button className="button outline" type="button" ref={stopAllButton}
          disabled={actionsDisabled || (!confirmingAll && !current && !queue?.queuedCount)}
          aria-expanded={confirmingAll} aria-controls={confirmingAll ? `${id}-confirmation` : undefined}
          onClick={() => setConfirmingAll((open) => !open)}>停止全部任务</button>
      </div>
      {current?.status === 'RUNNING' && current.kind === 'workflow' && !confirmingAll && (
        <p className="task-note">停止当前步骤会暂停本轮工作流，可以稍后继续。</p>
      )}
      {confirmingAll && (
        <div className="field" id={`${id}-confirmation`} role="group" aria-labelledby={`${id}-all-help`}>
          <p className="task-note" id={`${id}-all-help`}>确认时正在运行或排队的后台任务会停止，其中的工作流会结束。已保存的成果会保留。</p>
          <div className="task-actions">
            <button className="button outline" type="button" autoFocus disabled={actionsDisabled}
              onClick={() => {
                if (!actionsDisabled) void stop({ requestId: crypto.randomUUID(), scope: 'all' })
              }}>确认停止全部任务</button>
            <button className="button outline" type="button" onClick={() => {
              if (stopAllButton.current?.disabled) heading.current?.focus()
              else stopAllButton.current?.focus()
              setConfirmingAll(false)
            }}>返回</button>
          </div>
        </div>
      )}
      {pending === 'all' && <p role="status" className="task-note">正在提交停止全部任务的请求…</p>}
      {actionError && <p role="alert" className="form-error">{actionError}</p>}
      {retry && <div className="field">
        {retry.scope === 'all' && <p className="task-note">若前次请求未送达，重试会停止此刻的后台任务。</p>}
        <div className="task-actions">
          <button className="button outline" type="button" aria-disabled={pending !== undefined} onClick={() => void stop(retry)}>
            {retry.scope === 'all' ? '重试停止全部任务' : '重试停止当前任务'}
          </button>
          <button className="button outline" type="button" disabled={pending !== undefined} onClick={() => {
            heading.current?.focus()
            setRetry(undefined)
            setActionError('')
          }}>关闭重试</button>
        </div>
      </div>}
      {feedback && <p role="status" className="task-note">{describeStop(feedback)}</p>}
    </section>
  )
}
