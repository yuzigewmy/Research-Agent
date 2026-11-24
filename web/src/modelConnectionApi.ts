import { request } from './projects'

export interface ModelConnectionStatus {
  configured: boolean
  model?: string
  reasoningEffort?: string
}

const path = '/api/v1/model-connection'

function isModelConnectionStatus(value: unknown): value is ModelConnectionStatus {
  const status = value as Partial<ModelConnectionStatus> | null
  return !!status && typeof status.configured === 'boolean'
    && (status.model === undefined || typeof status.model === 'string')
    && (status.reasoningEffort === undefined || typeof status.reasoningEffort === 'string')
}

export function readModelConnection(signal: AbortSignal) {
  return request(path, isModelConnectionStatus, { signal, cache: 'no-store' })
}

export function testModelConnection(signal: AbortSignal) {
  return request(`${path}/test`, (value): value is { ok: true } =>
    (value as { ok?: unknown } | null)?.ok === true, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', signal,
  })
}
