import { ProjectApiError, request } from './projects'

export interface SurveyReview {
  row: number
  accept: boolean
  reason: string
}

export interface SurveyCrossTabOptions {
  rowColumn: string
  columnColumn: string
}

export interface SurveyCrossTab extends SurveyCrossTabOptions {
  included: number
  missing: number
  rowLabels: string[]
  columnLabels: string[]
  counts: number[][]
  rowTotals: number[]
  columnTotals: number[]
  weighted?: SurveyWeightedCrossTab
}

export interface SurveyWeightedCrossTab {
  included: number
  weights: string[][]
  rowTotals: string[]
  columnTotals: string[]
  totalWeight: string
}

export const crossTabCategoryLimit = 20
export const crossTabLabelByteLimit = 128
export const reliabilityItemLimit = 20
export const multipleRegressionPredictorLimit = 8
export const factorAnalysisColumnLimit = 20
export const conjointAttributeLimit = 6
export const cfaConstructLimit = 6
export const cfaItemLimit = 10

export interface SurveyMultipleRegressionOptions {
  outcomeColumn: string
  predictorColumns: string[]
}

export interface SurveyMultipleRegressionCoefficient {
  name: string
  coefficient: number
  stdError: number
  tStat?: number
}

export interface SurveyMultipleRegressionFit {
  intercept: SurveyMultipleRegressionCoefficient
  predictors: SurveyMultipleRegressionCoefficient[]
  rSquared?: number
  adjustedRSquared?: number
  fStat?: number
  dfModel: number
  dfResidual: number
}

export interface SurveyMultipleRegression extends SurveyMultipleRegressionOptions {
  included: number
  missing: number
  nonNumeric: number
  fit?: SurveyMultipleRegressionFit
  unavailableReason?: string
}

export interface SurveyFactorAnalysisOptions {
  columns: string[]
  factors?: number
}

export interface SurveyFactorSummary {
  factor: number
  eigenvalue: number
  variancePercent: number
  cumulativeVariancePercent: number
  rotatedSSLoadings: number
  rotatedVariancePercent: number
}

export interface SurveyFactorVariableResult {
  column: string
  msa: number
  communality: number
  loadings: number[]
}

export interface SurveyFactorAnalysisResult {
  kmo: number
  bartlettChiSquare: number
  bartlettDf: number
  bartlettPValue: number
  factorCount: number
  autoFactors: boolean
  eigenvalues: number[]
  factors: SurveyFactorSummary[]
  variables: SurveyFactorVariableResult[]
  correlationMatrix: number[][]
}

export interface SurveyFactorAnalysis extends SurveyFactorAnalysisOptions {
  included: number
  missing: number
  nonNumeric: number
  result?: SurveyFactorAnalysisResult
  unavailableReason?: string
}

export interface SurveyKanoOptions {
  functionalColumn: string
  dysfunctionalColumn: string
}

export interface SurveyKanoCategory {
  code: string
  label: string
  count: number
  share: number
}

export interface SurveyKanoResult {
  dominantCode: string
  dominantLabel: string
  categories: SurveyKanoCategory[]
  better?: number
  worse?: number
}

export interface SurveyKano extends SurveyKanoOptions {
  included: number
  missing: number
  invalid: number
  result?: SurveyKanoResult
  unavailableReason?: string
}

export interface SurveyPSMOptions {
  tooCheapColumn: string
  cheapColumn: string
  expensiveColumn: string
  tooExpensiveColumn: string
}

export interface SurveyPSMCurvePoint {
  price: number
  tooCheap: number
  cheap: number
  notCheap: number
  expensive: number
  notExpensive: number
  tooExpensive: number
}

export interface SurveyPSMResult {
  pmc: number
  pme: number
  opp: number
  idp: number
  acceptableRange: [number, number]
  curve: SurveyPSMCurvePoint[]
}

export interface SurveyPSM extends SurveyPSMOptions {
  included: number
  missing: number
  nonNumeric: number
  inconsistent: number
  result?: SurveyPSMResult
  unavailableReason?: string
}

export interface SurveyConjointOptions {
  ratingColumn: string
  attributeColumns: string[]
}

export interface SurveyConjointLevelUtility {
  level: string
  count: number
  partWorth: number
}

export interface SurveyConjointAttributeResult {
  column: string
  range: number
  importancePercent: number
  levels: SurveyConjointLevelUtility[]
}

export interface SurveyConjointResult {
  intercept: number
  rSquared?: number
  attributes: SurveyConjointAttributeResult[]
}

export interface SurveyConjoint extends SurveyConjointOptions {
  included: number
  missing: number
  nonNumeric: number
  result?: SurveyConjointResult
  unavailableReason?: string
}

export interface SurveyCFAConstructOptions {
  name: string
  columns: string[]
}

export interface SurveyCFAOptions {
  constructs: SurveyCFAConstructOptions[]
}

export interface SurveyCFAItemResult {
  column: string
  loading: number
}

export interface SurveyCFAConstructResult {
  name: string
  items: SurveyCFAItemResult[]
  cr: number
  ave: number
  sqrtAve: number
  maxInterConstructAbsCorr: number
  convergentValid: boolean
  discriminantValid: boolean
}

export interface SurveyCFAResult {
  constructs: SurveyCFAConstructResult[]
  correlationMatrix: number[][]
  discriminantValid: boolean
}

export interface SurveyCFA extends SurveyCFAOptions {
  included: number
  missing: number
  nonNumeric: number
  result?: SurveyCFAResult
  unavailableReason?: string
}

export interface SurveyReliabilityOptions {
  columns: string[]
}

export interface SurveyReliability extends SurveyReliabilityOptions {
  included: number
  missing: number
  nonNumeric: number
  alpha?: number
}

export interface SurveyRegressionOptions {
  predictorColumn: string
  outcomeColumn: string
}

export type SurveyRegressionUnavailableReason = 'INSUFFICIENT_ROWS' | 'CONSTANT_PREDICTOR' | 'OUT_OF_RANGE'

export type SurveyRegression = SurveyRegressionOptions & {
  included: number
  missing: number
  nonNumeric: number
} & (
  | { fit: { intercept: number; slope: number; rSquared?: number }; unavailableReason?: never }
  | { fit?: never; unavailableReason: SurveyRegressionUnavailableReason }
)

export interface SurveyCategoryMapping {
  column: string
  from: string
  to: string
}

export const categoryMappingLimit = 64
export const categoryMappingValueByteLimit = 128

export interface SurveyFieldPair {
  from: string
  to: string
}

export interface SurveySettingsSource {
  analysisId: string
  fields: SurveyFieldPair[]
}

export const sampleSizeMethodCochran = 'proportion-cochran-v1'
export const sampleSizeConfidenceLevels = [90, 95, 99] as const
export type SampleSizeConfidenceLevel = typeof sampleSizeConfidenceLevels[number]
export const defaultPlannedStatisticalMethods = [
  '描述统计',
  '交叉统计',
  '权重加权',
  '分类编码',
  '量表信度（Cronbach α）',
  '一元线性回归',
] as const

const sampleSizeZScores: Record<SampleSizeConfidenceLevel, number> = {
  90: 1.6448536269514722,
  95: 1.959963984540054,
  99: 2.5758293035489004,
}
const maxSamplePopulation = 1_000_000_000
const maxCalculatedSample = 1_000_000_000

export interface SurveySampleSizeInput {
  method?: string
  confidenceLevel: SampleSizeConfidenceLevel
  expectedProportion: number
  marginOfError: number
  populationSize?: number
  responseRate: number
}

export interface SurveySampleSizeResult {
  method: string
  zScore: number
  unadjustedSampleSize: number
  finitePopulationAdjusted: number
  plannedInvitations: number
  formulaSummary: string
}

export interface SurveyAnalysisPlan {
  planId: string
  projectId: string
  version: number
  createdAt: string
  title: string
  objective: string
  targetPopulation: string
  samplingMethod: string
  plannedMethods: string[]
  missingStrategy: string
  notes?: string
  sampleSizeInput: SurveySampleSizeInput
  sampleSizeResult: SurveySampleSizeResult
}

export interface SurveyAnalysisPlanList {
  latestVersion: number
  items: SurveyAnalysisPlan[]
}

export interface SurveyAnalysisPlanInput {
  requestId: string
  expectedVersion: number
  title: string
  objective: string
  targetPopulation: string
  samplingMethod: string
  plannedMethods: string[]
  missingStrategy: string
  notes?: string
  sampleSizeInput: SurveySampleSizeInput
}

export interface SurveyAnalysisPlanSource {
  planId: string
  version: number
  title: string
  objective: string
  method: string
  confidenceLevel: number
  unadjustedSampleSize: number
  finitePopulationAdjusted: number
  plannedInvitations: number
  formulaSummary: string
  plannedMethods: string[]
  createdAt: string
}

export interface SurveyOptions {
  settingsSource?: SurveySettingsSource
  categoryMappings?: SurveyCategoryMapping[]
  sheet?: string
  requiredColumns?: string[]
  duplicateColumns?: string[]
  reviews?: SurveyReview[]
  crossTab?: SurveyCrossTabOptions
  reliability?: SurveyReliabilityOptions
  regression?: SurveyRegressionOptions
  multipleRegression?: SurveyMultipleRegressionOptions
  factorAnalysis?: SurveyFactorAnalysisOptions
  kano?: SurveyKanoOptions
  psm?: SurveyPSMOptions
  conjoint?: SurveyConjointOptions
  cfa?: SurveyCFAOptions
  weightColumn?: string
  planId?: string
}

export interface SurveyWeighting {
  column: string
  included: number
  missing: number
  invalid: number
  totalWeight: string
}

export interface SurveyInput {
  requestId: string
  materialId: string
  options: SurveyOptions
}

export interface SurveyFieldMappingInput {
  requestId: string
  targetAnalysisId: string
  fields: SurveyFieldPair[]
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
  sourceRow?: number
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
  weighted?: { included: number; totalWeight: string; mean: number }
}

export interface SurveyReport extends SurveyCounts {
  columns: string[]
  rows: SurveyRow[]
  summaries: SurveySummary[]
  crossTab?: SurveyCrossTab
  reliability?: SurveyReliability
  regression?: SurveyRegression
  multipleRegression?: SurveyMultipleRegression
  factorAnalysis?: SurveyFactorAnalysis
  kano?: SurveyKano
  psm?: SurveyPSM
  conjoint?: SurveyConjoint
  cfa?: SurveyCFA
  weighting?: SurveyWeighting
  planSource?: SurveyAnalysisPlanSource
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

function isCrossTabOptions(value: unknown): value is SurveyCrossTabOptions {
  const options = value as Partial<SurveyCrossTabOptions> | null
  return !!options && typeof options.rowColumn === 'string' && typeof options.columnColumn === 'string'
    && options.rowColumn.length > 0 && options.columnColumn.length > 0 && options.rowColumn !== options.columnColumn
}

function isReliabilityOptions(value: unknown): value is SurveyReliabilityOptions {
  const columns = (value as Partial<SurveyReliabilityOptions> | null)?.columns
  return isStringList(columns) && columns.length >= 2 && columns.length <= reliabilityItemLimit
}

function isRegressionOptions(value: unknown): value is SurveyRegressionOptions {
  const options = value as Partial<SurveyRegressionOptions> | null
  return !!options && typeof options.predictorColumn === 'string' && typeof options.outcomeColumn === 'string'
    && options.predictorColumn.length > 0 && options.outcomeColumn.length > 0
    && options.predictorColumn !== options.outcomeColumn
}

function isSheetName(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}

function isCategoryMappings(value: unknown): value is SurveyCategoryMapping[] {
  return Array.isArray(value) && value.length <= categoryMappingLimit && value.every((entry: unknown) => {
    const mapping = entry as Partial<SurveyCategoryMapping> | null
    return !!mapping && typeof mapping.column === 'string' && typeof mapping.from === 'string' && typeof mapping.to === 'string'
  })
}

function isSettingsSource(value: unknown): value is SurveySettingsSource {
  const source = value as Partial<SurveySettingsSource> | null
  if (!source || !isId(source.analysisId) || !Array.isArray(source.fields)) return false
  const from = new Set<string>()
  const to = new Set<string>()
  for (const pair of source.fields) {
    if (!pair || typeof pair.from !== 'string' || !pair.from || typeof pair.to !== 'string' || !pair.to
      || from.has(pair.from) || to.has(pair.to)) return false
    from.add(pair.from)
    to.add(pair.to)
  }
  return true
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function isMultipleRegressionOptions(value: unknown): value is SurveyMultipleRegressionOptions {
  const opts = value as Partial<SurveyMultipleRegressionOptions> | null
  return !!opts && typeof opts.outcomeColumn === 'string' && opts.outcomeColumn.length > 0
    && isStringList(opts.predictorColumns) && opts.predictorColumns.length >= 2
    && opts.predictorColumns.length <= multipleRegressionPredictorLimit
}

function isFactorAnalysisOptions(value: unknown): value is SurveyFactorAnalysisOptions {
  const opts = value as Partial<SurveyFactorAnalysisOptions> | null
  return !!opts && isStringList(opts.columns) && opts.columns.length >= 3 && opts.columns.length <= factorAnalysisColumnLimit
    && (opts.factors === undefined || (isCount(opts.factors) && opts.factors <= opts.columns.length))
}

function isKanoOptions(value: unknown): value is SurveyKanoOptions {
  const opts = value as Partial<SurveyKanoOptions> | null
  return !!opts && typeof opts.functionalColumn === 'string' && opts.functionalColumn.length > 0
    && typeof opts.dysfunctionalColumn === 'string' && opts.dysfunctionalColumn.length > 0
}

function isPSMOptions(value: unknown): value is SurveyPSMOptions {
  const opts = value as Partial<SurveyPSMOptions> | null
  return !!opts && typeof opts.tooCheapColumn === 'string' && opts.tooCheapColumn.length > 0
    && typeof opts.cheapColumn === 'string' && opts.cheapColumn.length > 0
    && typeof opts.expensiveColumn === 'string' && opts.expensiveColumn.length > 0
    && typeof opts.tooExpensiveColumn === 'string' && opts.tooExpensiveColumn.length > 0
}

function isConjointOptions(value: unknown): value is SurveyConjointOptions {
  const opts = value as Partial<SurveyConjointOptions> | null
  return !!opts && typeof opts.ratingColumn === 'string' && opts.ratingColumn.length > 0
    && isStringList(opts.attributeColumns) && opts.attributeColumns.length >= 1
    && opts.attributeColumns.length <= conjointAttributeLimit
}

function isCFAOptions(value: unknown): value is SurveyCFAOptions {
  const opts = value as Partial<SurveyCFAOptions> | null
  return !!opts && Array.isArray(opts.constructs) && opts.constructs.length >= 2 && opts.constructs.length <= cfaConstructLimit
    && opts.constructs.every((c) => !!c && typeof c.name === 'string' && c.name.trim().length > 0
      && isStringList(c.columns) && c.columns.length >= 2 && c.columns.length <= cfaItemLimit)
}

function isOptions(value: unknown): value is SurveyOptions {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const options = value as SurveyOptions
  return (options.sheet === undefined || isSheetName(options.sheet))
    && (options.settingsSource === undefined || isSettingsSource(options.settingsSource))
    && (options.categoryMappings === undefined || isCategoryMappings(options.categoryMappings))
    && (options.requiredColumns === undefined || isStringList(options.requiredColumns))
    && (options.duplicateColumns === undefined || isStringList(options.duplicateColumns))
    && (options.reviews === undefined || (Array.isArray(options.reviews) && options.reviews.every(isReview)))
    && (options.crossTab === undefined || isCrossTabOptions(options.crossTab))
    && (options.reliability === undefined || isReliabilityOptions(options.reliability))
    && (options.regression === undefined || isRegressionOptions(options.regression))
    && (options.multipleRegression === undefined || isMultipleRegressionOptions(options.multipleRegression))
    && (options.factorAnalysis === undefined || isFactorAnalysisOptions(options.factorAnalysis))
    && (options.kano === undefined || isKanoOptions(options.kano))
    && (options.psm === undefined || isPSMOptions(options.psm))
    && (options.conjoint === undefined || isConjointOptions(options.conjoint))
    && (options.cfa === undefined || isCFAOptions(options.cfa))
    && (options.weightColumn === undefined || (typeof options.weightColumn === 'string' && options.weightColumn.trim().length > 0))
    && (options.planId === undefined || isId(options.planId))
}

function isRow(value: unknown): value is SurveyRow {
  const row = value as Partial<SurveyRow> | null
  return !!row && isCount(row.row) && row.row > 0 && isStringList(row.reasons)
    && (row.sourceRow === undefined || (isCount(row.sourceRow) && row.sourceRow > 0))
    && (row.status === 'VALID' || row.status === 'INVALID' || row.status === 'PENDING')
}

function isFrequency(value: unknown): value is Frequency {
  const entry = value as Partial<Frequency> | null
  return !!entry && typeof entry.value === 'string' && isCount(entry.count)
}

function isSummary(value: unknown): value is SurveySummary {
  const summary = value as Partial<SurveySummary> | null
  const weighted = summary?.weighted
  return !!summary && typeof summary.column === 'string' && isCount(summary.answered)
    && isCount(summary.missing) && isCount(summary.numeric)
    && [summary.mean, summary.min, summary.max].every((value) => value === undefined || Number.isFinite(value))
    && Array.isArray(summary.frequencies) && summary.frequencies.every(isFrequency)
    && (weighted === undefined || (!!weighted && isCount(weighted.included) && weighted.included > 0
      && weighted.included <= summary.numeric && isWeight(weighted.totalWeight) && Number.isFinite(weighted.mean)))
}

function isCrossTabLabels(value: unknown): value is string[] {
  return isStringList(value) && value.length <= crossTabCategoryLimit
}

function isCountList(value: unknown, length: number): value is number[] {
  return Array.isArray(value) && value.length === length && value.every(isCount)
}

function isCrossTab(value: unknown): value is SurveyCrossTab {
  const tab = value as Partial<SurveyCrossTab> | null
  if (!tab) return false
  const { included, missing, rowLabels, columnLabels, counts, rowTotals, columnTotals, weighted } = tab
  return isCrossTabOptions(tab) && isCount(included) && isCount(missing)
    && isCrossTabLabels(rowLabels) && isCrossTabLabels(columnLabels)
    && Array.isArray(counts) && counts.length === rowLabels.length
    && counts.every((row) => isCountList(row, columnLabels.length))
    && isCountList(rowTotals, rowLabels.length) && isCountList(columnTotals, columnLabels.length)
    && (weighted === undefined || isWeightedCrossTab(weighted, included, rowLabels.length, columnLabels.length))
}

function isWeight(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9]+(?:\.[0-9]{1,6})?$/.test(value)
}

function isWeightedCrossTab(value: SurveyWeightedCrossTab, included: number, rows: number, columns: number) {
  function isWeightList(list: unknown, length: number) {
    return Array.isArray(list) && list.length === length && list.every(isWeight)
  }
  return !!value && isCount(value.included) && value.included <= included && isWeight(value.totalWeight)
    && isWeightList(value.rowTotals, rows) && isWeightList(value.columnTotals, columns)
    && Array.isArray(value.weights) && value.weights.length === rows
    && value.weights.every((row) => isWeightList(row, columns))
}

function isReliability(value: unknown, valid: number): value is SurveyReliability {
  if (!isReliabilityOptions(value)) return false
  const { included, missing, nonNumeric, alpha } = value as Partial<SurveyReliability>
  return isCount(included) && isCount(missing) && isCount(nonNumeric) && included + missing + nonNumeric === valid
    && (alpha === undefined || (included >= 2 && Number.isFinite(alpha)))
}

function isRegression(value: unknown, valid: number): value is SurveyRegression {
  if (!isRegressionOptions(value)) return false
  const { included, missing, nonNumeric, fit, unavailableReason } = value as Partial<SurveyRegression>
  if (!isCount(included) || !isCount(missing) || !isCount(nonNumeric) || included + missing + nonNumeric !== valid) return false
  if (fit === undefined) {
    return unavailableReason === 'INSUFFICIENT_ROWS' || unavailableReason === 'CONSTANT_PREDICTOR'
      || unavailableReason === 'OUT_OF_RANGE'
  }
  return !!fit && unavailableReason === undefined && included >= 2
    && Number.isFinite(fit.intercept) && Number.isFinite(fit.slope)
    && (fit.rSquared === undefined || (Number.isFinite(fit.rSquared) && fit.rSquared >= 0 && fit.rSquared <= 1))
}

function isWeighting(value: unknown, valid: number, columns: string[]): value is SurveyWeighting {
  const weighting = value as Partial<SurveyWeighting> | null
  if (!weighting) return false
  const { column, included, missing, invalid, totalWeight } = weighting
  return typeof column === 'string' && columns.includes(column)
    && isCount(included) && isCount(missing) && isCount(invalid) && included + missing + invalid === valid
    && typeof totalWeight === 'string' && /^[0-9]+(?:\.[0-9]+)?$/.test(totalWeight)
}

function isMultipleRegression(value: unknown, valid: number): value is SurveyMultipleRegression {
  if (!isMultipleRegressionOptions(value)) return false
  const r = value as Partial<SurveyMultipleRegression>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.nonNumeric) || r.included + r.missing + r.nonNumeric !== valid) return false
  if (r.fit === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const { fit } = r
  const validCoef = (c: unknown): c is SurveyMultipleRegressionCoefficient => {
    const entry = c as Partial<SurveyMultipleRegressionCoefficient> | null
    return !!entry && typeof entry.name === 'string' && isFiniteNumber(entry.coefficient) && isFiniteNumber(entry.stdError)
      && (entry.tStat === undefined || isFiniteNumber(entry.tStat))
  }
  return !!fit && validCoef(fit.intercept) && Array.isArray(fit.predictors) && fit.predictors.every(validCoef)
    && isCount(fit.dfModel) && isCount(fit.dfResidual)
}

function isFactorAnalysis(value: unknown, valid: number): value is SurveyFactorAnalysis {
  if (!isFactorAnalysisOptions(value)) return false
  const r = value as Partial<SurveyFactorAnalysis>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.nonNumeric) || r.included + r.missing + r.nonNumeric !== valid) return false
  if (r.result === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const res = r.result
  return !!res && isFiniteNumber(res.kmo) && isFiniteNumber(res.bartlettChiSquare) && isCount(res.bartlettDf)
    && isFiniteNumber(res.bartlettPValue) && isCount(res.factorCount) && typeof res.autoFactors === 'boolean'
    && Array.isArray(res.eigenvalues) && Array.isArray(res.factors) && Array.isArray(res.variables)
}

function isKano(value: unknown, valid: number): value is SurveyKano {
  if (!isKanoOptions(value)) return false
  const r = value as Partial<SurveyKano>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.invalid) || r.included + r.missing + r.invalid !== valid) return false
  if (r.result === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const res = r.result
  return !!res && typeof res.dominantCode === 'string' && typeof res.dominantLabel === 'string' && Array.isArray(res.categories)
}

function isPSM(value: unknown, valid: number): value is SurveyPSM {
  if (!isPSMOptions(value)) return false
  const r = value as Partial<SurveyPSM>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.nonNumeric) || !isCount(r.inconsistent)
    || r.included + r.missing + r.nonNumeric + r.inconsistent !== valid) return false
  if (r.result === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const res = r.result
  return !!res && isFiniteNumber(res.pmc) && isFiniteNumber(res.pme) && isFiniteNumber(res.opp) && isFiniteNumber(res.idp)
    && Array.isArray(res.acceptableRange) && res.acceptableRange.length === 2
}

function isConjoint(value: unknown, valid: number): value is SurveyConjoint {
  if (!isConjointOptions(value)) return false
  const r = value as Partial<SurveyConjoint>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.nonNumeric) || r.included + r.missing + r.nonNumeric !== valid) return false
  if (r.result === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const res = r.result
  return !!res && isFiniteNumber(res.intercept) && Array.isArray(res.attributes)
}

function isCFA(value: unknown, valid: number): value is SurveyCFA {
  if (!isCFAOptions(value)) return false
  const r = value as Partial<SurveyCFA>
  if (!isCount(r.included) || !isCount(r.missing) || !isCount(r.nonNumeric) || r.included + r.missing + r.nonNumeric !== valid) return false
  if (r.result === undefined) return typeof r.unavailableReason === 'string' && r.unavailableReason.length > 0
  const res = r.result
  return !!res && Array.isArray(res.constructs) && Array.isArray(res.correlationMatrix) && typeof res.discriminantValid === 'boolean'
}

function isConfidenceLevel(value: unknown): value is SampleSizeConfidenceLevel {
  return value === 90 || value === 95 || value === 99
}

function isSampleSizeInput(value: unknown): value is SurveySampleSizeInput {
  const input = value as Partial<SurveySampleSizeInput> | null
  return !!input
    && (input.method === undefined || typeof input.method === 'string')
    && isConfidenceLevel(input.confidenceLevel)
    && typeof input.expectedProportion === 'number' && Number.isFinite(input.expectedProportion)
    && typeof input.marginOfError === 'number' && Number.isFinite(input.marginOfError)
    && (input.populationSize === undefined || (isCount(input.populationSize) && input.populationSize > 0))
    && typeof input.responseRate === 'number' && Number.isFinite(input.responseRate)
}

function isSampleSizeResult(value: unknown): value is SurveySampleSizeResult {
  const result = value as Partial<SurveySampleSizeResult> | null
  return !!result
    && typeof result.method === 'string' && result.method.length > 0
    && typeof result.zScore === 'number' && Number.isFinite(result.zScore)
    && isCount(result.unadjustedSampleSize) && result.unadjustedSampleSize > 0
    && isCount(result.finitePopulationAdjusted) && result.finitePopulationAdjusted > 0
    && isCount(result.plannedInvitations) && result.plannedInvitations > 0
    && typeof result.formulaSummary === 'string' && result.formulaSummary.length > 0
}

function isAnalysisPlan(value: unknown): value is SurveyAnalysisPlan {
  const plan = value as Partial<SurveyAnalysisPlan> | null
  return !!plan
    && isId(plan.planId) && isId(plan.projectId)
    && isCount(plan.version) && plan.version > 0
    && typeof plan.createdAt === 'string' && Number.isFinite(Date.parse(plan.createdAt))
    && typeof plan.title === 'string' && plan.title.length > 0
    && typeof plan.objective === 'string' && plan.objective.length > 0
    && typeof plan.targetPopulation === 'string' && plan.targetPopulation.length > 0
    && typeof plan.samplingMethod === 'string' && plan.samplingMethod.length > 0
    && isStringList(plan.plannedMethods) && plan.plannedMethods.length > 0
    && typeof plan.missingStrategy === 'string' && plan.missingStrategy.length > 0
    && (plan.notes === undefined || typeof plan.notes === 'string')
    && isSampleSizeInput(plan.sampleSizeInput)
    && isSampleSizeResult(plan.sampleSizeResult)
}

function isAnalysisPlanList(value: unknown): value is SurveyAnalysisPlanList {
  const list = value as Partial<SurveyAnalysisPlanList> | null
  return !!list && isCount(list.latestVersion) && Array.isArray(list.items) && list.items.every(isAnalysisPlan)
}

function isAnalysisPlanSource(value: unknown): value is SurveyAnalysisPlanSource {
  const source = value as Partial<SurveyAnalysisPlanSource> | null
  return !!source
    && isId(source.planId)
    && isCount(source.version) && source.version > 0
    && typeof source.title === 'string' && source.title.length > 0
    && typeof source.objective === 'string' && source.objective.length > 0
    && typeof source.method === 'string' && source.method.length > 0
    && isConfidenceLevel(source.confidenceLevel)
    && isCount(source.unadjustedSampleSize) && source.unadjustedSampleSize > 0
    && isCount(source.finitePopulationAdjusted) && source.finitePopulationAdjusted > 0
    && isCount(source.plannedInvitations) && source.plannedInvitations > 0
    && typeof source.formulaSummary === 'string' && source.formulaSummary.length > 0
    && isStringList(source.plannedMethods) && source.plannedMethods.length > 0
    && typeof source.createdAt === 'string' && Number.isFinite(Date.parse(source.createdAt))
}

function isReport(value: unknown): value is SurveyReport {
  const report = value as Partial<SurveyReport> | null
  return !!report && isCounts(value) && isStringList(report.columns)
    && Array.isArray(report.rows) && report.rows.every(isRow)
    && Array.isArray(report.summaries) && report.summaries.every(isSummary)
    && (report.crossTab === undefined || isCrossTab(report.crossTab))
    && (report.reliability === undefined || isReliability(report.reliability, value.valid))
    && (report.regression === undefined || isRegression(report.regression, value.valid))
    && (report.multipleRegression === undefined || isMultipleRegression(report.multipleRegression, value.valid))
    && (report.factorAnalysis === undefined || isFactorAnalysis(report.factorAnalysis, value.valid))
    && (report.kano === undefined || isKano(report.kano, value.valid))
    && (report.psm === undefined || isPSM(report.psm, value.valid))
    && (report.conjoint === undefined || isConjoint(report.conjoint, value.valid))
    && (report.cfa === undefined || isCFA(report.cfa, value.valid))
    && (report.weighting === undefined || isWeighting(report.weighting, value.valid, report.columns))
    && (report.planSource === undefined || isAnalysisPlanSource(report.planSource))
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

function projectPath(projectId: string) {
  if (!isId(projectId)) throw new ProjectApiError('项目链接无效。', 400)
  return `/api/v1/projects/${encodeURIComponent(projectId)}`
}

function surveysPath(projectId: string) {
  return `${projectPath(projectId)}/surveys`
}

export async function listSurveySheets(projectId: string, materialId: string, signal: AbortSignal) {
  if (!isId(materialId)) throw new ProjectApiError('资料链接无效。', 400)
  return request(`${projectPath(projectId)}/materials/${encodeURIComponent(materialId)}/survey-sheets`,
    (value): value is { sheets: string[] } => {
      const sheets = (value as { sheets?: unknown } | null)?.sheets
      return Array.isArray(sheets) && sheets.length > 0 && sheets.every(isSheetName) && new Set(sheets).size === sheets.length
    }, { signal })
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
  return request(surveysPath(projectId), (value): value is SurveyRun =>
    isRun(value) && value.materialId === input.materialId && value.options.sheet === input.options.sheet, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal,
  })
}

export async function mapSurveySettings(projectId: string, sourceAnalysisId: string, input: SurveyFieldMappingInput, signal: AbortSignal) {
  return request(`${surveyPath(projectId, sourceAnalysisId)}/map-settings`, (value): value is SurveyRun =>
    isRun(value) && value.options.settingsSource?.analysisId === sourceAnalysisId, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal,
  })
}

export function surveyDownloadPath(projectId: string, analysisId: string) {
  return `${surveyPath(projectId, analysisId)}/download`
}

function ceilSampleCount(value: number): number | undefined {
  if (!Number.isFinite(value) || value <= 0) return undefined
  const rounded = Math.round(value)
  const ceiled = Math.max(1, Math.abs(value - rounded) <= 1e-12 ? rounded : Math.ceil(value))
  return ceiled <= maxCalculatedSample ? ceiled : undefined
}

export function calculateSampleSizePreview(input: SurveySampleSizeInput): { result: SurveySampleSizeResult } | { error: string } {
  const method = input.method?.trim() || sampleSizeMethodCochran
  if (method !== sampleSizeMethodCochran) {
    return { error: `不支持的样本量计算口径 ${method}。` }
  }
  if (!isConfidenceLevel(input.confidenceLevel)) {
    return { error: '置信水平仅支持 90%、95% 或 99%。' }
  }
  const z = sampleSizeZScores[input.confidenceLevel]
  const p = input.expectedProportion
  if (!Number.isFinite(p) || p <= 0 || p >= 1) {
    return { error: '预期比例 p 须为大于 0 且小于 1 的数值。' }
  }
  const e = input.marginOfError
  if (!Number.isFinite(e) || e <= 0 || e >= 0.5) {
    return { error: '允许误差 e 须为大于 0 且小于 0.5 的数值。' }
  }
  let pop: number | undefined
  if (input.populationSize !== undefined) {
    if (!Number.isSafeInteger(input.populationSize) || input.populationSize < 0 || input.populationSize > maxSamplePopulation) {
      return { error: `总体规模 N 若指定则须为 1 至 ${maxSamplePopulation} 的整数，留空或 0 表示无限总体。` }
    }
    if (input.populationSize > 0) pop = input.populationSize
  }
  const r = input.responseRate
  if (!Number.isFinite(r) || r <= 0 || r > 1) {
    return { error: '预期有效回收率 r 须为大于 0 且不超过 1 的数值。' }
  }

  const n0Raw = (z * z * p * (1 - p)) / (e * e)
  const unadjusted = ceilSampleCount(n0Raw)
  if (unadjusted === undefined) return { error: '基础样本量超出系统计算上限。' }

  const fpcAdjusted = pop !== undefined ? ceilSampleCount(n0Raw / (1 + (n0Raw - 1) / pop)) : unadjusted
  if (fpcAdjusted === undefined) return { error: '有限总体校正样本量超出系统计算上限。' }

  const planned = ceilSampleCount(fpcAdjusted / r)
  if (planned === undefined) return { error: '计划发放量超出系统计算上限。' }

  const rPct = Number((Math.round(r * 10000) / 100).toString())
  const base = `Cochran 比例抽样口径（${method}）：置信水平 ${input.confidenceLevel}%（z = ${z.toFixed(4)}），预期比例 p = ${p}，允许误差 e = ${e}，基础样本量 n₀ = ⌈z²·p·(1−p)/e²⌉ = ${unadjusted}`
  const formulaSummary = pop !== undefined
    ? `${base}；有限总体 N = ${pop}，校正样本量 n_fpc = ⌈n₀,raw / (1 + (n₀,raw − 1) / N)⌉ = ${fpcAdjusted}；预期有效回收率 ${rPct}%（r = ${r}），计划发放量 = ⌈${fpcAdjusted} / ${r}⌉ = ${planned}。`
    : `${base}；按无限总体计算（n_fpc = ${fpcAdjusted}）；预期有效回收率 ${rPct}%（r = ${r}），计划发放量 = ⌈${fpcAdjusted} / ${r}⌉ = ${planned}。`

  return {
    result: {
      method,
      zScore: z,
      unadjustedSampleSize: unadjusted,
      finitePopulationAdjusted: fpcAdjusted,
      plannedInvitations: planned,
      formulaSummary,
    },
  }
}

export async function listSurveyPlans(projectId: string, signal: AbortSignal) {
  return request(`${projectPath(projectId)}/survey-plans`, isAnalysisPlanList, { signal })
}

export async function createSurveyPlan(projectId: string, input: SurveyAnalysisPlanInput, signal: AbortSignal) {
  return request(`${projectPath(projectId)}/survey-plans`, isAnalysisPlan, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal,
  })
}
