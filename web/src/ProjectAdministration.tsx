import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, getProject, isTurnMode, ProjectApiError, turnModeHelp, turnModeLabels, updateProject } from './projects'
import type { Project, ProjectChanges, TurnMode, UpdateProject } from './projects'
import './project-management.css'

interface ProjectAdministrationProps {
  project: Project
  onUpdated: (project: Project) => void
}

type Operation = 'save' | 'save-title' | 'save-mode' | 'save-limits' | 'archive' | 'restore' | 'refresh'
type Feedback = { kind: 'success' | 'error'; message: string } | { kind: 'conflict'; refreshError?: string }

const successMessages: Record<Operation, string> = {
  save: '显示名已保存。',
  'save-title': '研究题目已保存。',
  'save-mode': '默认协作方式已保存。',
  'save-limits': '项目级技术运行上限已保存。',
  archive: '项目已归档。',
  restore: '项目已恢复。',
  refresh: '已加载项目的最新版本。',
}

export function ProjectAdministration({ project, onUpdated }: ProjectAdministrationProps) {
  const id = useId()
  const [draft, setDraft] = useState<string>()
  const [titleDraft, setTitleDraft] = useState<string>()
  const [modeDraft, setModeDraft] = useState<TurnMode>()
  const [maxRunsDraft, setMaxRunsDraft] = useState<string>()
  const [maxTokensDraft, setMaxTokensDraft] = useState<string>()
  const [pending, setPending] = useState<Operation>()
  const [feedback, setFeedback] = useState<Feedback>()
  const operation = useRef<AbortController | null>(null)
  const attempt = useRef<UpdateProject | undefined>(undefined)
  const nameInput = useRef<HTMLInputElement>(null)
  const titleInput = useRef<HTMLInputElement>(null)
  const refreshButton = useRef<HTMLButtonElement>(null)
  const name = draft ?? project.displayName
  const dirty = name.trim() !== project.displayName
  const title = titleDraft ?? project.title
  const trimmedTitle = title.trim()
  const titleDirty = trimmedTitle !== project.title
  const titleInvalid = !trimmedTitle || Array.from(trimmedTitle).length > 200 || trimmedTitle.includes('\0')
  const savedMode = project.defaultTurnMode ?? 'CHAT'
  const mode = modeDraft ?? savedMode
  const modeDirty = mode !== savedMode
  const savedMaxRuns = project.maxModelRuns ?? 0
  const savedMaxTokens = project.maxOutputTokens ?? 0
  const maxRunsText = maxRunsDraft ?? String(savedMaxRuns)
  const maxTokensText = maxTokensDraft ?? String(savedMaxTokens)
  const parsedMaxRuns = maxRunsText.trim() === '' ? NaN : Number(maxRunsText)
  const parsedMaxTokens = maxTokensText.trim() === '' ? NaN : Number(maxTokensText)
  const maxRunsInvalid = !Number.isInteger(parsedMaxRuns) || parsedMaxRuns < 0 || parsedMaxRuns > 1000
  const maxTokensInvalid = !Number.isInteger(parsedMaxTokens) || (parsedMaxTokens !== 0 && (parsedMaxTokens < 256 || parsedMaxTokens > 32768))
  const limitsInvalid = maxRunsInvalid || maxTokensInvalid
  const limitsDirty = !limitsInvalid && (parsedMaxRuns !== savedMaxRuns || parsedMaxTokens !== savedMaxTokens)
  const runsUsed = project.modelRunsUsed ?? 0
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
        const input = previous?.expectedRevision === project.revision && previous.title === changes.title &&
          previous.displayName === changes.displayName && previous.status === changes.status &&
          previous.defaultTurnMode === changes.defaultTurnMode &&
          previous.maxModelRuns === changes.maxModelRuns && previous.maxOutputTokens === changes.maxOutputTokens
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
      if (kind === 'save-title' || !titleDirty || trimmedTitle === next.title) setTitleDraft(undefined)
      if (kind === 'save-mode' || !modeDirty || mode === (next.defaultTurnMode ?? 'CHAT')) setModeDraft(undefined)
      if (kind === 'save-limits' || !limitsDirty) {
        setMaxRunsDraft(undefined)
        setMaxTokensDraft(undefined)
      }
      if (kind === 'refresh' && document.activeElement === refreshButton.current) {
        (titleDirty ? titleInput : nameInput).current?.focus()
      }
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
        if (!writesBlocked && titleDirty && !titleInvalid) void run('save-title', { title: trimmedTitle })
      }}>
        <label htmlFor={`${id}-title`}>研究题目</label>
        <div className="project-name-controls">
          <input id={`${id}-title`} ref={titleInput} type="text" value={title} readOnly={pending !== undefined}
            required aria-invalid={titleDirty && titleInvalid ? true : undefined} aria-describedby={`${id}-title-help`}
            onChange={(event) => {
              setTitleDraft(event.currentTarget.value)
              if (!hasConflict) setFeedback(undefined)
            }} />
          <button className="button outline" type="submit" aria-disabled={writesBlocked || !titleDirty || titleInvalid}>
            {pending === 'save-title' ? '正在保存…' : '保存研究题目'}
          </button>
        </div>
        <p className="project-management-help" id={`${id}-title-help`}>
          {titleDirty && titleInvalid ? '请填写 1 至 200 个字符。' : '新工作流采用最新研究题目。续跑沿用本轮的题目。'}
        </p>
      </form>
      <form className="field" onSubmit={(event) => {
        event.preventDefault()
        if (!writesBlocked && modeDirty) void run('save-mode', { defaultTurnMode: mode })
      }}>
        <label htmlFor={`${id}-default-mode`}>默认协作方式</label>
        <select id={`${id}-default-mode`} value={mode} disabled={pending !== undefined} aria-describedby={`${id}-mode-help`} onChange={(event) => {
          const nextMode = event.currentTarget.value
          if (isTurnMode(nextMode)) setModeDraft(nextMode === savedMode ? undefined : nextMode)
          if (!hasConflict) setFeedback(undefined)
        }}>
          {Object.entries(turnModeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
        <p className="project-management-help" id={`${id}-mode-help`}>{turnModeHelp[mode]}</p>
        {modeDirty && <p className="project-management-help">当前已保存为「{turnModeLabels[savedMode]}」。</p>}
        <div className="project-management-actions">
          <button className="button outline" type="submit" aria-disabled={writesBlocked || !modeDirty}>
            {pending === 'save-mode' ? '正在保存…' : '保存协作方式'}
          </button>
        </div>
      </form>
      <form className="field" onSubmit={(event) => {
        event.preventDefault()
        if (!writesBlocked && limitsDirty) {
          void run('save-limits', { maxModelRuns: parsedMaxRuns, maxOutputTokens: parsedMaxTokens })
        }
      }}>
        <strong>项目级模型技术运行上限</strong>
        <p className="project-management-help">
          纯技术运行控制上限（仅针对用户自带模型生效）。当前项目已累计发起模型调用 <strong>{runsUsed}</strong> 次
          {savedMaxRuns > 0 ? ` / 上限 ${savedMaxRuns} 次` : '（当前不限次数）'}。
        </p>
        <label htmlFor={`${id}-max-model-runs`}>单项目最大模型调用次数（0 为不限，范围 1–1000）</label>
        <input id={`${id}-max-model-runs`} name="maxModelRuns" type="number" min={0} max={1000} step={1}
          value={maxRunsText} readOnly={pending !== undefined} aria-invalid={maxRunsInvalid ? true : undefined}
          onChange={(event) => {
            setMaxRunsDraft(event.currentTarget.value)
            if (!hasConflict) setFeedback(undefined)
          }} />
        <label htmlFor={`${id}-max-output-tokens`}>单轮最大输出 token（0 为默认 8192，范围 256–32768）</label>
        <input id={`${id}-max-output-tokens`} name="maxOutputTokens" type="number" min={0} max={32768} step={1}
          value={maxTokensText} readOnly={pending !== undefined} aria-invalid={maxTokensInvalid ? true : undefined}
          onChange={(event) => {
            setMaxTokensDraft(event.currentTarget.value)
            if (!hasConflict) setFeedback(undefined)
          }} />
        {limitsInvalid && (
          <p className="form-error" role="alert">
            最大调用次数须为 0 至 1000 的整数；单轮最大输出 token 须为 0 或 256 至 32768 的整数。
          </p>
        )}
        <div className="project-management-actions">
          <button className="button outline" type="submit" aria-disabled={writesBlocked || !limitsDirty}>
            {pending === 'save-limits' ? '正在保存…' : '保存技术上限'}
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
