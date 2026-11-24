import { useCallback, useEffect, useState } from 'react'
import { Conversation } from './Conversation'
import { DiagramPanel } from './DiagramPanel'
import { EngineeringPreview } from './EngineeringPreview'
import { LiteraturePanel } from './LiteraturePanel'
import { MaterialPanel } from './MaterialPanel'
import { ProjectAdministration } from './ProjectAdministration'
import { ProjectTaskQueue } from './ProjectTaskQueue'
import { QuestionnairePanel } from './QuestionnairePanel'
import { SurveyPanel } from './SurveyPanel'
import { TaskPanel } from './TaskPanel'
import type { Task, TaskLink } from './tasks'
import { WorkflowPanel } from './WorkflowPanel'
import { WorkspacePanel } from './WorkspacePanel'
import type { Material } from './materials'
import { errorMessage, getProject, ProjectApiError, templates } from './projects'
import type { Project } from './projects'
import {
  isManuscriptSelection, isManuscriptSelections, isTurnReferences, manuscriptSelectionLimit, sameManuscriptSelection, turnReferenceLimit,
} from './turns'
import type { ManuscriptSelection, ManuscriptSelectionInput, TurnReference } from './turns'
import type { FileRecord } from './workspace'

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

function readManuscriptSelections(key: string): ManuscriptSelection[] {
  try {
    const saved: unknown = JSON.parse(sessionStorage.getItem(key) ?? 'null')
    if (isManuscriptSelections(saved)) return saved
    return isManuscriptSelection(saved) ? [saved] : []
  } catch {
    return []
  }
}

export function ProjectDetails({ projectId, taskLink, onUpdated }: {
  projectId: string
  taskLink?: TaskLink
  onUpdated: (project: Project) => void
}) {
  const [detail, setDetail] = useState<DetailState>({ kind: 'loading' })
  const [retry, setRetry] = useState(0)
  const referenceKey = `research.references.${projectId}`
  const manuscriptKey = `research.manuscript-selection.${projectId}`
  const [references, setReferences] = useState(() => readReferences(referenceKey))
  const [manuscriptSelections, setManuscriptSelections] = useState(() => readManuscriptSelections(manuscriptKey))
  const [publishedPaths, setPublishedPaths] = useState<string[]>([])
  const [workflowRefresh, setWorkflowRefresh] = useState(0)
  const [acceptanceDraft, setAcceptanceDraft] = useState<{ file: FileRecord; openId: number }>()
  const [taskUpdates, setTaskUpdates] = useState<Task[]>([])

  const refreshWorkflow = useCallback(() => setWorkflowRefresh((version) => version + 1), [])

  const onTaskChanged = useCallback((task: Task) => {
    setTaskUpdates((previous) => {
      const newer = previous.find((item) => item.taskId === task.taskId && item.revision >= task.revision)
      return newer ? previous : [...previous.filter((item) => item.taskId !== task.taskId), task]
    })
  }, [])

  const templateId = detail.kind === 'ready' ? detail.project.templateId : undefined
  const hasWorkflow = templateId !== undefined && templateId !== 'freeform'
    && templateId !== 'automatic_review' && templateId !== 'interactive_survey' && templateId !== 'engineering_project'

  function reviewAppliedFile(file: FileRecord) {
    setAcceptanceDraft((current) => ({ file, openId: (current?.openId ?? 0) + 1 }))
    refreshWorkflow()
  }

  const taskSync = { updates: taskUpdates, onTaskChanged, onReviewAppliedFile: hasWorkflow ? reviewAppliedFile : undefined }

  useEffect(() => {
    if (references.length) sessionStorage.setItem(referenceKey, JSON.stringify(references))
    else sessionStorage.removeItem(referenceKey)
  }, [referenceKey, references])

  useEffect(() => {
    try {
      if (manuscriptSelections.length) {
        const saved = manuscriptSelections.length === 1 ? manuscriptSelections[0] : manuscriptSelections
        sessionStorage.setItem(manuscriptKey, JSON.stringify(saved))
      } else sessionStorage.removeItem(manuscriptKey)
    } catch {
      // The current selection remains available when session storage is restricted.
    }
  }, [manuscriptKey, manuscriptSelections])

  function addManuscriptSelection(selection: ManuscriptSelection) {
    setManuscriptSelections((current) => {
      if (current.some(({ path }) => path === selection.path)) {
        return current.map((item) => item.path === selection.path ? selection : item)
      }
      return current.length < manuscriptSelectionLimit ? [...current, selection] : current
    })
  }

  const clearManuscriptSelections = useCallback((sent: readonly ManuscriptSelectionInput[]) => {
    setManuscriptSelections((current) => current.filter((item) =>
      !sent.some((selection) => sameManuscriptSelection(item, selection))))
  }, [])

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
      <ProjectTaskQueue projectId={project.projectId} onStopped={refreshWorkflow} />
      <MaterialPanel key={project.projectId} projectId={project.projectId} onReference={toggleReference}
        onPdfQuote={addPdfQuote} referenceIds={references.map(({ materialId }) => materialId)}
        allowSoftwareFolder={project.templateId === 'software_registration'} />
      {project.templateId === 'interactive_survey' && (
        <QuestionnairePanel projectId={project.projectId} archived={project.status === 'ARCHIVED'} />
      )}
      <SurveyPanel projectId={project.projectId} />
      {(project.templateId === 'literature_review' || project.templateId === 'paper'
        || project.templateId === 'topic' || project.templateId === 'proposal'
        || project.templateId === 'course_paper' || project.templateId === 'course_report') && <LiteraturePanel projectId={project.projectId} />}
      {hasWorkflow &&
        <WorkflowPanel projectId={project.projectId} templateId={templateId} refreshVersion={workflowRefresh}
          acceptanceDraft={acceptanceDraft} onClearAcceptanceDraft={(openId) => setAcceptanceDraft((current) =>
            current?.openId === openId ? undefined : current)}
          onManuscriptSaved={(path) => setPublishedPaths((paths) => paths.includes(path) ? paths : [path, ...paths])} />}
      <TaskPanel projectId={project.projectId} taskLink={taskLink} sync={taskSync} />
      <WorkspacePanel projectId={project.projectId} publishedPaths={publishedPaths}
        manuscriptIntake={project.templateId === 'automatic_review'} manuscriptSelections={manuscriptSelections}
        onManuscriptSelection={addManuscriptSelection} />
      {project.templateId === 'engineering_project' && (
        <EngineeringPreview key={`${project.projectId}:${project.status}`} projectId={project.projectId}
          archived={project.status === 'ARCHIVED'} />
      )}
      <DiagramPanel projectId={project.projectId} />
      <Conversation projectId={project.projectId} defaultTurnMode={project.defaultTurnMode} references={references}
        onClearReferences={clearReferences} manuscriptSelections={manuscriptSelections}
        onClearManuscriptSelections={clearManuscriptSelections} onWorkflowConfirmed={refreshWorkflow} taskSync={taskSync} />
    </div>
  )
}
