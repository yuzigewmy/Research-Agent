import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import {
  continueWorkflow, getWorkflow, isWorkflowActive, pauseWorkflow, runStatusLabels, saveManuscript, startWorkflow,
  stepStatusLabels, workflowArtifactPath,
} from './workflow'
import type { ContinueWorkflowInput, PauseWorkflowInput, WorkflowRun, StartWorkflowInput, WorkflowSnapshot } from './workflow'
import './workflow.css'

const pollInterval = 1_000
const requestTimeout = 10_000

type Attempt =
  | { kind: 'start'; input: StartWorkflowInput }
  | { kind: 'continue'; input: ContinueWorkflowInput }
  | { kind: 'pause'; input: PauseWorkflowInput }

function hasNewerRevision(current: WorkflowRun | null | undefined, incoming: WorkflowRun | null) {
  return !!current && !!incoming && current.runId === incoming.runId && current.revision > incoming.revision
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
}

function WorkflowSteps({ projectId, run }: { projectId: string; run: WorkflowRun }) {
  return (
    <ol className="workflow-steps" aria-label="工作流步骤">
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

function ManuscriptHandoff({ projectId, runId, onSaved }: {
  projectId: string
  runId: string
  onSaved: (path: string) => void
}) {
  const active = useRef<AbortController | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => () => active.current?.abort(), [])

  async function save() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    setSaving(true)
    setError('')
    try {
      const file = await saveManuscript(projectId, runId, requestSignal(controller))
      if (controller.signal.aborted) return
      onSaved(file.path)
      setSaved(true)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setSaving(false)
    }
  }

  return <div className="workflow-actions">
    {saved ? <a className="workflow-download" href="#project-workspace">前往文本工作区</a> :
      <button className="button outline" type="button" disabled={saving} onClick={() => void save()}>
        {saving ? '正在保存稿件…' : '保存稿件到工作区'}
      </button>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </div>
}

export function WorkflowPanel({ projectId, templateId, onManuscriptSaved }: {
  projectId: string
  templateId: 'experiment' | 'literature_review' | 'paper'
  onManuscriptSaved: (path: string) => void
}) {
  const isReview = templateId === 'literature_review'
  const isPaper = templateId === 'paper'
  const title = isPaper ? '论文写作工作流' : isReview ? '文献综述工作流' : '实验工作流'
  const checkpointLabel = isPaper ? '生成大纲后，等待我确认再继续'
    : isReview ? '整理主题后，等待我确认再继续' : '生成实验计划后，等待我确认再继续'
  const headingId = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  const submission = useRef<AbortController | null>(null)
  const [snapshot, setSnapshot] = useState<WorkflowSnapshot | null>(null)
  const [checkpoints, setCheckpoints] = useState(false)
  const [targetPaperCount, setTargetPaperCount] = useState('20')
  const [paperLanguage, setPaperLanguage] = useState<'zh' | 'en'>('zh')
  const [paperOutline, setPaperOutline] = useState('')
  const [throughStep, setThroughStep] = useState('')
  const [refreshing, setRefreshing] = useState(true)
  const [reload, setReload] = useState(0)
  const [readError, setReadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [feedback, setFeedback] = useState('')
  const [retry, setRetry] = useState<Attempt | null>(null)
  const [pending, setPending] = useState<Attempt['kind'] | null>(null)
  const run = snapshot?.run ?? null
  const canStart = snapshot !== null && (run === null || run.status === 'COMPLETED')
  const waiting = run?.status === 'WAITING_CHECKPOINT'
  const rangeCompleted = run?.status === 'RANGE_COMPLETED'
  const pausing = run?.status === 'PAUSING'
  const paused = run?.status === 'PAUSED'
  const canContinue = rangeCompleted || waiting || paused || run?.status === 'FAILED' || run?.status === 'INTERRUPTED'
  const canPause = run?.status === 'QUEUED' || run?.status === 'RUNNING'
  const steps = snapshot?.steps ?? []
  const nextStep = rangeCompleted ? run.steps.find((step) => step.status !== 'SUCCEEDED') : undefined
  const rangeStartIndex = canStart ? 0 : steps.findIndex((step) => step.key === nextStep?.key)
  const rangeSteps = rangeStartIndex >= 0 ? steps.slice(rangeStartIndex) : []
  const firstStep = rangeSteps[0]
  const lastStep = rangeSteps.at(-1)
  const endStep = rangeSteps.some((step) => step.key === throughStep) ? throughStep : ''
  const retryStart = retry?.kind === 'start' && (retry.input.throughStep ?? '') === endStep ? retry : null
  const retryContinue = retry?.kind === 'continue' && retry.input.runId === run?.runId
    && retry.input.expectedRevision === run.revision && (retry.input.throughStep ?? '') === endStep ? retry : null
  const retryPause = retry?.kind === 'pause' && retry.input.runId === run?.runId
    && retry.input.expectedRevision === run.revision ? retry : null
  const scopeStart = run?.steps.find((step) => step.key === run.scope.startStep)
  const scopeEnd = run?.steps.find((step) => step.key === run.scope.endStep)
  const modelUnavailable = snapshot?.modelConfigured === false
  const requestBlocked = refreshing || readError !== '' || pending !== null
  const actionDisabled = requestBlocked || modelUnavailable || ((canStart || rangeCompleted) && !firstStep)
  const target = Number(targetPaperCount)
  const validTarget = !isReview || (Number.isInteger(target) && target >= 10 && target <= 80)
  const currentRun = useEffectEvent(() => run)

  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    let active = isWorkflowActive(currentRun())

    async function read() {
      try {
        const next = await getWorkflow(projectId, requestSignal(controller))
        if (controller.signal.aborted) return
        const latest = currentRun()
        active = isWorkflowActive(hasNewerRevision(latest, next.run) ? latest : next.run)
        setSnapshot((current) => current && hasNewerRevision(current.run, next.run) ? { ...next, run: current.run } : next)
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
    if (submission.current || requestBlocked) return
    if (kind === 'pause') {
      if (!canPause) return
    } else if (actionDisabled || (kind === 'start' ? !canStart || !validTarget : !canContinue)) return
    const outline = paperOutline
    if (kind === 'start' && isPaper && new TextEncoder().encode(outline).length > 16_384) {
      setActionError('大纲超过 16 KiB，请缩短后提交。')
      return
    }
    const controller = new AbortController()
    const control = document.activeElement
    submission.current = controller
    setPending(kind)
    setActionError('')
    setFeedback('')
    let attempt: Attempt | null = kind === 'start' ? retryStart : kind === 'pause' ? retryPause : retryContinue
    try {
      if (!attempt) {
        const requestId = crypto.randomUUID()
        if (kind === 'start') {
          attempt = { kind, input: {
            requestId, checkpoints, targetPaperCount: isReview ? target : undefined,
            paper: isPaper ? { language: paperLanguage, outline } : undefined,
            throughStep: endStep || undefined,
          } }
        } else {
          if (!run) return
          const input = { requestId, runId: run.runId, expectedRevision: run.revision }
          attempt = kind === 'pause' ? { kind, input }
            : { kind, input: { ...input, throughStep: rangeCompleted && endStep ? endStep : undefined } }
        }
      }
      const signal = requestSignal(controller)
      let updated: WorkflowRun
      switch (attempt.kind) {
        case 'start': updated = await startWorkflow(projectId, attempt.input, signal); break
        case 'continue': updated = await continueWorkflow(projectId, attempt.input, signal); break
        case 'pause': updated = await pauseWorkflow(projectId, attempt.input, signal); break
      }
      if (controller.signal.aborted) return
      setRetry(null)
      setThroughStep('')
      if (attempt.kind === 'start') setCheckpoints(false)
      setSnapshot((current) => current && !hasNewerRevision(current.run, updated) ? { ...current, run: updated } : current)
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
      if (!controller.signal.aborted) setPending(null)
    }
  }

  return (
    <section className="workflow-panel" aria-labelledby={headingId}>
      <div className="workflow-heading">
        <h4 id={headingId} ref={heading} tabIndex={-1}>{title}</h4>
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
      {scopeStart && scopeEnd && <p className="workflow-note">
        {scopeStart.key === scopeEnd.key ? `本次范围为「${scopeStart.title}」。` : `本次范围从「${scopeStart.title}」到「${scopeEnd.title}」。`}
        {canContinue && !rangeCompleted && '继续时沿用此范围。'}
      </p>}
      {isReview && run?.sourceCount !== undefined && <p className="workflow-note">本轮已固定 {run.sourceCount} 条来源，纳入上限为 {run.targetPaperCount} 条。</p>}
      {run?.language && <p className="workflow-note">本轮使用{run.language === 'zh' ? '中文' : '英文'}写作，纳入 {run.sourceCount ?? 0} 条已核验来源。</p>}
      {run && <WorkflowSteps projectId={projectId} run={run} />}
      {run?.steps.some((step) => step.key === 'manuscript' && step.status === 'SUCCEEDED') &&
        <ManuscriptHandoff key={run.runId} projectId={projectId} runId={run.runId} onSaved={onManuscriptSaved} />}
      {waiting && <p className="workflow-note">{isPaper ? '确认论文大纲后生成正文。'
        : isReview ? '确认主题规划后撰写综述。' : '确认实验计划后生成验收清单。'}</p>}
      {pausing && <p className="workflow-note">当前步骤停止后即可继续运行。</p>}
      {paused && <p className="workflow-note">继续时从第一个未完成的步骤开始。已完成的成果仍可下载。</p>}
      {readError && <p className="form-error" role="alert">{readError}</p>}
      {actionError && <p className="form-error" role="alert">{actionError}</p>}
      {feedback && <p className="workflow-note" role="status">{feedback}</p>}
      {modelUnavailable && <p className="workflow-note">请配置研究模型，然后刷新工作流状态。</p>}
      {(canStart || canContinue || canPause || readError || modelUnavailable) && (
        <div className="workflow-controls">
          {canStart && isReview && <div className="field">
            <label htmlFor={`${headingId}-papers`}>纳入文献数量上限</label>
            <input id={`${headingId}-papers`} type="number" min={10} max={80} step={1} value={targetPaperCount}
              disabled={actionDisabled || retryStart !== null} aria-describedby={`${headingId}-sources`}
              onChange={(event) => setTargetPaperCount(event.currentTarget.value)} />
            <p className="workflow-note" id={`${headingId}-sources`}>从 10 至 80 中选择上限。当前文献库按核验时间从近到远纳入。</p>
          </div>}
          {canStart && isPaper && <>
            <div className="field">
              <label htmlFor={`${headingId}-language`}>写作语言</label>
              <select id={`${headingId}-language`} value={paperLanguage} disabled={actionDisabled || retryStart !== null}
                onChange={(event) => setPaperLanguage(event.currentTarget.value === 'en' ? 'en' : 'zh')}>
                <option value="zh">中文</option>
                <option value="en">英文</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor={`${headingId}-outline`}>已有大纲（可选）</label>
              <textarea id={`${headingId}-outline`} rows={4} maxLength={16_384} value={paperOutline}
                readOnly={actionDisabled || retryStart !== null} aria-describedby={`${headingId}-paper-input`}
                onChange={(event) => setPaperOutline(event.currentTarget.value)} />
              <p className="workflow-note" id={`${headingId}-paper-input`}>大纲与最近核验的最多 20 条文献将随本轮写作请求发给模型。</p>
            </div>
          </>}
          {firstStep && lastStep && <div className="field">
            <label htmlFor={`${headingId}-through-step`}>本次终点（可选）</label>
            <select id={`${headingId}-through-step`} value={endStep} disabled={actionDisabled}
              aria-describedby={`${headingId}-range-help`} onChange={(event) => {
                setThroughStep(event.currentTarget.value)
                setRetry(null)
                setActionError('')
              }}>
              <option value="">
                {firstStep.key === lastStep.key ? `仅运行「${lastStep.title}」` : `运行至最后一步「${lastStep.title}」`}
              </option>
              {rangeSteps.slice(0, -1).map((step) => (
                <option key={step.key} value={step.key}>
                  {step.key === firstStep.key ? `仅运行「${step.title}」` : `运行至「${step.title}」`}
                </option>
              ))}
            </select>
            <p className="workflow-note" id={`${headingId}-range-help`}>
              从「{firstStep.title}」开始，按顺序运行。{rangeCompleted && '后续步骤沿用本轮前面已完成的成果。'}
            </p>
          </div>}
          {canStart && (
            <label className="workflow-checkpoint">
              <input type="checkbox" checked={checkpoints} disabled={actionDisabled || retryStart !== null}
                onChange={(event) => setCheckpoints(event.currentTarget.checked)} />
              <span>{checkpointLabel}</span>
            </label>
          )}
          {canPause && <p className="workflow-note" id={`${headingId}-pause-help`}>
            暂停会停止当前步骤，继续时将重新执行该步骤。
          </p>}
          <div className="workflow-actions">
            {canStart && (
              <button className="button primary" type="button" disabled={!validTarget || (actionDisabled && pending !== 'start')}
                aria-disabled={pending === 'start'} onClick={() => void submit('start')}>
                {pending === 'start' ? '正在启动…' : retryStart ? '重试启动' : run ? '开始新一轮' : '启动工作流'}
              </button>
            )}
            {canContinue && (
              <button className="button primary" type="button" disabled={actionDisabled && pending !== 'continue'}
                aria-disabled={pending === 'continue'} onClick={() => void submit('continue')}>
                {pending === 'continue' ? '正在提交…' : retryContinue ? '重试继续' : waiting ? '确认并继续' : '继续未完成的步骤'}
              </button>
            )}
            {canPause && (
              <button className="button outline" type="button" disabled={requestBlocked && pending !== 'pause'}
                aria-disabled={pending === 'pause'} aria-describedby={`${headingId}-pause-help`} onClick={() => void submit('pause')}>
                {pending === 'pause' ? '正在提交暂停…' : retryPause ? '重试暂停' : '暂停工作流'}
              </button>
            )}
            {(readError || modelUnavailable) && (
              <button className="button outline" type="button" disabled={refreshing || pending !== null} onClick={refresh}>
                {refreshing ? '正在刷新…' : '刷新工作流状态'}
              </button>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
