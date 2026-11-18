import { useEffect, useState } from 'react'
import type { MouseEvent } from 'react'
import { LoadingState, ProjectDate, RequestError } from './ProjectDetails'
import { errorMessage, listProjects, templates } from './projects'
import type { Project } from './projects'

type ListStatus = { kind: 'loading' } | { kind: 'ready' } | { kind: 'error'; message: string }

function mergeItems(first: Project[], second: Project[]): Project[] {
  const byId = new Map(first.map((project) => [project.projectId, project]))
  for (const project of second) byId.set(project.projectId, project)
  return [...byId.values()]
}

export function useProjectList() {
  const [items, setItems] = useState<Project[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [status, setStatus] = useState<ListStatus>({ kind: 'loading' })
  const [request, setRequest] = useState<{ cursor?: string }>({})

  useEffect(() => {
    const controller = new AbortController()
    listProjects(request.cursor, controller.signal).then((page) => {
      if (controller.signal.aborted) return
      setItems((current) => mergeItems(current, page.items))
      setNextCursor(page.nextCursor)
      setStatus({ kind: 'ready' })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setStatus({ kind: 'error', message: errorMessage(error) })
    })
    return () => controller.abort()
  }, [request])

  function load(cursor?: string) {
    setStatus({ kind: 'loading' })
    setRequest({ cursor })
  }

  function addProject(project: Project) {
    setItems((current) => current.some((item) => item.projectId === project.projectId)
      ? mergeItems(current, [project])
      : [project, ...current],
    )
  }

  return {
    items,
    status,
    nextCursor,
    addProject,
    loadMore: () => load(nextCursor),
    retry: () => load(request.cursor),
  }
}

interface ProjectListProps {
  list: ReturnType<typeof useProjectList>
  selectedId: string | null
  hrefFor: (id: string) => string
  onOpen: (event: MouseEvent<HTMLAnchorElement>, id: string) => void
}

export function ProjectList({ list, selectedId, hrefFor, onOpen }: ProjectListProps) {
  const { items, status, nextCursor, retry, loadMore } = list
  return (
    <section className="project-index" aria-labelledby="project-index-title">
      <header className="panel-heading"><h2 id="project-index-title">项目列表</h2></header>
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
                <span className="project-row-title" dir="auto">{project.title}</span>
                <span className="project-row-meta">
                  <span>{templates[project.templateId]}</span>
                  <ProjectDate value={project.createdAt} />
                </span>
                <span className="project-arrow" aria-hidden="true">→</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {status.kind === 'loading' && <LoadingState message="正在加载项目…" />}
      {status.kind === 'error' && <RequestError message={status.message} onRetry={retry} />}
      {status.kind === 'ready' && items.length === 0 && (
        <div className="empty-state" role="status">
          <p><strong>暂无项目</strong></p>
          <p>新建的项目会显示在这里。</p>
        </div>
      )}
      {nextCursor && status.kind === 'ready' && (
        <div className="pagination">
          <button type="button" className="button outline" onClick={loadMore}>加载更多</button>
        </div>
      )}
    </section>
  )
}
