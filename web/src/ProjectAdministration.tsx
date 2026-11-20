import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, getProject, isTurnMode, ProjectApiError, turnModeLabels, updateProject } from './projects'
import type { Project, ProjectChanges, TurnMode, UpdateProject } from './projects'
import './project-management.css'

interface ProjectAdministrationProps {
  project: Project
  onUpdated: (project: Project) => void
}

type Operation = 'save' | 'save-mode' | 'archive' | 'restore' | 'refresh'
type Feedback = { kind: 'success' | 'error'; message: string } | { kind: 'conflict'; refreshError?: string }

const successMessages: Record<Operation, string> = {
  save: '显示名已保存。',
  'save-mode': '默认协作方式已保存。',
  archive: '项目已归档。',
  restore: '项目已恢复。',
  refresh: '已加载项目的最新版本。',
}

export function ProjectAdministration({ project, onUpdated }: ProjectAdministrationProps) {
  const id = useId()
  const [draft, setDraft] = useState<string>()
  const [modeDraft, setModeDraft] = useState<TurnMode>()
  const [pending, setPending] = useState<Operation>()
  const [feedback, setFeedback] = useState<Feedback>()
  const operation = useRef<AbortController | null>(null)
  const attempt = useRef<UpdateProject | undefined>(undefined)
  const nameInput = useRef<HTMLInputElement>(null)
  const refreshButton = useRef<HTMLButtonElement>(null)
  const name = draft ?? project.displayName
  const dirty = name.trim() !== project.displayName
  const savedMode = project.defaultTurnMode ?? 'CHAT'
  const mode = modeDraft ?? savedMode
  const modeDirty = mode !== savedMode
  const hasConflict = feedback?.kind === 'conflict'
  const archived = project.status === 'ARCHIVED'
  const writesBlocked = pending !== undefined || hasConflict

  useEffect(() => () => operation.current?.abort(), [])

  async function run(kind: Operation, changes?: ProjectChanges) {
    if (operation.current || (changes && hasConflict)) return
    const controller = new AbortController()
    operation.current = controller
    setPending(kind)
    if (kind !== 'refresh') setFeedback(undefined)

    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      let next: Project
      if (changes) {
        const previous = attempt.current
        const input = previous?.expectedRevision === project.revision &&
          previous.displayName === changes.displayName && previous.status === changes.status &&
          previous.defaultTurnMode === changes.defaultTurnMode
          ? previous
          : { ...changes, requestId: crypto.randomUUID(), expectedRevision: project.revision }
        attempt.current = input
        next = await updateProject(project.projectId, input, signal)
      } else {
        next = await getProject(project.projectId, signal)
      }
      if (controller.signal.aborted) return
      attempt.current = undefined
      if (kind === 'save' || !dirty) setDraft(undefined)
      if (kind === 'save-mode' || !modeDirty || mode === (next.defaultTurnMode ?? 'CHAT')) setModeDraft(undefined)
      if (kind === 'refresh' && document.activeElement === refreshButton.current) nameInput.current?.focus()
      onUpdated(next)
      setFeedback({ kind: 'success', message: successMessages[kind] })
    } catch (error) {
      if (controller.signal.aborted) return
      if (kind === 'refresh') {
        setFeedback({ kind: 'conflict', refreshError: errorMessage(error) })
      } else if (error instanceof ProjectApiError && error.status === 409 && error.code === 'REVISION_CONFLICT') {
        attempt.current = undefined
        setFeedback({ kind: 'conflict' })
      } else {
        setFeedback({ kind: 'error', message: errorMessage(error) })
      }
    } finally {
      operation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  return (
    <section className="project-administration" aria-labelledby={`${id}-heading`}>
      <h4 id={`${id}-heading`}>项目管理</h4>
      <form className="field" onSubmit={(event) => {
        event.preventDefault()
        if (!writesBlocked && dirty) void run('save', { displayName: name.trim() })
      }}>
        <label htmlFor={`${id}-name`}>项目显示名</label>
        <div className="project-name-controls">
          <input id={`${id}-name`} ref={nameInput} type="text" value={name} readOnly={pending !== undefined}
            aria-describedby={`${id}-help`} onChange={(event) => {
              setDraft(event.currentTarget.value)
              if (!hasConflict) setFeedback(undefined)
            }} />
          <button className="button outline" type="submit" aria-disabled={writesBlocked || !dirty}>
            {pending === 'save' ? '正在保存…' : '保存显示名'}
          </button>
        </div>
        <p className="project-management-help" id={`${id}-help`}>
          {dirty && project.displayName && <>当前显示名为 <bdi>{project.displayName}</bdi>。 </>}
          留空时显示研究题目。
        </p>
      </form>
      <form className="field" onSubmit={(event) => {
        event.preventDefault()
        if (!writesBlocked && modeDirty) void run('save-mode', { defaultTurnMode: mode })
      }}>
        <label htmlFor={`${id}-default-mode`}>默认协作方式</label>
        <select id={`${id}-default-mode`} value={mode} disabled={pending !== undefined} onChange={(event) => {
          const nextMode = event.currentTarget.value
          if (isTurnMode(nextMode)) setModeDraft(nextMode === savedMode ? undefined : nextMode)
          if (!hasConflict) setFeedback(undefined)
        }}>
          {Object.entries(turnModeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
        {modeDirty && <p className="project-management-help">当前已保存为「{turnModeLabels[savedMode]}」。</p>}
        <div className="project-management-actions">
          <button className="button outline" type="submit" aria-disabled={writesBlocked || !modeDirty}>
            {pending === 'save-mode' ? '正在保存…' : '保存协作方式'}
          </button>
        </div>
      </form>
      <div className="project-management-actions">
        <button className="button outline" type="button" aria-disabled={writesBlocked} onClick={() => {
          if (!writesBlocked) void run(archived ? 'restore' : 'archive', { status: archived ? 'READY' : 'ARCHIVED' })
        }}>
          {pending === 'archive' ? '正在归档…' : pending === 'restore' ? '正在恢复…' : archived ? '恢复项目' : '归档项目'}
        </button>
      </div>
      {feedback?.kind === 'error' && <p role="alert" className="form-error">{feedback.message}</p>}
      {feedback?.kind === 'success' && <p role="status" className="project-management-help">{feedback.message}</p>}
      {feedback?.kind === 'conflict' && (
        <div className="project-conflict">
          <p role="alert" className="form-error">项目已有更新。请刷新项目，核对最新版本后再提交。</p>
          {feedback.refreshError && <p role="alert" className="form-error">{feedback.refreshError}</p>}
          <button ref={refreshButton} className="button outline" type="button" aria-disabled={pending !== undefined}
            onClick={() => { if (!pending) void run('refresh') }}>
            {pending === 'refresh' ? '正在刷新…' : '刷新项目'}
          </button>
        </div>
      )}
    </section>
  )
}
