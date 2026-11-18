import { useEffect, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import { CreateProjectForm } from './CreateProjectForm'
import { ProjectDetails } from './ProjectDetails'
import { ProjectList, useProjectList } from './ProjectList'
import type { Project } from './projects'

function selectedFromUrl() {
  return new URLSearchParams(window.location.search).get('project')
}

function projectUrl(projectId: string | null) {
  const url = new URL(window.location.href)
  if (projectId === null) url.searchParams.delete('project')
  else url.searchParams.set('project', projectId)
  url.hash = ''
  return url.href
}

export function App() {
  const [selectedId, setSelectedId] = useState(selectedFromUrl)
  const list = useProjectList()
  const heading = useRef<HTMLHeadingElement>(null)
  const focusOnNavigate = useRef(false)

  useEffect(() => {
    function syncSelection() {
      focusOnNavigate.current = true
      setSelectedId(selectedFromUrl())
    }
    window.addEventListener('popstate', syncSelection)
    return () => window.removeEventListener('popstate', syncSelection)
  }, [])

  useEffect(() => {
    if (focusOnNavigate.current) {
      heading.current?.focus()
      focusOnNavigate.current = false
    }
  }, [selectedId])

  function navigate(projectId: string | null) {
    if (projectId === selectedFromUrl()) {
      heading.current?.focus()
      return
    }
    window.history.pushState(null, '', projectUrl(projectId))
    focusOnNavigate.current = true
    setSelectedId(projectId)
  }

  function followProjectLink(event: MouseEvent<HTMLAnchorElement>, projectId: string | null) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(projectId)
  }

  function handleCreated(project: Project) {
    list.refresh()
    if (selectedFromUrl() === null) navigate(project.projectId)
  }

  return (
    <main className="workbench">
      <div className="masthead"><h1>研究工作台</h1></div>
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
          <div hidden={selectedId !== null}><CreateProjectForm onCreated={handleCreated} /></div>
          {selectedId !== null && <ProjectDetails key={selectedId} projectId={selectedId} onUpdated={list.refresh} />}
        </section>
        <ProjectList list={list} selectedId={selectedId} hrefFor={projectUrl} onOpen={followProjectLink} />
      </div>
    </main>
  )
}
