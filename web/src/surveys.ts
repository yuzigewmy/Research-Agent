import { ProjectApiError, request } from './projects'

export interface SurveyReview {
  row: number
  accept: boolean
  reason: string
}

export interface SurveyOptions {
  requiredColumns?: string[]
  duplicateColumns?: string[]
  reviews?: SurveyReview[]
}

export interface SurveyInput {
  requestId: string
  materialId: string
  options: SurveyOptions
}

interface SurveyCounts {
  total: number
  valid: number
  invalid: number
  pending: number
  demo: boolean
}

export interface SurveyMetadata extends SurveyCounts {
  analysisId: string
  materialId: string
  materialName: string
  materialSha256: string
  createdAt: string
}

export interface SurveyRow {
  row: number
  status: 'VALID' | 'INVALID' | 'PENDING'
  reasons: string[]
}

interface Frequency { value: string; count: number }

export interface SurveySummary {
  column: string
  answered: number
  missing: number
  numeric: number
  mean?: number
  min?: number
  max?: number
  frequencies: Frequency[]
}

export interface SurveyReport extends SurveyCounts {
  columns: string[]
  rows: SurveyRow[]
  summaries: SurveySummary[]
}

export interface SurveyRun extends SurveyMetadata {
  options: SurveyOptions
  report: SurveyReport
}

interface SurveyPage { items: SurveyMetadata[]; nextCursor?: string }

function isId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isStringList(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string')
}

function isCounts(value: unknown): value is SurveyCounts {
  const counts = value as Partial<SurveyCounts> | null
  return !!counts && isCount(counts.total) && isCount(counts.valid) && isCount(counts.invalid)
    && isCount(counts.pending) && typeof counts.demo === 'boolean'
}

function isMetadata(value: unknown): value is SurveyMetadata {
  const run = value as Partial<SurveyMetadata> | null
  return !!run && isId(run.analysisId) && isId(run.materialId)
    && typeof run.materialName === 'string' && typeof run.materialSha256 === 'string'
    && /^[a-f\d]{64}$/i.test(run.materialSha256)
    && typeof run.createdAt === 'string' && Number.isFinite(Date.parse(run.createdAt)) && isCounts(run)
}

function isReview(value: unknown): value is SurveyReview {
  const review = value as Partial<SurveyReview> | null
  return !!review && isCount(review.row) && review.row > 0 && typeof review.accept === 'boolean' && typeof review.reason === 'string'
}

function isOptions(value: unknown): value is SurveyOptions {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const options = value as SurveyOptions
  return (options.requiredColumns === undefined || isStringList(options.requiredColumns))
    && (options.duplicateColumns === undefined || isStringList(options.duplicateColumns))
    && (options.reviews === undefined || (Array.isArray(options.reviews) && options.reviews.every(isReview)))
}

function isRow(value: unknown): value is SurveyRow {
  const row = value as Partial<SurveyRow> | null
  return !!row && isCount(row.row) && row.row > 0 && isStringList(row.reasons)
    && (row.status === 'VALID' || row.status === 'INVALID' || row.status === 'PENDING')
}

function isFrequency(value: unknown): value is Frequency {
  const entry = value as Partial<Frequency> | null
  return !!entry && typeof entry.value === 'string' && isCount(entry.count)
}

function isSummary(value: unknown): value is SurveySummary {
  const summary = value as Partial<SurveySummary> | null
  return !!summary && typeof summary.column === 'string' && isCount(summary.answered)
    && isCount(summary.missing) && isCount(summary.numeric)
    && [summary.mean, summary.min, summary.max].every((value) => value === undefined || Number.isFinite(value))
    && Array.isArray(summary.frequencies) && summary.frequencies.every(isFrequency)
}

function isReport(value: unknown): value is SurveyReport {
  const report = value as Partial<SurveyReport> | null
  return !!report && isStringList(report.columns)
    && Array.isArray(report.rows) && report.rows.every(isRow)
    && Array.isArray(report.summaries) && report.summaries.every(isSummary) && isCounts(report)
}

function isRun(value: unknown): value is SurveyRun {
  const run = value as Partial<SurveyRun> | null
  return !!run && isOptions(run.options) && isReport(run.report) && isMetadata(run)
}

function isPage(value: unknown): value is SurveyPage {
  const page = value as Partial<SurveyPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isMetadata)
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
}

function surveysPath(projectId: string) {
  if (!isId(projectId)) throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}/surveys`
}

function surveyPath(projectId: string, analysisId: string) {
  if (!isId(analysisId)) throw new ProjectApiError('报告链接无效。', 400)
  return `${surveysPath(projectId)}/${encodeURIComponent(analysisId)}`
}

export async function listSurveys(projectId: string, signal: AbortSignal, cursor?: string) {
  const query = cursor ? `?${new URLSearchParams({ cursor })}` : ''
  return request(surveysPath(projectId) + query, isPage, { signal })
}

export async function readSurvey(projectId: string, analysisId: string, signal: AbortSignal) {
  return request(surveyPath(projectId, analysisId), isRun, { signal })
}

export async function analyzeSurvey(projectId: string, input: SurveyInput, signal: AbortSignal) {
  return request(surveysPath(projectId), isRun, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal,
  })
}

export function surveyDownloadPath(projectId: string, analysisId: string) {
  return `${surveyPath(projectId, analysisId)}/download`
}
