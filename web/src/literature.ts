import { ProjectApiError, request } from './projects'

export interface Work {
  doi: string
  title?: string
  journal?: string
  year?: number
  language?: string
  authors?: string[]
  abstract?: string
  url?: string
}

export interface Citation extends Work {
  verifiedAt: string
}

interface WorkPage { items: Work[] }
interface CitationPage { items: Citation[]; nextCursor?: string }

function isDOI(value: unknown): value is string {
  if (typeof value !== 'string' || /[\\\s\p{Cc}\p{Cs}]/u.test(value)) return false
  const [prefix, ...segments] = value.split('/')
  return /^10\.\d{4,}(?:\.\d+)*$/.test(prefix) && segments.length > 0
    && segments.every((segment) => segment !== '' && segment !== '.' && segment !== '..')
}

function isWork(value: unknown): value is Work {
  const work = value as Partial<Work> | null
  return !!work && isDOI(work.doi)
    && [work.title, work.journal, work.language, work.abstract, work.url].every((text) => text === undefined || typeof text === 'string')
    && (work.year === undefined || (typeof work.year === 'number' && Number.isInteger(work.year)))
    && (work.authors === undefined || (Array.isArray(work.authors) && work.authors.every((author) => typeof author === 'string')))
}

export function isCitation(value: unknown): value is Citation {
  const citation = value as Partial<Citation> | null
  return !!citation && typeof citation.verifiedAt === 'string' && Number.isFinite(Date.parse(citation.verifiedAt)) && isWork(citation)
}

function isWorkPage(value: unknown): value is WorkPage {
  const page = value as Partial<WorkPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isWork)
}

function isCitationPage(value: unknown): value is CitationPage {
  const page = value as Partial<CitationPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isCitation)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function literaturePath(projectId: string) {
  if (!projectId || projectId === '.' || projectId === '..') throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}/literature`
}

export async function listCitations(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(literaturePath(projectId) + query, isCitationPage, { signal })
}

export async function searchLiterature(projectId: string, query: string, signal: AbortSignal, limit?: number) {
  const params = new URLSearchParams({ q: query })
  if (limit !== undefined) params.set('limit', String(limit))
  return request(`${literaturePath(projectId)}/search?${params}`, isWorkPage, { signal })
}

export async function saveCitation(projectId: string, doi: string, signal: AbortSignal) {
  return request(literaturePath(projectId), isCitation, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ doi }), signal,
  })
}

export function bibliographyPath(projectId: string) {
  return `${literaturePath(projectId)}/bibliography`
}

export function doiUrl(doi: string) {
  return `https://doi.org/${doi.split('/').map(encodeURIComponent).join('/')}`
}
