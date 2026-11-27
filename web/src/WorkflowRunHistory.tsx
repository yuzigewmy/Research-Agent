import { useEffect, useId, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { errorMessage } from './projects'
import { getWorkflowRun, listWorkflowRuns, runStatusLabels } from './workflow'
import type { WorkflowRun, WorkflowRunPage } from './workflow'

const runDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })

export type WorkflowRunLink = { workflowRunId: string }

type HistoryProps = {
  projectId: string
  currentRunId?: string
  renderSteps: (run: WorkflowRun) => ReactNode
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
}

export function WorkflowRunHistory({ projectId, currentRunId, renderSteps }: HistoryProps) {
  const [open, setOpen] = useState(false)
  return <details className="workflow-run-history" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>运行历史</summary>
    {open && <RunList key={JSON.stringify([projectId, currentRunId])} projectId={projectId} renderSteps={renderSteps} />}
  </details>
}

function RunList({ projectId, renderSteps }: Pick<HistoryProps, 'projectId' | 'renderSteps'>) {
  const detailId = useId()
  const [page, setPage] = useState<WorkflowRunPage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string>()

  useEffect(() => {
    const controller = new AbortController()
    void listWorkflowRuns(projectId, requestSignal(controller), query.cursor).then((next) => {
      if (controller.signal.aborted) return
      setPage((previous) => query.cursor ? { ...next, items: [...previous.items, ...next.items] } : next)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  function load(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  return <div className="workflow-run-history-content">
    {page.items.length > 0 && <ul className="workflow-run-list" role="list" aria-label="历次工作流运行">
      {page.items.map(({ runId, createdAt, status }) => {
        const expanded = selected === runId
        return <li key={runId}>
          <button className="workflow-run-row" type="button" aria-expanded={expanded}
            aria-controls={expanded ? detailId : undefined} onClick={() => setSelected(expanded ? undefined : runId)}>
            <time dateTime={createdAt}>{runDate.format(new Date(createdAt))}</time>
            <span className="workflow-run-status" data-status={status}>{runStatusLabels[status]}</span>
          </button>
          {expanded && <div className="workflow-run-detail" id={detailId}>
            <RunDetail projectId={projectId} runId={runId} renderSteps={renderSteps} />
          </div>}
        </li>
      })}
    </ul>}
    {loading && <p className="workflow-note" role="status">正在加载运行历史…</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loading && !error && !page.items.length && <p className="workflow-note">暂无历史运行。</p>}
    {!loading && (error || page.nextCursor) && <div className="workflow-actions">
      <button className="button outline" type="button" onClick={() => load(error ? query.cursor : page.nextCursor)}>
        {error ? '重试加载运行历史' : '加载更多运行'}
      </button>
    </div>}
  </div>
}

type RunDetailResult = { link?: WorkflowRunLink } &
  ({ kind: 'ready'; run: WorkflowRun } | { kind: 'error'; message: string })

export function RunDetail({ projectId, runId, renderSteps, workflowRunLink }: Pick<HistoryProps, 'projectId' | 'renderSteps'> & {
  runId: string
  workflowRunLink?: WorkflowRunLink
}) {
  const id = useId()
  const focusTarget = useRef<HTMLElement>(null)
  const [result, setResult] = useState<RunDetailResult>()
  const [attempt, setAttempt] = useState(0)
  const detail = result?.link === workflowRunLink ? result : undefined

  useEffect(() => {
    const controller = new AbortController()
    void getWorkflowRun(projectId, runId, requestSignal(controller)).then((run) => {
      if (!controller.signal.aborted) setResult({ link: workflowRunLink, kind: 'ready', run })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setResult({ link: workflowRunLink, kind: 'error', message: errorMessage(cause) })
    })
    return () => controller.abort()
  }, [projectId, runId, attempt, workflowRunLink])

  useEffect(() => {
    if (workflowRunLink && detail) focusTarget.current?.focus()
  }, [workflowRunLink, detail])

  if (!detail) return <p className="workflow-note" role="status">正在读取运行详情…</p>
  const tabIndex = workflowRunLink ? -1 : undefined
  if (detail.kind === 'error') return <section className="workflow-run-history-content" ref={focusTarget} tabIndex={tabIndex}>
    <p className="form-error" role="alert">{detail.message}</p>
    <div className="workflow-actions">
      <button className="button outline" type="button" onClick={() => {
        setResult(undefined)
        setAttempt((value) => value + 1)
      }}>重试读取运行详情</button>
    </div>
  </section>

  const { run } = detail
  return <section className="workflow-run-history-content" aria-labelledby={id} ref={focusTarget} tabIndex={tabIndex}>
    <div className="workflow-heading">
      <strong id={id}>{workflowRunLink ? '工作流运行详情' : '历史运行详情'}</strong>
      <span className="workflow-run-status" data-status={run.status}>{runStatusLabels[run.status]}</span>
    </div>
    <time className="workflow-note" dateTime={run.createdAt}>{runDate.format(new Date(run.createdAt))}</time>
    {renderSteps(run)}
  </section>
}
