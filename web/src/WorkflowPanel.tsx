import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import type { TemplateId } from './projects'
import { WorkflowArtifactHistory } from './WorkflowArtifactHistory'
import { initialCourseDraft, prepareCourse, WorkflowCourseOptions, WorkflowCourseSummary } from './WorkflowCourseOptions'
import { initialProposalDraft, prepareProposal, WorkflowProposalOptions, WorkflowProposalSummary } from './WorkflowProposalOptions'
import { WorkflowRunHistory } from './WorkflowRunHistory'
import { WorkflowSkipStep } from './WorkflowSkipStep'
import type { SkipTarget } from './WorkflowSkipStep'
import { WorkflowStageAcceptance } from './WorkflowStageAcceptance'
import type { AcceptanceTarget } from './WorkflowStageAcceptance'
import { WorkflowSelectedTopic, WorkflowTopicChoice } from './WorkflowTopicChoice'
import type { TopicSelectionTarget } from './WorkflowTopicChoice'
import {
  acceptWorkflow, cancelWorkflow, continueWorkflow, getWorkflow, isWorkflowActive, pauseWorkflow, rerunWorkflow,
  runStatusLabels, saveManuscript, selectWorkflowTopic, skipWorkflow, startWorkflow, stepStatusLabels, workflowArtifactPath,
} from './workflow'
import type {
  AcceptWorkflowInput, ContinueWorkflowInput, RerunWorkflowInput, SelectWorkflowTopicInput, SkipWorkflowInput,
  StartWorkflowInput, WorkflowRun, WorkflowRunActionInput, WorkflowSnapshot,
} from './workflow'
import './workflow.css'

const pollInterval = 1_000
const requestTimeout = 10_000
const topicRequirementsLimit = 4 * 1024

type Attempt =
  | { kind: 'start'; input: StartWorkflowInput }
  | { kind: 'continue'; input: ContinueWorkflowInput }
  | { kind: 'pause'; input: WorkflowRunActionInput }
  | { kind: 'cancel'; input: WorkflowRunActionInput }
  | { kind: 'rerun'; input: RerunWorkflowInput }
  | { kind: 'accept'; input: AcceptWorkflowInput }
  | { kind: 'skip'; input: SkipWorkflowInput }
  | { kind: 'select-topic'; input: SelectWorkflowTopicInput }

type CancelTarget = Omit<WorkflowRunActionInput, 'requestId'>
type RerunTarget = Omit<RerunWorkflowInput, 'requestId'>

function canRerunStep(run: WorkflowRun, index: number) {
  const step = run.steps[index]
  if (!step || isWorkflowActive(run) || run.status === 'CANCELLED') return false
  if (step.key === 'contract' && (run.status === 'WAITING_TOPIC_CHOICE' || !run.topicChoice?.selectedNumber)) return false
  return (step.status === 'SUCCEEDED' || step.status === 'STALE' || step.status === 'SKIPPED')
    && run.steps.slice(0, index).every((preceding) => preceding.status === 'SUCCEEDED')
}

function hasNewerRevision(current: WorkflowRun | null | undefined, incoming: WorkflowRun | null) {
  return !!current && !!incoming && current.runId === incoming.runId && current.revision > incoming.revision
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(requestTimeout)])
}

function WorkflowSteps({ projectId, run, rerun }: {
  projectId: string
  run: WorkflowRun
  rerun?: {
    selected?: string
    disabled: boolean
    pending: boolean
    retry: boolean
    onSelect: (target: RerunTarget) => void
    onConfirm: () => void
    onCancel: () => void
  }
}) {
  const confirmationId = useId()
  const selectedButton = useRef<HTMLButtonElement>(null)
  return (
    <ol className="workflow-steps" aria-label="工作流步骤">
      {run.steps.map((step, index) => {
        const selected = rerun?.selected === step.key
        const eligible = !!rerun && canRerunStep(run, index)
        const skipped = step.status === 'SKIPPED'
        const runLabel = skipped ? '运行此步骤' : '重跑此步骤'
        const canDownload = step.status === 'SUCCEEDED' && (!!rerun || !!step.artifactId)
        const downloadLabel = rerun ? '下载当前成果' : '下载该轮成果'
        const hasHistory = (step.artifactCount ?? 0) > 0 || step.status === 'SUCCEEDED' || step.status === 'STALE'
        return <li key={step.key}>
          <div className="workflow-step-heading">
            <strong dir="auto">{step.title}</strong>
            <span className="workflow-step-status" data-status={step.status}>{stepStatusLabels[step.status]}</span>
          </div>
          {step.message && <p className="workflow-note workflow-step-message" dir="auto">{step.message}</p>}
          {step.source && <p className="workflow-note">
            工作区来源 <bdi>{step.source.path}</bdi> · 版本 <bdi>{step.source.versionId}</bdi>
          </p>}
          {(canDownload || eligible) && <div className="workflow-actions">
            {canDownload && <a className="workflow-download"
              href={workflowArtifactPath(projectId, run.runId, step.key, step.artifactId)}
              aria-label={`${downloadLabel}：${step.title}`} download>{downloadLabel}</a>}
            {rerun && eligible && <button className="button outline" type="button" disabled={rerun.disabled}
              aria-label={`${runLabel}：${step.title}`} aria-expanded={selected}
              aria-controls={selected ? confirmationId : undefined} onClick={(event) => {
                if (selected) rerun.onCancel()
                else {
                  selectedButton.current = event.currentTarget
                  rerun.onSelect({ runId: run.runId, expectedRevision: run.revision, stepKey: step.key })
                }
              }}>{runLabel}</button>}
          </div>}
          {rerun && selected && <div className="workflow-rerun-confirm" id={confirmationId} role="group" aria-labelledby={`${confirmationId}-help`}>
            <p className="workflow-note" id={`${confirmationId}-help`}>
              {skipped ? `仅运行「${step.title}」。` : `仅重跑「${step.title}」。旧成果仍可从历史下载，后续已生成的成果会标为待重算。`}
            </p>
            <div className="workflow-actions">
              <button className="button primary" type="button" disabled={!eligible || (rerun.disabled && !rerun.pending)}
                aria-disabled={rerun.pending} onClick={rerun.onConfirm}>
                {rerun.pending ? '正在提交…' : `${rerun.retry ? '重试' : '确认'}${skipped ? '运行' : '重跑'}`}
              </button>
              <button className="button outline" type="button" disabled={rerun.pending} onClick={() => {
                rerun.onCancel()
                selectedButton.current?.focus()
              }}>取消</button>
            </div>
          </div>}
          {hasHistory && <WorkflowArtifactHistory projectId={projectId} runId={run.runId} stepKey={step.key}
            stepTitle={step.title} artifactId={step.artifactId} count={step.artifactCount} />}
        </li>
      })}
    </ol>
  )
}

function ManuscriptHandoff({ projectId, runId, artifactId, onSaved }: {
  projectId: string
  runId: string
  artifactId: string
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
      const file = await saveManuscript(projectId, runId, requestSignal(controller), artifactId)
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

export function WorkflowPanel({ projectId, templateId, onManuscriptSaved, refreshVersion = 0 }: {
  projectId: string
  templateId: Exclude<TemplateId, 'freeform' | 'automatic_review'>
  onManuscriptSaved: (path: string) => void
  refreshVersion?: number
}) {
  const isReview = templateId === 'literature_review'
  const isPaper = templateId === 'paper'
  const isTopic = templateId === 'topic'
  const courseTemplate = templateId === 'course_paper' || templateId === 'course_report' ? templateId : null
  const isProposal = templateId === 'proposal'
  const title = {
    experiment: '实验工作流',
    literature_review: '文献综述工作流',
    paper: '论文写作工作流',
    topic: '研究选题工作流',
    proposal: '开题报告工作流',
    course_paper: '课程论文工作流',
    course_report: '课程报告工作流',
  }[templateId]
  const checkpointLabel = {
    experiment: '生成实验计划后，等待我确认再继续',
    literature_review: '整理主题后，等待我确认再继续',
    paper: '生成大纲后，等待我确认再继续',
    topic: '入围后由我确认选题',
    proposal: '完成开题规划后由我确认',
    course_paper: '完成课程写作规划后由我确认',
    course_report: '完成课程写作规划后由我确认',
  }[templateId]
  const checkpointHint = {
    experiment: '确认实验计划后生成验收清单。',
    literature_review: '确认主题规划后撰写综述。',
    paper: '确认论文大纲后生成正文。',
    topic: '确认后继续本轮选题。',
    proposal: '确认开题规划后继续撰写',
    course_paper: '确认课程写作规划后开始分节撰写。',
    course_report: '确认课程写作规划后开始分节撰写。',
  }[templateId]
  const headingId = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  const cancelButton = useRef<HTMLButtonElement>(null)
  const submission = useRef<AbortController | null>(null)
  const [snapshot, setSnapshot] = useState<WorkflowSnapshot | null>(null)
  const [checkpoints, setCheckpoints] = useState(false)
  const [targetPaperCount, setTargetPaperCount] = useState('20')
  const [paperLanguage, setPaperLanguage] = useState<'zh' | 'en'>('zh')
  const [paperOutline, setPaperOutline] = useState('')
  const [topicRequirements, setTopicRequirements] = useState('')
  const [proposalDraft, setProposalDraft] = useState(initialProposalDraft)
  const [courseDraft, setCourseDraft] = useState(() => initialCourseDraft(courseTemplate))
  const [throughStep, setThroughStep] = useState('')
  const [refreshing, setRefreshing] = useState(true)
  const [reload, setReload] = useState(0)
  const [readError, setReadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [acceptError, setAcceptError] = useState<AcceptanceTarget & { message: string }>()
  const [feedback, setFeedback] = useState('')
  const [retry, setRetry] = useState<Attempt | null>(null)
  const [rerunTarget, setRerunTarget] = useState<RerunTarget | null>(null)
  const [cancelTarget, setCancelTarget] = useState<CancelTarget | null>(null)
  const [pending, setPending] = useState<Attempt['kind'] | null>(null)
  const run = snapshot?.run ?? null
  const acceptStep = run?.steps.find((step) => step.key === run.acceptStepKey)
  const skipStep = run?.steps.find((step) => step.key === run.skipStepKey)
  const manuscript = run?.steps.find((step) => step.key === 'manuscript' && step.status === 'SUCCEEDED')
  const cancelled = run?.status === 'CANCELLED'
  const completedWithSkip = run?.status === 'COMPLETED' && run.steps.some((step) => step.status === 'SKIPPED')
  const canStart = snapshot !== null && (run === null || run.status === 'COMPLETED' || cancelled)
  const waiting = run?.status === 'WAITING_CHECKPOINT'
  const waitingTopic = run?.status === 'WAITING_TOPIC_CHOICE'
  const rangeCompleted = run?.status === 'RANGE_COMPLETED'
  const pausing = run?.status === 'PAUSING'
  const paused = run?.status === 'PAUSED'
  const canContinue = rangeCompleted || waiting || paused || run?.status === 'FAILED' || run?.status === 'INTERRUPTED'
  const canPause = run?.status === 'QUEUED' || run?.status === 'RUNNING'
  const canCancel = canPause || canContinue || waitingTopic
  const selectedCancel = canCancel && cancelTarget && run?.runId === cancelTarget.runId ? cancelTarget : null
  const confirmingCancel = selectedCancel !== null
  const cancelConfirmationId = `${headingId}-cancel-confirmation`
  const steps = (canStart ? snapshot.steps : run?.steps) ?? []
  const rangeStartIndex = rangeCompleted ? run.steps.findIndex((step) => step.status !== 'SUCCEEDED') : canStart ? 0 : -1
  const rangeSteps = rangeStartIndex >= 0 ? steps.slice(rangeStartIndex) : []
  const firstStep = rangeSteps[0]
  const lastStep = rangeSteps.at(-1)
  const endStep = rangeSteps.some((step) => step.key === throughStep) ? throughStep : ''
  const retryStart = retry?.kind === 'start' && (retry.input.throughStep ?? '') === endStep ? retry : null
  const retryContinue = retry?.kind === 'continue' && retry.input.runId === run?.runId
    && retry.input.expectedRevision === run.revision && (retry.input.throughStep ?? '') === endStep ? retry : null
  const retryPause = retry?.kind === 'pause' && retry.input.runId === run?.runId
    && retry.input.expectedRevision === run.revision ? retry : null
  const retryCancel = retry?.kind === 'cancel' && selectedCancel
    && retry.input.runId === selectedCancel.runId && retry.input.expectedRevision === selectedCancel.expectedRevision ? retry : null
  const scopeStart = run?.steps.find((step) => step.key === run.scope.startStep)
  const scopeEnd = run?.steps.find((step) => step.key === run.scope.endStep)
  const modelUnavailable = snapshot?.modelConfigured === false
  const requestBlocked = refreshing || readError !== '' || pending !== null
  const topicSelectionDisabled = requestBlocked || (modelUnavailable && run?.scope.endStep !== 'shortlist')
  const actionDisabled = requestBlocked || modelUnavailable || ((canStart || rangeCompleted) && !firstStep)
  const rerunDisabled = requestBlocked || modelUnavailable
  const selectedRerun = rerunTarget && run?.runId === rerunTarget.runId ? rerunTarget : null
  const retryRerun = retry?.kind === 'rerun' && selectedRerun
    && retry.input.runId === selectedRerun.runId && retry.input.expectedRevision === selectedRerun.expectedRevision
    && retry.input.stepKey === selectedRerun.stepKey ? retry : null
  const retryAcceptance = retry?.kind === 'accept' && run?.runId === retry.input.runId
    && run.revision === retry.input.expectedRevision && acceptStep?.key === retry.input.stepKey ? retry : null
  const retrySkip = retry?.kind === 'skip' && run?.runId === retry.input.runId
    && run.revision === retry.input.expectedRevision && skipStep?.key === retry.input.stepKey ? retry : null
  const retryTopic = retry?.kind === 'select-topic' && waitingTopic && run.runId === retry.input.runId
    && run.revision === retry.input.expectedRevision ? retry : null
  const acceptanceError = acceptError && run?.runId === acceptError.runId
    && run.revision === acceptError.expectedRevision && acceptStep?.key === acceptError.stepKey ? acceptError : undefined
  const target = Number(targetPaperCount)
  const validTarget = !isReview || (Number.isInteger(target) && target >= 10 && target <= 80)
  const requirements = topicRequirements.trim()
  const invalidRequirements = isTopic && (new TextEncoder().encode(requirements).length > topicRequirementsLimit
    || requirements.includes('\0'))
  const proposal = isProposal ? prepareProposal(proposalDraft) : undefined
  const course = courseTemplate ? prepareCourse(courseTemplate, courseDraft) : undefined
  const invalidStart = !validTarget || invalidRequirements || proposal?.valid === false || course?.valid === false
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

    setRefreshing(true)
    void read()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [projectId, reload, refreshVersion])

  useEffect(() => () => submission.current?.abort(), [])

  function refresh() {
    setRefreshing(true)
    setReload((value) => value + 1)
  }

  function toggleCancelConfirmation() {
    if (!run || !canCancel || requestBlocked || submission.current) return
    if (confirmingCancel) {
      setCancelTarget(null)
    } else {
      setRerunTarget(null)
      setCancelTarget({ runId: run.runId, expectedRevision: run.revision })
    }
  }

  async function submit(...[kind, stepTarget]:
    | [Exclude<Attempt['kind'], 'accept' | 'skip' | 'select-topic'>]
    | ['accept', AcceptanceTarget]
    | ['skip', SkipTarget]
    | ['select-topic', TopicSelectionTarget]
  ) {
    if (submission.current || requestBlocked) return
    if (kind === 'pause') {
      if (!canPause) return
    } else if (kind === 'cancel') {
      if (!selectedCancel) return
    } else if (kind === 'accept' || kind === 'skip') {
      const step = kind === 'accept' ? acceptStep : skipStep
      if (!run || stepTarget.runId !== run.runId || stepTarget.expectedRevision !== run.revision
        || stepTarget.stepKey !== step?.key) return
    } else if (kind === 'select-topic') {
      if (topicSelectionDisabled || !waitingTopic || stepTarget.runId !== run.runId
        || stepTarget.expectedRevision !== run.revision
        || !run.topicChoice?.candidates.some(({ number }) => number === stepTarget.candidateNumber)) return
    } else if (kind === 'rerun') {
      if (rerunDisabled || !selectedRerun || !run) return
      const index = run.steps.findIndex((step) => step.key === selectedRerun.stepKey)
      if (!canRerunStep(run, index)) return
    } else if (actionDisabled || (kind === 'start' ? !canStart || invalidStart : !canContinue)) return
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
    setAcceptError(undefined)
    setFeedback('')
    let attempt: Attempt | null = {
      start: retryStart, continue: retryContinue, pause: retryPause, cancel: retryCancel, rerun: retryRerun,
      accept: kind === 'accept' && retryAcceptance?.input.sourceVersionId === stepTarget.sourceVersionId ? retryAcceptance : null,
      skip: kind === 'skip' && retrySkip?.input.reason === stepTarget.reason ? retrySkip : null,
      'select-topic': kind === 'select-topic' && retryTopic?.input.candidateNumber === stepTarget.candidateNumber ? retryTopic : null,
    }[kind]
    try {
      if (!attempt) {
        const requestId = crypto.randomUUID()
        if (kind === 'start') {
          attempt = { kind, input: {
            requestId, checkpoints, targetPaperCount: isReview ? target : undefined,
            paper: isPaper ? { language: paperLanguage, outline } : undefined,
            topic: isTopic ? { requirements: requirements || undefined } : undefined,
            proposal: proposal?.options,
            course: course?.options,
            throughStep: endStep || undefined,
          } }
        } else if (kind === 'rerun') {
          if (!selectedRerun) return
          attempt = { kind, input: { requestId, ...selectedRerun } }
        } else if (kind === 'cancel') {
          if (!selectedCancel) return
          attempt = { kind, input: { requestId, ...selectedCancel } }
        } else if (kind === 'accept') {
          attempt = { kind, input: { requestId, ...stepTarget } }
        } else if (kind === 'skip') {
          attempt = { kind, input: { requestId, ...stepTarget } }
        } else if (kind === 'select-topic') {
          attempt = { kind, input: { requestId, ...stepTarget } }
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
        case 'cancel': updated = await cancelWorkflow(projectId, attempt.input, signal); break
        case 'rerun': updated = await rerunWorkflow(projectId, attempt.input, signal); break
        case 'accept': updated = await acceptWorkflow(projectId, attempt.input, signal); break
        case 'skip': updated = await skipWorkflow(projectId, attempt.input, signal); break
        case 'select-topic': updated = await selectWorkflowTopic(projectId, attempt.input, signal); break
      }
      if (controller.signal.aborted) return
      setRetry(null)
      setRerunTarget(null)
      setCancelTarget(null)
      setThroughStep('')
      if (attempt.kind === 'start') {
        setCheckpoints(false)
        if (courseTemplate) setCourseDraft(initialCourseDraft(courseTemplate))
      }
      if (attempt.kind === 'accept') setFeedback('已将所选稿件验收为步骤成果。')
      if (attempt.kind === 'skip') setFeedback('已跳过该步骤。')
      if (attempt.kind === 'select-topic') setFeedback('选题已确认。')
      setSnapshot((current) => current && !hasNewerRevision(current.run, updated) ? { ...current, run: updated } : current)
      if (control instanceof HTMLButtonElement && document.activeElement === control) heading.current?.focus()
      // A replay may return an earlier revision. Read the latest state before accepting another action.
      refresh()
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof ProjectApiError && (error.status === 409 || error.code === 'REVISION_CONFLICT')) {
        setRetry(null)
        setRerunTarget(null)
        setCancelTarget(null)
        setFeedback('工作流状态已更新，请查看最新进度。')
        if ((kind === 'cancel' || kind === 'accept' || kind === 'skip' || kind === 'select-topic')
          && document.activeElement === control) heading.current?.focus()
        refresh()
      } else {
        const message = errorMessage(error)
        if (attempt?.kind === 'accept') setAcceptError({ ...attempt.input, message })
        else setActionError(message)
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
          {completedWithSkip ? '本轮已完成' : run ? runStatusLabels[run.status] : snapshot && '尚未启动'}
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
      {run && <WorkflowProposalSummary run={run} />}
      {run && <WorkflowCourseSummary run={run} />}
      {run?.workflowKey === 'proposal-v2' && <p className="workflow-note">正文各节分别保存。本轮可从未完成的一节继续。</p>}
      {run && <WorkflowSteps projectId={projectId} run={run} rerun={{
        selected: selectedRerun?.stepKey,
        disabled: rerunDisabled,
        pending: pending === 'rerun',
        retry: retryRerun !== null,
        onSelect: (target) => {
          setCancelTarget(null)
          setRerunTarget(target)
        },
        onConfirm: () => void submit('rerun'),
        onCancel: () => setRerunTarget(null),
      }} />}
      {waitingTopic && run.topicChoice && <WorkflowTopicChoice
        key={JSON.stringify([projectId, run.runId, run.revision, reload])}
        choice={run.topicChoice} target={{ runId: run.runId, expectedRevision: run.revision }}
        disabled={topicSelectionDisabled} pending={pending === 'select-topic'} retryNumber={retryTopic?.input.candidateNumber}
        onConfirm={(target) => void submit('select-topic', target)} />}
      <WorkflowSelectedTopic choice={run?.topicChoice} />
      {run && manuscript?.artifactId && <ManuscriptHandoff key={manuscript.artifactId}
        projectId={projectId} runId={run.runId} artifactId={manuscript.artifactId} onSaved={onManuscriptSaved} />}
      {run && acceptStep && <WorkflowStageAcceptance key={JSON.stringify([projectId, run.runId, run.revision, acceptStep.key, reload])}
        projectId={projectId} target={{ runId: run.runId, expectedRevision: run.revision, stepKey: acceptStep.key }}
        stepTitle={acceptStep.title} disabled={requestBlocked} pending={pending === 'accept'}
        retryVersionId={retryAcceptance?.input.sourceVersionId} submissionError={acceptanceError}
        onConfirm={(target) => void submit('accept', target)} />}
      {run && skipStep && <WorkflowSkipStep key={JSON.stringify([projectId, run.runId, run.revision, skipStep.key, reload])}
        target={{ runId: run.runId, expectedRevision: run.revision, stepKey: skipStep.key }} stepTitle={skipStep.title}
        disabled={requestBlocked} pending={pending === 'skip'} retryReason={retrySkip?.input.reason}
        onConfirm={(target) => void submit('skip', target)} />}
      {waiting && <p className="workflow-note">{checkpointHint}</p>}
      {pausing && <p className="workflow-note">当前步骤停止后即可继续运行。</p>}
      {paused && <p className="workflow-note">继续时从第一个未完成的步骤开始。已完成的成果仍可下载。</p>}
      {cancelled && <p className="workflow-note">已完成的成果仍可下载。</p>}
      {readError && <p className="form-error" role="alert">{readError}</p>}
      {actionError && <p className="form-error" role="alert">{actionError}</p>}
      {feedback && <p className="workflow-note" role="status">{feedback}</p>}
      {modelUnavailable && <p className="workflow-note">请配置研究模型，然后刷新工作流状态。</p>}
      {(canStart || canCancel || readError || modelUnavailable) && (
        <div className="workflow-controls">
          {canStart && courseTemplate && course && <WorkflowCourseOptions projectId={projectId} templateId={courseTemplate}
            draft={courseDraft} errors={course.errors} disabled={actionDisabled || retryStart !== null} onChange={setCourseDraft} />}
          {canStart && proposal && <WorkflowProposalOptions draft={proposalDraft} errors={proposal.errors}
            disabled={actionDisabled || retryStart !== null} onChange={setProposalDraft} />}
          {canStart && isTopic && <div className="field workflow-topic-requirements">
            <label htmlFor={`${headingId}-topic-requirements`}>选题要求</label>
            <textarea id={`${headingId}-topic-requirements`} rows={3} value={topicRequirements}
              readOnly={actionDisabled || retryStart !== null} aria-invalid={invalidRequirements || undefined}
              aria-describedby={`${headingId}-topic-input`} onChange={(event) => setTopicRequirements(event.currentTarget.value)} />
            <p id={`${headingId}-topic-input`} className={invalidRequirements ? 'form-error' : 'workflow-note'}>
              {invalidRequirements ? '选题要求须为 4 KiB 以内且不含空字符。'
                : '选填，最多 4 KiB（UTF-8）。选题要求和本轮文献元数据将发送给研究模型。'}
            </p>
          </div>}
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
            <select id={`${headingId}-through-step`} value={endStep} disabled={actionDisabled || (!!courseTemplate && !!retryStart)}
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
          {canStart && courseTemplate && <p id={`${headingId}-course-model`} className="workflow-note">
            启动后，补充要求、所选资料的文字节选和本轮文献元数据会发送给研究模型。
          </p>}
          <div className="workflow-actions">
            {canStart && (
              <button className="button primary" type="button"
                disabled={invalidStart || (actionDisabled && pending !== 'start')}
                aria-disabled={pending === 'start'} aria-describedby={courseTemplate ? `${headingId}-course-model` : undefined}
                onClick={() => void submit('start')}>
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
            {canCancel && (
              <button className="button outline" type="button" ref={cancelButton} disabled={requestBlocked}
                aria-expanded={confirmingCancel} aria-controls={confirmingCancel ? cancelConfirmationId : undefined}
                onClick={toggleCancelConfirmation}>取消本轮</button>
            )}
            {(readError || modelUnavailable) && (
              <button className="button outline" type="button" disabled={refreshing || pending !== null} onClick={refresh}>
                {refreshing ? '正在刷新…' : '刷新工作流状态'}
              </button>
            )}
          </div>
          {confirmingCancel && (
            <div className="workflow-cancel-confirm" id={cancelConfirmationId} role="group" aria-labelledby={`${cancelConfirmationId}-help`}>
              <p className="workflow-note" id={`${cancelConfirmationId}-help`}>
                此操作只结束本轮。正在执行的步骤会停止，已完成的成果仍可下载。
              </p>
              <div className="workflow-actions">
                <button className="button outline workflow-cancel-action" type="button"
                  disabled={requestBlocked && pending !== 'cancel'} aria-disabled={pending === 'cancel'}
                  onClick={() => void submit('cancel')}>
                  {pending === 'cancel' ? '正在提交取消…' : retryCancel ? '重试取消本轮' : '确认取消本轮'}
                </button>
                <button className="button outline" type="button" disabled={pending !== null} onClick={() => {
                  setCancelTarget(null)
                  cancelButton.current?.focus()
                }}>返回</button>
              </div>
            </div>
          )}
        </div>
      )}
      <WorkflowRunHistory projectId={projectId} currentRunId={run?.runId}
        renderSteps={(historicalRun) => <>
          <WorkflowSelectedTopic choice={historicalRun.topicChoice} />
          <WorkflowProposalSummary run={historicalRun} />
          <WorkflowCourseSummary run={historicalRun} />
          <WorkflowSteps projectId={projectId} run={historicalRun} />
        </>} />
    </section>
  )
}
