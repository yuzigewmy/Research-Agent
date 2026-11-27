import { useEffect, useId, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import { t, useLocale } from './i18n'
import { LoadingState, ProjectDate, RequestError } from './ProjectDetails'
import { errorMessage, listProjects, templates } from './projects'
import type { Project, ProjectFilters, ProjectStatus } from './projects'
import './project-management.css'

type ListStatus = { kind: 'loading' } | { kind: 'ready' } | { kind: 'error'; message: string }
type ListRequest = { filters: ProjectFilters; cursor?: string }

const projectListScopeKey = 'research-agent.project-list.scope'
const projectScopes = [
  ['READY', '当前项目'],
  ['ARCHIVED', '归档项目'],
] as const satisfies ReadonlyArray<readonly [ProjectStatus, string]>

function storedProjectScope(): ProjectStatus {
  try {
    const value = localStorage.getItem(projectListScopeKey)
    if (value === 'READY' || value === 'ARCHIVED') return value
  } catch {
    // 浏览器拒绝读取本地偏好时使用当前项目。
  }
  return 'READY'
}

function rememberProjectScope(status: ProjectStatus) {
  try {
    localStorage.setItem(projectListScopeKey, status)
  } catch {
    // 无法写入时保留本次选择，下次打开回到当前项目。
  }
}

function mergeItems(first: Project[], second: Project[]): Project[] {
  const byId = new Map(first.map((project) => [project.projectId, project]))
  for (const project of second) byId.set(project.projectId, project)
  return [...byId.values()]
}

interface ListState {
  request: ListRequest
  items: Project[]
  nextCursor?: string
  status: ListStatus
}

function firstPage(filters: ProjectFilters): ListState {
  return { request: { filters }, items: [], status: { kind: 'loading' } }
}

export function useProjectList() {
  const [state, setState] = useState(() => firstPage({ q: '', status: storedProjectScope() }))
  const { request, items, nextCursor, status } = state

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    listProjects(request.filters, request.cursor, signal).then((page) => {
      if (controller.signal.aborted) return
      setState((current) => current.request === request ? {
        ...current,
        items: request.cursor ? mergeItems(current.items, page.items) : page.items,
        nextCursor: page.nextCursor,
        status: { kind: 'ready' },
      } : current)
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      setState((current) => current.request === request
        ? { ...current, status: { kind: 'error', message: errorMessage(error) } }
        : current)
    })
    return () => controller.abort()
  }, [request])

  function filter(changes: Partial<ProjectFilters>) {
    if (changes.status !== undefined) rememberProjectScope(changes.status)
    setState((current) => {
      const previous = current.request.filters
      const next = { ...previous, ...changes }
      return next.q === previous.q && next.status === previous.status ? current : firstPage(next)
    })
  }

  return {
    items,
    status,
    nextCursor,
    filters: request.filters,
    filter,
    refresh: () => setState((current) => firstPage(current.request.filters)),
    loadMore: () => setState((current) => {
      if (current.status.kind !== 'ready' || !current.nextCursor) return current
      return {
        ...current,
        request: { filters: current.request.filters, cursor: current.nextCursor },
        status: { kind: 'loading' },
      }
    }),
    retry: () => setState((current) => ({ ...current, request: { ...current.request }, status: { kind: 'loading' } })),
  }
}

interface ProjectListProps {
  list: ReturnType<typeof useProjectList>
  hidden: boolean
  selectedId: string | null
  hrefFor: (id: string) => string
  onOpen: (event: MouseEvent<HTMLAnchorElement>, id: string) => void
}

export function ProjectList({ list, hidden, selectedId, hrefFor, onOpen }: ProjectListProps) {
  const locale = useLocale()
  const { items, status, nextCursor, retry, loadMore, filters, filter } = list
  const [query, setQuery] = useState(filters.q)
  const queryId = useId()
  const queryInput = useRef<HTMLInputElement>(null)
  return (
    <section className="project-index" id="project-index" hidden={hidden} aria-labelledby="project-index-title">
      <header className="panel-heading"><h2 id="project-index-title">{t(locale, 'projects.title')}</h2></header>
      <div className="project-filters">
        <div role="group" aria-label={t(locale, 'projects.scopeGroup')} className="project-scope">
          {projectScopes.map(([value]) => (
            <button key={value} className="button outline" type="button"
              aria-pressed={filters.status === value} onClick={() => filter({ status: value })}>
              {t(locale, value === 'ARCHIVED' ? 'projects.scope.ARCHIVED' : 'projects.scope.READY')}
            </button>
          ))}
        </div>
        <form className="field" role="search" aria-label={t(locale, 'projects.searchGroup')} onSubmit={(event) => {
          event.preventDefault()
          const q = query.trim()
          setQuery(q)
          filter({ q })
        }}>
          <label htmlFor={queryId}>{t(locale, 'projects.searchLabel')}</label>
          <div className="project-search-controls">
            <input id={queryId} ref={queryInput} type="search" value={query} autoComplete="off"
              onChange={(event) => setQuery(event.currentTarget.value)} />
            <button className="button outline" type="submit">{t(locale, 'projects.searchSubmit')}</button>
            {filters.q && <button className="button outline" type="button" onClick={() => {
              setQuery('')
              filter({ q: '' })
              queryInput.current?.focus()
            }}>{t(locale, 'projects.searchClear')}</button>}
          </div>
        </form>
      </div>
      {items.length > 0 && (
        <ul className="project-list" role="list">
          {items.map((project) => (
            <li key={project.projectId}>
              <a
                className="project-link"
                href={hrefFor(project.projectId)}
                onClick={(event) => onOpen(event, project.projectId)}
                aria-current={selectedId === project.projectId ? 'page' : undefined}
              >
                <span className="project-row-title" dir="auto">{project.displayName || project.title}</span>
                <span className="project-row-meta">
                  {project.displayName && project.displayName !== project.title && (
                    <span className="project-row-research-title">题目：<bdi>{project.title}</bdi></span>
                  )}
                  <span>{templates[project.templateId]}</span>
                  <ProjectDate value={project.createdAt} />
                </span>
                <span className="project-arrow" aria-hidden="true">→</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {status.kind === 'loading' && <LoadingState message={t(locale, 'projects.loading')} />}
      {status.kind === 'error' && <RequestError message={status.message} onRetry={retry} />}
      {status.kind === 'ready' && items.length === 0 && (
        <div className="empty-state" role="status">
          <p><strong>{filters.q ? t(locale, 'projects.emptySearch') : filters.status === 'ARCHIVED' ? t(locale, 'projects.emptyArchived') : t(locale, 'projects.emptyReady')}</strong></p>
          {filters.q && <p>{t(locale, 'projects.emptySearchHint')}</p>}
          {!filters.q && filters.status === 'READY' && <p>{t(locale, 'projects.emptyReadyHint')}</p>}
        </div>
      )}
      {nextCursor && status.kind === 'ready' && (
        <div className="pagination">
          <button type="button" className="button outline" onClick={loadMore}>{t(locale, 'projects.loadMore')}</button>
        </div>
      )}
    </section>
  )
}
