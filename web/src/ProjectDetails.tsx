import { useEffect, useState } from 'react'
import { Conversation } from './Conversation'
import { MaterialPanel } from './MaterialPanel'
import { TaskPanel } from './TaskPanel'
import { WorkflowPanel } from './WorkflowPanel'
import { errorMessage, getProject, ProjectApiError, templates } from './projects'
import type { Project } from './projects'

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

export function ProjectDetails({ projectId }: { projectId: string }) {
  const [detail, setDetail] = useState<DetailState>({ kind: 'loading' })
  const [retry, setRetry] = useState(0)

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
      <h3 dir="auto">{project.title}</h3>
      <dl className="project-properties">
        <div><dt>项目模板</dt><dd>{templates[project.templateId]}</dd></div>
        <div><dt>状态</dt><dd><span className="ready-label">已就绪</span></dd></div>
        <div><dt>创建时间</dt><dd><ProjectDate value={project.createdAt} withTime /></dd></div>
        <div><dt>修订</dt><dd>{project.revision}</dd></div>
      </dl>
      <MaterialPanel key={project.projectId} projectId={project.projectId} />
      {project.templateId === 'experiment' && <WorkflowPanel projectId={project.projectId} />}
      <TaskPanel projectId={project.projectId} />
      <Conversation projectId={project.projectId} />
    </div>
  )
}
