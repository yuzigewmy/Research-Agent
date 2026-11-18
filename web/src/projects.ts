export const templates = {
  freeform: '自由研究',
  literature_review: '文献综述',
  paper: '论文写作',
  experiment: '实验计划',
} as const

export type TemplateId = keyof typeof templates

export interface Project {
  projectId: string
  title: string
  templateId: TemplateId
  status: 'READY'
  revision: number
  createdAt: string
}

export interface ProjectPage {
  items: Project[]
  nextCursor?: string
}

export interface CreateProject {
  requestId: string
  title: string
  templateId: TemplateId
}

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

function isProject(value: unknown): value is Project {
  return isRecord(value)
    && typeof value.projectId === 'string' && value.projectId.length > 0
    && typeof value.title === 'string' && value.title.trim().length > 0
    && isTemplateId(value.templateId)
    && value.status === 'READY'
    && typeof value.revision === 'number' && Number.isSafeInteger(value.revision)
    && typeof value.createdAt === 'string' && Number.isFinite(Date.parse(value.createdAt))
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

export function listProjects(cursor: string | undefined, signal: AbortSignal) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(`${endpoint}${query}`, isProjectPage, { signal })
}

export function getProject(projectId: string, signal: AbortSignal) {
  // URL parsing folds dot segments even when their dots are percent-encoded.
  if (!projectId || projectId === '.' || projectId === '..') {
    return Promise.reject(new ProjectApiError('项目链接无效。', 400))
  }
  return request(`${endpoint}/${encodeURIComponent(projectId)}`, isProject, { signal })
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
