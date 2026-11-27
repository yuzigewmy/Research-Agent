import { useCallback, useEffect, useId, useState } from 'react'
import { errorMessage, getProjectEvents } from './projects'
import type { ProjectEvent } from './projects'

const eventKindLabels: Record<string, string> = {
  'file.saved': '文件保存',
  'task.updated': '任务更新',
  'workflow.step': '工作流步骤',
  'workflow.checkpoint': '工作流检查点',
  'review.updated': '评审/审稿',
  'script.updated': '受限脚本',
}

const dateTimeFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' })

function mergeEventsMonotonic(existing: ProjectEvent[], incoming: ProjectEvent[]): ProjectEvent[] {
  if (incoming.length === 0) return existing
  const bySeq = new Map<number, ProjectEvent>()
  for (const item of existing) bySeq.set(item.seq, item)
  for (const item of incoming) bySeq.set(item.seq, item)
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq)
}

function summarizePayload(payload: Record<string, unknown>): string {
  const parts: string[] = []
  if (typeof payload.path === 'string' && payload.path) parts.push(`路径：${payload.path}`)
  if (typeof payload.stepKey === 'string' && payload.stepKey) parts.push(`步骤：${payload.stepKey}`)
  if (typeof payload.status === 'string' && payload.status) parts.push(`状态：${payload.status}`)
  if (typeof payload.stopReason === 'string' && payload.stopReason) parts.push(`原因：${payload.stopReason}`)
  if (typeof payload.sha256Prefix === 'string' && payload.sha256Prefix) parts.push(`摘要：${payload.sha256Prefix}`)
  if (typeof payload.versionId === 'string' && payload.versionId) parts.push(`版本：${payload.versionId.slice(0, 8)}`)
  return parts.join(' · ')
}

export function ProjectEventsPanel({ projectId }: { projectId: string }) {
  const headingId = useId()
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const [items, setItems] = useState<ProjectEvent[]>([])
  const [latestSeq, setLatestSeq] = useState(0)
  const [loaded, setLoaded] = useState(false)

  const fetchSnapshot = useCallback(async (afterSeq: number, reset: boolean, signal?: AbortSignal) => {
    setLoading(true)
    setError(undefined)
    try {
      const requestSignal = signal
        ? AbortSignal.any([signal, AbortSignal.timeout(10_000)])
        : AbortSignal.timeout(10_000)
      const snapshot = await getProjectEvents(projectId, afterSeq, 100, requestSignal)
      if (signal?.aborted) return
      setItems((prev) => reset ? mergeEventsMonotonic([], snapshot.items) : mergeEventsMonotonic(prev, snapshot.items))
      setLatestSeq((prev) => reset ? snapshot.latestSeq : Math.max(prev, snapshot.latestSeq))
      setLoaded(true)
    } catch (cause: unknown) {
      if (signal?.aborted) return
      setError(errorMessage(cause))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [projectId])

  useEffect(() => {
    setItems([])
    setLatestSeq(0)
    setLoaded(false)
    setError(undefined)
  }, [projectId])

  useEffect(() => {
    if (!open || loaded) return
    const controller = new AbortController()
    void fetchSnapshot(0, true, controller.signal)
    return () => controller.abort()
  }, [open, loaded, fetchSnapshot])

  return (
    <details
      className="project-memory-summary project-events-panel"
      aria-labelledby={headingId}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary id={headingId}>项目活动事件</summary>
      <div className="project-memory-body">
        <div className="project-management-actions">
          <button
            className="button outline"
            type="button"
            disabled={loading}
            onClick={() => void fetchSnapshot(latestSeq, false)}
          >
            {loading ? '正在同步…' : '同步新事件'}
          </button>
          <button
            className="button outline"
            type="button"
            disabled={loading}
            onClick={() => void fetchSnapshot(0, true)}
          >
            重新载入快照
          </button>
          <span className="project-management-help" data-testid="project-events-cursor">
            当前游标：#{latestSeq}（共 {items.length} 条）
          </span>
        </div>
        {error && <p className="form-error" role="alert">{error}</p>}
        {items.length === 0 ? (
          <p className="project-management-help">
            {loaded ? '暂无项目活动事件。保存工作区文件、执行任务、工作流或审稿后将按序号记录于此。' : '正在读取活动事件快照…'}
          </p>
        ) : (
          <ul className="project-memory-chain" role="list" data-testid="project-events-list">
            {items.map((item) => {
              const meta = summarizePayload(item.payload)
              return (
                <li key={item.seq} className="project-memory-chain-item" data-event-seq={item.seq} data-event-kind={item.kind}>
                  <div className="project-memory-chain-head">
                    <code>#{item.seq}</code>
                    <span className="project-memory-badge">
                      {eventKindLabels[item.kind] ?? item.kind}
                    </span>
                    <code>{item.kind}</code>
                    <span className="project-management-help">
                      <time dateTime={item.createdAt}>{dateTimeFormat.format(new Date(item.createdAt))}</time>
                    </span>
                  </div>
                  <p className="project-memory-chain-source" dir="auto">{item.summary}</p>
                  {meta && <p className="project-management-help">{meta}</p>}
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </details>
  )
}
