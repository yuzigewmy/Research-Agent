import { isMaterial } from './materials'
import { ProjectApiError, request } from './projects'

export interface Question {
  id: string
  label: string
  type: 'single' | 'text'
  required: boolean
  options?: string[]
}

export interface QuestionnaireDefinition {
  title: string
  intro: string
  questions: Question[]
}

export interface QuestionnaireSnapshot extends QuestionnaireDefinition {
  revision: number
  status: 'DRAFT' | 'PUBLISHED' | 'CLOSED'
  publicId?: string
  responseCount: number
}

export interface QuestionnaireReceipt {
  responseId: string
  submittedAt: string
}

export const questionnaireQuestionLimit = 20
export const questionnaireAnswerLimit = 320
const questionId = /^[A-Za-z][A-Za-z0-9_]{0,19}$/
const utf8 = new TextEncoder()

export function questionnaireTextFits(text: string, limit: number) {
  return !text.includes('\0') && utf8.encode(text).length <= limit
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isQuestion(value: unknown): value is Question {
  if (!isRecord(value)) return false
  const { id, label, type, required, options } = value
  return typeof id === 'string' && questionId.test(id) && typeof label === 'string'
    && typeof required === 'boolean' && (type === 'single' || type === 'text')
    && (options === undefined || (Array.isArray(options) && options.every((option) => typeof option === 'string')))
}

export function normalizeQuestionnaire({ title, intro, questions }: QuestionnaireDefinition): QuestionnaireDefinition {
  return { title: title.trim(), intro: intro.trim(), questions: questions.map(({ id, label, type, required, options }) => ({
    id, label: label.trim(), type, required,
    options: type === 'single' ? options?.map((option) => option.trim()).filter(Boolean) : undefined,
  })) }
}

function isShortText(text: string, limit: number) {
  return !!text.trim() && !/\p{Cc}/u.test(text) && questionnaireTextFits(text, limit)
}

export function questionnaireProblem({ title, intro, questions }: QuestionnaireDefinition) {
  if (!isShortText(title, 240)) return '请输入问卷标题，最多 240 个 UTF-8 字节，且不能含控制字符。'
  if (!questionnaireTextFits(intro, 2 * 1024)) return '卷首语最多 2 KiB（UTF-8），且不能含空字符。'
  if (questions.length < 1 || questions.length > questionnaireQuestionLimit) return '请添加 1 至 20 道题。'
  if (new Set(questions.map(({ id }) => id)).size !== questions.length) return '题号必须唯一。'
  for (const [index, question] of questions.entries()) {
    const prefix = `第 ${index + 1} 题`
    if (!questionId.test(question.id) || !isShortText(question.label, 220)) {
      return `${prefix}请填写题干，最多 220 个 UTF-8 字节，且不能含控制字符。`
    }
    const options = question.options ?? []
    if (question.type === 'text') {
      if (options.length) return `${prefix}是文本题，无需填写选项。`
    } else if (options.length < 2 || options.length > 12 || new Set(options).size !== options.length
      || options.some((option) => !isShortText(option, 120))) {
      return `${prefix}请填写 2 至 12 个不同的选项，每项最多 120 个 UTF-8 字节，且不能含控制字符。`
    }
  }
}

function hasDefinition(value: unknown): value is QuestionnaireDefinition {
  return isRecord(value) && typeof value.title === 'string' && typeof value.intro === 'string'
    && Array.isArray(value.questions) && value.questions.every(isQuestion)
}

function isDefinition(value: unknown): value is QuestionnaireDefinition {
  return hasDefinition(value) && !questionnaireProblem(value)
}

function isSnapshot(value: unknown): value is QuestionnaireSnapshot {
  if (!isRecord(value)) return false
  const { revision, status, responseCount, publicId } = value
  return typeof revision === 'number' && Number.isSafeInteger(revision) && revision >= 0
    && typeof responseCount === 'number' && Number.isSafeInteger(responseCount) && responseCount >= 0
    && (status === 'DRAFT' || status === 'PUBLISHED' || status === 'CLOSED')
    && (status === 'DRAFT' ? publicId === undefined : isId(publicId))
    && (revision === 0 ? status === 'DRAFT' && hasDefinition(value) && value.questions.length === 0 : isDefinition(value))
}

export function isQuestionnaireReceipt(value: unknown): value is QuestionnaireReceipt {
  return isRecord(value) && isId(value.responseId) && typeof value.submittedAt === 'string' && Number.isFinite(Date.parse(value.submittedAt))
}

function idSegment(id: string) {
  if (!isId(id)) throw new ProjectApiError('问卷链接无效。', 400)
  return encodeURIComponent(id)
}

function adminPath(projectId: string) { return `/api/v1/projects/${idSegment(projectId)}/questionnaire` }
function publicPath(publicId: string) { return `/api/v1/questionnaires/${idSegment(publicId)}` }

function json(method: 'PUT' | 'POST', body: unknown, signal: AbortSignal): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal }
}

export async function getQuestionnaire(projectId: string, signal: AbortSignal) {
  return request(adminPath(projectId), isSnapshot, { cache: 'no-store', signal })
}

export async function saveQuestionnaire(projectId: string, expectedRevision: number, definition: QuestionnaireDefinition, signal: AbortSignal) {
  return request(adminPath(projectId), isSnapshot, json('PUT', { expectedRevision, ...definition }, signal))
}

export async function changeQuestionnaireStatus(projectId: string, action: 'publish' | 'close', expectedRevision: number, signal: AbortSignal) {
  return request(`${adminPath(projectId)}/${action}`, isSnapshot, json('POST', { expectedRevision }, signal))
}

export function questionnaireCsvPath(projectId: string) { return `${adminPath(projectId)}/responses.csv` }
export function questionnaireLink(publicId: string) { return `/?${new URLSearchParams({ questionnaire: publicId })}` }

export async function saveQuestionnaireResponses(projectId: string, signal: AbortSignal) {
  return request(`${adminPath(projectId)}/responses/material`, isMaterial, { method: 'POST', signal })
}

export async function getPublicQuestionnaire(publicId: string, signal: AbortSignal) {
  return request(publicPath(publicId), isDefinition, { cache: 'no-store', signal })
}

export async function sendQuestionnaireResponse(publicId: string, requestId: string, answers: Record<string, string>, signal: AbortSignal) {
  return request(`${publicPath(publicId)}/responses`, isQuestionnaireReceipt, json('POST', { requestId, answers }, signal))
}
