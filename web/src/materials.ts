import { ProjectApiError, request } from './projects'

export interface Material {
  materialId: string
  name: string
  sha256: string
  size: number
  kind: 'text' | 'file'
  format?: 'word' | 'pdf' | 'archive'
  createdAt: string
}

export interface MaterialPage {
  items: Material[]
  nextCursor?: string
}

export interface MaterialPreview {
  text: string
  truncated: boolean
}

export const materialSizeLimit = 64 * 1024 * 1024

export function archiveFormatLabel(name: string) {
  const lower = name.toLowerCase()
  if (lower.endsWith('.zip')) return 'ZIP 压缩包'
  if (lower.endsWith('.tar.gz')) return 'TAR.GZ 压缩包'
  if (lower.endsWith('.tgz')) return 'TGZ 压缩包'
  return '压缩包'
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

export function isMaterial(value: unknown): value is Material {
  const material = value as Partial<Material> | null
  return !!material && isPathId(material.materialId)
    && typeof material.name === 'string' && material.name.length > 0
    && typeof material.sha256 === 'string' && /^[a-f\d]{64}$/i.test(material.sha256)
    && typeof material.size === 'number' && Number.isSafeInteger(material.size) && material.size >= 0
    && (material.kind === 'text' || material.kind === 'file')
    && (material.format === undefined || material.format === 'word' || material.format === 'pdf' || material.format === 'archive')
    && typeof material.createdAt === 'string' && Number.isFinite(Date.parse(material.createdAt))
}

export function isMaterialPage(value: unknown): value is MaterialPage {
  const page = value as Partial<MaterialPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isMaterial)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function materialsPath(projectId: string) {
  if (!isPathId(projectId)) throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}/materials`
}

function materialPath(projectId: string, materialId: string) {
  if (!isPathId(materialId)) throw new ProjectApiError('资料链接无效。', 400)
  return `${materialsPath(projectId)}/${encodeURIComponent(materialId)}`
}

export async function listMaterials(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(materialsPath(projectId) + query, isMaterialPage, { signal })
}

export async function uploadMaterial(projectId: string, file: File, signal: AbortSignal) {
  if (file.size > materialSizeLimit) throw new ProjectApiError('单个文件不能超过 64 MiB。', 413)
  const query = new URLSearchParams({ name: file.name })
  return request(`${materialsPath(projectId)}?${query}`, isMaterial, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: file,
    signal,
  })
}

function isMaterialPreview(value: unknown): value is MaterialPreview {
  const preview = value as Partial<MaterialPreview> | null
  return !!preview && typeof preview.text === 'string' && typeof preview.truncated === 'boolean'
}

export function getMaterialPreview(projectId: string, materialId: string, signal: AbortSignal) {
  return request(`${materialPath(projectId, materialId)}/preview`, isMaterialPreview, { signal })
}

export function materialDownloadPath(projectId: string, materialId: string) {
  return `${materialPath(projectId, materialId)}/download`
}
