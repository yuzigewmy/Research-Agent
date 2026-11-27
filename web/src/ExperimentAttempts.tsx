import { useEffect, useState } from 'react'
import { ProjectDate } from './ProjectDetails'
import { errorMessage, request } from './projects'
import { taskStatusLabels } from './tasks'
import type { TaskInputVersion, TaskStatus } from './tasks'

type ExperimentAttempt = {
  revision: number
  status: Exclude<TaskStatus, 'PROPOSED' | 'QUEUED'>
  startedAt: string
  finishedAt?: string
  input: TaskInputVersion
  message?: string
  resultSha256?: string
}

type AttemptPage = { items: ExperimentAttempt[]; timeoutSeconds: number }

type Props = { projectId: string; taskId: string; revision: number }

function isAttempt(value: unknown): value is ExperimentAttempt {
  const attempt = value as (Partial<Omit<ExperimentAttempt, 'status'>> & { status?: unknown }) | null
  if (!attempt) return false
  const { revision, status, startedAt, finishedAt, input, message, resultSha256 } = attempt
  return typeof revision === 'number' && Number.isSafeInteger(revision) && revision >= 1
    && typeof status === 'string' && Object.hasOwn(taskStatusLabels, status) && status !== 'PROPOSED' && status !== 'QUEUED'
    && typeof startedAt === 'string' && Number.isFinite(Date.parse(startedAt))
    && (finishedAt === undefined || (typeof finishedAt === 'string' && Number.isFinite(Date.parse(finishedAt))))
    && !!input && typeof input.versionId === 'string' && typeof input.path === 'string' && typeof input.sha256 === 'string'
    && (message === undefined || typeof message === 'string')
    && (resultSha256 === undefined || typeof resultSha256 === 'string')
}

function isAttemptPage(value: unknown): value is AttemptPage {
  const page = value as Partial<AttemptPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isAttempt)
    && typeof page.timeoutSeconds === 'number' && Number.isFinite(page.timeoutSeconds)
}

export function ExperimentAttempts({ projectId, taskId, revision }: Props) {
  const [open, setOpen] = useState(false)
  return <details className="task-input-disclosure" data-task-attempts={taskId} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>执行记录</summary>
    {open && <AttemptList key={JSON.stringify([projectId, taskId, revision])} projectId={projectId} taskId={taskId} />}
  </details>
}

function AttemptList({ projectId, taskId }: Pick<Props, 'projectId' | 'taskId'>) {
  const [retry, setRetry] = useState(0)
  const [state, setState] = useState<AttemptPage | string>()

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    const path = `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/attempts`
    void request(path, isAttemptPage, { cache: 'no-store', signal }).then((page) => {
      signal.throwIfAborted()
      setState(page)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setState(errorMessage(cause))
    })
    return () => controller.abort()
  }, [projectId, taskId, retry])

  if (typeof state === 'string') return <div className="task-input-body" data-attempts-state="error">
    <p className="form-error" role="alert">{state}</p>
    <div className="task-actions">
      <button className="button outline" type="button" onClick={() => {
        setState(undefined)
        setRetry((value) => value + 1)
      }}>重试读取执行记录</button>
    </div>
  </div>

  if (!state) return <p className="task-note" role="status" data-attempts-state="loading">正在读取执行记录…</p>
  const { items, timeoutSeconds } = state
  return <div className="task-input-body" data-attempts-state={items.length ? 'ready' : 'empty'}>
    <p className="task-note">每次运行限时 {timeoutSeconds} 秒</p>
    {items.length ? <ol className="task-input-list task-input-choices" role="list" aria-label="执行记录" tabIndex={0}>
      {items.map(({ revision, status, startedAt, finishedAt, input, message, resultSha256 }) => (
        <li key={revision} data-attempt-revision={revision} data-status={status}>
          <div className="task-card-header">
            <div className="task-status"><strong>{taskStatusLabels[status]}</strong></div>
            <span className="task-note">修订 {revision}</span>
          </div>
          <p className="task-note">开始 <ProjectDate value={startedAt} withTime />
            {finishedAt && <> · 结束 <ProjectDate value={finishedAt} withTime /></>}
          </p>
          <p className="task-note">输入 <bdi>{input.path}</bdi> · 版本 <bdi>{input.versionId}</bdi> · SHA-256 <code>{input.sha256}</code></p>
          {message && <p className="task-note" dir="auto">{message}</p>}
          {resultSha256 && <p className="task-note">结果 SHA-256 <code>{resultSha256}</code></p>}
        </li>
      ))}
    </ol> : <p className="task-note" role="status">暂无执行记录。旧任务可能没有保存记录。</p>}
  </div>
}
