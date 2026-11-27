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
  sections?: string[]
}

export interface MaterialTablePreview {
  headers: string[]
  rows: string[][]
  totalPreviewRows: number
  truncated: boolean
  maskedColumns: string[]
  maskedCellCount: number
}

export function isTableMaterialName(name: string) {
  return /\.(csv|tsv)$/i.test(name)
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

export async function listMaterials(projectId: string, signal: AbortSignal, cursor?: string, q?: string) {
  const params = new URLSearchParams()
  if (q) params.set('q', q)
  if (cursor) params.set('cursor', cursor)
  const query = params.toString()
  return request(materialsPath(projectId) + (query ? `?${query}` : ''), isMaterialPage, { signal })
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
    && (preview.sections === undefined || (Array.isArray(preview.sections) && preview.sections.length >= 2
      && preview.sections.every(section => typeof section === 'string')))
}

export function getMaterialPreview(projectId: string, materialId: string, signal: AbortSignal) {
  return request(`${materialPath(projectId, materialId)}/preview`, isMaterialPreview, { signal })
}

function isMaterialTablePreview(value: unknown): value is MaterialTablePreview {
  const preview = value as Partial<MaterialTablePreview> | null
  return !!preview
    && Array.isArray(preview.headers) && preview.headers.every((cell) => typeof cell === 'string')
    && Array.isArray(preview.rows) && preview.rows.every((row) => Array.isArray(row) && row.every((cell) => typeof cell === 'string'))
    && typeof preview.totalPreviewRows === 'number' && Number.isSafeInteger(preview.totalPreviewRows) && preview.totalPreviewRows >= 0
    && typeof preview.truncated === 'boolean'
    && Array.isArray(preview.maskedColumns) && preview.maskedColumns.every((col) => typeof col === 'string')
    && typeof preview.maskedCellCount === 'number' && Number.isSafeInteger(preview.maskedCellCount) && preview.maskedCellCount >= 0
}

export function getMaterialTablePreview(projectId: string, materialId: string, signal: AbortSignal) {
  return request(`${materialPath(projectId, materialId)}/table-preview`, isMaterialTablePreview, { signal })
}

function isDeleteMaterialReceipt(value: unknown): value is { deleted: boolean; materialId: string } {
  const receipt = value as { deleted?: unknown; materialId?: unknown } | null
  return !!receipt && receipt.deleted === true && typeof receipt.materialId === 'string'
}

export function deleteMaterial(projectId: string, materialId: string, signal: AbortSignal) {
  return request(materialPath(projectId, materialId), isDeleteMaterialReceipt, {
    method: 'DELETE',
    signal,
  })
}

export function materialDownloadPath(projectId: string, materialId: string) {
  return `${materialPath(projectId, materialId)}/download`
}

export function materialImagePreviewPath(projectId: string, materialId: string) {
  return `${materialPath(projectId, materialId)}/image-preview`
}
