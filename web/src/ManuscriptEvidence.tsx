import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { listTasks } from './tasks'
import type { Task, TaskInputVersion } from './tasks'
import { manuscriptSelectionByteLimit } from './turns'
import type { ManuscriptSelection } from './turns'

const metricLabels = {
  baselineMean: '基线均值',
  variantMean: '方案均值',
  meanDifference: '平均差（方案 − 基线）',
} as const

type Metric = keyof typeof metricLabels
type CreateLink = Pick<ManuscriptSelection, 'startByte' | 'endByte'> & { taskId: string; metric: Metric }
type Link = CreateLink & { linkId: string; value: string; text: string; input: TaskInputVersion; sourceChanged: boolean }
type LinkPage = { items: Link[] }
type Props = { projectId: string; versionId: string } & (
  { readOnly: true } | { readOnly?: false; selection?: ManuscriptSelection; blockReason?: string }
)

function isId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isMetric(value: unknown): value is Metric {
  return typeof value === 'string' && Object.hasOwn(metricLabels, value)
}

function isLink(value: unknown): value is Link {
  const link = value as Partial<Link> | null
  return !!link && isId(link.linkId) && isId(link.taskId) && isMetric(link.metric)
    && typeof link.value === 'string' && link.value.length > 0
    && typeof link.startByte === 'number' && Number.isSafeInteger(link.startByte) && link.startByte >= 0
    && typeof link.endByte === 'number' && Number.isSafeInteger(link.endByte) && link.endByte > link.startByte
    && typeof link.text === 'string' && link.text.length > 0
    && typeof link.input?.path === 'string' && link.input.path.length > 0 && isId(link.input.versionId)
    && typeof link.input.sha256 === 'string' && /^[a-f\d]{64}$/i.test(link.input.sha256)
    && typeof link.sourceChanged === 'boolean'
}

function isLinkPage(value: unknown): value is LinkPage {
  const items = (value as Partial<LinkPage> | null)?.items
  return Array.isArray(items) && items.every(isLink)
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function EvidenceList({ items, projectId }: { items: Link[]; projectId: string }) {
  return <ul className="manuscript-evidence-list" role="list" aria-label="本稿件版本的配对证据">
    {items.map(({ linkId, taskId, metric, value, text, input, sourceChanged }) => (
      <li key={linkId} data-link-id={linkId}>
        <p><strong>{metricLabels[metric]}</strong> <code className="manuscript-evidence-value">{value}</code></p>
        <pre className="workspace-text" tabIndex={0} aria-label="关联选区" dir="auto">{text}</pre>
        <p className="workspace-note manuscript-evidence-source">
          CSV <bdi>{input.path}</bdi> · 版本 <bdi>{input.versionId}</bdi> · SHA-256 <code>{input.sha256}</code>
        </p>
        {sourceChanged && <p className="workspace-note manuscript-evidence-source-changed">
          CSV 已换版。此结果仍使用上方的原始数据版本。
        </p>}
        <p><a href={`?${new URLSearchParams({ project: projectId, task: taskId })}`} target="_blank" rel="noopener">
          打开原任务（新标签页）
        </a></p>
      </li>
    ))}
  </ul>
}

function EvidenceControls({ projectId, selection, blockReason, saving, disabled, onLink }: {
  projectId: string
  selection?: ManuscriptSelection
  blockReason?: string
  saving: boolean
  disabled: boolean
  onLink: (input: CreateLink) => void
}) {
  const id = useId()
  const [tasks, setTasks] = useState<Task[]>([])
  const [taskId, setTaskId] = useState('')
  const [metric, setMetric] = useState<Metric | ''>('')
  const [nextCursor, setNextCursor] = useState<string>()
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const hasTask = tasks.some((task) => task.taskId === taskId)

  useEffect(() => {
    const controller = new AbortController()
    void listTasks(projectId, requestSignal(controller), query.cursor).then((page) => {
      if (controller.signal.aborted) return
      const completed = page.items.filter((task) => task.kind === 'experiment-comparison' && task.status === 'SUCCEEDED')
      setTasks((current) => {
        const byId = new Map(query.cursor ? current.map((task) => [task.taskId, task]) : [])
        for (const task of completed) byId.set(task.taskId, task)
        return [...byId.values()]
      })
      setNextCursor(page.nextCursor)
      setLoading(false)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setError(errorMessage(cause))
      setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  function loadTasks(cursor?: string) {
    setError('')
    setLoading(true)
    setQuery({ cursor })
  }

  const hint = blockReason ?? (!selection ? `请在稿件中选取非空白文字（最多 ${manuscriptSelectionByteLimit / 1024} KiB）。` : undefined)
  return <div className="manuscript-evidence-controls">
    <div className="workspace-actions">
      <div className="field">
        <label htmlFor={`${id}-task`}>已完成的配对任务</label>
        <select id={`${id}-task`} value={hasTask ? taskId : ''} disabled={saving || !tasks.length}
          onChange={(event) => setTaskId(event.currentTarget.value)}>
          <option value="">选择任务</option>
          {tasks.map((task) => <option key={task.taskId} value={task.taskId}>
            {task.instruction.slice(0, 100)} · {task.inputs?.[0]?.path} · {task.taskId.slice(-8)}
          </option>)}
        </select>
      </div>
      <div className="field">
        <label htmlFor={`${id}-metric`}>关联指标</label>
        <select id={`${id}-metric`} value={metric} disabled={saving} onChange={(event) => {
          const value = event.currentTarget.value
          setMetric(isMetric(value) ? value : '')
        }}>
          <option value="">选择指标</option>
          {Object.entries(metricLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
      </div>
    </div>
    {loading && <p className="workspace-note" role="status">正在读取配对任务…</p>}
    {error && <p className="form-error" role="alert">读取任务失败。{error}</p>}
    {!loading && !error && !tasks.length && <p className="workspace-note">
      {nextCursor ? '已加载的任务中没有已完成的配对任务。' : '暂无已完成的配对任务。'}
    </p>}
    <div className="workspace-actions">
      {nextCursor && !error && <button className="button outline manuscript-evidence-more-tasks" type="button"
        disabled={loading || saving} onClick={() => loadTasks(nextCursor)}>加载更多任务</button>}
      <button className="button outline" type="button" disabled={loading || saving}
        onClick={() => {
          if (!error) setTaskId('')
          loadTasks(error ? query.cursor : undefined)
        }}>{error ? '重试读取任务' : '刷新任务'}</button>
    </div>
    {selection && !blockReason && <pre className="workspace-text manuscript-evidence-selection" tabIndex={0}
      aria-label="当前选区" dir="auto">{selection.text}</pre>}
    {hint && <p className="workspace-note" id={`${id}-hint`}>{hint}</p>}
    <div className="workspace-actions">
      <button className="button primary manuscript-evidence-link" type="button" aria-describedby={hint ? `${id}-hint` : undefined}
        disabled={disabled || !!blockReason || !selection || !hasTask || !metric}
        onClick={() => {
          if (selection && hasTask && metric) onLink({ startByte: selection.startByte, endByte: selection.endByte, taskId, metric })
        }}>{saving ? '正在关联…' : '关联配对结果'}</button>
    </div>
  </div>
}

export function ManuscriptEvidence(props: Props) {
  const { projectId, versionId } = props
  if (!isId(projectId) || !isId(versionId)) return <p className="form-error" role="alert">稿件版本链接无效。</p>
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/comparison-links`
  return <VersionEvidence key={route} route={route} {...props} />
}

function VersionEvidence(props: Props & { route: string }) {
  const { projectId, route, readOnly } = props
  const id = useId()
  const [links, setLinks] = useState<Link[]>()
  const [loadError, setLoadError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refresh, setRefresh] = useState(0)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saved, setSaved] = useState(false)
  const mutation = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    void request(route, isLinkPage, { cache: 'no-store', signal: requestSignal(controller) }).then(({ items }) => {
      if (controller.signal.aborted) return
      setLinks(items)
      setLoading(false)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setLoadError(errorMessage(cause))
      setLoading(false)
    })
    return () => controller.abort()
  }, [route, refresh])

  useEffect(() => () => mutation.current?.abort(), [])

  async function link(input: CreateLink) {
    if (readOnly || props.blockReason || loading || mutation.current) return
    const controller = new AbortController()
    mutation.current = controller
    setSaving(true)
    setSaved(false)
    setSaveError('')
    try {
      const record = await request(route, (value): value is Link => isLink(value) && value.taskId === input.taskId
        && value.metric === input.metric && value.startByte === input.startByte && value.endByte === input.endByte, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
        signal: requestSignal(controller),
      })
      if (controller.signal.aborted) return
      setLinks((current = []) => current.some(({ linkId }) => linkId === record.linkId)
        ? current.map((item) => item.linkId === record.linkId ? record : item) : [record, ...current])
      setSaved(true)
    } catch (cause) {
      if (controller.signal.aborted) return
      setSaveError(cause instanceof ProjectApiError && cause.status === 409 && cause.code === 'MANUSCRIPT_VERSION_CHANGED'
        ? '稿件已有新版本。请点击「重新加载」，再选择要关联的文字。' : errorMessage(cause))
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setSaving(false)
    }
  }

  return <section className="manuscript-evidence" aria-labelledby={id}>
    <h5 id={id}>{readOnly ? '配对证据（只读）' : '稿件配对证据'}</h5>
    {!readOnly && <EvidenceControls projectId={projectId} selection={props.selection} blockReason={props.blockReason}
      saving={saving} disabled={loading || saving} onLink={(input) => void link(input)} />}
    {saveError && <p className="form-error manuscript-evidence-save-error" role="alert">{saveError}</p>}
    {saved && <p className="workspace-note" role="status">已关联配对结果。</p>}
    <div className="workspace-actions">
      <button className="button outline manuscript-evidence-refresh" type="button" disabled={loading || saving}
        onClick={() => {
          setLoadError('')
          setLoading(true)
          setRefresh((value) => value + 1)
        }}>{loadError ? '重试读取关联' : '刷新关联'}</button>
    </div>
    {loading && <p className="workspace-note" role="status">正在读取关联…</p>}
    {loadError && <p className="form-error" role="alert">读取关联失败。{loadError}</p>}
    {!!links?.length && <EvidenceList items={links} projectId={projectId} />}
    {!loading && !loadError && !links?.length && <p className="workspace-note">此稿件版本尚未关联配对证据。</p>}
  </section>
}
