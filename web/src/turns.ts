import type { Material } from './materials'
import { request } from './projects'

const statuses = ['RUNNING', 'COMPLETED', 'FAILED', 'INTERRUPTED'] as const
export const turnReferenceLimit = 4
export type TurnStatus = typeof statuses[number]
export type TurnReference = Pick<Material, 'materialId' | 'name' | 'sha256'>

export interface TurnInput {
  requestId: string
  text: string
  referenceIds?: string[]
}

export interface Turn extends Pick<TurnInput, 'requestId' | 'text'> {
  references?: TurnReference[]
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
  | { seq: number; kind: 'done'; status: Exclude<TurnStatus, 'RUNNING'>; message?: string }

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

function isTurn(value: unknown): value is Turn {
  const turn = value as Partial<Turn> | null
  return !!turn && typeof turn.turnId === 'string' && typeof turn.requestId === 'string'
    && typeof turn.text === 'string' && typeof turn.answer === 'string'
    && turn.status !== undefined && statuses.includes(turn.status)
    && typeof turn.afterSeq === 'number' && typeof turn.createdAt === 'string'
    && (turn.references === undefined || isTurnReferences(turn.references))
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
