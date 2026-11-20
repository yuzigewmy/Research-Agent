import type { Material } from './materials'
import { isTurnMode, ProjectApiError, request } from './projects'
import type { TurnMode } from './projects'
import type { SelectionEditRange } from './SelectionEdit'
import type { FileRecord } from './workspace'

export type { TurnMode } from './projects'

const statuses = ['RUNNING', 'COMPLETED', 'FAILED', 'INTERRUPTED', 'STOPPED'] as const
export const turnReferenceLimit = 4
export const pdfQuoteByteLimit = 8 * 1024
export const manuscriptSelectionByteLimit = 8 * 1024
const turnTaskResultLimit = 4
const utf8 = new TextEncoder()
export type TurnStatus = typeof statuses[number]
export type TurnReference = Pick<Material, 'materialId' | 'name' | 'sha256'> & { page?: number; quote?: string }
export type PdfQuote = { sha256: string; page: number; text: string }
export type ManuscriptSelectionInput = Pick<FileRecord, 'versionId'> & Pick<SelectionEditRange, 'startByte' | 'endByte'>
export type ManuscriptSelection = ManuscriptSelectionInput & Pick<FileRecord, 'path' | 'sha256'> & Pick<SelectionEditRange, 'text'>

interface TurnTaskResult {
  taskId: string
  sourceTurnId: string
  instruction: string
}

export interface TurnInput {
  requestId: string
  text: string
  referenceIds?: string[]
  pdfQuotes?: Record<string, PdfQuote>
  manuscriptSelection?: ManuscriptSelectionInput
  mode?: TurnMode
}

export interface WorkflowProposal {
  action: 'start' | 'continue'
  scope: { startStep: string; endStep: string }
  steps: { key: string; title: string }[]
  instruction: string
  checkpoints: boolean
  status: 'PROPOSED' | 'CONFIRMED' | 'CANCELLED'
  runId?: string
}

export interface Turn extends Pick<TurnInput, 'requestId' | 'text' | 'mode'> {
  references?: TurnReference[]
  manuscriptSelection?: ManuscriptSelection
  taskResults?: TurnTaskResult[]
  proposedTaskId?: string
  workflowProposal?: WorkflowProposal
  turnId: string
  answer: string
  status: TurnStatus
  message?: string
  afterSeq: number
  createdAt: string
}

export interface TurnPage {
  items: Turn[]
  nextCursor?: string
  modelConfigured: boolean
}

export type TurnEvent =
  | { seq: number; kind: 'delta'; text: string }
  | {
    seq: number
    kind: 'done'
    status: Exclude<TurnStatus, 'RUNNING'>
    message?: string
    proposedTaskId?: string
    workflowProposal?: WorkflowProposal
  }

export function isProposedTaskId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

export function isWorkflowProposal(value: unknown): value is WorkflowProposal {
  if (!value || typeof value !== 'object') return false
  const { action, scope, steps, instruction, checkpoints, status, runId } = value as Partial<WorkflowProposal>
  if ((action !== 'start' && action !== 'continue')
    || (status !== 'PROPOSED' && status !== 'CONFIRMED' && status !== 'CANCELLED')
    || typeof instruction !== 'string' || typeof checkpoints !== 'boolean'
    || (runId !== undefined && !isProposedTaskId(runId))
    || !scope || typeof scope.startStep !== 'string' || typeof scope.endStep !== 'string'
    || !Array.isArray(steps) || !steps.every((step) => step && typeof step.key === 'string' && step.key.length > 0
      && typeof step.title === 'string')) return false
  if (new Set(steps.map((step) => step.key)).size !== steps.length) return false
  const first = steps.findIndex((step) => step.key === scope.startStep)
  const last = steps.findIndex((step) => step.key === scope.endStep)
  return first >= 0 && last >= first
}

export function isManuscriptSelectionInput(value: unknown): value is ManuscriptSelectionInput {
  const selection = value as Partial<ManuscriptSelectionInput> | null
  if (!selection || !isProposedTaskId(selection.versionId) || typeof selection.startByte !== 'number'
    || typeof selection.endByte !== 'number' || !Number.isSafeInteger(selection.startByte)
    || !Number.isSafeInteger(selection.endByte) || selection.startByte < 0) return false
  const length = selection.endByte - selection.startByte
  return length > 0 && length <= manuscriptSelectionByteLimit
}

export function isManuscriptSelection(value: unknown): value is ManuscriptSelection {
  if (!isManuscriptSelectionInput(value)) return false
  const { path, sha256, text } = value as Partial<ManuscriptSelection>
  return typeof path === 'string' && /\.(md|markdown)$/i.test(path)
    && typeof sha256 === 'string' && /^[a-f\d]{64}$/i.test(sha256)
    && typeof text === 'string' && /[^\p{White_Space}]/u.test(text) && !text.includes('\0')
    && utf8.encode(text).length === value.endByte - value.startByte
}

export function manuscriptSelectionInput({ versionId, startByte, endByte }: ManuscriptSelectionInput): ManuscriptSelectionInput {
  return { versionId, startByte, endByte }
}

export function sameManuscriptSelection(left?: ManuscriptSelectionInput, right?: ManuscriptSelectionInput) {
  return left?.versionId === right?.versionId && left?.startByte === right?.startByte && left?.endByte === right?.endByte
}

function isPdfExcerpt(page: unknown, text: unknown) {
  return typeof page === 'number' && Number.isInteger(page) && page > 0 && page <= 0x7fff_ffff
    && typeof text === 'string' && !!text.trim() && !text.includes('\0')
    && new TextEncoder().encode(text).length <= pdfQuoteByteLimit
}

export function isPdfQuotes(value: unknown, ids: string[] = []): value is Record<string, PdfQuote> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  return Object.entries(value).every(([id, quote]: [string, Partial<PdfQuote> | null]) =>
    ids.includes(id) && !!quote && typeof quote.sha256 === 'string' && /^[0-9a-f]{64}$/.test(quote.sha256)
    && isPdfExcerpt(quote.page, quote.text))
}

function isTurnReference(value: unknown): value is TurnReference {
  const reference = value as Partial<TurnReference> | null
  return !!reference && isProposedTaskId(reference.materialId)
    && typeof reference.name === 'string' && typeof reference.sha256 === 'string'
    && ((reference.page === undefined && reference.quote === undefined) || isPdfExcerpt(reference.page, reference.quote))
}

export function isTurnReferences(value: unknown): value is TurnReference[] {
  return Array.isArray(value) && value.length <= turnReferenceLimit && value.every(isTurnReference)
    && new Set(value.map((reference) => reference.materialId)).size === value.length
}

function isTurnTaskResult(value: unknown): value is TurnTaskResult {
  const result = value as Partial<TurnTaskResult> | null
  return !!result && typeof result.taskId === 'string' && !!result.taskId
    && result.taskId !== '.' && result.taskId !== '..'
    && typeof result.sourceTurnId === 'string' && !!result.sourceTurnId
    && typeof result.instruction === 'string'
}

function isTurnTaskResults(value: unknown): value is TurnTaskResult[] {
  return Array.isArray(value) && value.length <= turnTaskResultLimit && value.every(isTurnTaskResult)
    && new Set(value.map((result) => result.taskId)).size === value.length
}

function isTurn(value: unknown): value is Turn {
  const turn = value as Partial<Turn> | null
  return !!turn && typeof turn.turnId === 'string' && typeof turn.requestId === 'string'
    && typeof turn.text === 'string' && typeof turn.answer === 'string'
    && turn.status !== undefined && statuses.includes(turn.status)
    && typeof turn.afterSeq === 'number' && typeof turn.createdAt === 'string'
    && (turn.references === undefined || isTurnReferences(turn.references))
    && (turn.manuscriptSelection === undefined || isManuscriptSelection(turn.manuscriptSelection))
    && (turn.taskResults === undefined || isTurnTaskResults(turn.taskResults))
    && (turn.mode === undefined || isTurnMode(turn.mode))
    && (turn.proposedTaskId === undefined || isProposedTaskId(turn.proposedTaskId))
    && (turn.workflowProposal === undefined || (turn.status === 'COMPLETED' && turn.proposedTaskId === undefined
      && isWorkflowProposal(turn.workflowProposal)))
}

function isTurnPage(value: unknown): value is TurnPage {
  const page = value as Partial<TurnPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isTurn)
    && typeof page.modelConfigured === 'boolean'
}

export function turnsPath(projectId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/turns`
}

export function listTurns(projectId: string, signal: AbortSignal, before?: string) {
  const query = before ? `?${new URLSearchParams({ before })}` : ''
  return request(turnsPath(projectId) + query, isTurnPage, { signal })
}

export function sendTurn(projectId: string, input: TurnInput, signal: AbortSignal) {
  return request(turnsPath(projectId), (value): value is Turn => isTurn(value)
    && value.requestId === input.requestId && sameManuscriptSelection(value.manuscriptSelection, input.manuscriptSelection), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...input, manuscriptSelection: input.manuscriptSelection
      ? manuscriptSelectionInput(input.manuscriptSelection) : undefined }),
    signal,
  })
}

function turnPath(projectId: string, turnId: string) {
  if (!isProposedTaskId(turnId)) throw new ProjectApiError('对话链接无效。', 400)
  return `${turnsPath(projectId)}/${encodeURIComponent(turnId)}`
}

export async function stopTurn(projectId: string, turnId: string, signal: AbortSignal) {
  return request(`${turnPath(projectId, turnId)}/stop`, (value): value is Turn =>
    isTurn(value) && value.turnId === turnId && value.status !== 'RUNNING', { method: 'POST', signal })
}

export async function changeWorkflowProposal(projectId: string, turnId: string, action: 'confirm' | 'cancel', signal: AbortSignal) {
  return request(`${turnPath(projectId, turnId)}/workflow-proposal/${action}`, (value): value is WorkflowProposal =>
    isWorkflowProposal(value) && value.status !== 'PROPOSED', { method: 'POST', signal })
}
