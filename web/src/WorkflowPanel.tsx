import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import type { TemplateId } from './projects'
import { WorkflowArtifactHistory } from './WorkflowArtifactHistory'
import { initialCourseDraft, prepareCourse, WorkflowCourseOptions, WorkflowCourseSummary } from './WorkflowCourseOptions'
import { WorkflowPatentFigureLinks } from './WorkflowPatentFigureLinks'
import { initialPatentDraft, preparePatent, WorkflowPatentOptions, WorkflowPatentSummary } from './WorkflowPatentOptions'
import { initialProposalDraft, prepareProposal, WorkflowProposalOptions, WorkflowProposalSummary } from './WorkflowProposalOptions'
import { RunDetail, WorkflowRunHistory } from './WorkflowRunHistory'
import type { WorkflowRunLink } from './WorkflowRunHistory'
import { WorkflowSkipStep } from './WorkflowSkipStep'
import { WorkflowSoftwareBundleDownload } from './WorkflowSoftwareBundleDownload'
import { initialSoftwareDraft, prepareSoftware, WorkflowSoftwareOptions, WorkflowSoftwareSummary } from './WorkflowSoftwareOptions'
import { WorkflowSoftwarePdfDownload } from './WorkflowSoftwarePdfDownload'
import type { SkipTarget } from './WorkflowSkipStep'
import { WorkflowStageAcceptance } from './WorkflowStageAcceptance'
import type { AcceptanceTarget } from './WorkflowStageAcceptance'
import { WorkflowSelectedTopic, WorkflowTopicChoice } from './WorkflowTopicChoice'
import type { TopicSelectionTarget } from './WorkflowTopicChoice'
import {
  acceptWorkflow, cancelWorkflow, continueWorkflow, getWorkflow, isSoftwarePdfStep, isWorkflowActive, pauseWorkflow, rerunWorkflow,
  runStatusLabels, saveManuscript, selectWorkflowTopic, skipWorkflow, startWorkflow, stepStatusLabels, workflowArtifactPath,
} from './workflow'
import type {
  AcceptWorkflowInput, ContinueWorkflowInput, RerunWorkflowInput, SelectWorkflowTopicInput, SkipWorkflowInput,
  StartWorkflowInput, WorkflowModelId, WorkflowRun, WorkflowRunActionInput, WorkflowSnapshot,
} from './workflow'
import { WorkspaceArtifactBundleAdd } from './WorkspaceBundle'
import type { BundleSelection } from './WorkspaceBundle'
import type { FileRecord } from './workspace'
import './workflow.css'

const pollInterval = 1_000
const requestTimeout = 10_000
const topicRequirementsLimit = 4 * 1024
const softwareRegistrationV2 = 'software-registration-v2'

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

function WorkflowRunContext({ run }: { run: WorkflowRun }) {
  return <>
    {run.projectTitle && <p className="workflow-note">本轮研究题目 <bdi>{run.projectTitle}</bdi></p>}
    {run.contest && <p className="workflow-note">
      竞赛规格 <bdi>{run.contest.name}</bdi>（{run.contest.language === 'en' ? '英文' : '中文'} · 规则校验 {run.contest.verifiedAt}）
    </p>}
    {(run.model || run.reasoningEffort) && <p className="workflow-note">
      {run.model && <>本轮模型 <bdi>{run.model}</bdi> · </>}
      推理档 <bdi>{run.reasoningEffort || '默认'}</bdi>
    </p>}
    {(run.maxStepRetries !== undefined || (run.stepOrder && run.stepOrder.length > 0)) && <p className="workflow-note">
      {run.maxStepRetries !== undefined && <>单步重试上限 {run.maxStepRetries} 次</>}
      {run.skippedSteps?.length ? ` · 启动时跳过步骤：${run.skippedSteps.join('、')}` : ''}
      {run.stepOrder?.length ? ` · 自定义步骤顺序：${run.steps.map((s) => s.title).join(' → ')}` : ''}
    </p>}
  </>
}

function WorkflowSteps({ projectId, run, rerun, softwarePdfConfigured, patentWordConfigured, bundleSelection }: {
  projectId: string
  run: WorkflowRun
  bundleSelection: BundleSelection
  softwarePdfConfigured?: boolean
  patentWordConfigured?: boolean
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
  const softwarePdfEnabled = softwarePdfConfigured === true
    && (run.workflowKey === 'software-registration-v1' || run.workflowKey === softwareRegistrationV2)
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
        const pdfStep = softwarePdfEnabled && isSoftwarePdfStep(step.key) ? step.key : undefined
        const patentFigures = run.workflowKey === 'patent-disclosure-v2' && step.key === 'figures'
        return <li key={step.key} data-workflow-step-key={step.key}>
          <div className="workflow-step-heading">
            <strong dir="auto">{step.title}</strong>
            <span className="workflow-step-status" data-status={step.status}>{stepStatusLabels[step.status]}</span>
          </div>
          {(step.model || step.reasoningEffort) && <p className="workflow-note">
            {step.model && <>步骤模型 <bdi>{step.model}</bdi> · </>}
            推理档 <bdi>{step.reasoningEffort || '默认'}</bdi>
          </p>}
          {step.message && <p className="workflow-note workflow-step-message" dir="auto">{step.message}</p>}
          {step.source && <p className="workflow-note">
            工作区来源 <bdi>{step.source.path}</bdi> · 版本 <bdi>{step.source.versionId}</bdi>
          </p>}
          {(canDownload || eligible) && <div className="workflow-actions">
            {canDownload && <a className="workflow-download"
              href={workflowArtifactPath(projectId, run.runId, step.key, step.artifactId)}
              aria-label={`${downloadLabel}：${step.title}`} download>{downloadLabel}</a>}
            {canDownload && <WorkspaceArtifactBundleAdd artifactId={step.artifactId} name={step.artifactName}
              size={step.artifactSize} selection={bundleSelection} />}
            {canDownload && pdfStep && step.artifactId && (
              <WorkflowSoftwarePdfDownload projectId={projectId} runId={run.runId} stepKey={pdfStep}
                stepTitle={step.title} artifactId={step.artifactId} />
            )}
            {canDownload && patentFigures && step.artifactId && (
              <WorkflowPatentFigureLinks projectId={projectId} runId={run.runId} artifactId={step.artifactId}
                wordEnabled={patentWordConfigured === true} />
            )}
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
            stepTitle={step.title} artifactId={step.artifactId} count={step.artifactCount}
            softwarePdfEnabled={pdfStep !== undefined} patentFigures={patentFigures}
            patentWordConfigured={patentWordConfigured} bundleSelection={bundleSelection} />}
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

export function WorkflowPanel({ projectId, templateId, onManuscriptSaved, refreshVersion = 0, autoStart = false,
  initialContestId, workflowRunLink, acceptanceDraft, onClearAcceptanceDraft, bundleSelection }: {
  projectId: string
  templateId: Exclude<TemplateId, 'freeform' | 'automatic_review' | 'interactive_survey' | 'engineering_project'>
  bundleSelection: BundleSelection
  onManuscriptSaved: (path: string) => void
  refreshVersion?: number
  autoStart?: boolean
  initialContestId?: string
  workflowRunLink?: WorkflowRunLink
  acceptanceDraft?: { file: FileRecord; openId: number }
  onClearAcceptanceDraft: (openId: number) => void
}) {
  const isReview = templateId === 'literature_review'
  const isPaper = templateId === 'paper'
  const isTopic = templateId === 'topic'
  const courseTemplate = templateId === 'course_paper' || templateId === 'course_report' ? templateId : null
  const isProposal = templateId === 'proposal'
  const isSoftware = templateId === 'software_registration'
  const isPatent = templateId === 'patent_disclosure'
  const isMathModeling = templateId === 'math_modeling'
  const defaultCheckpoints = isSoftware || isPatent
  const title = {
    experiment: '实验工作流',
    literature_review: '文献综述工作流',
    paper: '论文写作工作流',
    topic: '研究选题工作流',
    proposal: '开题报告工作流',
    course_paper: '课程论文工作流',
    course_report: '课程报告工作流',
    software_registration: '软件著作权材料工作流',
    patent_disclosure: '专利技术交底工作流',
    full_research: '科研全流程工作流',
    chinese_paper: '中文论文工作流',
    humanities_paper: '人文社科论文工作流',
    thesis: '学位论文工作流',
    existing_materials: '已有材料成稿工作流',
    market_research: '市场调查工作流',
    math_modeling: '数学建模竞赛工作流',
    big_data_contest: '大数据竞赛工作流',
  }[templateId]
  const checkpointLabel = {
    experiment: '生成实验计划后，等待我确认再继续',
    literature_review: '整理主题后，等待我确认再继续',
    paper: '生成大纲后，等待我确认再继续',
    topic: '入围后由我确认选题',
    proposal: '完成开题规划后由我确认',
    course_paper: '完成课程写作规划后由我确认',
    course_report: '完成课程写作规划后由我确认',
    software_registration: '操作手册草稿完成后由我确认',
    patent_disclosure: '技术方案草案完成后由我确认',
    full_research: '完成选题与研究契约后由我确认',
    chinese_paper: '完成中文大纲后由我确认',
    humanities_paper: '完成论证大纲后由我确认',
    thesis: '完成目录与文献规划后由我确认',
    existing_materials: '完成成稿规划后由我确认',
    market_research: '完成问卷与编码设计后由我确认',
    math_modeling: '完成问题拆解与假设后由我确认',
    big_data_contest: '完成特征与基线方案后由我确认',
  }[templateId]
  const checkpointHint = {
    experiment: '确认实验计划后生成验收清单。',
    literature_review: '确认主题规划后撰写综述。',
    paper: '确认论文大纲后生成正文。',
    topic: '确认后继续本轮选题。',
    proposal: '确认开题规划后继续撰写',
    course_paper: '确认课程写作规划后开始分节撰写。',
    course_report: '确认课程写作规划后开始分节撰写。',
    software_registration: '核对操作手册草稿后，确认继续整理程序材料。',
    patent_disclosure: '核对技术方案草案后，确认继续编写技术交底稿。',
    full_research: '确认选题与研究契约后推进实验与论文撰写。',
    chinese_paper: '确认中文大纲后开始正文撰写。',
    humanities_paper: '确认论证大纲后开始章节写作。',
    thesis: '确认目录与文献规划后开始主体章节撰写。',
    existing_materials: '确认成稿规划后开始整合撰写。',
    market_research: '确认问卷与编码设计后继续数据清洗与统计解读。',
    math_modeling: '确认问题拆解与假设后进入模型构建与求解验证。',
    big_data_contest: '确认特征与基线方案后进入模型迭代与消融验证。',
  }[templateId]
  const headingId = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  const cancelButton = useRef<HTMLButtonElement>(null)
  const submission = useRef<AbortController | null>(null)
  const [snapshot, setSnapshot] = useState<WorkflowSnapshot | null>(null)
  const [preferredModelId, setPreferredModelId] = useState<WorkflowModelId>()
  const [stepModelChoices, setStepModelChoices] = useState(() => new Map<string, WorkflowModelId>())
  const [checkpoints, setCheckpoints] = useState(defaultCheckpoints)
  const [targetPaperCount, setTargetPaperCount] = useState('20')
  const [paperLanguage, setPaperLanguage] = useState<'zh' | 'en'>('zh')
  const [paperOutline, setPaperOutline] = useState('')
  const [topicRequirements, setTopicRequirements] = useState('')
  const [proposalDraft, setProposalDraft] = useState(initialProposalDraft)
  const [courseDraft, setCourseDraft] = useState(() => initialCourseDraft(courseTemplate))
  const [softwareDraft, setSoftwareDraft] = useState(initialSoftwareDraft)
  const [patentDraft, setPatentDraft] = useState(initialPatentDraft)
  const [preferredContestId, setPreferredContestId] = useState(initialContestId ?? '')
  const [skippedSteps, setSkippedSteps] = useState<string[]>([])
  const [customStepOrder, setCustomStepOrder] = useState<string[]>([])
  const [maxStepRetriesText, setMaxStepRetriesText] = useState('2')
  const [throughStep, setThroughStep] = useState('')
  const autoStartedRef = useRef(false)
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
  const checkpointMessage = run?.confirmEachStep ? '核对已完成的成果后，确认运行下一步。'
    : run?.workflowKey === softwareRegistrationV2
      ? '核对操作手册草稿后，确认继续整理程序材料。若无可用源码，下一步会生成待核对的 Python 示例。'
      : checkpointHint
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
  const defaultStartSteps = snapshot?.steps ?? []
  const orderedStartSteps = (() => {
    if (!canStart || customStepOrder.length !== defaultStartSteps.length || customStepOrder.length === 0) {
      return defaultStartSteps
    }
    const byKey = new Map(defaultStartSteps.map((step) => [step.key, step]))
    const mapped = customStepOrder.map((key) => byKey.get(key)).filter((step): step is NonNullable<typeof step> => !!step)
    return mapped.length === defaultStartSteps.length ? mapped : defaultStartSteps
  })()
  const isCustomOrder = canStart && orderedStartSteps.some((step, idx) => step.key !== defaultStartSteps[idx]?.key)
  const steps = (canStart ? orderedStartSteps : run?.steps) ?? []
  const rangeStartIndex = rangeCompleted ? run.steps.findIndex((step) => step.status !== 'SUCCEEDED') : canStart ? 0 : -1
  const rangeSteps = rangeStartIndex >= 0 ? steps.slice(rangeStartIndex) : []
  const firstStep = rangeSteps[0]
  const lastStep = rangeSteps.at(-1)
  const endStep = rangeSteps.some((step) => step.key === throughStep) ? throughStep : ''
  const modelOptions = snapshot?.modelOptions ?? []
  const startModel = modelOptions.find(({ id }) => id === preferredModelId) ?? modelOptions[0]
  const modelSteps = canStart && modelOptions.length > 1 ? steps.filter((step) => step.requiresModel) : []
  const modelSelectOptions = modelOptions.map(({ id, model, reasoningEffort }) => (
    <option key={id} value={id}>{model} · 推理档 {reasoningEffort || '默认'}</option>
  ))
  const retryStart = retry?.kind === 'start' && (retry.input.throughStep ?? '') === endStep
    && retry.input.modelId === startModel?.id ? retry : null
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
  const software = isSoftware ? prepareSoftware(softwareDraft) : undefined
  const patent = isPatent ? preparePatent(patentDraft) : undefined
  const contests = snapshot?.contests ?? []
  const selectedContest = isMathModeling
    ? (contests.find((item) => item.id === preferredContestId) ?? contests[0])
    : undefined
  const optionalSteps = canStart ? steps.filter((step) => step.optional) : []
  const parsedRetries = Number(maxStepRetriesText)
  const invalidRetries = !Number.isInteger(parsedRetries) || parsedRetries < 1 || parsedRetries > 5
  const invalidStart = !validTarget || invalidRequirements || invalidRetries || proposal?.valid === false
    || course?.valid === false || software?.valid === false || !!patent?.descriptionError
  let modelInputNotice: string | undefined
  if (isPatent) {
    modelInputNotice = '编写技术方案草案和交底稿时，技术方案名称、技术说明及所选资料的文字节选会发送给研究模型。'
  } else if (isSoftware) {
    modelInputNotice = '编制操作手册时，软件名称、版本、说明、资料信息和说明文档节选会发送给研究模型。源码正文用于在本机整理程序材料。'
  } else if (courseTemplate) {
    modelInputNotice = '启动后，补充要求、所选资料的文字节选和本轮文献元数据会发送给研究模型。'
  }
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

  const triggerAutoStart = useEffectEvent(() => {
    void submit('start')
  })

  useEffect(() => {
    if (!autoStart || autoStartedRef.current || refreshing || !snapshot || snapshot.run !== null || pending !== null) return
    autoStartedRef.current = true
    if (!snapshot.modelConfigured) {
      setFeedback('项目已创建。请先配置研究模型后再启动工作流。')
      return
    }
    triggerAutoStart()
  }, [autoStart, refreshing, snapshot, pending])

  function refresh() {
    setRefreshing(true)
    setReload((value) => value + 1)
  }

  function canSwapStartSteps(indexA: number, indexB: number) {
    if (indexA < 0 || indexB >= orderedStartSteps.length || indexB !== indexA + 1) return false
    const first = orderedStartSteps[indexA]
    const second = orderedStartSteps[indexB]
    return !second.dependsOn?.includes(first.key)
  }

  function moveStartStep(index: number, delta: -1 | 1) {
    const targetIndex = index + delta
    const low = Math.min(index, targetIndex)
    const high = Math.max(index, targetIndex)
    if (!canSwapStartSteps(low, high)) return
    const nextOrder = orderedStartSteps.map((step) => step.key)
    const temp = nextOrder[low]
    nextOrder[low] = nextOrder[high]
    nextOrder[high] = temp
    setCustomStepOrder(nextOrder)
    if (retry?.kind === 'start') setRetry(null)
    setActionError('')
  }

  function changeStepModel(stepKey: string, value: string) {
    const modelId = modelOptions.find(({ id }) => id === value)?.id
    if ((value !== '' && !modelId) || modelId === stepModelChoices.get(stepKey)) return
    setStepModelChoices((current) => {
      const next = new Map(current)
      if (modelId) next.set(stepKey, modelId)
      else next.delete(stepKey)
      return next
    })
    if (retry?.kind === 'start') setRetry(null)
    setActionError('')
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
          setPreferredModelId(startModel?.id)
          const stepModelIds: Record<string, WorkflowModelId> = {}
          for (const { key } of modelSteps) {
            const modelId = stepModelChoices.get(key)
            if (modelId) stepModelIds[key] = modelId
          }
          const activeSkipped = optionalSteps.map((step) => step.key).filter((key) => skippedSteps.includes(key))
          attempt = { kind, input: {
            requestId, checkpoints, modelId: startModel?.id,
            stepModelIds: Object.keys(stepModelIds).length ? stepModelIds : undefined,
            targetPaperCount: isReview ? target : undefined,
            paper: isPaper ? { language: paperLanguage, outline } : undefined,
            topic: isTopic ? { requirements: requirements || undefined } : undefined,
            proposal: proposal?.options,
            course: course?.options,
            software: software?.options,
            patent: patent?.options,
            contestId: selectedContest?.id,
            skippedSteps: activeSkipped.length ? activeSkipped : undefined,
            stepOrder: isCustomOrder ? orderedStartSteps.map((step) => step.key) : undefined,
            maxStepRetries: parsedRetries !== 2 ? parsedRetries : undefined,
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
        setPreferredModelId(undefined)
        setStepModelChoices(new Map())
        setCheckpoints(defaultCheckpoints)
        setSkippedSteps([])
        setCustomStepOrder([])
        setMaxStepRetriesText('2')
        if (courseTemplate) setCourseDraft(initialCourseDraft(courseTemplate))
        if (isSoftware) setSoftwareDraft(initialSoftwareDraft())
        if (isPatent) setPatentDraft(initialPatentDraft())
      }
      if (attempt.kind === 'accept') {
        if (acceptanceDraft) onClearAcceptanceDraft(acceptanceDraft.openId)
        setFeedback('已将所选稿件验收为步骤成果。')
      }
      if (attempt.kind === 'skip') setFeedback('已跳过该步骤。')
      if (attempt.kind === 'select-topic') setFeedback('选题已确认。')
      setSnapshot((current) => current && !hasNewerRevision(current.run, updated) ? { ...current, run: updated } : current)
      if (control instanceof HTMLButtonElement && document.activeElement === control) heading.current?.focus()
      // A replay may return an earlier revision. Read the latest state before accepting another action.
      refresh()
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof ProjectApiError && error.code !== 'WORKFLOW_MODEL_CONNECTION_CHANGED'
        && (error.status === 409 || error.code === 'REVISION_CONFLICT')) {
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

  const renderRunSteps = (historicalRun: WorkflowRun) => <>
    <WorkflowSelectedTopic choice={historicalRun.topicChoice} />
    <WorkflowRunContext run={historicalRun} />
    <WorkflowProposalSummary run={historicalRun} />
    <WorkflowCourseSummary run={historicalRun} />
    <WorkflowSoftwareSummary run={historicalRun} />
    <WorkflowPatentSummary run={historicalRun} />
    <WorkflowSteps projectId={projectId} run={historicalRun} softwarePdfConfigured={snapshot?.softwarePdfConfigured}
      patentWordConfigured={snapshot?.patentWordConfigured} bundleSelection={bundleSelection} />
    <WorkflowSoftwareBundleDownload projectId={projectId} run={historicalRun} />
  </>

  return (
    <section className="workflow-panel" id="project-workflow" aria-labelledby={headingId}>
      <div className="workflow-heading">
        <h4 id={headingId} ref={heading} tabIndex={-1}>{title}</h4>
        <p className="workflow-run-status" role="status" data-status={run?.status}>
          {completedWithSkip ? '本轮已完成' : run ? runStatusLabels[run.status] : snapshot && '尚未启动'}
        </p>
      </div>
      {workflowRunLink && <div className="workflow-run-detail" data-workflow-run-id={workflowRunLink.workflowRunId}>
        <RunDetail key={workflowRunLink.workflowRunId} projectId={projectId} runId={workflowRunLink.workflowRunId}
          workflowRunLink={workflowRunLink} renderSteps={renderRunSteps} />
      </div>}
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
      {run && <WorkflowRunContext run={run} />}
      {run && <WorkflowProposalSummary run={run} />}
      {run && <WorkflowCourseSummary run={run} />}
      {run && <WorkflowSoftwareSummary run={run} />}
      {run && <WorkflowPatentSummary run={run} />}
      {isSoftware && snapshot?.softwarePdfConfigured === false && <p className="workflow-note">
        PDF 导出需要字体。请在服务端设置 <code>RESEARCH_PDF_FONT</code> 指向可嵌入的 TrueType 字体文件，重启服务后刷新页面。
      </p>}
      {isPatent && snapshot?.patentWordConfigured === false && <p className="workflow-note">
        要下载带附图的 Word，请在服务端设置 <code>RESEARCH_WORD_PYTHON</code> 和 <code>RESEARCH_PDF_FONT</code>，
        并在该 Python 环境中安装附图导出所需的可选依赖 Pillow。配置后请重启服务并刷新页面。
      </p>}
      {run?.workflowKey === 'proposal-v2' && <p className="workflow-note">正文各节分别保存。本轮可从未完成的一节继续。</p>}
      {run && <WorkflowSteps projectId={projectId} run={run} softwarePdfConfigured={snapshot?.softwarePdfConfigured}
        patentWordConfigured={snapshot?.patentWordConfigured} bundleSelection={bundleSelection} rerun={{
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
      {run && <WorkflowSoftwareBundleDownload projectId={projectId} run={run} />}
      {waitingTopic && run.topicChoice && <WorkflowTopicChoice
        key={JSON.stringify([projectId, run.runId, run.revision, reload])}
        choice={run.topicChoice} target={{ runId: run.runId, expectedRevision: run.revision }}
        disabled={topicSelectionDisabled} pending={pending === 'select-topic'} retryNumber={retryTopic?.input.candidateNumber}
        onConfirm={(target) => void submit('select-topic', target)} />}
      <WorkflowSelectedTopic choice={run?.topicChoice} />
      {run && manuscript?.artifactId && <ManuscriptHandoff key={manuscript.artifactId}
        projectId={projectId} runId={run.runId} artifactId={manuscript.artifactId} onSaved={onManuscriptSaved} />}
      {acceptanceDraft && !acceptStep && <p className="workflow-note" role="status">
        {refreshing ? '正在核对可验收的步骤…' : <>当前没有可验收的步骤。已选稿件为 <bdi>{acceptanceDraft.file.path}</bdi>。</>}
      </p>}
      {run && acceptStep && <WorkflowStageAcceptance
        key={JSON.stringify([projectId, run.runId, run.revision, acceptStep.key, reload, acceptanceDraft?.openId])}
        projectId={projectId} target={{ runId: run.runId, expectedRevision: run.revision, stepKey: acceptStep.key }}
        stepTitle={acceptStep.title} disabled={requestBlocked} pending={pending === 'accept'} preferredFile={acceptanceDraft?.file}
        retryVersionId={retryAcceptance?.input.sourceVersionId} submissionError={acceptanceError}
        onConfirm={(target) => void submit('accept', target)} />}
      {run && skipStep && <WorkflowSkipStep key={JSON.stringify([projectId, run.runId, run.revision, skipStep.key, reload])}
        target={{ runId: run.runId, expectedRevision: run.revision, stepKey: skipStep.key }} stepTitle={skipStep.title}
        disabled={requestBlocked} pending={pending === 'skip'} retryReason={retrySkip?.input.reason}
        onConfirm={(target) => void submit('skip', target)} />}
      {waiting && <p className="workflow-note">{checkpointMessage}</p>}
      {pausing && <p className="workflow-note">当前步骤停止后即可继续运行。</p>}
      {paused && <p className="workflow-note">继续时从第一个未完成的步骤开始。已完成的成果仍可下载。</p>}
      {cancelled && <p className="workflow-note">已完成的成果仍可下载。</p>}
      {readError && <p className="form-error" role="alert">{readError}</p>}
      {actionError && <p className="form-error" role="alert">{actionError}</p>}
      {feedback && <p className="workflow-note" role="status">{feedback}</p>}
      {modelUnavailable && <p className="workflow-note">请配置研究模型，然后刷新工作流状态。</p>}
      {(canStart || canCancel || readError || modelUnavailable) && (
        <div className="workflow-controls">
          {canStart && startModel && (modelOptions.length > 1 ? <div className="field">
            <label htmlFor={`${headingId}-model`}>工作流模型</label>
            <select id={`${headingId}-model`} value={startModel.id} disabled={actionDisabled}
              onChange={(event) => {
                const next = modelOptions.find(({ id }) => id === event.currentTarget.value)
                if (!next || next.id === startModel.id) return
                setPreferredModelId(next.id)
                if (retry?.kind === 'start') setRetry(null)
                setActionError('')
              }}>
              {modelSelectOptions}
            </select>
          </div> : <p className="workflow-note">
            工作流模型 <bdi>{startModel.model}</bdi> · 推理档 <bdi>{startModel.reasoningEffort || '默认'}</bdi>
          </p>)}
          {modelSteps.length > 0 && <details className="workflow-artifact-history">
            <summary>步骤模型</summary>
            <div className="workflow-run-history-content">
              <p className="workflow-note">可为本轮稍后运行的步骤提前选模。继续运行或重跑时沿用已保存的选择。</p>
              {modelSteps.map(({ key, title }) => {
                const selectId = `${headingId}-step-model-${key}`
                return <div className="field" key={key}>
                  <label htmlFor={selectId}>{title}的模型</label>
                  <select id={selectId} value={stepModelChoices.get(key) ?? ''} disabled={actionDisabled}
                    onChange={(event) => changeStepModel(key, event.currentTarget.value)}>
                    <option value="">沿用工作流模型</option>
                    {modelSelectOptions}
                  </select>
                </div>
              })}
            </div>
          </details>}
          {canStart && courseTemplate && course && <WorkflowCourseOptions projectId={projectId} templateId={courseTemplate}
            draft={courseDraft} errors={course.errors} disabled={actionDisabled || retryStart !== null} onChange={setCourseDraft} />}
          {canStart && software && <WorkflowSoftwareOptions projectId={projectId} draft={softwareDraft}
            errors={software.errors} disabled={actionDisabled || retryStart !== null} onChange={setSoftwareDraft} />}
          {canStart && patent && <WorkflowPatentOptions projectId={projectId} draft={patentDraft}
            descriptionError={patent.descriptionError} disabled={actionDisabled || retryStart !== null} onChange={setPatentDraft} />}
          {canStart && proposal && <WorkflowProposalOptions draft={proposalDraft} errors={proposal.errors}
            disabled={actionDisabled || retryStart !== null} onChange={setProposalDraft} />}
          {canStart && isMathModeling && contests.length > 0 && selectedContest && (
            <div className="field">
              <label htmlFor={`${headingId}-contest`}>数学建模竞赛规格</label>
              <select id={`${headingId}-contest`} value={selectedContest.id} disabled={actionDisabled || retryStart !== null}
                aria-describedby={`${headingId}-contest-help`}
                onChange={(event) => {
                  setPreferredContestId(event.currentTarget.value)
                  if (retry?.kind === 'start') setRetry(null)
                  setActionError('')
                }}>
                {contests.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}（{item.language === 'en' ? '英文' : '中文'} · {item.ruleYear}）
                  </option>
                ))}
              </select>
              <p className="workflow-note" id={`${headingId}-contest-help`}>
                默认语言：{selectedContest.language === 'en' ? '英文' : '中文'} · 规则校验：{selectedContest.verifiedAt} · 交付提醒：{selectedContest.reminders.join('；')}
              </p>
            </div>
          )}
          {canStart && orderedStartSteps.length > 1 && (
            <div className="field workflow-step-order">
              <div className="workflow-step-order-header">
                <span>执行步骤顺序（无前置依赖的步骤可调整）</span>
                {isCustomOrder && (
                  <button className="button outline" type="button"
                    disabled={actionDisabled || retryStart !== null}
                    onClick={() => {
                      setCustomStepOrder([])
                      if (retry?.kind === 'start') setRetry(null)
                    }}>恢复默认顺序</button>
                )}
              </div>
              <ol className="workflow-step-order-list" aria-label="自定义步骤顺序">
                {orderedStartSteps.map((step, index) => {
                  const canMoveUp = canSwapStartSteps(index - 1, index)
                  const canMoveDown = canSwapStartSteps(index, index + 1)
                  return (
                    <li key={step.key} className="workflow-step-order-item" data-step-order-key={step.key}>
                      <span className="workflow-step-order-title">
                        {index + 1}. {step.title}
                        {step.optional ? '（可选）' : ''}
                      </span>
                      <div className="workflow-step-order-controls">
                        <button className="button outline" type="button"
                          aria-label={`上移步骤：${step.title}`}
                          disabled={actionDisabled || retryStart !== null || !canMoveUp}
                          onClick={() => moveStartStep(index, -1)}>上移</button>
                        <button className="button outline" type="button"
                          aria-label={`下移步骤：${step.title}`}
                          disabled={actionDisabled || retryStart !== null || !canMoveDown}
                          onClick={() => moveStartStep(index, 1)}>下移</button>
                      </div>
                    </li>
                  )
                })}
              </ol>
            </div>
          )}
          {canStart && optionalSteps.length > 0 && (
            <div className="field">
              <span>可选跳过步骤（启动前自定义）</span>
              {optionalSteps.map((step) => (
                <label className="workflow-checkpoint" key={step.key}>
                  <input type="checkbox" checked={skippedSteps.includes(step.key)}
                    disabled={actionDisabled || retryStart !== null}
                    onChange={(event) => {
                      const checked = event.currentTarget.checked
                      setSkippedSteps((current) => checked
                        ? (current.includes(step.key) ? current : [...current, step.key])
                        : current.filter((item) => item !== step.key))
                      if (retry?.kind === 'start') setRetry(null)
                    }} />
                  <span>本轮跳过「{step.title}」</span>
                </label>
              ))}
            </div>
          )}
          {canStart && (
            <div className="field">
              <label htmlFor={`${headingId}-max-retries`}>单步失败重试上限（1–5 次，默认 2）</label>
              <input id={`${headingId}-max-retries`} type="number" min={1} max={5} step={1}
                value={maxStepRetriesText} disabled={actionDisabled || retryStart !== null}
                aria-invalid={invalidRetries ? true : undefined}
                onChange={(event) => {
                  setMaxStepRetriesText(event.currentTarget.value)
                  if (retry?.kind === 'start') setRetry(null)
                }} />
              {invalidRetries && <p className="form-error" role="alert">单步失败重试上限须为 1 至 5 的整数。</p>}
            </div>
          )}
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
            <select id={`${headingId}-through-step`} value={endStep}
              disabled={actionDisabled || ((!!courseTemplate || defaultCheckpoints) && !!retryStart)}
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
          {canStart && modelInputNotice && <p id={`${headingId}-model-input`} className="workflow-note">
            {modelInputNotice}
          </p>}
          <div className="workflow-actions">
            {canStart && (
              <button className="button primary" type="button"
                disabled={invalidStart || (actionDisabled && pending !== 'start')}
                aria-disabled={pending === 'start'} aria-describedby={modelInputNotice ? `${headingId}-model-input` : undefined}
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
      <WorkflowRunHistory projectId={projectId} currentRunId={run?.runId} renderSteps={renderRunSteps} />
    </section>
  )
}
