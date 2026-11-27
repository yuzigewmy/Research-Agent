import { isMaterial } from './materials'
import { ProjectApiError, request } from './projects'

export interface QuestionBranch {
  option: string
  target: string
}

export interface Question {
  id: string
  label: string
  type: 'single' | 'text'
  required: boolean
  options?: string[]
  branches?: QuestionBranch[]
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
export const questionnaireBranchEnd = '__END__'
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

function isBranch(value: unknown): value is QuestionBranch {
  return isRecord(value) && typeof value.option === 'string' && typeof value.target === 'string'
}

function isQuestion(value: unknown): value is Question {
  if (!isRecord(value)) return false
  const { id, label, type, required, options, branches } = value
  return typeof id === 'string' && questionId.test(id) && typeof label === 'string'
    && typeof required === 'boolean' && (type === 'single' || type === 'text')
    && (options === undefined || (Array.isArray(options) && options.every((option) => typeof option === 'string')))
    && (branches === undefined || (Array.isArray(branches) && branches.every(isBranch)))
}

export function normalizeQuestionnaire({ title, intro, questions }: QuestionnaireDefinition): QuestionnaireDefinition {
  return { title: title.trim(), intro: intro.trim(), questions: questions.map(({ id, label, type, required, options, branches }) => {
    const normalizedBranches = branches?.map(({ option, target }) => ({ option: option.trim(), target: target.trim() }))
    return {
      id, label: label.trim(), type, required,
      options: type === 'single' ? options?.map((option) => option.trim()).filter(Boolean) : undefined,
      ...(normalizedBranches?.length ? { branches: normalizedBranches } : {}),
    }
  }) }
}

function isShortText(text: string, limit: number) {
  return !!text.trim() && !/\p{Cc}/u.test(text) && questionnaireTextFits(text, limit)
}

export function questionnaireProblem({ title, intro, questions }: QuestionnaireDefinition) {
  if (!isShortText(title, 240)) return '请输入问卷标题，最多 240 个 UTF-8 字节，且不能含控制字符。'
  if (!questionnaireTextFits(intro, 2 * 1024)) return '卷首语最多 2 KiB（UTF-8），且不能含空字符。'
  if (questions.length < 1 || questions.length > questionnaireQuestionLimit) return '请添加 1 至 20 道题。'
  const idIndex = new Map<string, number>()
  for (const [index, question] of questions.entries()) {
    if (idIndex.has(question.id)) return '题号必须唯一。'
    idIndex.set(question.id, index)
  }
  const optionSets: Array<Set<string>> = []
  for (const [index, question] of questions.entries()) {
    const prefix = `第 ${index + 1} 题`
    if (!questionId.test(question.id) || !isShortText(question.label, 220)) {
      return `${prefix}请填写题干，最多 220 个 UTF-8 字节，且不能含控制字符。`
    }
    const options = question.options ?? []
    if (question.type === 'text') {
      if (options.length) return `${prefix}是文本题，无需填写选项。`
      optionSets.push(new Set())
    } else if (options.length < 2 || options.length > 12 || new Set(options).size !== options.length
      || options.some((option) => !isShortText(option, 120))) {
      return `${prefix}请填写 2 至 12 个不同的选项，每项最多 120 个 UTF-8 字节，且不能含控制字符。`
    } else {
      optionSets.push(new Set(options))
    }
  }
  for (const [index, question] of questions.entries()) {
    const branches = question.branches ?? []
    if (!branches.length) continue
    const prefix = `第 ${index + 1} 题`
    if (question.type !== 'single') return `${prefix}是文本题，不支持配置跳转条件。`
    const seen = new Set<string>()
    for (const branch of branches) {
      const option = branch.option.trim()
      const target = branch.target.trim()
      if (!optionSets[index].has(option)) return `${prefix}的跳转条件必须选择本题已有选项。`
      if (seen.has(option)) return `${prefix}同一选项只能配置一条跳转条件。`
      seen.add(option)
      if (target !== questionnaireBranchEnd) {
        const targetIndex = idIndex.get(target)
        if (targetIndex === undefined) return `${prefix}的跳转目标题目不存在。`
        if (targetIndex <= index) return `${prefix}的跳转目标必须是排在本题之后的题目或结束问卷。`
      }
    }
  }
}

export interface QuestionnairePath {
  visibleIds: Set<string>
  visibleQuestions: Array<{ question: Question; index: number }>
  skippedQuestions: Array<{ question: Question; index: number }>
  triggeredBranches: Map<string, QuestionBranch>
  endedEarly: boolean
  endedByQuestionId?: string
}

export function evaluateQuestionnairePath(
  questions: Question[],
  answers: ReadonlyMap<string, string>,
): QuestionnairePath {
  const idIndex = new Map<string, number>()
  questions.forEach((question, index) => idIndex.set(question.id, index))
  const visibleIds = new Set<string>()
  const visibleQuestions: Array<{ question: Question; index: number }> = []
  const triggeredBranches = new Map<string, QuestionBranch>()
  let endedEarly = false
  let endedByQuestionId: string | undefined

  for (let index = 0; index < questions.length;) {
    const question = questions[index]
    visibleIds.add(question.id)
    visibleQuestions.push({ question, index })
    let next = index + 1
    if (question.type === 'single' && question.branches?.length) {
      const answer = (answers.get(question.id) ?? '').trim()
      if (answer) {
        const matched = question.branches.find((branch) => branch.option === answer)
        if (matched) {
          triggeredBranches.set(question.id, matched)
          if (matched.target === questionnaireBranchEnd) {
            endedEarly = true
            endedByQuestionId = question.id
            break
          }
          const targetIndex = idIndex.get(matched.target)
          if (targetIndex !== undefined && targetIndex > index) {
            next = targetIndex
          }
        }
      }
    }
    index = next
  }

  const skippedQuestions = questions
    .map((question, index) => ({ question, index }))
    .filter(({ question }) => !visibleIds.has(question.id))

  return { visibleIds, visibleQuestions, skippedQuestions, triggeredBranches, endedEarly, endedByQuestionId }
}

export function formatBranchTarget(target: string, questions: Question[]) {
  if (target === questionnaireBranchEnd) return '结束问卷'
  const index = questions.findIndex((question) => question.id === target)
  if (index < 0) return `未知题目（${target || '未选择'}）`
  const label = questions[index].label.trim()
  return label ? `第 ${index + 1} 题 · ${label}` : `第 ${index + 1} 题（${target}）`
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
