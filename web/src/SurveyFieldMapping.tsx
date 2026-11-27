import { useEffect, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { errorMessage } from './projects'
import { readSurvey } from './surveys'
import type { SurveyFieldPair, SurveyMetadata, SurveyRun } from './surveys'

const reportDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })

function fieldKey(name: string) {
  // Match Go's per-code-point lowercase and Unicode whitespace trimming for survey headers.
  const trimmed = name.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
  return Array.from(trimmed, (character) => character === 'İ' ? 'i' : character.toLowerCase()).join('')
}

function fieldsUsedBy({ options, report }: SurveyRun) {
  const headers = new Map(report.columns.map((column) => [fieldKey(column), column]))
  const fields = new Map<string, Set<string>>()
  function add(usage: string, columns: readonly string[] = []) {
    for (const name of columns) {
      const column = headers.get(fieldKey(name)) ?? name
      if (!fields.has(column)) fields.set(column, new Set())
      fields.get(column)?.add(usage)
    }
  }

  add('必答', options.requiredColumns)
  add('查重', options.duplicateColumns)
  if (options.weightColumn) add('权重', [options.weightColumn])
  if (options.crossTab) add('交叉统计', [options.crossTab.rowColumn, options.crossTab.columnColumn])
  add('量表信度', options.reliability?.columns)
  if (options.regression) add('线性回归', [options.regression.predictorColumn, options.regression.outcomeColumn])
  if (options.multipleRegression) add('多元回归', [options.multipleRegression.outcomeColumn, ...options.multipleRegression.predictorColumns])
  add('效度与因子分析', options.factorAnalysis?.columns)
  if (options.kano) add('Kano 模型', [options.kano.functionalColumn, options.kano.dysfunctionalColumn])
  if (options.psm) add('PSM 价格敏感度', [options.psm.tooCheapColumn, options.psm.cheapColumn, options.psm.expensiveColumn, options.psm.tooExpensiveColumn])
  if (options.conjoint) add('联合分析', [options.conjoint.ratingColumn, ...options.conjoint.attributeColumns])
  if (options.cfa) {
    for (const construct of options.cfa.constructs) add(`CFA（${construct.name}）`, construct.columns)
  }
  add('分类编码', options.categoryMappings?.map(({ column }) => column))
  return [...fields]
}

function FieldPairs({ source, columns, busy, onEdit, onApply }: {
  source: SurveyRun
  columns: string[]
  busy: boolean
  onEdit: () => void
  onApply: (fields: SurveyFieldPair[]) => void
}) {
  const id = useId()
  const [fields] = useState(() => fieldsUsedBy(source))
  const [targets, setTargets] = useState(() => {
    const byName = new Map(columns.map((column) => [fieldKey(column), column]))
    return fields.map(([from]) => byName.get(fieldKey(from)) ?? '')
  })
  const selected = new Set(targets.filter(Boolean))
  const ready = selected.size === fields.length

  return <>
    {fields.length === 0 && <p className="survey-note">所选报告没有需要对应的字段。</p>}
    {fields.length > 0 && <>
      <p className="survey-note" id={`${id}-pairs`}>同名字段已预选。请确认每个历史字段对应一个不同的当前字段。</p>
      {columns.length < fields.length && <p className="survey-note" role="status">当前报告的字段数量不足，无法完成一一对应。</p>}
      <div className="survey-cross-tab-fields">
        {fields.map(([from, usages], index) => <div className="field" key={from}>
          <label htmlFor={`${id}-${index}`}><bdi>{from}</bdi> 对应的当前字段</label>
          <select id={`${id}-${index}`} data-survey-field-mapping-target={from} value={targets[index]}
            disabled={busy} aria-describedby={`${id}-usage-${index}`} onChange={(event) => {
              const value = event.currentTarget.value
              setTargets((current) => current.map((target, position) => position === index ? value : target))
              onEdit()
            }}>
            <option value="">请选择字段</option>
            {columns.map((column) => <option key={column} value={column}
              disabled={selected.has(column) && targets[index] !== column}>{column}</option>)}
          </select>
          <p className="survey-note" id={`${id}-usage-${index}`}>{[...usages].join('、')}</p>
        </div>)}
      </div>
    </>}
    <button type="button" className="button outline" data-survey-field-mapping-apply disabled={busy || !ready}
      aria-describedby={fields.length ? `${id}-pairs` : undefined}
      onClick={() => onApply(fields.map(([from], index) => ({ from, to: targets[index] })))}>确认沿用并生成报告</button>
  </>
}

function LoadedFieldPairs({ projectId, analysisId, ...props }: Omit<Parameters<typeof FieldPairs>[0], 'source'> & {
  projectId: string
  analysisId: string
}) {
  const [result, setResult] = useState<{ source: SurveyRun } | { error: string }>()
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void readSurvey(projectId, analysisId, controller.signal).then((source) => {
      if (!controller.signal.aborted) setResult({ source })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setResult({ error: errorMessage(cause) })
    })
    return () => controller.abort()
  }, [projectId, analysisId, retry])

  if (!result) return <p className="survey-note" role="status">正在加载历史分析设置…</p>
  if ('error' in result) return <div className="survey-actions">
    <p className="form-error" role="alert">{result.error}</p>
    <button type="button" className="button outline" disabled={props.busy} onClick={() => {
      setResult(undefined)
      setRetry((count) => count + 1)
    }}>重试加载历史分析设置</button>
  </div>
  return <FieldPairs source={result.source} {...props} />
}

export function SurveyFieldMapping({ projectId, target, reports, historyControls, loadingHistory, busy, error, onEdit, onApply }: {
  projectId: string
  target: SurveyRun
  reports: SurveyMetadata[]
  historyControls: ReactNode
  loadingHistory: boolean
  busy: boolean
  error?: string
  onEdit: () => void
  onApply: (analysisId: string, fields: SurveyFieldPair[]) => void
}) {
  const id = useId()
  const [selectedId, setSelectedId] = useState('')
  const eligible = reports.filter(({ materialId, materialSha256 }) => materialId !== target.materialId
    && materialSha256.toLowerCase() !== target.materialSha256.toLowerCase())
  const sourceId = eligible.find(({ analysisId }) => analysisId === selectedId)?.analysisId ?? ''

  return <details className="survey-details" data-survey-field-mapping>
    <summary>沿用历史分析设置</summary>
    <div className="survey-section">
      <p className="survey-note" id={`${id}-help`}>可沿用同项目其他资料版本的分析设置。新报告基于当前资料和工作表，答卷复核需根据新资料重新进行。</p>
      <div className="field">
        <label htmlFor={id}>历史分析报告</label>
        <select id={id} data-survey-field-mapping-source value={sourceId} disabled={busy || !eligible.length}
          aria-describedby={`${id}-help`} onChange={(event) => {
            setSelectedId(event.currentTarget.value)
            onEdit()
          }}>
          <option value="">请选择历史报告</option>
          {eligible.map(({ analysisId, materialName, createdAt }) => <option value={analysisId} key={analysisId}>
            {materialName} · {reportDate.format(new Date(createdAt))} · {analysisId}
          </option>)}
        </select>
      </div>
      {historyControls}
      {!eligible.length && !loadingHistory && <p className="survey-note">已加载的报告中没有其他资料版本的报告。</p>}
      {sourceId && <LoadedFieldPairs key={sourceId} projectId={projectId} analysisId={sourceId} columns={target.report.columns}
        busy={busy} onEdit={onEdit} onApply={(fields) => onApply(sourceId, fields)} />}
      {error && <p className="form-error" data-survey-field-mapping-error role="alert">{error}</p>}
    </div>
  </details>
}
