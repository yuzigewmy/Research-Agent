import { useCallback, useEffect, useState } from 'react'
import { Conversation } from './Conversation'
import { LiteraturePanel } from './LiteraturePanel'
import { MaterialPanel } from './MaterialPanel'
import { ProjectAdministration } from './ProjectAdministration'
import { SurveyPanel } from './SurveyPanel'
import { TaskPanel } from './TaskPanel'
import type { Task } from './tasks'
import { WorkflowPanel } from './WorkflowPanel'
import { WorkspacePanel } from './WorkspacePanel'
import type { Material } from './materials'
import { errorMessage, getProject, ProjectApiError, templates } from './projects'
import type { Project } from './projects'
import { isTurnReferences, turnReferenceLimit } from './turns'
import type { TurnReference } from './turns'

const dateFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' })
const dateTimeFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })

export function ProjectDate({ value, withTime = false }: { value: string; withTime?: boolean }) {
  return <time dateTime={value}>{(withTime ? dateTimeFormat : dateFormat).format(new Date(value))}</time>
}

export function LoadingState({ message }: { message: string }) {
  return (
    <div className="loading-state" role="status">
      <p>{message}</p>
      <div className="loading-lines" aria-hidden="true"><span /><span /></div>
    </div>
  )
}

export function RequestError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="request-error">
      <p role="alert">{message}</p>
      <button className="button outline" type="button" onClick={onRetry}>重试</button>
    </div>
  )
}

type DetailState = { kind: 'loading' } | { kind: 'ready'; project: Project } | { kind: 'error'; message: string }

function readReferences(key: string): TurnReference[] {
  try {
    const saved: unknown = JSON.parse(sessionStorage.getItem(key) ?? 'null')
    return isTurnReferences(saved) ? saved : []
  } catch {
    return []
  }
}

export function ProjectDetails({ projectId, onUpdated }: { projectId: string; onUpdated: (project: Project) => void }) {
  const [detail, setDetail] = useState<DetailState>({ kind: 'loading' })
  const [retry, setRetry] = useState(0)
  const referenceKey = `research.references.${projectId}`
  const [references, setReferences] = useState(() => readReferences(referenceKey))
  const [publishedPaths, setPublishedPaths] = useState<string[]>([])
  const [workflowRefresh, setWorkflowRefresh] = useState(0)
  const [taskUpdates, setTaskUpdates] = useState<Task[]>([])

  const onTaskChanged = useCallback((task: Task) => {
    setTaskUpdates((previous) => {
      const newer = previous.find((item) => item.taskId === task.taskId && item.revision >= task.revision)
      return newer ? previous : [...previous.filter((item) => item.taskId !== task.taskId), task]
    })
  }, [])

  const taskSync = { updates: taskUpdates, onTaskChanged }

  useEffect(() => {
    if (references.length) sessionStorage.setItem(referenceKey, JSON.stringify(references))
    else sessionStorage.removeItem(referenceKey)
  }, [referenceKey, references])

  const clearReferences = useCallback((matches: (reference: TurnReference) => boolean) => {
    setReferences((current) => current.filter((reference) => !matches(reference)))
  }, [])

  function addPdfQuote({ materialId, name, sha256 }: Material, page: number, quote: string) {
    const selection = { materialId, name, sha256, page, quote }
    setReferences((current) => {
      if (current.some((reference) => reference.materialId === materialId)) {
        return current.map((reference) => reference.materialId === materialId ? selection : reference)
      }
      return current.length < turnReferenceLimit ? [...current, selection] : current
    })
  }

  function toggleReference({ materialId, name, sha256 }: Material) {
    setReferences((current) => {
      if (current.some((reference) => reference.materialId === materialId)) {
        return current.filter((reference) => reference.materialId !== materialId)
      }
      return current.length < turnReferenceLimit ? [...current, { materialId, name, sha256 }] : current
    })
  }

  useEffect(() => {
    const controller = new AbortController()
    getProject(projectId, controller.signal).then((project) => {
      if (!controller.signal.aborted) setDetail({ kind: 'ready', project })
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      const message = error instanceof ProjectApiError && error.status === 404
        ? '找不到此项目，请检查链接或从项目列表打开。'
        : errorMessage(error)
      setDetail({ kind: 'error', message })
    })
    return () => controller.abort()
  }, [projectId, retry])

  if (detail.kind === 'loading') return <LoadingState message="正在加载项目详情…" />
  if (detail.kind === 'error') {
    return <RequestError message={detail.message} onRetry={() => {
      setDetail({ kind: 'loading' })
      setRetry((value) => value + 1)
    }} />
  }

  const { project } = detail
  return (
    <div className="project-detail">
      <h3 dir="auto">{project.displayName || project.title}</h3>
      <dl className="project-properties">
        {project.displayName && project.displayName !== project.title &&
          <div><dt>研究题目</dt><dd dir="auto">{project.title}</dd></div>}
        <div><dt>项目模板</dt><dd>{templates[project.templateId]}</dd></div>
        {project.goal && <div><dt>研究目标</dt><dd className="turn-content" dir="auto">{project.goal}</dd></div>}
        {project.deliverables && <div><dt>预期交付物</dt><dd className="turn-content" dir="auto">{project.deliverables}</dd></div>}
        <div><dt>状态</dt><dd>{project.status === 'ARCHIVED' ? '已归档' : <span className="ready-label">已就绪</span>}</dd></div>
        <div><dt>创建时间</dt><dd><ProjectDate value={project.createdAt} withTime /></dd></div>
        <div><dt>修订</dt><dd>{project.revision}</dd></div>
      </dl>
      <ProjectAdministration project={project} onUpdated={(updated) => {
        setDetail({ kind: 'ready', project: updated })
        onUpdated(updated)
      }} />
      <MaterialPanel key={project.projectId} projectId={project.projectId} onReference={toggleReference}
        onPdfQuote={addPdfQuote} referenceIds={references.map(({ materialId }) => materialId)} />
      <SurveyPanel projectId={project.projectId} />
      {(project.templateId === 'literature_review' || project.templateId === 'paper'
        || project.templateId === 'topic' || project.templateId === 'proposal') && <LiteraturePanel projectId={project.projectId} />}
      {project.templateId !== 'freeform' && project.templateId !== 'automatic_review' &&
        <WorkflowPanel projectId={project.projectId} templateId={project.templateId} refreshVersion={workflowRefresh}
          onManuscriptSaved={(path) => setPublishedPaths((paths) => paths.includes(path) ? paths : [path, ...paths])} />}
      <TaskPanel projectId={project.projectId} sync={taskSync} />
      <WorkspacePanel projectId={project.projectId} publishedPaths={publishedPaths}
        manuscriptIntake={project.templateId === 'automatic_review'} />
      <Conversation projectId={project.projectId} defaultTurnMode={project.defaultTurnMode} references={references}
        onClearReferences={clearReferences} onWorkflowConfirmed={() => setWorkflowRefresh((version) => version + 1)}
        taskSync={taskSync} />
    </div>
  )
}
