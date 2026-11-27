import { useEffect, useId, useState } from 'react'
import { t, useLocale } from './i18n'
import { errorMessage, ProjectApiError, request } from './projects'
import { WorkspaceBundleAdd } from './WorkspaceBundle'
import type { BundleEntry, BundleSelection } from './WorkspaceBundle'
import { workspaceBundleItemKey } from './workspace'
import './project-artifacts.css'

const kindLabels: Record<BundleEntry['kind'], string> = {
  version: '工作区文件版本',
  material: '项目资料原件',
  artifact: '工作流原始成果',
}

const formatLabels = {
  markdown: 'Markdown',
  data: '数据',
  html: 'HTML',
  diagram: '图表',
  pdf: 'PDF',
  image: '图片',
  word: 'Word',
  code: '代码',
  text: '纯文本',
  other: '其他',
}

const dateFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })
const sizeFormat = new Intl.NumberFormat('zh-CN')

type Artifact = BundleEntry & { createdAt: string; format: keyof typeof formatLabels; downloadUrl: string }
type ArtifactPage = { items: Artifact[]; nextCursor?: string }
type Filters = { kind: Artifact['kind'] | ''; format: Artifact['format'] | ''; q: string }
type PageQuery = { filters: Filters; cursor?: string }
type Status = { kind: 'loading' } | { kind: 'ready' } | { kind: 'error'; message: string }
type State = ArtifactPage & { query: PageQuery; status: Status }

function isProjectDownload(value: unknown, projectPrefix: string): value is string {
  if (typeof value !== 'string' || !value.startsWith(projectPrefix) || /[\\\s]/.test(value)) return false
  try {
    const url = new URL(value, window.location.origin)
    if (url.origin !== window.location.origin || !url.pathname.startsWith(projectPrefix) || url.hash) return false
    const relativePath = url.pathname.slice(projectPrefix.length)
    // Check path segments after decoding as well, so an escaped separator cannot change the project scope.
    return relativePath.length > 0 && relativePath.split('/').every((segment) => {
      const decoded = decodeURIComponent(segment)
      return decoded !== '.' && decoded !== '..' && !/[\\/\p{Cc}]/u.test(decoded)
    })
  } catch {
    return false
  }
}

function isArtifact(value: unknown, projectPrefix: string): value is Artifact {
  const item = value as Partial<Artifact> | null
  return !!item && typeof item.kind === 'string' && Object.hasOwn(kindLabels, item.kind)
    && typeof item.id === 'string' && item.id.length > 0 && item.id !== '.' && item.id !== '..'
    && typeof item.name === 'string' && item.name.length > 0
    && typeof item.size === 'number' && Number.isSafeInteger(item.size) && item.size >= 0
    && typeof item.createdAt === 'string' && Number.isFinite(Date.parse(item.createdAt))
    && typeof item.format === 'string' && Object.hasOwn(formatLabels, item.format)
    && isProjectDownload(item.downloadUrl, projectPrefix)
}

async function listArtifacts(projectId: string, { filters, cursor }: PageQuery, signal: AbortSignal) {
  if (!projectId || projectId === '.' || projectId === '..') throw new ProjectApiError('项目链接无效。', 400)
  const projectPrefix = `/api/v1/projects/${encodeURIComponent(projectId)}/`
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...filters, cursor })) {
    if (value) params.set(key, value)
  }
  return request(`${projectPrefix}artifacts${params.size ? `?${params}` : ''}`, (value): value is ArtifactPage => {
    const page = value as Partial<ArtifactPage> | null
    return !!page && Array.isArray(page.items) && page.items.every((item) => isArtifact(item, projectPrefix))
      && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
  }, { signal, cache: 'no-store' })
}

function firstPage(filters: Filters): State {
  return { query: { filters }, items: [], status: { kind: 'loading' } }
}

function pageItems(current: Artifact[], incoming: Artifact[]) {
  const byKey = new Map(current.map((item) => [workspaceBundleItemKey(item), item]))
  for (const item of incoming) byKey.set(workspaceBundleItemKey(item), item)
  return [...byKey.values()]
}

type Props = { projectId: string; bundleSelection: BundleSelection }

export function ProjectArtifacts(props: Props) {
  const locale = useLocale()
  const [open, setOpen] = useState(false)
  return <details id="project-artifacts" className="project-artifacts" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t(locale, 'panel.artifacts')}</summary>
    {open && <ArtifactList key={props.projectId} {...props} />}
  </details>
}

function ArtifactList({ projectId, bundleSelection }: Props) {
  const id = useId()
  const [search, setSearch] = useState('')
  const [state, setState] = useState(() => firstPage({ kind: '', format: '', q: '' }))
  const { query, items, nextCursor, status } = state
  const { filters } = query

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void listArtifacts(projectId, query, signal).then((page) => {
      signal.throwIfAborted()
      setState((current) => current.query === query ? {
        ...current,
        items: pageItems(query.cursor ? current.items : [], page.items),
        nextCursor: page.nextCursor === query.cursor ? undefined : page.nextCursor,
        status: { kind: 'ready' },
      } : current)
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      const message = signal.aborted ? '加载项目成果超时，请重试。' : errorMessage(error)
      setState((current) => current.query === query ? { ...current, status: { kind: 'error', message } } : current)
    })
    return () => controller.abort()
  }, [projectId, query])

  function filter(changes: Partial<Filters>) {
    setState((current) => {
      const previous = current.query.filters
      const next = { ...previous, ...changes }
      return next.kind === previous.kind && next.format === previous.format && next.q === previous.q
        ? current : firstPage(next)
    })
  }

  function loadPage(cursor?: string) {
    setState((current) => ({ ...current, query: { filters: current.query.filters, cursor }, status: { kind: 'loading' } }))
  }

  return <div className="project-artifacts-body">
    <div className="project-artifacts-filters">
      <label className="field" htmlFor={`${id}-kind`}>
        <span>成果来源</span>
        <select id={`${id}-kind`} name="kind" value={filters.kind}
          onChange={(event) => filter({ kind: event.currentTarget.value as Filters['kind'] })}>
          <option value="">全部来源</option>
          {Object.entries(kindLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </label>
      <label className="field" htmlFor={`${id}-format`}>
        <span>文件格式</span>
        <select id={`${id}-format`} name="format" value={filters.format}
          onChange={(event) => filter({ format: event.currentTarget.value as Filters['format'] })}>
          <option value="">全部格式</option>
          {Object.entries(formatLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </label>
      <form className="field project-artifacts-search" role="search" aria-label="成果库搜索" onSubmit={(event) => {
        event.preventDefault()
        const q = search.trim()
        setSearch(q)
        filter({ q })
      }}>
        <label htmlFor={`${id}-q`}>搜索文件名或路径</label>
        <div className="project-artifacts-search-controls">
          <input id={`${id}-q`} name="q" type="search" value={search} autoComplete="off"
            onChange={(event) => setSearch(event.currentTarget.value)} />
          <button className="button outline" type="submit">搜索</button>
        </div>
      </form>
    </div>
    {items.length > 0 && <ul className="project-artifacts-list" role="list" aria-label="项目成果">
      {items.map((item) => <ArtifactRow key={workspaceBundleItemKey(item)} item={item} selection={bundleSelection} />)}
    </ul>}
    {status.kind === 'loading' && <p className="project-artifacts-meta" role="status">正在加载项目成果…</p>}
    {status.kind === 'error' && <div className="request-error">
      <p role="alert">{status.message}</p>
      <button className="button outline" type="button" data-project-artifacts-retry onClick={() => loadPage(query.cursor)}>重试</button>
    </div>}
    {status.kind === 'ready' && !items.length && <p className="project-artifacts-meta" role="status">
      {filters.kind || filters.format || filters.q ? '没有符合筛选条件的成果。' : '此项目暂无成果。'}
    </p>}
    {nextCursor && status.kind !== 'error' && <button className="button outline" type="button" data-project-artifacts-more
      aria-disabled={status.kind === 'loading' || undefined}
      onClick={() => { if (status.kind === 'ready') loadPage(nextCursor) }}>加载更多成果</button>}
  </div>
}

function ArtifactRow({ item, selection }: { item: Artifact; selection: BundleSelection }) {
  const { kind, id, name, size, createdAt, format, downloadUrl } = item
  return <li data-project-artifact-kind={kind} data-project-artifact-id={id}>
    <div className="project-artifact-info">
      <strong><bdi>{name}</bdi></strong>
      <p className="project-artifacts-meta">
        {kindLabels[kind]} · {formatLabels[format]} · {sizeFormat.format(size)} 字节 ·{' '}
        <time dateTime={createdAt}>{dateFormat.format(new Date(createdAt))}</time> · <code title={id}>{id.slice(-8)}</code>
      </p>
    </div>
    <div className="project-artifact-actions">
      <a className="button outline" href={downloadUrl} download aria-label={`下载原件：${name}`}>下载原件</a>
      <WorkspaceBundleAdd item={{ kind, id, name, size }} selection={selection} source={kind === 'version' ? 'history' : kind} />
    </div>
  </li>
}
