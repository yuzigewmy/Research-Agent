import { ProjectApiError, request } from './projects'

export interface FileRecord {
  path: string
  versionId: string
  sha256: string
  size: number
  createdAt: string
}

export interface Document extends FileRecord {
  content: string
}

export interface FilePage {
  items: FileRecord[]
  nextCursor?: string
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isFileRecord(value: unknown): value is FileRecord {
  const record = value as Partial<FileRecord> | null
  return !!record && typeof record.path === 'string' && record.path.length > 0
    && isPathId(record.versionId)
    && typeof record.sha256 === 'string' && /^[a-f\d]{64}$/i.test(record.sha256)
    && typeof record.size === 'number' && Number.isSafeInteger(record.size) && record.size >= 0
    && typeof record.createdAt === 'string' && Number.isFinite(Date.parse(record.createdAt))
}

function isDocument(value: unknown): value is Document {
  return isFileRecord(value) && 'content' in value && typeof value.content === 'string'
}

function isFilePage(value: unknown): value is FilePage {
  const page = value as Partial<FilePage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isFileRecord)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function filesPath(projectId: string) {
  if (!isPathId(projectId)) throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}/files`
}

function versionPath(projectId: string, versionId: string) {
  if (!isPathId(versionId)) throw new ProjectApiError('版本链接无效。', 400)
  return `${filesPath(projectId)}/versions/${encodeURIComponent(versionId)}`
}

export async function listFiles(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(filesPath(projectId) + query, isFilePage, { signal })
}

export async function readText(projectId: string, path: string, signal: AbortSignal) {
  return request(`${filesPath(projectId)}/text?${new URLSearchParams({ path })}`, isDocument, { signal })
}

export async function saveText(projectId: string, path: string, content: string, baseSha256: string, signal: AbortSignal) {
  return request(`${filesPath(projectId)}/text`, isDocument, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, content, baseSha256 }), signal,
  })
}

export async function listVersions(projectId: string, path: string, signal: AbortSignal, cursor?: string) {
  const query = new URLSearchParams({ path })
  if (cursor) query.set('cursor', cursor)
  return request(`${filesPath(projectId)}/versions?${query}`, isFilePage, { signal })
}

export async function readVersion(projectId: string, versionId: string, signal: AbortSignal) {
  return request(versionPath(projectId, versionId), isDocument, { signal })
}

export async function restoreVersion(projectId: string, versionId: string, baseSha256: string, signal: AbortSignal) {
  return request(`${versionPath(projectId, versionId)}/restore`, isDocument, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ baseSha256 }), signal,
  })
}
