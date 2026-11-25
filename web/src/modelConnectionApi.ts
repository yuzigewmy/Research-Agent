import { request } from './projects'

export const connectionModelLabels = {
  default: '全局默认',
  conversation: '对话备选',
  workflow: '工作流备选',
  review: '审稿',
  edit: '改稿',
  vision: '视觉识别',
} as const

export type ConnectionModelId = keyof typeof connectionModelLabels

type ConnectionModelOption = {
  id: ConnectionModelId
  model: string
  reasoningEffort?: string
}

export interface ModelConnectionStatus {
  configured: boolean
  model?: string
  reasoningEffort?: string
  modelOptions?: ConnectionModelOption[]
}

const path = '/api/v1/model-connection'

function isModelOption(value: unknown): value is ConnectionModelOption {
  const option = value as Partial<ConnectionModelOption> | null
  return !!option && typeof option.id === 'string' && Object.hasOwn(connectionModelLabels, option.id)
    && typeof option.model === 'string' && option.model.trim().length > 0
    && (option.reasoningEffort === undefined || typeof option.reasoningEffort === 'string')
}

function isModelConnectionStatus(value: unknown): value is ModelConnectionStatus {
  const status = value as Partial<ModelConnectionStatus> | null
  return !!status && typeof status.configured === 'boolean'
    && (status.model === undefined || typeof status.model === 'string')
    && (status.reasoningEffort === undefined || typeof status.reasoningEffort === 'string')
    && (status.modelOptions === undefined || (Array.isArray(status.modelOptions) && status.modelOptions.every(isModelOption)))
}

export function readModelConnection(signal: AbortSignal) {
  return request(path, isModelConnectionStatus, { signal, cache: 'no-store' })
}

export function testModelConnection(modelId: ConnectionModelId | undefined, signal: AbortSignal) {
  return request(`${path}/test`, (value): value is { ok: true } =>
    (value as { ok?: unknown } | null)?.ok === true, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ modelId }), signal,
  })
}
