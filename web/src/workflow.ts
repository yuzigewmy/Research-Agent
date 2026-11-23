import { ProjectApiError, request } from './projects'
import { isFileRecord } from './workspace'
import type { FileRecord } from './workspace'

export function isWorkflowAcceptanceFile(file: FileRecord) {
  return /\.(md|markdown)$/i.test(file.path) && file.size > 0 && file.size <= 32 * 1024
}

export const runStatusLabels = {
  QUEUED: '排队中',
  RUNNING: '运行中',
  PAUSING: '正在暂停',
  PAUSED: '已暂停',
  CANCELLING: '正在取消本轮',
  CANCELLED: '本轮已取消',
  WAITING_CHECKPOINT: '等待确认',
  WAITING_TOPIC_CHOICE: '待选题',
  RANGE_COMPLETED: '本次范围已完成',
  COMPLETED: '全部步骤已完成',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

export const stepStatusLabels = {
  PENDING: '未开始',
  QUEUED: '排队中',
  RUNNING: '运行中',
  CANCELLING: '正在停止',
  CANCELLED: '已停止',
  SUCCEEDED: '已完成',
  SKIPPED: '已跳过',
  STALE: '待重算',
  FAILED: '失败',
  INTERRUPTED: '已中断',
} as const

interface WorkflowStepDefinition {
  key: string
  title: string
}

interface WorkflowArtifactSource {
  versionId: string
  path: string
  sha256: string
}

interface WorkflowStep extends WorkflowStepDefinition {
  status: keyof typeof stepStatusLabels
  message?: string
  artifactId?: string
  artifactCount?: number
  source?: WorkflowArtifactSource
}

interface WorkflowArtifact {
  artifactId: string
  createdAt: string
  current: boolean
  source?: WorkflowArtifactSource
}

export interface WorkflowArtifactPage {
  items: WorkflowArtifact[]
  nextCursor?: string
}

interface WorkflowRunSummary {
  runId: string
  status: keyof typeof runStatusLabels
  createdAt: string
}

export interface WorkflowRunPage {
  items: WorkflowRunSummary[]
  nextCursor?: string
}

interface WorkflowTopicCandidate {
  number: number
  title: string
  summary: string
  noveltyScore: number
  strength: string
  risk: string
  effort: string
  evidenceKeys?: string[]
}

export interface WorkflowTopicChoiceData {
  candidates: WorkflowTopicCandidate[]
  recommendedNumber: number
  recommendationReason: string
  selectedNumber?: number
}

export interface ProposalTargets {
  degree: 'bachelor' | 'master' | 'doctor'
  degreeType: 'academic' | 'professional'
  targetCharacters: number
  targetSources: number
}

export interface ProposalOptions extends Partial<ProposalTargets> {
  discipline?: string
  method?: string
  projectSource?: string
  proposalMonth?: string
  defenseMonth?: string
  requirements?: string
}

export const courseDisciplineLabels = {
  auto: '自动判断',
  computing: '计算机与信息技术',
  humanities: '人文与社会科学',
  business: '经济与管理',
  engineering: '工程与技术',
} as const

export const courseMethodLabels = {
  auto: '自动判断',
  experiment: '实验研究',
  modeling: '建模',
  simulation: '仿真',
  clinical: '临床研究',
  regression: '回归分析',
  survey: '问卷调查',
  qualitative: '质性研究',
  textual: '文本分析',
  mixed: '混合方法',
} as const

export const courseReportTypeLabels = {
  auto: '自动判断',
  project: '项目报告',
  experiment: '实验报告',
  survey: '调研报告',
  literature: '文献报告',
} as const

export const courseCharacterMin = 1_000
export const courseCharacterMax = 30_000
export const courseRequirementsLimit = 4 * 1024
export const workflowMaterialLimit = 4
export const defaultSoftwareVersion = 'V1.0'
export const softwareVersionLimit = 64
export const softwareDescriptionLimit = 4 * 1024

const utf8 = new TextEncoder()

export function isSoftwareVersion(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && !/\p{Cc}/u.test(value)
    && utf8.encode(value).length <= softwareVersionLimit
}

export function isSoftwareDescription(value: unknown): value is string {
  return typeof value === 'string' && !value.includes('\0') && utf8.encode(value).length <= softwareDescriptionLimit
}

export interface CourseProfile {
  language: 'zh'
  targetCharacters: number
  discipline: keyof typeof courseDisciplineLabels
  method?: keyof typeof courseMethodLabels
  reportType?: keyof typeof courseReportTypeLabels
}

export type CourseOptions = Partial<Omit<CourseProfile, 'language'>> & {
  requirements?: string
  materialIds?: string[]
}

interface WorkflowMaterial {
  materialId: string
  name: string
  sha256: string
  format?: 'word' | 'archive'
}

export interface CourseSettings extends CourseProfile {
  requirements?: string
  materialIds?: string[]
  materials?: (WorkflowMaterial & { format?: 'word' })[]
}

export interface SoftwareOptions {
  version?: string
  description?: string
  materialIds?: string[]
}

export interface SoftwareSettings extends SoftwareOptions {
  version: string
  materials?: WorkflowMaterial[]
}

export interface WorkflowRun extends WorkflowRunSummary {
  workflowKey: string
  revision: number
  checkpoints: boolean
  confirmEachStep?: boolean
  targetPaperCount?: number
  sourceCount?: number
  language?: 'zh' | 'en'
  acceptStepKey?: string
  skipStepKey?: string
  acceptedArtifactId?: string
  topicChoice?: WorkflowTopicChoiceData
  proposal?: ProposalTargets
  course?: CourseSettings
  software?: SoftwareSettings
  scope: { startStep: string; endStep: string }
  steps: WorkflowStep[]
}

export interface WorkflowSnapshot {
  run: WorkflowRun | null
  modelConfigured: boolean
  softwarePdfConfigured?: boolean
  steps: WorkflowStepDefinition[]
}

export interface StartWorkflowInput {
  requestId: string
  checkpoints: boolean
  targetPaperCount?: number
  paper?: { language: 'zh' | 'en'; outline?: string }
  topic?: { requirements?: string }
  proposal?: ProposalOptions
  course?: CourseOptions
  software?: SoftwareOptions
  throughStep?: string
}

export interface WorkflowRunActionInput {
  requestId: string
  runId: string
  expectedRevision: number
}

export interface ContinueWorkflowInput extends WorkflowRunActionInput {
  throughStep?: string
}

export interface SelectWorkflowTopicInput extends WorkflowRunActionInput {
  candidateNumber: number
}

export interface RerunWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
}

export interface AcceptWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
  sourceVersionId: string
}

export interface SkipWorkflowInput extends WorkflowRunActionInput {
  stepKey: string
  reason: string
}

export function isWorkflowActive(run: WorkflowRun | null) {
  const status = run?.status
  return status === 'QUEUED' || status === 'RUNNING' || status === 'PAUSING' || status === 'CANCELLING'
}

function isStepDefinition(value: unknown): value is WorkflowStepDefinition {
  const step = value as Partial<WorkflowStepDefinition> | null
  return !!step && typeof step.key === 'string' && /^[a-z][a-z0-9-]*$/.test(step.key) && typeof step.title === 'string'
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isArtifactSource(value: unknown): value is WorkflowArtifactSource {
  const source = value as Partial<WorkflowArtifactSource> | null
  return !!source && isPathId(source.versionId) && typeof source.path === 'string' && source.path.length > 0
    && typeof source.sha256 === 'string' && /^[a-f\d]{64}$/i.test(source.sha256)
}

function isStep(value: unknown): value is WorkflowStep {
  const step = value as Partial<WorkflowStep> | null
  return !!step && typeof step.status === 'string' && Object.hasOwn(stepStatusLabels, step.status)
    && (step.message === undefined || typeof step.message === 'string')
    && (step.artifactId === undefined || isPathId(step.artifactId))
    && (step.artifactCount === undefined || (Number.isSafeInteger(step.artifactCount) && step.artifactCount >= 0))
    && (step.source === undefined || isArtifactSource(step.source))
    && isStepDefinition(step)
}

function isWorkflowArtifact(value: unknown): value is WorkflowArtifact {
  const artifact = value as Partial<WorkflowArtifact> | null
  return !!artifact && isPathId(artifact.artifactId) && typeof artifact.current === 'boolean'
    && typeof artifact.createdAt === 'string' && Number.isFinite(Date.parse(artifact.createdAt))
    && (artifact.source === undefined || isArtifactSource(artifact.source))
}

function isWorkflowArtifactPage(value: unknown): value is WorkflowArtifactPage {
  const page = value as Partial<WorkflowArtifactPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isWorkflowArtifact)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function isScope(value: unknown, steps: WorkflowStep[]) {
  const scope = value as Partial<WorkflowRun['scope']> | null
  if (!scope) return false
  const start = steps.findIndex(({ key }) => key === scope.startStep)
  const end = steps.findIndex(({ key }) => key === scope.endStep)
  return start >= 0 && end >= start
}

function isRunSummary(value: unknown): value is WorkflowRunSummary {
  const run = value as Partial<WorkflowRunSummary> | null
  return !!run && isPathId(run.runId)
    && typeof run.status === 'string' && Object.hasOwn(runStatusLabels, run.status)
    && typeof run.createdAt === 'string' && Number.isFinite(Date.parse(run.createdAt))
}

function isRunPage(value: unknown): value is WorkflowRunPage {
  const page = value as Partial<WorkflowRunPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isRunSummary)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function isTopicCandidate(value: unknown): value is WorkflowTopicCandidate {
  const candidate = value as Partial<WorkflowTopicCandidate> | null
  return !!candidate && typeof candidate.number === 'number' && Number.isSafeInteger(candidate.number) && candidate.number > 0
    && typeof candidate.noveltyScore === 'number' && Number.isInteger(candidate.noveltyScore)
    && candidate.noveltyScore >= 0 && candidate.noveltyScore <= 10
    && [candidate.title, candidate.summary, candidate.strength, candidate.risk, candidate.effort]
      .every((text) => typeof text === 'string')
    && (candidate.evidenceKeys === undefined || (Array.isArray(candidate.evidenceKeys)
      && candidate.evidenceKeys.every((key) => typeof key === 'string')))
}

function isTopicChoice(value: unknown): value is WorkflowTopicChoiceData {
  const choice = value as Partial<WorkflowTopicChoiceData> | null
  return !!choice && typeof choice.recommendationReason === 'string'
    && Array.isArray(choice.candidates) && choice.candidates.every(isTopicCandidate)
    && choice.candidates.some(({ number }) => number === choice.recommendedNumber)
    && (choice.selectedNumber === undefined || choice.candidates.some(({ number }) => number === choice.selectedNumber))
}

function isProposalTargets(value: unknown): value is ProposalTargets {
  const targets = value as Partial<ProposalTargets> | null
  return !!targets && (targets.degree === 'bachelor' || targets.degree === 'master' || targets.degree === 'doctor')
    && (targets.degreeType === 'academic' || targets.degreeType === 'professional')
    && typeof targets.targetCharacters === 'number' && Number.isInteger(targets.targetCharacters)
    && targets.targetCharacters >= 500 && targets.targetCharacters <= 30_000
    && typeof targets.targetSources === 'number' && Number.isInteger(targets.targetSources)
    && targets.targetSources >= 1 && targets.targetSources <= 80
}

function hasLabel(labels: Record<string, string>, key: unknown) {
  return typeof key === 'string' && Object.hasOwn(labels, key)
}

export function isCourseProfile(value: unknown): value is CourseProfile {
  const profile = value as Partial<CourseProfile> | null
  return !!profile && profile.language === 'zh'
    && typeof profile.targetCharacters === 'number' && Number.isInteger(profile.targetCharacters)
    && profile.targetCharacters >= courseCharacterMin && profile.targetCharacters <= courseCharacterMax
    && hasLabel(courseDisciplineLabels, profile.discipline)
    && (profile.method === undefined || hasLabel(courseMethodLabels, profile.method))
    && (profile.reportType === undefined || hasLabel(courseReportTypeLabels, profile.reportType))
}

function isWorkflowMaterial(value: unknown, allowArchives: boolean): value is WorkflowMaterial {
  const material = value as Partial<WorkflowMaterial> | null
  return !!material && isPathId(material.materialId) && typeof material.name === 'string' && material.name.length > 0
    && typeof material.sha256 === 'string' && /^[a-f\d]{64}$/i.test(material.sha256)
    && (material.format === undefined || material.format === 'word' || (allowArchives && material.format === 'archive'))
}

function isCourseSettings(value: unknown): value is CourseSettings {
  if (!isCourseProfile(value)) return false
  const { requirements, materialIds, materials } = value as CourseSettings
  return (requirements === undefined || typeof requirements === 'string')
    && (materialIds === undefined || (Array.isArray(materialIds) && materialIds.every(isPathId)))
    && (materials === undefined || (Array.isArray(materials) && materials.every((material) => isWorkflowMaterial(material, false))))
}

function isSoftwareSettings(value: unknown): value is SoftwareSettings {
  const settings = value as Partial<SoftwareSettings> | null
  return !!settings && isSoftwareVersion(settings.version)
    && (settings.description === undefined || isSoftwareDescription(settings.description))
    && (settings.materialIds === undefined || (Array.isArray(settings.materialIds)
      && settings.materialIds.length <= workflowMaterialLimit && settings.materialIds.every(isPathId)))
    && (settings.materials === undefined || (Array.isArray(settings.materials)
      && settings.materials.length <= workflowMaterialLimit
      && settings.materials.every((material) => isWorkflowMaterial(material, true))))
}

function isRun(value: unknown): value is WorkflowRun {
  const run = value as Partial<WorkflowRun> | null
  return !!run && typeof run.workflowKey === 'string' && typeof run.revision === 'number' && Number.isSafeInteger(run.revision)
    && typeof run.checkpoints === 'boolean'
    && (run.confirmEachStep === undefined || typeof run.confirmEachStep === 'boolean')
    && (run.sourceCount === undefined || Number.isSafeInteger(run.sourceCount))
    && (run.targetPaperCount === undefined || Number.isSafeInteger(run.targetPaperCount))
    && (run.language === undefined || run.language === 'zh' || run.language === 'en')
    && (run.acceptedArtifactId === undefined || isPathId(run.acceptedArtifactId))
    && Array.isArray(run.steps) && run.steps.every(isStep)
    && (run.acceptStepKey === undefined || run.steps.some((step) => step.key === run.acceptStepKey))
    && (run.skipStepKey === undefined || run.steps.some((step) => step.key === run.skipStepKey))
    && (run.topicChoice === undefined || isTopicChoice(run.topicChoice))
    && (run.proposal === undefined || isProposalTargets(run.proposal))
    && (run.course === undefined || isCourseSettings(run.course))
    && (run.software === undefined || isSoftwareSettings(run.software))
    && isScope(run.scope, run.steps) && isRunSummary(run)
}

function isSnapshot(value: unknown): value is WorkflowSnapshot {
  const snapshot = value as Partial<WorkflowSnapshot> | null
  return !!snapshot && typeof snapshot.modelConfigured === 'boolean'
    && (snapshot.softwarePdfConfigured === undefined || typeof snapshot.softwarePdfConfigured === 'boolean')
    && (snapshot.run === null || isRun(snapshot.run))
    && Array.isArray(snapshot.steps) && snapshot.steps.every(isStepDefinition)
}

function workflowPath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/workflow`
}

export function getWorkflow(projectId: string, signal: AbortSignal) {
  return request(workflowPath(projectId), isSnapshot, { cache: 'no-store', signal })
}

export function listWorkflowRuns(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(`${workflowPath(projectId)}/runs${query}`, isRunPage, { cache: 'no-store', signal })
}

export function getWorkflowRun(projectId: string, runId: string, signal: AbortSignal) {
  return request(`${workflowPath(projectId)}/runs/${encodeURIComponent(runId)}`,
    (value): value is WorkflowRun => isRun(value) && value.runId === runId, { cache: 'no-store', signal })
}

function postRun(path: string, input: StartWorkflowInput | WorkflowRunActionInput, signal: AbortSignal) {
  return request(path, isRun, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}

export function startWorkflow(projectId: string, input: StartWorkflowInput, signal: AbortSignal) {
  return postRun(workflowPath(projectId), input, signal)
}

export function continueWorkflow(projectId: string, input: ContinueWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/continue`, input, signal)
}

export function pauseWorkflow(projectId: string, input: WorkflowRunActionInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/pause`, input, signal)
}

export function cancelWorkflow(projectId: string, input: WorkflowRunActionInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/cancel`, input, signal)
}

export function rerunWorkflow(projectId: string, input: RerunWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/rerun`, input, signal)
}

export function acceptWorkflow(projectId: string, input: AcceptWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/accept`, input, signal)
}

export function skipWorkflow(projectId: string, input: SkipWorkflowInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/skip`, input, signal)
}

export function selectWorkflowTopic(projectId: string, input: SelectWorkflowTopicInput, signal: AbortSignal) {
  return postRun(`${workflowPath(projectId)}/select-topic`, input, signal)
}

function workflowStepPath(projectId: string, runId: string, key: string) {
  return `${workflowPath(projectId)}/${encodeURIComponent(runId)}/steps/${encodeURIComponent(key)}`
}

export function workflowArtifactPath(projectId: string, runId: string, key: string, artifactId?: string) {
  const step = workflowStepPath(projectId, runId, key)
  return artifactId === undefined ? `${step}/artifact` : `${step}/artifacts/${encodeURIComponent(artifactId)}`
}

export type SoftwarePdfStep = 'manuscript' | 'program'

export function isSoftwarePdfStep(key: string): key is SoftwarePdfStep {
  return key === 'manuscript' || key === 'program'
}

async function fetchArtifact(path: string, accept: string, label: string, signal: AbortSignal) {
  const response = await fetch(path, {
    mode: 'same-origin', redirect: 'error', cache: 'no-store', headers: { Accept: accept }, signal,
  })
  if (!response.ok) {
    const body = await response.json().catch(() => null) as { error?: { message?: unknown; code?: unknown } } | null
    const message = body?.error?.message
    const code = body?.error?.code
    throw new ProjectApiError(typeof message === 'string' && message.trim() ? message : `无法读取${label}（HTTP ${response.status}）。`,
      response.status, typeof code === 'string' ? code : undefined)
  }
  return response
}

async function sha256Hex(bytes: ArrayBuffer) {
  if (!globalThis.crypto?.subtle) throw new ProjectApiError('当前浏览器环境无法校验成果摘要。', 0)
  const hash = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

export async function readWorkflowSoftwarePdf(projectId: string, runId: string, key: SoftwarePdfStep, artifactId: string, signal: AbortSignal) {
  if (![projectId, runId, artifactId].every(isPathId)) throw new ProjectApiError('成果版本链接无效。', 400)
  const sourcePath = workflowArtifactPath(projectId, runId, key, artifactId)
  const response = await fetchArtifact(`${sourcePath}/pdf`, 'application/pdf', 'PDF', signal)
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase()
  if (contentType !== 'application/pdf') throw new ProjectApiError('成果服务返回的文件无法识别为 PDF。', 422)

  const sourceDigest = response.headers.get('X-Source-SHA256')
  if (response.headers.get('X-Source-Artifact-ID') !== artifactId || !sourceDigest || !/^[a-f\d]{64}$/i.test(sourceDigest)) {
    throw new ProjectApiError('无法核对 PDF 的成果版本和来源摘要。', 409)
  }

  const original = await fetchArtifact(sourcePath, key === 'manuscript' ? 'text/markdown' : 'text/plain', '原成果', signal)
  const [bytes, pdf] = await Promise.all([original.arrayBuffer(), response.blob()])
  signal.throwIfAborted()
  if (sourceDigest.toLowerCase() !== await sha256Hex(bytes)) {
    throw new ProjectApiError('PDF 的来源摘要与所选成果版本不符。', 409)
  }
  if (await pdf.slice(0, 5).text() !== '%PDF-') throw new ProjectApiError('PDF 文件内容无法识别。', 422)
  signal.throwIfAborted()
  return pdf
}

export async function readWorkflowSoftwareBundle(projectId: string, runId: string, revision: number, signal: AbortSignal) {
  if (!isPathId(projectId) || !isPathId(runId) || !Number.isSafeInteger(revision) || revision < 1) {
    throw new ProjectApiError('登记材料的运行或修订无效。', 400)
  }
  const query = new URLSearchParams({ revision: String(revision) })
  const path = `${workflowPath(projectId)}/runs/${encodeURIComponent(runId)}/bundle?${query}`
  const response = await fetchArtifact(path, 'application/zip', '登记材料 ZIP', signal)
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase()
  if (contentType !== 'application/zip') throw new ProjectApiError('成果服务返回的文件无法识别为 ZIP。', 422)
  if (response.headers.get('X-Workflow-Run-ID') !== runId
    || response.headers.get('X-Workflow-Revision') !== String(revision)) {
    throw new ProjectApiError('登记材料 ZIP 的运行或修订不符。请刷新后重试。', 409)
  }
  const digest = response.headers.get('X-Bundle-SHA256')
  if (!digest || !/^[a-f\d]{64}$/i.test(digest)) {
    throw new ProjectApiError('无法核对登记材料 ZIP 的摘要。', 422)
  }
  const bytes = await response.arrayBuffer()
  signal.throwIfAborted()
  if (digest.toLowerCase() !== await sha256Hex(bytes)) {
    throw new ProjectApiError('登记材料 ZIP 的摘要校验失败。请重试。', 422)
  }
  signal.throwIfAborted()
  return new Blob([bytes], { type: contentType })
}

export function listWorkflowArtifacts(projectId: string, runId: string, key: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(`${workflowStepPath(projectId, runId, key)}/artifacts${query}`, isWorkflowArtifactPage, { cache: 'no-store', signal })
}

export function saveManuscript(projectId: string, runId: string, signal: AbortSignal, artifactId?: string) {
  const query = artifactId ? `?${new URLSearchParams({ artifactId })}` : ''
  return request(`${workflowPath(projectId)}/${encodeURIComponent(runId)}/manuscript${query}`, isFileRecord, { method: 'POST', signal })
}
