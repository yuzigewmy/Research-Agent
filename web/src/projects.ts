export const templates = {
  freeform: '自由研究',
  topic: '研究选题',
  proposal: '开题报告',
  literature_review: '文献综述',
  paper: '论文写作',
  course_paper: '课程论文',
  course_report: '课程报告',
  software_registration: '软件著作权材料',
  patent_disclosure: '专利技术交底',
  interactive_survey: '互动问卷',
  engineering_project: '工程项目',
  experiment: '实验计划',
  automatic_review: '多轮审稿',
  full_research: '科研全流程',
  chinese_paper: '中文论文',
  humanities_paper: '人文社科论文',
  thesis: '学位论文',
  existing_materials: '已有材料成稿',
  market_research: '市场调查',
  math_modeling: '数学建模竞赛',
  big_data_contest: '大数据竞赛',
} as const

export const projectStartModeLabels = {
  save_only: '仅保存项目（默认）',
  start_workflow: '创建后立即启动工作流',
  collaborate: '创建后进入对话协同',
} as const

export type ProjectStartMode = keyof typeof projectStartModeLabels

export const projectStartModeHelp: Record<ProjectStartMode, string> = {
  save_only: '仅保存项目设置，进入项目详情后再决定下一步。',
  start_workflow: '创建后按默认参数立即启动工作流，并在项目详情呈现运行状态。',
  collaborate: '自动将默认协作设为「建议任务，确认后运行」，并聚焦到对话区。',
}

export type TemplateId = keyof typeof templates
export type CourseTemplateId = Extract<TemplateId, 'course_paper' | 'course_report'>
export type ProjectStatus = 'READY' | 'ARCHIVED'

export const turnModeLabels = {
  CHAT: '仅问答',
  CONFIRM: '建议任务，确认后运行',
  CONFIRM_ALL: '所有任务先确认',
  CONFIRM_STEPS: '整步先确认',
  AUTO: '自动推进',
} as const

export type TurnMode = keyof typeof turnModeLabels

export const turnModeHelp: Record<TurnMode, string> = {
  CHAT: '只回答本轮问题。',
  CONFIRM: '先回答问题，并将建议的任务交给你确认。',
  CONFIRM_ALL: '每项任务都要确认。对话发起的工作流按步骤确认执行。',
  CONFIRM_STEPS: '研究计划、稿件检查和选区修改建议可直接入队。工作流区间需要确认，改稿应用由你决定。',
  AUTO: '符合条件的任务和工作流直接入队。模板检查点仍需确认，改稿应用由你决定。',
}

export function isTurnMode(value: unknown): value is TurnMode {
  return typeof value === 'string' && Object.hasOwn(turnModeLabels, value)
}

export interface Project {
  projectId: string
  title: string
  displayName: string
  templateId: TemplateId
  status: ProjectStatus
  revision: number
  createdAt: string
  goal?: string
  deliverables?: string
  defaultTurnMode?: TurnMode
  maxModelRuns?: number
  maxOutputTokens?: number
  modelRunsUsed?: number
}

export interface ProjectPage {
  items: Project[]
  nextCursor?: string
}

export interface CreateProject {
  requestId: string
  title: string
  templateId: TemplateId
  startMode?: ProjectStartMode
}

export interface ProjectFilters {
  status: ProjectStatus
  q: string
}

export type ProjectChanges = {
  title?: string
  displayName?: string
  status?: ProjectStatus
  defaultTurnMode?: TurnMode
  maxModelRuns?: number
  maxOutputTokens?: number
} & (
  | { title: string }
  | { displayName: string }
  | { status: ProjectStatus }
  | { defaultTurnMode: TurnMode }
  | { maxModelRuns: number }
  | { maxOutputTokens: number }
)

export type UpdateProject = ProjectChanges & { requestId: string; expectedRevision: number }

const endpoint = '/api/v1/projects'

export class ProjectApiError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message)
    this.name = 'ProjectApiError'
  }
}

export function isTemplateId(value: unknown): value is TemplateId {
  return typeof value === 'string' && Object.hasOwn(templates, value)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function isProject(value: unknown): value is Project {
  return isRecord(value)
    && typeof value.projectId === 'string' && value.projectId.length > 0
    && typeof value.title === 'string' && value.title.trim().length > 0
    && typeof value.displayName === 'string'
    && isTemplateId(value.templateId)
    && (value.status === 'READY' || value.status === 'ARCHIVED')
    && typeof value.revision === 'number' && Number.isSafeInteger(value.revision)
    && typeof value.createdAt === 'string' && Number.isFinite(Date.parse(value.createdAt))
    && (value.goal === undefined || typeof value.goal === 'string')
    && (value.deliverables === undefined || typeof value.deliverables === 'string')
    && (value.defaultTurnMode === undefined || isTurnMode(value.defaultTurnMode))
    && (value.maxModelRuns === undefined || (typeof value.maxModelRuns === 'number' && Number.isSafeInteger(value.maxModelRuns) && value.maxModelRuns >= 0))
    && (value.maxOutputTokens === undefined || (typeof value.maxOutputTokens === 'number' && Number.isSafeInteger(value.maxOutputTokens) && value.maxOutputTokens >= 0))
    && (value.modelRunsUsed === undefined || (typeof value.modelRunsUsed === 'number' && Number.isSafeInteger(value.modelRunsUsed) && value.modelRunsUsed >= 0))
}

function isProjectPage(value: unknown): value is ProjectPage {
  return isRecord(value)
    && Array.isArray(value.items) && value.items.every(isProject)
    && (value.nextCursor === undefined || typeof value.nextCursor === 'string')
}

export async function request<T>(
  path: string,
  validate: (value: unknown) => value is T,
  init: RequestInit = {},
): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  let response: Response
  try {
    response = await fetch(path, { ...init, headers, mode: 'same-origin' })
  } catch (error) {
    if (init.signal?.aborted) throw error
    throw new ProjectApiError('无法连接到项目服务，请检查连接后重试。', 0)
  }

  const body: unknown = await response.json().catch(() => null)
  if (!response.ok) {
    const error = isRecord(body) && isRecord(body.error) ? body.error : undefined
    const message = typeof error?.message === 'string' && error.message.trim()
      ? error.message
      : `项目服务返回错误（HTTP ${response.status}），请重试。`
    const code = typeof error?.code === 'string' ? error.code : undefined
    throw new ProjectApiError(message, response.status, code)
  }
  if (!validate(body)) throw new ProjectApiError('项目服务返回了无法读取的数据。', response.status)
  return body
}

export function listProjects(filters: ProjectFilters, cursor: string | undefined, signal: AbortSignal) {
  const params = new URLSearchParams({ status: filters.status })
  if (filters.q) params.set('q', filters.q)
  if (cursor) params.set('cursor', cursor)
  return request(`${endpoint}?${params}`, isProjectPage, { signal })
}

function projectPath(projectId: string) {
  // URL parsing folds dot segments even when their dots are percent-encoded.
  if (!projectId || projectId === '.' || projectId === '..') throw new ProjectApiError('项目链接无效。', 400)
  return `${endpoint}/${encodeURIComponent(projectId)}`
}

export async function getProject(projectId: string, signal: AbortSignal) {
  return request(projectPath(projectId), isProject, { signal })
}

export async function updateProject(projectId: string, input: UpdateProject, signal: AbortSignal) {
  return request(projectPath(projectId), isProject, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal,
  })
}

export function createProject(input: CreateProject) {
  return request(endpoint, isProject, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export function errorMessage(error: unknown): string {
  return error instanceof ProjectApiError ? error.message : '操作未完成，请重试。'
}
