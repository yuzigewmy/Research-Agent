import type { Material } from './materials'
import { isTurnMode, ProjectApiError, request } from './projects'
import type { TurnMode } from './projects'

export type { TurnMode } from './projects'

const statuses = ['RUNNING', 'COMPLETED', 'FAILED', 'INTERRUPTED', 'STOPPED'] as const
export const turnReferenceLimit = 4
const turnTaskResultLimit = 4
export type TurnStatus = typeof statuses[number]
export type TurnReference = Pick<Material, 'materialId' | 'name' | 'sha256'>

interface TurnTaskResult {
  taskId: string
  sourceTurnId: string
  instruction: string
}

export interface TurnInput {
  requestId: string
  text: string
  referenceIds?: string[]
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

function isTurnReference(value: unknown): value is TurnReference {
  const reference = value as Partial<TurnReference> | null
  return !!reference && typeof reference.materialId === 'string' && !!reference.materialId
    && reference.materialId !== '.' && reference.materialId !== '..'
    && typeof reference.name === 'string' && typeof reference.sha256 === 'string'
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
  return request(turnsPath(projectId), isTurn, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
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
