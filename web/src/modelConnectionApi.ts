import { request } from './projects'

export const connectionModelLabels = {
  default: '全局默认',
  conversation: '对话备选',
  workflow: '工作流备选',
  review: '审稿',
  edit: '改稿',
  vision: '视觉识别',
  image: '图片生成',
} as const

export type ConnectionModelId = keyof typeof connectionModelLabels

type ConnectionModelOption = {
  id: ConnectionModelId
  model: string
  reasoningEffort?: string
  imageQuality?: string
}

type ModelCatalogRole = {
  id: ConnectionModelId
  status: 'configured' | 'inherited' | 'unconfigured'
  model?: string
  reasoningEffort?: string
  imageQuality?: string
  capability: 'text' | 'vision' | 'image'
  testSeconds: number
}

type ModelCatalogTemplate = {
  templateId: string
  label: string
  modelId: 'default' | 'workflow'
}

type ModelCatalogLimit = {
  id: string
  value: number
  unit: 'seconds' | 'bytes' | 'count' | 'pixels'
}

export interface ModelCapabilityCatalog {
  roles: ModelCatalogRole[]
  templateDefaults: ModelCatalogTemplate[]
  limits: ModelCatalogLimit[]
}

export interface ModelConnectionStatus {
  configured: boolean
  model?: string
  reasoningEffort?: string
  modelOptions?: ConnectionModelOption[]
  catalog?: ModelCapabilityCatalog
}

const path = '/api/v1/model-connection'

function isModelOption(value: unknown): value is ConnectionModelOption {
  const option = value as Partial<ConnectionModelOption> | null
  return !!option && typeof option.id === 'string' && Object.hasOwn(connectionModelLabels, option.id)
    && typeof option.model === 'string' && option.model.trim().length > 0
    && (option.reasoningEffort === undefined || typeof option.reasoningEffort === 'string')
    && (option.id !== 'image' || typeof option.imageQuality === 'string')
}

type ModelConnectionResponse = Omit<ModelConnectionStatus, 'catalog'> & { catalog?: unknown }

function isModelConnectionResponse(value: unknown): value is ModelConnectionResponse {
  const status = value as Partial<ModelConnectionResponse> | null
  return !!status && typeof status.configured === 'boolean'
    && (status.model === undefined || typeof status.model === 'string')
    && (status.reasoningEffort === undefined || typeof status.reasoningEffort === 'string')
    && (status.modelOptions === undefined || (Array.isArray(status.modelOptions) && status.modelOptions.every(isModelOption)))
}

function isCatalogRole(value: unknown): value is ModelCatalogRole {
  const role = value as Partial<ModelCatalogRole> | null
  return !!role && typeof role.id === 'string' && Object.hasOwn(connectionModelLabels, role.id)
    && (role.status === 'configured' || role.status === 'inherited' || role.status === 'unconfigured')
    && (role.model === undefined || typeof role.model === 'string')
    && (role.reasoningEffort === undefined || typeof role.reasoningEffort === 'string')
    && (role.imageQuality === undefined || typeof role.imageQuality === 'string')
    && (role.capability === 'text' || role.capability === 'vision' || role.capability === 'image')
    && typeof role.testSeconds === 'number' && Number.isFinite(role.testSeconds) && role.testSeconds >= 0
}

function isCatalogTemplate(value: unknown): value is ModelCatalogTemplate {
  const template = value as Partial<ModelCatalogTemplate> | null
  return !!template && typeof template.templateId === 'string' && typeof template.label === 'string'
    && (template.modelId === 'default' || template.modelId === 'workflow')
}

function isCatalogLimit(value: unknown): value is ModelCatalogLimit {
  const limit = value as Partial<ModelCatalogLimit> | null
  return !!limit && typeof limit.id === 'string'
    && typeof limit.value === 'number' && Number.isFinite(limit.value) && limit.value >= 0
    && (limit.unit === 'seconds' || limit.unit === 'bytes' || limit.unit === 'count' || limit.unit === 'pixels')
}

function isModelCapabilityCatalog(value: unknown): value is ModelCapabilityCatalog {
  const catalog = value as Partial<ModelCapabilityCatalog> | null
  return !!catalog && Array.isArray(catalog.roles) && catalog.roles.every(isCatalogRole)
    && Array.isArray(catalog.templateDefaults) && catalog.templateDefaults.every(isCatalogTemplate)
    && Array.isArray(catalog.limits) && catalog.limits.every(isCatalogLimit)
}

export async function readModelConnection(signal: AbortSignal): Promise<ModelConnectionStatus> {
  const { catalog, ...status } = await request(path, isModelConnectionResponse, { signal, cache: 'no-store' })
  // An unsupported catalog must not prevent use of the existing connection controls.
  return isModelCapabilityCatalog(catalog) ? { ...status, catalog } : status
}

export function testModelConnection(modelId: ConnectionModelId | undefined, signal: AbortSignal) {
  return request(`${path}/test`, (value): value is { ok: true } =>
    (value as { ok?: unknown } | null)?.ok === true, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ modelId }), signal,
  })
}
