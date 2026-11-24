import { lazy, Suspense, useEffect, useEffectEvent, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import { CreateProjectForm } from './CreateProjectForm'
import { ModelConnection } from './ModelConnection'
import { Notifications } from './Notifications'
import { ProjectDetails } from './ProjectDetails'
import { ProjectDraftPanel } from './ProjectDraftPanel'
import { ProjectList, useProjectList } from './ProjectList'
import { listProjectTemplates } from './projectTemplates'
import type { ProjectTemplate } from './projectTemplates'
import { errorMessage } from './projects'
import type { Project } from './projects'
import type { TaskLink } from './tasks'
import { WorkspaceBackup } from './WorkspaceBackup'

type Catalog = { kind: 'loading' } | { kind: 'ready'; items: ProjectTemplate[] } | { kind: 'error'; message: string }

type Selection = { projectId: string | null; taskLink?: TaskLink }

function selectedFromUrl(): Selection {
  const params = new URLSearchParams(window.location.search)
  const projectId = params.get('project')
  const taskId = params.get('task')
  return { projectId, taskLink: projectId && taskId ? { taskId } : undefined }
}

function projectUrl(projectId: string | null, taskId?: string) {
  const url = new URL(window.location.href)
  if (projectId === null) url.searchParams.delete('project')
  else url.searchParams.set('project', projectId)
  if (projectId !== null && taskId) url.searchParams.set('task', taskId)
  else url.searchParams.delete('task')
  url.hash = ''
  return url.href
}

const QuestionnaireRespond = lazy(() => import('./QuestionnaireRespond'))

export function App() {
  const publicId = new URLSearchParams(window.location.search).get('questionnaire')
  if (publicId === null) return <Workbench />

  return <Suspense fallback={<main className="workbench"><p role="status">正在读取问卷…</p></main>}>
    <QuestionnaireRespond key={publicId} publicId={publicId} />
  </Suspense>
}

function Workbench() {
  const [selection, setSelection] = useState(selectedFromUrl)
  const { projectId: selectedId, taskLink } = selection
  const [catalog, setCatalog] = useState<Catalog>({ kind: 'loading' })
  const list = useProjectList()
  const heading = useRef<HTMLHeadingElement>(null)
  const focusOnNavigate = useRef(false)
  const creating = selectedId === null
  const catalogLoading = catalog.kind === 'loading'
  const templateItems = catalog.kind === 'ready' ? catalog.items : []

  useEffect(() => {
    if (!creating || !catalogLoading) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void listProjectTemplates(signal).then(({ items }) => {
      if (!controller.signal.aborted) setCatalog({ kind: 'ready', items })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setCatalog({ kind: 'error', message: errorMessage(cause) })
    })
    return () => controller.abort()
  }, [creating, catalogLoading])

  const syncSelection = useEffectEvent(() => {
    const next = selectedFromUrl()
    if (next.projectId === selectedId && next.taskLink?.taskId === taskLink?.taskId) return
    focusOnNavigate.current = true
    setSelection(next)
  })

  useEffect(() => {
    const onPopState = () => syncSelection()
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  useEffect(() => {
    if (focusOnNavigate.current) {
      heading.current?.focus()
      focusOnNavigate.current = false
    }
  }, [selection])

  function navigate(projectId: string | null, taskId?: string) {
    const current = selectedFromUrl()
    const sameSelection = projectId === current.projectId && taskId === current.taskLink?.taskId
    if (sameSelection && taskId === undefined) {
      heading.current?.focus()
      return
    }
    if (!sameSelection) window.history.pushState(null, '', projectUrl(projectId, taskId))
    focusOnNavigate.current = true
    setSelection({ projectId, taskLink: taskId === undefined ? undefined : { taskId } })
  }

  function followProjectLink(event: MouseEvent<HTMLAnchorElement>, projectId: string | null, taskId?: string) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(projectId, taskId)
  }

  function handleCreated(project: Project) {
    list.refresh()
    if (selectedFromUrl().projectId === null) navigate(project.projectId)
  }

  return (
    <main className="workbench">
      <header className="masthead workbench-masthead">
        <h1>研究工作台</h1>
        <Notifications hrefFor={projectUrl} onOpen={followProjectLink} />
      </header>
      <ModelConnection />
      <WorkspaceBackup />
      <div className="workbench-grid">
        <section className="project-panel" aria-labelledby="workspace-title">
          <header className="panel-heading">
            <h2 id="workspace-title" ref={heading} tabIndex={-1}>
              {selectedId === null ? '新建项目' : '项目详情'}
            </h2>
            {selectedId !== null && (
              <a className="button outline" href={projectUrl(null)} onClick={(event) => followProjectLink(event, null)}>
                <span aria-hidden="true">+</span> 新建项目
              </a>
            )}
          </header>
          <div hidden={!creating}>
            {catalog.kind === 'loading' && <p className="workspace-note" role="status">正在读取模板目录…</p>}
            {catalog.kind === 'error' && <div className="request-error">
              <p role="alert">读取模板目录失败。{catalog.message}</p>
              <button className="button outline" type="button" onClick={() => setCatalog({ kind: 'loading' })}>重试读取模板</button>
            </div>}
            <CreateProjectForm templateItems={templateItems} onCreated={handleCreated} />
            <ProjectDraftPanel templateItems={templateItems} onCreated={handleCreated} />
          </div>
          {selectedId !== null && <ProjectDetails key={selectedId} projectId={selectedId} taskLink={taskLink} onUpdated={list.refresh} />}
        </section>
        <ProjectList list={list} selectedId={selectedId} hrefFor={projectUrl} onOpen={followProjectLink} />
      </div>
    </main>
  )
}
