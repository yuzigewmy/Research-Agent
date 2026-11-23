import { isTemplateId, request } from './projects'
import type { TemplateId } from './projects'
import { isCourseProfile, isSoftwareVersion } from './workflow'
import type { CourseProfile } from './workflow'

interface TemplateStep {
  key: string
  title: string
  checkpoint?: 'confirm' | 'choose-topic'
  optional: boolean
  acceptMarkdown: boolean
  requiresModel: boolean
}

export interface TemplateDefaults {
  checkpoints?: boolean
  language?: 'zh' | 'en'
  targetPaperCount?: number
  proposal?: {
    degree: 'bachelor' | 'master' | 'doctor'
    degreeType: 'academic' | 'professional'
    targetCharacters: number
    targetSources: number
  }
  course?: CourseProfile
  software?: { version: string }
  maxRounds?: number
  targetScore?: number
}

export interface ProjectTemplate {
  id: TemplateId
  label: string
  revision: string
  kind: 'chat' | 'workflow' | 'review' | 'survey'
  summary: string
  deliverables: string[]
  steps: TemplateStep[]
  defaults: TemplateDefaults
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function isStep(value: unknown): value is TemplateStep {
  return isRecord(value) && typeof value.key === 'string' && typeof value.title === 'string'
    && (value.checkpoint === undefined || value.checkpoint === 'confirm' || value.checkpoint === 'choose-topic')
    && typeof value.optional === 'boolean' && typeof value.acceptMarkdown === 'boolean'
    && typeof value.requiresModel === 'boolean'
}

function isProposalDefaults(value: unknown): value is NonNullable<TemplateDefaults['proposal']> {
  return isRecord(value)
    && (value.degree === 'bachelor' || value.degree === 'master' || value.degree === 'doctor')
    && (value.degreeType === 'academic' || value.degreeType === 'professional')
    && isNumber(value.targetCharacters) && isNumber(value.targetSources)
}

function isDefaults(value: unknown): value is TemplateDefaults {
  return isRecord(value)
    && (value.checkpoints === undefined || typeof value.checkpoints === 'boolean')
    && (value.language === undefined || value.language === 'zh' || value.language === 'en')
    && (value.targetPaperCount === undefined || isNumber(value.targetPaperCount))
    && (value.proposal === undefined || isProposalDefaults(value.proposal))
    && (value.course === undefined || isCourseProfile(value.course))
    && (value.software === undefined || (isRecord(value.software) && isSoftwareVersion(value.software.version)))
    && (value.maxRounds === undefined || isNumber(value.maxRounds))
    && (value.targetScore === undefined || isNumber(value.targetScore))
}

function isTemplate(value: unknown): value is ProjectTemplate {
  return isRecord(value) && isTemplateId(value.id) && typeof value.label === 'string'
    && typeof value.revision === 'string' && typeof value.summary === 'string'
    && (value.kind === 'chat' || value.kind === 'workflow' || value.kind === 'review' || value.kind === 'survey')
    && Array.isArray(value.deliverables) && value.deliverables.every((item) => typeof item === 'string')
    && Array.isArray(value.steps) && value.steps.every(isStep)
    && isDefaults(value.defaults)
}

export function listProjectTemplates(signal: AbortSignal) {
  return request('/api/v1/templates', (value): value is { items: ProjectTemplate[] } => {
    if (!isRecord(value) || !Array.isArray(value.items) || !value.items.every(isTemplate)) return false
    return new Set(value.items.map((item) => item.id)).size === value.items.length
  }, { signal })
}
