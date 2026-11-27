import { lazy, Suspense, useEffect, useEffectEvent, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import { ComputeNode } from './ComputeNode'
import { CreateProjectForm } from './CreateProjectForm'
import type { CreateProjectLaunchOptions } from './CreateProjectForm'
import { ModelConnection } from './ModelConnection'
import { readReviewLink } from './notificationApi'
import type { ReviewLink } from './notificationApi'
import { Notifications } from './Notifications'
import { ProjectArchiveImport } from './ProjectArchiveImport'
import { ProjectDetails } from './ProjectDetails'
import { ProjectDraftPanel } from './ProjectDraftPanel'
import { ProjectList, useProjectList } from './ProjectList'
import { listProjectTemplates } from './projectTemplates'
import type { ProjectTemplate } from './projectTemplates'
import { errorMessage } from './projects'
import type { Project } from './projects'
import type { TaskLink } from './tasks'
import type { WorkflowRunLink } from './WorkflowRunHistory'
import { WorkspaceBackup } from './WorkspaceBackup'
import { WorkspaceComponents } from './WorkspaceComponents'
import { WorkbenchAssistant } from './WorkbenchAssistant'
import { useWorkbenchPreferences, WorkbenchPreferences } from './WorkbenchPreferences'
import { LocaleProvider, t } from './i18n'

type Catalog = { kind: 'loading' } | { kind: 'ready'; items: ProjectTemplate[] } | { kind: 'error'; message: string }

export type ProjectTarget =
  | (TaskLink & { workflowRunId?: never; reviewId?: never })
  | (WorkflowRunLink & { taskId?: never; reviewId?: never })
  | (ReviewLink & { taskId?: never; workflowRunId?: never })

type Selection = { projectId: string | null; target?: ProjectTarget }

function selectedFromUrl(): Selection {
  const params = new URLSearchParams(window.location.search)
  const projectId = params.get('project')
  const workflowRunId = params.get('workflowRun')
  const taskId = params.get('task')
  if (projectId && workflowRunId) return { projectId, target: { workflowRunId } }
  if (projectId && taskId) return { projectId, target: { taskId } }
  const target = readReviewLink(params.get('review'), params.get('reviewVersion'), params.get('reviewKind'))
  if (projectId && target) return { projectId, target }
  return { projectId }
}

function sameSelection(a: Selection, b: Selection) {
  const aReview = a.target?.reviewId ? a.target : undefined
  const bReview = b.target?.reviewId ? b.target : undefined
  return a.projectId === b.projectId && a.target?.taskId === b.target?.taskId
    && a.target?.workflowRunId === b.target?.workflowRunId
    && aReview?.reviewId === bReview?.reviewId && aReview?.reviewVersionId === bReview?.reviewVersionId
    && aReview?.reviewKind === bReview?.reviewKind
}

function projectUrl(projectId: string | null, target?: ProjectTarget) {
  const url = new URL(window.location.href)
  if (projectId === null) url.searchParams.delete('project')
  else url.searchParams.set('project', projectId)
  if (projectId !== null && target?.taskId) url.searchParams.set('task', target.taskId)
  else url.searchParams.delete('task')
  if (projectId !== null && target?.workflowRunId) url.searchParams.set('workflowRun', target.workflowRunId)
  else url.searchParams.delete('workflowRun')
  if (projectId !== null && target?.reviewId) {
    url.searchParams.set('review', target.reviewId)
    url.searchParams.set('reviewVersion', target.reviewVersionId)
    url.searchParams.set('reviewKind', target.reviewKind)
  } else {
    url.searchParams.delete('review')
    url.searchParams.delete('reviewVersion')
    url.searchParams.delete('reviewKind')
  }
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
  const { projectId: selectedId, target } = selection
  const [projectLaunch, setProjectLaunch] = useState<(CreateProjectLaunchOptions & { projectId: string }) | undefined>()
  const [catalog, setCatalog] = useState<Catalog>({ kind: 'loading' })
  const list = useProjectList()
  const preferences = useWorkbenchPreferences()
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
    if (sameSelection(next, selection)) return
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

  function navigate(projectId: string | null, target?: ProjectTarget) {
    const next = { projectId, target }
    if (!sameSelection(next, selectedFromUrl())) window.history.pushState(null, '', projectUrl(projectId, target))
    else if (!target) {
      heading.current?.focus()
      return
    }
    focusOnNavigate.current = true
    setSelection(next)
  }

  function followProjectLink(event: MouseEvent<HTMLAnchorElement>, projectId: string | null, target?: ProjectTarget) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(projectId, target)
  }

  function handleCreated(project: Project, options?: CreateProjectLaunchOptions) {
    list.refresh()
    setProjectLaunch(options ? { projectId: project.projectId, ...options } : undefined)
    if (selectedFromUrl().projectId === null) navigate(project.projectId)
  }

  const locale = preferences.language

  return (
    <LocaleProvider value={locale}>
      <main className="workbench">
        <header className="masthead workbench-masthead">
          <h1>{t(locale, 'app.title')}</h1>
          <Notifications hrefFor={projectUrl} onOpen={followProjectLink} onNavigate={navigate} language={locale} />
        </header>
        <WorkbenchPreferences {...preferences} />
        <ModelConnection />
        <ComputeNode />
        <WorkspaceBackup />
        <WorkspaceComponents />
        <div className="workbench-grid" data-sidebar-hidden={preferences.sidebarHidden}>
          <section className="project-panel" aria-labelledby="workspace-title">
            <header className="panel-heading">
              <h2 id="workspace-title" ref={heading} tabIndex={-1}>
                {selectedId === null ? t(locale, 'app.newProject') : t(locale, 'app.projectDetails')}
              </h2>
              {selectedId !== null && (
                <a className="button outline" href={projectUrl(null)} onClick={(event) => followProjectLink(event, null)}>
                  <span aria-hidden="true">+</span> {t(locale, 'app.newProject')}
                </a>
              )}
            </header>
            <div hidden={!creating}>
              {catalog.kind === 'loading' && <p className="workspace-note" role="status">{t(locale, 'app.loadingCatalog')}</p>}
              {catalog.kind === 'error' && <div className="request-error">
                <p role="alert">{t(locale, 'app.catalogErrorPrefix')}{catalog.message}</p>
                <button className="button outline" type="button" onClick={() => setCatalog({ kind: 'loading' })}>{t(locale, 'app.retryCatalog')}</button>
              </div>}
              <CreateProjectForm templateItems={templateItems} onCreated={handleCreated} />
              <ProjectDraftPanel templateItems={templateItems} onCreated={handleCreated} />
              <ProjectArchiveImport onImported={handleCreated} />
            </div>
            {selectedId !== null && <ProjectDetails key={selectedId} projectId={selectedId}
              launch={projectLaunch?.projectId === selectedId ? projectLaunch : undefined}
              taskLink={target?.taskId ? target : undefined} workflowRunLink={target?.workflowRunId ? target : undefined}
              reviewLink={target?.reviewId ? target : undefined} onUpdated={list.refresh}
              onDeleted={() => {
                list.refresh()
                navigate(null)
              }} />}
          </section>
          <ProjectList list={list} hidden={preferences.sidebarHidden} selectedId={selectedId} hrefFor={projectUrl} onOpen={followProjectLink} />
        </div>
        <WorkbenchAssistant projectId={selectedId} language={locale} />
      </main>
    </LocaleProvider>
  )
}
