import { useCallback, useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import {
  changeTask, isTaskActive, listTasks, proposeTask, refreshTasks,
  taskArtifactPath, taskInstructionLimit, taskStatusLabels,
} from './tasks'
import type { Task, TaskAction, TaskPage } from './tasks'
import './tasks.css'

const refreshInterval = 2_000
const requestTimeout = 10_000
const taskDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })

type Mutation = { kind: 'propose'; instruction: string } | { kind: TaskAction; task: Task }
type ChangeAttempt = { requestId: string; expectedRevision: number }

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

function TaskCard({ projectId, task, statusId, modelConfigured, pending, onAction }: {
  projectId: string
  task: Task
  statusId: string
  modelConfigured: boolean
  pending: string | null
  onAction: (kind: TaskAction, task: Task) => void
}) {
  const proposed = task.status === 'PROPOSED'
  const canCancel = proposed || task.status === 'QUEUED' || task.status === 'RUNNING'
  return (
    <li className="task-card">
      <div className="task-card-header">
        <div className="task-status" id={statusId} role="status" tabIndex={-1}>
          <strong>{taskStatusLabels[task.status]}</strong>
          {task.message && <p className="task-note" dir="auto">{task.message}</p>}
        </div>
        <time className="task-note" dateTime={task.createdAt}>{taskDate.format(new Date(task.createdAt))}</time>
      </div>
      <p className="task-instruction" dir="auto">{task.instruction}</p>
      {canCancel && <div className="task-actions">
        {proposed && <button className="button primary" type="button"
          disabled={!modelConfigured} aria-disabled={pending !== null}
          onClick={() => onAction('confirm', task)}>
          {pending === `confirm:${task.taskId}` ? '正在确认…' : '确认运行'}
        </button>}
        <button className="button outline" type="button" aria-disabled={pending !== null}
          onClick={() => onAction('cancel', task)}>
          {pending === `cancel:${task.taskId}` ? '正在提交…' : '取消任务'}
        </button>
      </div>}
      {task.status === 'SUCCEEDED' && <div className="task-actions">
        <a className="button outline" href={taskArtifactPath(projectId, task.taskId)} download>下载 Markdown</a>
      </div>}
    </li>
  )
}

export function TaskPanel({ projectId }: { projectId: string }) {
  const panelId = useId()
  const [tasks, setTasks] = useState<Task[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState<boolean>()
  const [loading, setLoading] = useState(true)
  const [olderPending, setOlderPending] = useState(false)
  const [queueError, setQueueError] = useState('')
  const [reload, setReload] = useState(0)
  const [draft, setDraft] = useState('')
  const [inputError, setInputError] = useState('')
  const [actionError, setActionError] = useState('')
  const [pending, setPending] = useState<string | null>(null)
  const instructionField = useRef<HTMLTextAreaElement>(null)
  const submission = useRef<AbortController | null>(null)
  const olderRequest = useRef<AbortController | null>(null)
  const proposalAttempt = useRef<{ requestId: string; instruction: string } | null>(null)
  const changeAttempts = useRef(new Map<string, ChangeAttempt>())
  const focusAfterAction = useRef<{ taskId: string; control: Element | null } | null>(null)
  const hasActiveTasks = tasks.some((task) => isTaskActive(task.status))

  const acceptPage = useCallback((page: TaskPage) => {
    setTasks((current) => mergeTasks(current, page.items))
    setConfigured(page.modelConfigured)
    setQueueError('')
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
    void listTasks(projectId, signal).then((page) => {
      if (controller.signal.aborted) return
      acceptPage(page)
      setCursor(page.nextCursor)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setQueueError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [acceptPage, projectId, reload])

  const poll = useEffectEvent(async (signal: AbortSignal) => {
    const activeIds = tasks.filter((task) => isTaskActive(task.status)).map((task) => task.taskId)
    if (activeIds.length === 0) return
    const page = await refreshTasks(projectId, activeIds, signal)
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
  }, [hasActiveTasks, projectId])

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
      const page = await listTasks(projectId, signal, cursor)
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

  async function mutate(mutation: Mutation) {
    if (submission.current || configured === undefined || (mutation.kind === 'confirm' && !configured)) return
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
        const { instruction } = mutation
        const attempt = proposalAttempt.current?.instruction === instruction
          ? proposalAttempt.current : { requestId: crypto.randomUUID(), instruction }
        proposalAttempt.current = attempt
        task = await proposeTask(projectId, attempt, signal)
        if (controller.signal.aborted) return
        proposalAttempt.current = null
        setDraft('')
      } else {
        // Reuse the revision as well as the ID until this attempt gets a definitive response.
        const attempt = changeAttempts.current.get(key)
          ?? { requestId: crypto.randomUUID(), expectedRevision: mutation.task.revision }
        changeAttempts.current.set(key, attempt)
        task = await changeTask(projectId, mutation.task.taskId, mutation.kind, attempt, signal)
        if (controller.signal.aborted) return
        changeAttempts.current.delete(key)
      }
      focusAfterAction.current = { taskId: task.taskId, control: focusControl }
      setTasks((current) => mergeTasks(current, [task]))
    } catch (error) {
      if (controller.signal.aborted) return
      setActionError(errorMessage(error))
      if (error instanceof ProjectApiError && error.code === 'MODEL_UNAVAILABLE') setConfigured(false)
      if (mutation.kind !== 'propose' && error instanceof ProjectApiError && error.code === 'REVISION_CONFLICT') {
        changeAttempts.current.delete(key)
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
        try {
          const page = await refreshTasks(projectId, [mutation.task.taskId], signal)
          if (!controller.signal.aborted) acceptPage(page)
        } catch (refreshError) {
          if (!controller.signal.aborted) setQueueError(errorMessage(refreshError))
        }
      }
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setPending(null)
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submission.current) return
    const instruction = draft.trim()
    let problem = ''
    if (!instruction) problem = '请输入任务说明。'
    else if (instruction.includes('\0')) problem = '说明中不能包含空字符。'
    else if (new TextEncoder().encode(instruction).length > taskInstructionLimit) problem = '任务说明不能超过 8 KiB。'
    setInputError(problem)
    if (problem) {
      instructionField.current?.focus()
      return
    }
    void mutate({ kind: 'propose', instruction })
  }

  return (
    <section className="task-panel" aria-labelledby={`${panelId}-heading`}>
      <h4 id={`${panelId}-heading`}>研究计划</h4>
      {configured === false && <p className="task-note" role="status">研究模型未配置。可以提交任务提议，配置模型后即可运行。</p>}
      <form className="create-form" onSubmit={submit} noValidate aria-busy={pending === 'propose'}>
        <div className="field">
          <label htmlFor={`${panelId}-instruction`}>任务说明</label>
          <textarea id={`${panelId}-instruction`} name="instruction" rows={3} required ref={instructionField}
            value={draft} readOnly={pending === 'propose'} aria-invalid={inputError ? true : undefined}
            aria-describedby={`${panelId}-hint${inputError ? ` ${panelId}-error` : ''}`}
            onChange={(event) => {
              setDraft(event.currentTarget.value)
              setInputError('')
            }} />
          <p id={`${panelId}-hint`} className="task-note">说明研究目标，以及计划需要回答的问题。</p>
          {inputError && <p className="form-error" role="alert" id={`${panelId}-error`}>{inputError}</p>}
        </div>
        <div className="task-actions">
          <button className="button primary" type="submit" disabled={configured === undefined} aria-disabled={pending !== null}>
            {pending === 'propose' ? '正在提交…' : '提交提议'}
          </button>
        </div>
      </form>
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
          onAction={(kind, item) => void mutate({ kind, task: item })} />)}
      </ol>}
      {!loading && !queueError && tasks.length === 0 && <p className="task-note">提交提议后，可在此查看任务进度并下载计划。</p>}
      {cursor && <div className="task-actions">
        <button className="button outline" type="button" disabled={loading || olderPending} onClick={() => void loadOlder()}>
          {olderPending ? '正在读取…' : '更早的任务'}
        </button>
      </div>}
    </section>
  )
}
