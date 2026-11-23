import { useCallback, useEffect, useEffectEvent, useId, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import { selectionEditInstructionLimit } from './SelectionEdit'
import { TaskInputSummary, TaskInputVersions } from './TaskInputVersions'
import { TaskSelectionEdit } from './TaskSelectionEdit'
import {
  changeTask, conversationTurnAnchor, editTask, isMarkdownTaskPath, isTaskActive, listTasks, proposeTask, refreshTasks,
  taskArtifactPath, taskInputLimit, taskInstructionLimit, taskStatusLabels,
} from './tasks'
import type { ProposalInput, Task, TaskAction, TaskInputVersion, TaskPage } from './tasks'
import './tasks.css'

const refreshInterval = 2_000
const requestTimeout = 10_000
const taskDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const turnSourceNotice = '确认运行后，用户原话、部分回复和当时引用的资料节选会发送给研究模型。'

export interface TaskPanelSync {
  updates: Task[]
  onTaskChanged: (task: Task) => void
}

type TaskFields = Omit<ProposalInput, 'requestId' | 'sourceTurnId'>
type CardMutation = { kind: TaskAction; task: Task }
  | ({ kind: 'edit'; task: Task; expectedRevision: number } & TaskFields)
type Mutation = ({ kind: 'propose' } & TaskFields) | CardMutation
type ChangeAttempt = { requestId: string; expectedRevision: number } & Partial<TaskFields>
type MutationResult = { ok: true } | { ok: false; message: string; inputChanged?: boolean }
type InstructionDraft = {
  text: string
  revision: number
  inputs: TaskInputVersion[]
  baseInstruction: string
  baseInputs: TaskInputVersion[]
}
const instructionEncoder = new TextEncoder()

function versionIds(inputs: TaskInputVersion[]) {
  return inputs.map(({ versionId }) => versionId)
}

function sameVersionIds(left?: string[], right?: string[]) {
  if (!left || !right) return left === right
  return left.length === right.length && left.every((id, index) => id === right[index])
}

function rebaseInputs(draft: InstructionDraft, latest: TaskInputVersion[], singleInput: boolean) {
  if (singleInput) {
    return sameVersionIds(versionIds(draft.inputs), versionIds(draft.baseInputs)) ? latest : draft.inputs
  }
  const merged = new Map(latest.map((input) => [input.path, input]))
  const previous = new Map(draft.baseInputs.map((input) => [input.path, input.versionId]))
  for (const path of previous.keys()) {
    if (!draft.inputs.some((input) => input.path === path)) merged.delete(path)
  }
  for (const input of draft.inputs) {
    if (previous.get(input.path) !== input.versionId) merged.set(input.path, input)
  }
  return [...merged.values()]
}

function instructionError(instruction: string, limit = taskInstructionLimit) {
  if (!instruction) return '请输入任务说明。'
  if (instruction.includes('\0')) return '说明中不能包含空字符。'
  if (instructionEncoder.encode(instruction).length > limit) return `任务说明不能超过 ${limit / 1024} KiB。`
  return ''
}

function editInputError(inputs: TaskInputVersion[], kind: Task['kind']) {
  if (kind === 'manuscript-check' && (inputs.length !== 1 || !isMarkdownTaskPath(inputs[0].path))) {
    return '请选择一份已保存的 Markdown 稿件。'
  }
  if (kind === 'focused-research' && (inputs.length < 1 || inputs.length > taskInputLimit
    || inputs.some(({ path }) => !isMarkdownTaskPath(path)))) {
    return `请选择 1 至 ${taskInputLimit} 份已保存的 Markdown 文件。`
  }
  return inputs.length > taskInputLimit ? `请将已选文件调整为最多 ${taskInputLimit} 个。` : ''
}

function mergeTasks(current: Task[], incoming: Task[]) {
  const byId = new Map(current.map((task) => [task.taskId, task]))
  for (const task of incoming) {
    // A list response can arrive after a more recent action response.
    if ((byId.get(task.taskId)?.revision ?? 0) < task.revision) byId.set(task.taskId, task)
  }
  return [...byId.values()].sort((a, b) =>
    Date.parse(b.createdAt) - Date.parse(a.createdAt) || b.createdAt.localeCompare(a.createdAt),
  )
}

function TaskCard({ projectId, task, statusId, modelConfigured, pending, onAction, showSourceLink, refreshVersion }: {
  projectId: string
  task: Task
  statusId: string
  modelConfigured: boolean
  pending: string | null
  onAction: (mutation: CardMutation) => Promise<MutationResult | undefined>
  showSourceLink: boolean
  refreshVersion: number
}) {
  const [edit, setEdit] = useState<InstructionDraft>()
  const [editError, setEditError] = useState<{ message: string; invalid?: boolean }>()
  const [inputConflict, setInputConflict] = useState<{ revision: number; message: string }>()
  const editField = useRef<HTMLTextAreaElement>(null)
  const proposed = task.status === 'PROPOSED'
  const isManuscriptCheck = task.kind === 'manuscript-check'
  const isSelectionEdit = task.kind === 'selection-edit'
  const isFocusedResearch = task.kind === 'focused-research'
  const canCancel = proposed || task.status === 'QUEUED' || task.status === 'RUNNING'
  const editing = edit !== undefined
  const saving = pending === `edit:${task.taskId}`
  const outdated = editing && edit.revision !== task.revision
  const currentInputs = task.inputs ?? []
  const selectionSource = isSelectionEdit ? currentInputs[0] : undefined
  const inputsChanged = editing && !isSelectionEdit && !sameVersionIds(versionIds(edit.inputs), versionIds(currentInputs))
  const changed = editing && (edit.text.trim() !== task.instruction || inputsChanged)
  const inputProblem = edit && !isSelectionEdit ? editInputError(edit.inputs, task.kind) : ''
  const editProblem = proposed && outdated
    ? '任务已有新修订。草稿已保留，请核对上方卡片再继续。'
    : editError?.message

  useEffect(() => {
    if (editing) editField.current?.focus()
  }, [editing])

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!edit || !changed || !proposed || outdated || inputProblem || pending !== null) return
    const instruction = edit.text.trim()
    const message = instructionError(instruction, isSelectionEdit ? selectionEditInstructionLimit : taskInstructionLimit)
    if (message) {
      setEditError({ message, invalid: true })
      editField.current?.focus()
      return
    }
    setEditError(undefined)
    const result = await onAction({
      kind: 'edit', task, instruction, expectedRevision: edit.revision,
      inputVersionIds: inputsChanged ? versionIds(edit.inputs) : undefined,
    })
    if (result?.ok) {
      setEdit(undefined)
      if (!isSelectionEdit) setInputConflict(undefined)
    } else if (result) setEditError({ message: result.message })
  }

  const confirmationConflict = proposed && inputConflict && (isSelectionEdit || inputConflict.revision === task.revision)
    ? inputConflict.message : undefined

  async function confirm() {
    if (editing || confirmationConflict) return
    const result = await onAction({ kind: 'confirm', task })
    if (result?.ok) setInputConflict(undefined)
    else if (result?.inputChanged) setInputConflict({ revision: task.revision, message: result.message })
  }

  return (
    <li className="task-card">
      <div className="task-card-header">
        <div className="task-status" id={statusId} role="status" tabIndex={-1} aria-describedby={`${statusId}-revision`}>
          <strong>{taskStatusLabels[task.status]}</strong>
          {task.message && <p className="task-note" dir="auto">{task.message}</p>}
        </div>
        <div className="task-card-meta task-note">
          <time dateTime={task.createdAt}>{taskDate.format(new Date(task.createdAt))}</time>
          <span id={`${statusId}-revision`}>修订 {task.revision}</span>
        </div>
      </div>
      {isManuscriptCheck && <div>
        <p className="turn-role">只读稿件检查</p>
        <p className="task-note">输出：稿件结构检查报告</p>
        <p className="task-note">在本机读取整份已保存稿件。稿件正文不会发送给研究模型。</p>
      </div>}
      {isFocusedResearch && <div>
        <p className="turn-role">聚焦研究</p>
        {proposed && <p className="task-note">确认运行后，每份所选 Markdown 版本开头最多 8 KiB 的正文节选会发送给研究模型。</p>}
      </div>}
      {edit && <p className="task-note">当前任务说明</p>}
      <p className="task-instruction" dir="auto">{task.instruction}</p>
      {showSourceLink && task.sourceTurnId && <p className="task-note">
        <a href={`#${encodeURIComponent(conversationTurnAnchor(task.sourceTurnId))}`}>回到原消息</a>
      </p>}
      {proposed && task.sourceTurnId && !isManuscriptCheck && !isSelectionEdit && <p className="task-note">{turnSourceNotice}</p>}
      {selectionSource ? <>
        <p className="turn-role">局部改稿</p>
        {proposed && <p className="task-note">确认运行后，模型会收到选区、相邻文字和修改要求。核对建议后可保存为新版本。</p>}
        <TaskSelectionEdit projectId={projectId} taskId={task.taskId} source={selectionSource}
          status={task.status} revision={task.revision} refreshVersion={refreshVersion} />
      </> : <TaskInputSummary projectId={projectId} inputs={task.inputs} />}
      {confirmationConflict && <p className="form-error" role="alert">{confirmationConflict}</p>}
      {edit && <form className="field" onSubmit={(event) => void save(event)} noValidate aria-busy={saving}>
        <label htmlFor={`${statusId}-edit`}>修改任务说明</label>
        <textarea id={`${statusId}-edit`} value={edit.text} rows={3} required ref={editField}
          readOnly={saving || !proposed} aria-invalid={editError?.invalid || undefined}
          aria-describedby={editProblem ? `${statusId}-edit-error` : undefined}
          onChange={(event) => {
            setEdit({ ...edit, text: event.currentTarget.value })
            setEditError(undefined)
          }} />
        {isSelectionEdit ? <p className="task-note">修改说明最多 2 KiB。更换范围时请从稿件重新引用选区。</p>
          : <TaskInputVersions projectId={projectId} inputs={edit.inputs} disabled={pending !== null || !proposed}
            markdown={isManuscriptCheck ? 'single' : isFocusedResearch ? 'multiple' : undefined} onChange={(inputs) => {
              setEdit({ ...edit, inputs })
              setEditError(undefined)
            }} />}
        {!proposed && <p className="task-note" role="status">任务已离开待确认状态。草稿仍可复制。</p>}
        {editProblem && <p className="form-error" role="alert" id={`${statusId}-edit-error`}>{editProblem}</p>}
        {inputProblem && <p className="form-error" role="alert">{inputProblem}</p>}
        <div className="task-actions">
          {proposed && outdated && <button className="button outline" type="button"
            disabled={pending !== null} onClick={() => {
              setEdit({
                text: edit.text.trim() === edit.baseInstruction ? task.instruction : edit.text,
                revision: task.revision,
                inputs: isSelectionEdit ? currentInputs : rebaseInputs(edit, currentInputs, isManuscriptCheck),
                baseInstruction: task.instruction,
                baseInputs: currentInputs,
              })
              setEditError(undefined)
              editField.current?.focus()
            }}>继续编辑最新修订</button>}
          {proposed && !outdated && <button className="button primary" type="submit"
            disabled={!changed || !!inputProblem} aria-disabled={pending !== null}>
            {saving ? '正在保存…' : '保存说明'}
          </button>}
          <button className="button outline" type="button" disabled={saving} onClick={() => {
            setEdit(undefined)
            setEditError(undefined)
            document.getElementById(statusId)?.focus()
          }}>{changed ? '放弃修改' : '退出编辑'}</button>
        </div>
      </form>}
      {canCancel && <div className="task-actions">
        {proposed && <>
          <button className="button primary" type="button"
            disabled={(!isManuscriptCheck && !modelConfigured) || editing || !!confirmationConflict}
            aria-disabled={pending !== null} onClick={() => void confirm()}>
            {pending === `confirm:${task.taskId}` ? '正在确认…' : '确认运行'}
          </button>
          {!editing && <button className="button outline" type="button" aria-disabled={pending !== null}
            onClick={() => {
              if (pending !== null) return
              setEdit({
                text: task.instruction,
                revision: task.revision,
                inputs: currentInputs,
                baseInstruction: task.instruction,
                baseInputs: currentInputs,
              })
            }}>编辑说明</button>}
        </>}
        <button className="button outline" type="button" aria-disabled={pending !== null}
          onClick={() => void onAction({ kind: 'cancel', task })}>
          {pending === `cancel:${task.taskId}` ? '正在提交…' : '取消任务'}
        </button>
      </div>}
      {task.status === 'SUCCEEDED' && !isSelectionEdit && <div className="task-actions">
        <a className="button outline" href={taskArtifactPath(projectId, task.taskId)} download>
          {isManuscriptCheck ? '下载检查报告' : isFocusedResearch ? '下载聚焦研究报告' : '下载 Markdown'}
        </a>
      </div>}
    </li>
  )
}

export function TaskPanel({ projectId, sourceTurnId, initialInstruction = '', proposedTaskId, sync }: {
  projectId: string
  sourceTurnId?: string
  initialInstruction?: string
  proposedTaskId?: string
  sync?: TaskPanelSync
}) {
  const panelId = useId()
  const [localTasks, setTasks] = useState<Task[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState<boolean>()
  const [loading, setLoading] = useState(true)
  const [olderPending, setOlderPending] = useState(false)
  const [queueError, setQueueError] = useState('')
  const [reload, setReload] = useState(0)
  const prefill = initialInstruction.trim()
  const prefillTooLong = instructionEncoder.encode(prefill).length > taskInstructionLimit
  const [draft, setDraft] = useState(() => instructionError(prefill) ? '' : prefill)
  const [draftInputs, setDraftInputs] = useState<TaskInputVersion[]>([])
  const [inputError, setInputError] = useState('')
  const [actionError, setActionError] = useState('')
  const [pending, setPending] = useState<string | null>(null)
  const instructionField = useRef<HTMLTextAreaElement>(null)
  const submission = useRef<AbortController | null>(null)
  const olderRequest = useRef<AbortController | null>(null)
  const proposalAttempt = useRef<ProposalInput | null>(null)
  const changeAttempts = useRef(new Map<string, ChangeAttempt>())
  const focusAfterAction = useRef<{ taskId: string; control: Element | null } | null>(null)
  const sharedUpdates = sync?.updates
  const tasks = useMemo(() => {
    const updates = sourceTurnId ? sharedUpdates?.filter((task) => task.sourceTurnId === sourceTurnId) : sharedUpdates
    const merged = updates?.length ? mergeTasks(localTasks, updates) : localTasks
    const proposedTask = proposedTaskId && merged.find((task) => task.taskId === proposedTaskId)
    return proposedTask ? [proposedTask, ...merged.filter((task) => task.taskId !== proposedTaskId)] : merged
  }, [localTasks, sharedUpdates, sourceTurnId, proposedTaskId])
  const hasActiveTasks = tasks.some((task) => isTaskActive(task.status))

  const onTaskChanged = sync?.onTaskChanged
  const acceptPage = useCallback((page: TaskPage) => {
    setTasks((current) => mergeTasks(current, page.items))
    setConfigured(page.modelConfigured)
    setQueueError('')
    const proposedTask = proposedTaskId && page.items.find((task) => task.taskId === proposedTaskId)
    if (proposedTask) onTaskChanged?.(proposedTask)
  }, [onTaskChanged, proposedTaskId])

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
    const request = proposedTaskId ? refreshTasks(projectId, [proposedTaskId], signal, sourceTurnId)
      : listTasks(projectId, signal, undefined, sourceTurnId)
    void request.then((page) => {
      if (controller.signal.aborted) return
      acceptPage(page)
      setCursor(page.nextCursor)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setQueueError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [acceptPage, projectId, proposedTaskId, reload, sourceTurnId])

  const poll = useEffectEvent(async (signal: AbortSignal) => {
    const activeIds = tasks.filter((task) => isTaskActive(task.status)).map((task) => task.taskId)
    if (activeIds.length === 0) return
    const page = await refreshTasks(projectId, activeIds, signal, sourceTurnId)
    if (!signal.aborted) acceptPage(page)
  })

  useEffect(() => {
    if (!hasActiveTasks) return
    const controller = new AbortController()
    let timer = window.setTimeout(refresh, refreshInterval)
    async function refresh() {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
      try {
        await poll(signal)
      } catch (error) {
        if (!controller.signal.aborted) setQueueError(errorMessage(error))
      } finally {
        if (!controller.signal.aborted) timer = window.setTimeout(refresh, refreshInterval)
      }
    }
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [hasActiveTasks, projectId, sourceTurnId])

  useEffect(() => () => {
    submission.current?.abort()
    olderRequest.current?.abort()
  }, [])

  useEffect(() => {
    const focus = focusAfterAction.current
    focusAfterAction.current = null
    if (focus && (document.activeElement === focus.control || document.activeElement === document.body)) {
      document.getElementById(`${panelId}-${focus.taskId}`)?.focus()
    }
  }, [panelId, tasks])

  async function loadOlder() {
    if (!cursor || olderRequest.current) return
    const controller = new AbortController()
    olderRequest.current = controller
    setOlderPending(true)
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
    try {
      const page = await listTasks(projectId, signal, cursor, sourceTurnId)
      if (!controller.signal.aborted) {
        acceptPage(page)
        setCursor(page.nextCursor)
      }
    } catch (error) {
      if (!controller.signal.aborted) setQueueError(errorMessage(error))
    } finally {
      olderRequest.current = null
      if (!controller.signal.aborted) setOlderPending(false)
    }
  }

  async function mutate(mutation: Mutation): Promise<MutationResult | undefined> {
    const localCheck = mutation.kind !== 'propose' && mutation.task.kind === 'manuscript-check'
    if (submission.current) return
    if (!localCheck && (configured === undefined || (mutation.kind === 'confirm' && !configured))) return
    const key = mutation.kind === 'propose' ? 'propose' : `${mutation.kind}:${mutation.task.taskId}`
    const controller = new AbortController()
    const focusControl = document.activeElement
    submission.current = controller
    setActionError('')
    setPending(key)
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
      let task: Task
      if (mutation.kind === 'propose') {
        const { instruction, inputVersionIds } = mutation
        const previous = proposalAttempt.current
        const attempt = previous?.instruction === instruction && previous.sourceTurnId === sourceTurnId
          && sameVersionIds(previous.inputVersionIds, inputVersionIds)
          ? previous : { requestId: crypto.randomUUID(), instruction, inputVersionIds, sourceTurnId }
        proposalAttempt.current = attempt
        task = await proposeTask(projectId, attempt, signal)
        if (controller.signal.aborted) return
        proposalAttempt.current = null
        setDraft('')
        setDraftInputs([])
      } else {
        const previous = changeAttempts.current.get(key)
        const instruction = mutation.kind === 'edit' ? mutation.instruction : undefined
        const inputVersionIds = mutation.kind === 'edit' ? mutation.inputVersionIds : undefined
        const expectedRevision = mutation.kind === 'edit' ? mutation.expectedRevision : mutation.task.revision
        // Match the original payload, including the distinction between omitted and cleared inputs.
        const attempt = previous?.expectedRevision === expectedRevision && previous.instruction === instruction
          && sameVersionIds(previous.inputVersionIds, inputVersionIds)
          ? previous
          : { requestId: crypto.randomUUID(), expectedRevision, instruction, inputVersionIds }
        changeAttempts.current.set(key, attempt)
        task = mutation.kind === 'edit'
          ? await editTask(projectId, mutation.task.taskId, { ...attempt, instruction: mutation.instruction }, signal)
          : await changeTask(projectId, mutation.task.taskId, mutation.kind, attempt, signal)
        if (controller.signal.aborted) return
        changeAttempts.current.delete(key)
      }
      focusAfterAction.current = { taskId: task.taskId, control: focusControl }
      setTasks((current) => mergeTasks(current, [task]))
      sync?.onTaskChanged(task)
      return { ok: true } as const
    } catch (error) {
      if (controller.signal.aborted) return
      let message = errorMessage(error)
      const code = error instanceof ProjectApiError ? error.code : undefined
      const editing = mutation.kind === 'edit'
      const inputChanged = code === 'TASK_INPUT_CHANGED'
      if (inputChanged) {
        if (mutation.kind !== 'propose' && mutation.task.kind === 'selection-edit') {
          message = '稿件已有新版本。请取消此任务，在最新稿件中重新选取文字并发送。'
        } else {
          message = mutation.kind === 'confirm'
            ? '任务所用文件已有新版本。请通过「编辑说明」刷新文件列表并替换旧版本，然后重新确认。'
            : '所选文件已有新版本。草稿已保留，请刷新文件列表并替换旧版本后再提交。'
        }
      }
      const refreshConflict = inputChanged || code === 'REVISION_CONFLICT'
        || (editing && (code === 'TASK_STATE_CONFLICT' || code === 'REQUEST_ID_CONFLICT'))
      if (code === 'MODEL_UNAVAILABLE') setConfigured(false)
      if (!editing && !(mutation.kind === 'confirm' && inputChanged)) setActionError(message)
      if (mutation.kind !== 'propose' && refreshConflict) {
        if (!inputChanged) changeAttempts.current.delete(key)
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
        try {
          const page = await refreshTasks(projectId, [mutation.task.taskId], signal, sourceTurnId)
          if (controller.signal.aborted) return
          acceptPage(page)
          if (editing && !inputChanged) message = `${message} 草稿已保留，请核对上方任务卡。`
        } catch (refreshError) {
          if (controller.signal.aborted) return
          if (editing && !inputChanged) message = `${message} 草稿已保留。暂时无法读取最新卡片，可再次保存以重试。`
          setQueueError(errorMessage(refreshError))
        }
      }
      return { ok: false, message, inputChanged } as const
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setPending(null)
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submission.current) return
    const instruction = draft.trim()
    const problem = instructionError(instruction)
    setInputError(problem)
    if (problem) {
      instructionField.current?.focus()
      return
    }
    void mutate({ kind: 'propose', instruction, inputVersionIds: draftInputs.length ? versionIds(draftInputs) : undefined })
  }

  const proposalForm = (
    <form className="create-form" onSubmit={submit} noValidate aria-busy={pending === 'propose'}>
      {sourceTurnId && <p className="task-note">{turnSourceNotice}</p>}
      <div className="field">
        <label htmlFor={`${panelId}-instruction`}>任务说明</label>
        <textarea id={`${panelId}-instruction`} name="instruction" rows={3} required ref={instructionField}
          value={draft} readOnly={pending === 'propose'} aria-invalid={inputError ? true : undefined}
          aria-describedby={`${panelId}-hint${inputError ? ` ${panelId}-error` : ''}`}
          onChange={(event) => {
            setDraft(event.currentTarget.value)
            setInputError('')
          }} />
        <p id={`${panelId}-hint`} className="task-note">
          {sourceTurnId && prefillTooLong ? '原话超过 8 KiB，请填写简短的任务说明。'
            : '说明研究目标，以及计划需要回答的问题。'}
        </p>
        {inputError && <p className="form-error" role="alert" id={`${panelId}-error`}>{inputError}</p>}
      </div>
      <TaskInputVersions projectId={projectId} inputs={draftInputs} disabled={pending !== null} onChange={setDraftInputs} />
      <div className="task-actions">
        <button className="button primary" type="submit" disabled={configured === undefined} aria-disabled={pending !== null}>
          {pending === 'propose' ? '正在提交…' : '提交提议'}
        </button>
      </div>
    </form>
  )

  return (
    <section className="task-panel" aria-labelledby={`${panelId}-heading`}>
      <h4 id={`${panelId}-heading`}>{sourceTurnId ? '此对话的任务' : '研究计划'}</h4>
      {configured === false && <p className="task-note" role="status">研究模型未配置。研究计划需要配置模型后运行，只读稿件检查可在本机运行。</p>}
      {!proposedTaskId && proposalForm}
      {actionError && <p className="form-error" role="alert">{actionError}</p>}
      {loading && <p className="task-note" role="status">正在读取任务…</p>}
      {queueError && <div className="request-error">
        <p role="alert">{queueError}</p>
        <button className="button outline" type="button" disabled={loading || olderPending} onClick={() => {
          setQueueError('')
          setLoading(true)
          setReload((value) => value + 1)
        }}>刷新任务</button>
      </div>}
      {tasks.length > 0 && <ol className="task-list" aria-label="研究计划任务">
        {tasks.map((task) => <TaskCard key={task.taskId} task={task} projectId={projectId}
          statusId={`${panelId}-${task.taskId}`} modelConfigured={configured === true} pending={pending}
          onAction={mutate} showSourceLink={!sourceTurnId} refreshVersion={reload} />)}
      </ol>}
      {!loading && !queueError && tasks.length === 0 && <p className="task-note">提交提议后，可在此查看任务进度并下载计划。</p>}
      {proposedTaskId && <details>
        <summary className="turn-role">从此对话提议任务</summary>
        {proposalForm}
      </details>}
      {cursor && <div className="task-actions">
        <button className="button outline" type="button" disabled={loading || olderPending} onClick={() => void loadOlder()}>
          {olderPending ? '正在读取…' : '更早的任务'}
        </button>
      </div>}
    </section>
  )
}
