import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import {
  continueWorkflow, getWorkflow, isWorkflowActive, runStatusLabels, startWorkflow, stepStatusLabels,
  workflowArtifactPath,
} from './workflow'
import type { ContinueExperimentInput, ExperimentRun, StartExperimentInput, WorkflowSnapshot } from './workflow'
import './workflow.css'

const pollInterval = 1_000
const requestTimeout = 10_000

type Attempt =
  | { kind: 'start'; input: StartExperimentInput }
  | { kind: 'continue'; input: ContinueExperimentInput }

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
}

function WorkflowSteps({ projectId, run }: { projectId: string; run: ExperimentRun }) {
  return (
    <ol className="workflow-steps" aria-label="实验步骤">
      {run.steps.map((step) => (
        <li key={step.key}>
          <div className="workflow-step-heading">
            <strong dir="auto">{step.title}</strong>
            <span className="workflow-step-status" data-status={step.status}>{stepStatusLabels[step.status]}</span>
          </div>
          {step.message && <p className="workflow-note workflow-step-message" dir="auto">{step.message}</p>}
          {step.status === 'SUCCEEDED' && (
            <a className="workflow-download" href={workflowArtifactPath(projectId, run.runId, step.key)}
              aria-label={`下载 Markdown：${step.title}`} download>下载 Markdown</a>
          )}
        </li>
      ))}
    </ol>
  )
}

export function WorkflowPanel({ projectId }: { projectId: string }) {
  const headingId = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  const submission = useRef<AbortController | null>(null)
  const [snapshot, setSnapshot] = useState<WorkflowSnapshot | null>(null)
  const [checkpoints, setCheckpoints] = useState(false)
  const [refreshing, setRefreshing] = useState(true)
  const [reload, setReload] = useState(0)
  const [readError, setReadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [feedback, setFeedback] = useState('')
  const [retry, setRetry] = useState<Attempt | null>(null)
  const [pending, setPending] = useState(false)
  const run = snapshot?.run ?? null
  const canStart = snapshot !== null && (run === null || run.status === 'COMPLETED')
  const waiting = run?.status === 'WAITING_CHECKPOINT'
  const canContinue = waiting || run?.status === 'FAILED' || run?.status === 'INTERRUPTED'
  const retryStart = retry?.kind === 'start' ? retry : null
  const retryContinue = retry?.kind === 'continue' && retry.input.runId === run?.runId ? retry : null
  const modelUnavailable = snapshot?.modelConfigured === false
  const actionDisabled = refreshing || readError !== '' || modelUnavailable || pending

  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    let active = false

    async function read() {
      try {
        const next = await getWorkflow(projectId, requestSignal(controller))
        if (controller.signal.aborted) return
        active = isWorkflowActive(next.run)
        setSnapshot(next)
        setReadError('')
      } catch (error) {
        if (controller.signal.aborted) return
        if (error instanceof ProjectApiError && error.status > 0 && error.status < 500) active = false
        setReadError(errorMessage(error))
      } finally {
        if (!controller.signal.aborted) {
          setRefreshing(false)
          if (active) timer = window.setTimeout(read, pollInterval)
        }
      }
    }

    void read()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [projectId, reload])

  useEffect(() => () => submission.current?.abort(), [])

  function refresh() {
    setRefreshing(true)
    setReload((value) => value + 1)
  }

  async function submit(kind: Attempt['kind']) {
    if (submission.current || actionDisabled || (kind === 'start' ? !canStart : !canContinue || !run)) return
    const controller = new AbortController()
    const control = document.activeElement
    submission.current = controller
    setPending(true)
    setActionError('')
    setFeedback('')
    let attempt: Attempt | null = kind === 'start' ? retryStart : retryContinue
    try {
      if (!attempt) {
        const requestId = crypto.randomUUID()
        if (kind === 'start') attempt = { kind, input: { requestId, checkpoints } }
        else if (run) attempt = { kind, input: { requestId, runId: run.runId, expectedRevision: run.revision } }
        else return
      }
      const signal = requestSignal(controller)
      const updated = attempt.kind === 'start' ? await startWorkflow(projectId, attempt.input, signal)
        : await continueWorkflow(projectId, attempt.input, signal)
      if (controller.signal.aborted) return
      setRetry(null)
      if (attempt.kind === 'start') setCheckpoints(false)
      setSnapshot((current) => current && { ...current, run: updated })
      if (control instanceof HTMLButtonElement && document.activeElement === control) heading.current?.focus()
      // A replay may return an earlier revision. Read the latest state before accepting another action.
      refresh()
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof ProjectApiError && (error.status === 409 || error.code === 'REVISION_CONFLICT')) {
        setRetry(null)
        setFeedback('工作流状态已更新，请查看最新进度。')
        refresh()
      } else {
        setActionError(errorMessage(error))
        const modelRejected = error instanceof ProjectApiError && error.code === 'MODEL_UNAVAILABLE'
        if (modelRejected) setSnapshot((current) => current && { ...current, modelConfigured: false })
        // Retain the full payload if the server may have applied it before the response failed.
        const rejected = modelRejected || (error instanceof ProjectApiError && error.status >= 400 && error.status < 500
          && error.status !== 408 && error.status !== 429)
        setRetry(rejected ? null : attempt)
      }
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setPending(false)
    }
  }

  return (
    <section className="workflow-panel" aria-labelledby={headingId}>
      <div className="workflow-heading">
        <h4 id={headingId} ref={heading} tabIndex={-1}>实验工作流</h4>
        <p className="workflow-run-status" role="status" data-status={run?.status}>
          {run ? runStatusLabels[run.status] : snapshot && '尚未启动'}
        </p>
      </div>
      {refreshing && !snapshot && (
        <div className="loading-state" role="status">
          <p>正在读取工作流…</p>
          <div className="loading-lines" aria-hidden="true"><span /><span /></div>
        </div>
      )}
      {run && <WorkflowSteps projectId={projectId} run={run} />}
      {waiting && <p className="workflow-note">确认实验计划后生成验收清单。</p>}
      {readError && <p className="form-error" role="alert">{readError}</p>}
      {actionError && <p className="form-error" role="alert">{actionError}</p>}
      {feedback && <p className="workflow-note" role="status">{feedback}</p>}
      {modelUnavailable && <p className="workflow-note">请配置研究模型，然后刷新工作流状态。</p>}
      {(canStart || canContinue || readError || modelUnavailable) && (
        <div className="workflow-controls">
          {canStart && (
            <label className="workflow-checkpoint">
              <input type="checkbox" checked={checkpoints} disabled={actionDisabled || retryStart !== null}
                onChange={(event) => setCheckpoints(event.currentTarget.checked)} />
              <span>生成实验计划后，等待我确认再继续</span>
            </label>
          )}
          <div className="workflow-actions">
            {canStart && (
              <button className="button primary" type="button" disabled={actionDisabled && !pending}
                aria-disabled={pending} onClick={() => void submit('start')}>
                {pending ? '正在启动…' : retryStart ? '重试启动' : run ? '开始新一轮' : '启动工作流'}
              </button>
            )}
            {canContinue && (
              <button className="button primary" type="button" disabled={actionDisabled && !pending}
                aria-disabled={pending} onClick={() => void submit('continue')}>
                {pending ? '正在提交…' : retryContinue ? '重试继续' : waiting ? '确认并继续' : '继续未完成的步骤'}
              </button>
            )}
            {(readError || modelUnavailable) && (
              <button className="button outline" type="button" disabled={refreshing || pending} onClick={refresh}>
                {refreshing ? '正在刷新…' : '刷新工作流状态'}
              </button>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
