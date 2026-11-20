import { isProject, isTemplateId, ProjectApiError, request } from './projects'
import type { TemplateId } from './projects'

const endpoint = '/api/v1/project-drafts'
const statuses = ['DRAFT', 'PLANNING', 'CREATED'] as const

export interface ProjectDraftFields {
  title: string
  templateId: TemplateId
  goal: string
  deliverables: string
  clarification: string
}

export interface ProjectDraft extends ProjectDraftFields {
  draftId: string
  prompt: string
  question?: string
  status: typeof statuses[number]
  revision: number
  updatedAt: string
  message?: string
  projectId?: string
}

interface DraftPage {
  items: ProjectDraft[]
  modelConfigured: boolean
  nextCursor?: string
}

interface DraftRevision {
  requestId: string
  expectedRevision: number
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isDraft(value: unknown): value is ProjectDraft {
  const draft = value as Partial<ProjectDraft> | null
  return !!draft && isPathId(draft.draftId) && isTemplateId(draft.templateId)
    && [draft.prompt, draft.title, draft.goal, draft.deliverables, draft.clarification].every((text) => typeof text === 'string')
    && draft.status !== undefined && statuses.includes(draft.status)
    && typeof draft.revision === 'number' && Number.isSafeInteger(draft.revision) && draft.revision >= 1
    && typeof draft.updatedAt === 'string' && Number.isFinite(Date.parse(draft.updatedAt))
    && (draft.question === undefined || typeof draft.question === 'string')
    && (draft.message === undefined || typeof draft.message === 'string')
    && (draft.projectId === undefined || isPathId(draft.projectId))
}

function isDraftPage(value: unknown): value is DraftPage {
  const page = value as Partial<DraftPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isDraft)
    && typeof page.modelConfigured === 'boolean'
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function draftPath(draftId: string) {
  if (!isPathId(draftId)) throw new ProjectApiError('草稿链接无效。', 400)
  return `${endpoint}/${encodeURIComponent(draftId)}`
}

function jsonRequest(method: 'POST' | 'PATCH', input: object, signal: AbortSignal): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal }
}

export function listProjectDrafts(signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(endpoint + query, isDraftPage, { cache: 'no-store', signal })
}

export async function getProjectDraft(draftId: string, signal: AbortSignal) {
  return request(draftPath(draftId), (value): value is ProjectDraft => isDraft(value) && value.draftId === draftId,
    { cache: 'no-store', signal })
}

export function createProjectDraft(input: { requestId: string; prompt: string }, signal: AbortSignal) {
  return request(endpoint, isDraft, jsonRequest('POST', input, signal))
}

export async function saveProjectDraft(draftId: string, input: DraftRevision & ProjectDraftFields, signal: AbortSignal) {
  return request(draftPath(draftId), (value): value is ProjectDraft => isDraft(value) && value.draftId === draftId,
    jsonRequest('PATCH', input, signal))
}

export async function planProjectDraft(draftId: string, input: DraftRevision, signal: AbortSignal) {
  return request(`${draftPath(draftId)}/plan`, (value): value is ProjectDraft => isDraft(value) && value.draftId === draftId,
    jsonRequest('POST', input, signal))
}

export async function finalizeProjectDraft(draftId: string, input: DraftRevision, signal: AbortSignal) {
  return request(`${draftPath(draftId)}/finalize`, isProject, jsonRequest('POST', input, signal))
}
