import { useCallback, useEffect, useId, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { listMaterials } from './materials'
import { errorMessage } from './projects'
import { ReportCoding } from './SurveyCoding'
import { ReportAdvancedStats } from './SurveyAdvancedStats'
import type { AdvancedStatMethod } from './SurveyAdvancedStats'
import { SurveyFieldMapping } from './SurveyFieldMapping'
import { ReportRegression } from './SurveyRegression'
import {
  analyzeSurvey, crossTabCategoryLimit, crossTabLabelByteLimit, listSurveySheets, listSurveys, mapSurveySettings,
  readSurvey, reliabilityItemLimit, surveyDownloadPath,
} from './surveys'
import type { SurveyCrossTab, SurveyCrossTabOptions, SurveyFieldPair, SurveyOptions, SurveyReliability, SurveyReliabilityOptions, SurveyReview, SurveyRow, SurveyRun, SurveySummary, SurveyWeighting } from './surveys'
import './survey.css'

const reportDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })
const numberFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 6 })
const rowPageSize = 20
const rowLabels = { VALID: '有效', INVALID: '无效', PENDING: '待复核' } as const
const ruleNames = ['requiredColumns', 'duplicateColumns'] as const
type RuleName = typeof ruleNames[number]
type SurveyCalculation = 'analyze' | 'coding' | 'weighting' | 'cross-tab' | 'reliability' | 'regression' | 'advanced'
type SurveyOperation = SurveyCalculation | 'map-settings' | 'read'
const surveyOperationLabels: Record<SurveyOperation, string> = {
  analyze: '正在分析并保存报告…',
  coding: '正在应用分类编码并保存报告…',
  weighting: '正在更新权重并保存报告…',
  'cross-tab': '正在计算交叉统计…',
  reliability: '正在计算量表信度…',
  regression: '正在计算线性回归…',
  advanced: '正在计算高级统计并保存报告…',
  'map-settings': '正在沿用历史分析设置并保存报告…',
  read: '正在打开报告…',
}

function useSurveyList<T>(load: (signal: AbortSignal, cursor?: string) => Promise<{ items: T[]; nextCursor?: string }>, idOf: (item: T) => string) {
  const [page, setPage] = useState<{ items: T[]; nextCursor?: string }>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal, query.cursor).then((next) => {
      if (controller.signal.aborted) return
      setPage((current) => {
        if (!query.cursor) return next
        const byId = new Map(current.items.map((item) => [idOf(item), item]))
        for (const item of next.items) byId.set(idOf(item), item)
        return { items: [...byId.values()], nextCursor: next.nextCursor }
      })
      setLoading(false)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setError(errorMessage(cause))
      setLoading(false)
    })
    return () => controller.abort()
  }, [load, idOf, query])

  function loadPage(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  return { ...page, loading, error, refresh: () => loadPage(), retry: () => loadPage(query.cursor), more: () => loadPage(page.nextCursor) }
}

function ListControls({ list, label }: { list: ReturnType<typeof useSurveyList<unknown>>; label: string }) {
  if (list.loading) return <p className="survey-note" role="status">正在加载{label}…</p>
  if (list.error) return <div className="survey-actions">
    <p className="form-error" role="alert">{list.error}</p>
    <button className="button outline" type="button" onClick={list.retry}>重试加载{label}</button>
  </div>
  if (list.nextCursor) return <button className="button outline" type="button" onClick={list.more}>加载更多{label}</button>
  return null
}

function ReportWeighting({ summaries, valid, result, busy, error, onApply }: {
  summaries: SurveySummary[]
  valid: number
  result?: SurveyWeighting
  busy: boolean
  error?: string
  onApply: (column?: string) => void
}) {
  const id = useId()
  const appliedColumn = result?.column ?? ''
  const [column, setColumn] = useState(appliedColumn)
  const columns = summaries.map(({ column }) => column)

  return <section className="survey-section" aria-labelledby={`${id}-heading`}>
    <h6 id={`${id}-heading`}>答卷权重</h6>
    {result && <>
      <p className="survey-note">当前权重列：<bdi>{result.column}</bdi></p>
      <dl className="survey-counts">
        <div><dt>未加权有效答卷</dt><dd>{valid}</dd></div>
        <div><dt>参与加权答卷</dt><dd>{result.included}</dd></div>
        <div><dt>缺失权重</dt><dd>{result.missing}</dd></div>
        <div><dt>无效权重</dt><dd>{result.invalid}</dd></div>
        <div><dt>权重合计</dt><dd>{result.totalWeight}</dd></div>
      </dl>
    </>}
    {columns.length > 0 ? <>
      <div className="field">
        <label htmlFor={id}>权重列</label>
        <select id={id} value={column} disabled={busy} aria-describedby={`${id}-help`}
          onChange={(event) => setColumn(event.currentTarget.value)}>
          <option value="">不使用权重</option>
          {columns.map((name) => <option key={name} value={name}>{name}</option>)}
        </select>
      </div>
      <p className="survey-note" id={`${id}-help`}>
        权重须为大于 0、至多 1,000,000 的普通十进制数，最多 6 位小数。应用后会沿用当前报告的清洗规则和复核决定，生成新报告。
      </p>
      <button className="button outline" type="button" disabled={busy || column === appliedColumn}
        onClick={() => onApply(column || undefined)}>应用权重</button>
    </> : <p className="survey-note">此报告没有可用的权重列。</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>
}

function ReportSummary({ summaries, valid }: { summaries: SurveySummary[]; valid: number }) {
  const selectId = useId()
  const [column, setColumn] = useState(summaries[0]?.column ?? '')
  const summary = summaries.find((item) => item.column === column)

  return <section className="survey-section" aria-labelledby={`${selectId}-heading`}>
    <h6 id={`${selectId}-heading`}>答卷统计</h6>
    <p className="survey-note">描述统计覆盖 {valid} 份有效答卷。均值和范围按数值回答计算，每份答卷计一次。</p>
    {summary ? <>
      <div className="field">
        <label htmlFor={selectId}>统计列</label>
        <select id={selectId} value={column} onChange={(event) => setColumn(event.target.value)}>
          {summaries.map(({ column }) => <option key={column} value={column}>{column}</option>)}
        </select>
      </div>
      <p>已回答 {summary.answered} 份，缺失 {summary.missing} 份，数值回答 {summary.numeric} 份。</p>
      {summary.mean !== undefined && summary.min !== undefined && summary.max !== undefined && (
        <p>均值 {numberFormat.format(summary.mean)}，范围 {numberFormat.format(summary.min)} 至 {numberFormat.format(summary.max)}。</p>
      )}
      {summary.weighted && <p>加权均值 {numberFormat.format(summary.weighted.mean)}，纳入 {summary.weighted.included} 份数值答卷，权重合计 {summary.weighted.totalWeight}。</p>}
      {summary.frequencies.length > 0 && <dl className="survey-frequencies" aria-label="常见回答及次数">
        {summary.frequencies.map(({ value, count }) => <div key={value}>
          <dt dir="auto">{value}</dt><dd>{count} 次</dd>
        </div>)}
      </dl>}
    </> : <p className="survey-note">此报告没有可汇总的列。</p>}
  </section>
}

function CrossTabTable({ tab, totals }: {
  tab: SurveyCrossTab
  totals?: NonNullable<SurveyCrossTab['weighted']>
}) {
  const values = totals?.weights ?? tab.counts
  const rowTotals = totals?.rowTotals ?? tab.rowTotals
  const columnTotals = totals?.columnTotals ?? tab.columnTotals
  return <div className="survey-cross-tab-table" role="region" aria-label={totals ? '加权交叉统计表' : '交叉统计表'} tabIndex={0}>
    <table>
      <caption><bdi>{tab.rowColumn}</bdi> × <bdi>{tab.columnColumn}</bdi>（{totals ? '权重合计' : '次数'}）</caption>
      <thead><tr>
        <th scope="col" dir="auto">{tab.rowColumn}</th>
        {tab.columnLabels.map((label) => <th key={label} scope="col" dir="auto">{label}</th>)}
        <th scope="col">行合计</th>
      </tr></thead>
      <tbody>{tab.rowLabels.map((label, rowIndex) => <tr key={label}>
        <th scope="row" dir="auto">{label}</th>
        {tab.columnLabels.map((column, columnIndex) => <td key={column}>{values[rowIndex][columnIndex]}</td>)}
        <td className="survey-cross-tab-total">{rowTotals[rowIndex]}</td>
      </tr>)}</tbody>
      <tfoot><tr>
        <th scope="row">列合计</th>
        {tab.columnLabels.map((column, index) => <td key={column}>{columnTotals[index]}</td>)}
        <td>{totals?.totalWeight ?? tab.included}</td>
      </tr></tfoot>
    </table>
  </div>
}

function ReportCrossTab({ summaries, result, busy, error, onCalculate }: {
  summaries: SurveySummary[]
  result?: SurveyCrossTab
  busy: boolean
  error?: string
  onCalculate: (options: SurveyCrossTabOptions) => void
}) {
  const id = useId()
  const [rowColumn, setRowColumn] = useState(result?.rowColumn ?? '')
  const [columnColumn, setColumnColumn] = useState(result?.columnColumn ?? '')
  const columns = summaries.map(({ column }) => column)
  const canCalculate = !busy && columns.includes(rowColumn) && columns.includes(columnColumn) && rowColumn !== columnColumn

  return <section className="survey-section" aria-labelledby={`${id}-heading`}>
    <h6 id={`${id}-heading`}>交叉统计</h6>
    {columns.length >= 2 ? <>
      <div className="survey-cross-tab-fields">
        <div className="field">
          <label htmlFor={`${id}-row`}>行分类</label>
          <select id={`${id}-row`} value={rowColumn} disabled={busy}
            onChange={(event) => setRowColumn(event.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((column) => <option key={column} value={column} disabled={column === columnColumn}>{column}</option>)}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-column`}>列分类</label>
          <select id={`${id}-column`} value={columnColumn} disabled={busy}
            onChange={(event) => setColumnColumn(event.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((column) => <option key={column} value={column} disabled={column === rowColumn}>{column}</option>)}
          </select>
        </div>
      </div>
      <p className="survey-note" id={`${id}-help`}>
        计算沿用当前报告已应用的清洗规则和复核决定。每个字段最多 {crossTabCategoryLimit} 类，类别名称最多 {crossTabLabelByteLimit} 个 UTF-8 字节。
      </p>
      <button className="button outline" type="button" disabled={!canCalculate}
        aria-describedby={error ? `${id}-help ${id}-error` : `${id}-help`}
        onClick={() => onCalculate({ rowColumn, columnColumn })}>计算交叉统计</button>
    </> : <p className="survey-note">至少需要两个可统计字段。</p>}
    {error && <p className="form-error" id={`${id}-error`} role="alert">{error}</p>}
    {result && <>
      <p className="survey-note" role="status">完整配对 {result.included} 份。有效答卷中任一字段缺失 {result.missing} 份。</p>
      <CrossTabTable tab={result} />
      {result.weighted && <>
        <p className="survey-note">完整配对中有 {result.weighted.included} 份权重有效，权重合计 {result.weighted.totalWeight}。</p>
        <CrossTabTable tab={result} totals={result.weighted} />
      </>}
    </>}
  </section>
}

function ReportReliability({ summaries, result, busy, error, onCalculate }: {
  summaries: SurveySummary[]
  result?: SurveyReliability
  busy: boolean
  error?: string
  onCalculate: (options: SurveyReliabilityOptions) => void
}) {
  const id = useId()
  const [selected, setSelected] = useState(() => result?.columns ?? [])
  const columns = summaries.map(({ column }) => column)
  const atLimit = selected.length === reliabilityItemLimit

  return <section className="survey-section" aria-labelledby={`${id}-heading`}>
    <h6 id={`${id}-heading`}>量表信度</h6>
    {result && <div className="survey-section" role="status">
      <strong>{result.alpha === undefined ? '当前答卷无法计算 Cronbach α' : `Cronbach α ${numberFormat.format(result.alpha)}`}</strong>
      <p className="survey-note">本次题项 <bdi>{result.columns.join('、')}</bdi></p>
      <p className="survey-note">纳入 {result.included} 份，缺失 {result.missing} 份，非数值 {result.nonNumeric} 份。</p>
      {result.alpha === undefined && <p className="survey-note">计算需要至少两份完整数值答卷，且总分须有变异。</p>}
    </div>}
    {columns.length >= 2 ? <>
      <p className="survey-note" id={`${id}-help`}>
        选择同一量表的 2 至 {reliabilityItemLimit} 个题项，反向题请先完成反向计分。计算沿用当前报告的清洗规则与复核决定。
      </p>
      <fieldset className="survey-scale-items" aria-describedby={`${id}-help`} disabled={busy}>
        <legend>量表题项（已选 {selected.length} 项）</legend>
        {columns.map((column) => <label key={column}>
          <input type="checkbox" checked={selected.includes(column)} disabled={atLimit && !selected.includes(column)}
            onChange={(event) => {
              const checked = event.currentTarget.checked
              setSelected((current) => checked ? [...current, column] : current.filter((item) => item !== column))
            }} />
          <bdi>{column}</bdi>
        </label>)}
      </fieldset>
      <button className="button outline" type="button" disabled={busy || selected.length < 2}
        onClick={() => onCalculate({ columns: selected })}>计算量表信度</button>
    </> : <p className="survey-note">至少需要两个可统计字段。</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>
}

type DraftReview = { accept: boolean; reason: string }

function positiveRowNumber(value: string) {
  if (!/^\d+$/.test(value) || value.length > 15) return undefined
  const number = Number(value)
  return Number.isSafeInteger(number) && number > 0 ? number : undefined
}

function ReportRows({ rows, busy, rulesChanged, onReview }: {
  rows: SurveyRow[]
  busy: boolean
  rulesChanged: boolean
  onReview: (reviews: SurveyReview[]) => void
}) {
  const filterId = useId()
  const rowQueryId = useId()
  const [filter, setFilter] = useState<SurveyRow['status'] | 'all'>('all')
  const [rowQuery, setRowQuery] = useState('')
  const [rowNumber, setRowNumber] = useState<number>()
  const [rowQueryError, setRowQueryError] = useState('')
  const [page, setPage] = useState(0)
  const [drafts, setDrafts] = useState<Record<number, DraftReview>>({})
  const [error, setError] = useState('')
  const statusRows = filter === 'all' ? rows : rows.filter((row) => row.status === filter)
  const visible = rowQueryError ? [] : rowNumber === undefined ? statusRows
    : statusRows.filter((row) => row.row === rowNumber || row.sourceRow === rowNumber)
  const start = page * rowPageSize
  const end = Math.min(start + rowPageSize, visible.length)
  const reviewCount = Object.keys(drafts).length

  function applyRowQuery(raw: string) {
    const text = raw.trim()
    setRowQuery(text)
    setPage(0)
    if (text === '') {
      setRowNumber(undefined)
      setRowQueryError('')
      return
    }
    const number = positiveRowNumber(text)
    if (number === undefined) {
      setRowNumber(undefined)
      setRowQueryError('请输入正整数。')
      return
    }
    setRowNumber(number)
    setRowQueryError('')
  }

  function clearRowQuery() {
    setRowQuery('')
    setRowNumber(undefined)
    setRowQueryError('')
    setPage(0)
  }

  function updateReview(row: number, review?: DraftReview) {
    setDrafts((current) => {
      const next = { ...current }
      if (review) next[row] = review
      else delete next[row]
      return next
    })
    setError('')
  }

  function submitReviews() {
    const reviews = Object.entries(drafts).map(([row, review]) => ({ ...review, row: Number(row), reason: review.reason.trim() }))
    const encoder = new TextEncoder()
    const invalid = reviews.find(({ reason }) => !reason || encoder.encode(reason).length > 256)
    if (invalid) {
      setError(`第 ${invalid.row} 条答卷需要填写复核理由，最多 256 个 UTF-8 字节。`)
      return
    }
    onReview(reviews)
  }

  return <section className="survey-section" aria-labelledby={`${filterId}-heading`}>
    <h6 id={`${filterId}-heading`}>答卷明细</h6>
    <div className="field">
      <label htmlFor={filterId}>按状态筛选</label>
      <select id={filterId} value={filter} onChange={(event) => {
        setFilter(event.target.value as typeof filter)
        setPage(0)
      }}>
        <option value="all">全部答卷</option>
        <option value="PENDING">待复核</option>
        <option value="INVALID">无效</option>
        <option value="VALID">有效</option>
      </select>
    </div>
    <form className="field" role="search" aria-label="答卷定位" onSubmit={(event) => {
      event.preventDefault()
      applyRowQuery(rowQuery)
    }}>
      <label htmlFor={rowQueryId}>按答卷序号或原行号定位</label>
      <div className="survey-row-search">
        <input id={rowQueryId} type="search" inputMode="numeric" value={rowQuery} autoComplete="off"
          onChange={(event) => setRowQuery(event.currentTarget.value)} />
        <button className="button outline" type="submit">定位</button>
        {(rowQuery || rowNumber !== undefined) && <button className="button outline" type="button" onClick={clearRowQuery}>清除</button>}
      </div>
    </form>
    <p className="survey-note">可输入报告里的答卷序号，或 XLSX 工作表原行号。两种编号对应不同答卷时会同时列出。</p>
    {rulesChanged && <p className="survey-note">请先应用清洗规则，再根据新报告复核。{reviewCount > 0 && '应用后会清空本次复核草稿。'}</p>}
    {visible.length ? <ol className="survey-rows" role="list">
      {visible.slice(start, end).map(({ row, sourceRow, status, reasons }) => {
        const review = drafts[row]
        return <li key={row}>
          <div className="survey-heading">
            <strong>第 {row} 条答卷</strong>
            <span className={`survey-status survey-status-${status.toLowerCase()}`}>{rowLabels[status]}</span>
          </div>
          {sourceRow !== undefined && <p className="survey-note">工作表第 {sourceRow} 行</p>}
          {reasons.length > 0 && <ul className="survey-reasons">
            {reasons.map((reason, index) => <li key={index} dir="auto">{reason}</li>)}
          </ul>}
          {status === 'PENDING' && <div className="field survey-review">
            <label htmlFor={`${filterId}-review-${row}`}>复核决定 <span className="survey-note">第 {row} 条</span></label>
            <select id={`${filterId}-review-${row}`} disabled={busy || rulesChanged}
              value={review ? (review.accept ? 'accept' : 'reject') : ''}
              onChange={(event) => {
                const decision = event.target.value
                updateReview(row, decision ? { accept: decision === 'accept', reason: review?.reason ?? '' } : undefined)
              }}>
              <option value="">保持待复核</option>
              <option value="accept">纳入有效答卷</option>
              <option value="reject">判为无效答卷</option>
            </select>
            {review && <>
              <label htmlFor={`${filterId}-reason-${row}`}>复核理由 <span className="survey-note">第 {row} 条</span></label>
              <input id={`${filterId}-reason-${row}`} type="text" maxLength={256} readOnly={busy || rulesChanged}
                value={review.reason} onChange={(event) => updateReview(row, { ...review, reason: event.target.value })} />
            </>}
          </div>}
        </li>
      })}
    </ol> : rowQueryError ? <p className="form-error" role="alert">{rowQueryError}</p>
      : <p className="survey-note">{rowNumber === undefined ? '暂无符合此筛选条件的答卷。' : '没有匹配的答卷。'}</p>}
    {visible.length > 0 && <div className="survey-actions">
      <p className="survey-note" role="status">显示 {start + 1} 至 {end} 条，共 {visible.length} 条</p>
      {visible.length > rowPageSize && <>
        <button className="button outline" type="button" disabled={page === 0} onClick={() => setPage(page - 1)}>上一页</button>
        <button className="button outline" type="button" disabled={end === visible.length} onClick={() => setPage(page + 1)}>下一页</button>
      </>}
    </div>}
    {reviewCount > 0 && <button className="button primary" type="button" disabled={busy || rulesChanged}
      onClick={submitReviews}>保存 {reviewCount} 条复核决定</button>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </section>
}

function SurveyAnalysis({ projectId, run, busy, newerMaterial, settingsMapping, calculationError, activeAdvancedTab, onAdvancedTabChange, onAnalyze }: {
  projectId: string
  run: SurveyRun
  busy: boolean
  newerMaterial: boolean
  settingsMapping: ReactNode
  calculationError?: { kind: SurveyOperation; message: string }
  activeAdvancedTab: AdvancedStatMethod
  onAdvancedTabChange: (tab: AdvancedStatMethod) => void
  onAnalyze: (options: SurveyOptions, kind?: SurveyCalculation) => void
}) {
  const { report, options } = run
  const heading = useRef<HTMLHeadingElement>(null)
  const [initialRules] = useState(() => {
    function columnsFor(key: RuleName) {
      const selected = new Set(options[key]?.map((name) => name.trim().toLowerCase()))
      return report.columns.filter((column) => selected.has(column.toLowerCase()))
    }
    return { requiredColumns: columnsFor('requiredColumns'), duplicateColumns: columnsFor('duplicateColumns') }
  })
  const [rules, setRules] = useState(initialRules)
  const rulesChanged = ruleNames.some((key) => rules[key].length !== initialRules[key].length
    || rules[key].some((column, index) => column !== initialRules[key][index]))
  const ruleOptions: SurveyOptions = {
    ...options,
    requiredColumns: rules.requiredColumns.length ? rules.requiredColumns : undefined,
    duplicateColumns: rules.duplicateColumns.length ? rules.duplicateColumns : undefined,
    reviews: undefined,
  }

  useEffect(() => { heading.current?.focus() }, [])

  function toggleColumn(key: RuleName, column: string) {
    setRules((current) => {
      const selected = new Set(current[key])
      if (!selected.delete(column)) selected.add(column)
      return { ...current, [key]: report.columns.filter((entry) => selected.has(entry)) }
    })
  }

  return <article className="survey-report">
    <div className="survey-heading">
      <h5 ref={heading} tabIndex={-1} dir="auto">{run.materialName}</h5>
      {report.demo && <strong className="survey-demo">演示数据</strong>}
    </div>
    <div className="survey-actions">
      <time className="survey-note" dateTime={run.createdAt}>{reportDate.format(new Date(run.createdAt))}</time>
      <a className="button outline" href={surveyDownloadPath(projectId, run.analysisId)} download>下载报告 JSON</a>
    </div>
    {options.sheet && <p className="survey-note">工作表 <bdi>{options.sheet}</bdi></p>}
    <p className="survey-note">原资料 SHA-256 <code>{run.materialSha256}</code></p>
    {options.settingsSource && <details className="survey-details" data-survey-settings-source>
      <summary>设置来源与字段对应</summary>
      <div className="survey-section">
        <p className="survey-note">历史报告 <code>{options.settingsSource.analysisId}</code></p>
        {options.settingsSource.fields.length > 0 ? <dl className="survey-frequencies" aria-label="历史字段与当前字段">
          {options.settingsSource.fields.map(({ from, to }) => <div key={from}>
            <dt><bdi>{from}</bdi></dt><dd><bdi>{to}</bdi></dd>
          </div>)}
        </dl> : <p className="survey-note">来源设置没有引用字段。</p>}
      </div>
    </details>}
    {newerMaterial && <p className="survey-note">资料列表中有较新的同名文件。当前报告仍基于此摘要对应的旧版资料。</p>}
    <dl className="survey-counts">
      <div><dt>总答卷</dt><dd>{report.total}</dd></div>
      <div><dt>有效答卷</dt><dd>{report.valid}</dd></div>
      <div><dt>无效</dt><dd>{report.invalid}</dd></div>
      <div><dt>待复核</dt><dd>{report.pending}</dd></div>
    </dl>
    {settingsMapping}
    <details className="survey-details">
      <summary>清洗规则（{options.requiredColumns?.length ?? 0} 列必答，{options.duplicateColumns?.length ?? 0} 列查重）</summary>
      <div className="survey-section">
        <p className="survey-note">必答列缺失超过 20% 判为无效。查重按所选列组合比较，保留第一条。应用规则会生成新报告，需要重新复核。</p>
        <div className="survey-rules" role="region" aria-label="选择用于必答检查和查重的列" tabIndex={0}>
          <table>
            <thead><tr><th scope="col">列名</th><th scope="col">必答</th><th scope="col">查重</th></tr></thead>
            <tbody>{report.columns.map((column) => <tr key={column}>
              <th scope="row" dir="auto">{column}</th>
              {ruleNames.map((key) => <td key={key}>
                <input type="checkbox" aria-label={`${key === 'requiredColumns' ? '必答列' : '查重列'}：${column}`}
                  checked={rules[key].includes(column)} disabled={busy} onChange={() => toggleColumn(key, column)} />
              </td>)}
            </tr>)}</tbody>
          </table>
        </div>
        <button className="button outline" type="button" disabled={busy || !rulesChanged}
          onClick={() => onAnalyze(ruleOptions)}>应用清洗规则</button>
      </div>
    </details>
    <ReportWeighting summaries={report.summaries} valid={report.valid} result={report.weighting} busy={busy}
      error={calculationError?.kind === 'weighting' ? calculationError.message : undefined}
      onApply={(weightColumn) => onAnalyze({ ...options, weightColumn }, 'weighting')} />
    <ReportCoding summaries={report.summaries} mappings={options.categoryMappings} busy={busy}
      error={calculationError?.kind === 'coding' ? calculationError.message : undefined}
      onApply={(rules) => onAnalyze({ ...options, categoryMappings: rules.length ? rules : undefined }, 'coding')} />
    <ReportSummary summaries={report.summaries} valid={report.valid} />
    <ReportCrossTab summaries={report.summaries} result={report.crossTab} busy={busy}
      error={calculationError?.kind === 'cross-tab' ? calculationError.message : undefined}
      onCalculate={(crossTab) => onAnalyze({ ...options, crossTab }, 'cross-tab')} />
    <ReportReliability summaries={report.summaries} result={report.reliability} busy={busy}
      error={calculationError?.kind === 'reliability' ? calculationError.message : undefined}
      onCalculate={(reliability) => onAnalyze({ ...options, reliability }, 'reliability')} />
    <ReportRegression summaries={report.summaries} result={report.regression} busy={busy}
      error={calculationError?.kind === 'regression' ? calculationError.message : undefined}
      onCalculate={(regression) => onAnalyze({ ...options, regression }, 'regression')} />
    <ReportAdvancedStats summaries={report.summaries} report={report} activeTab={activeAdvancedTab} busy={busy}
      error={calculationError?.kind === 'advanced' ? calculationError.message : undefined}
      onTabChange={onAdvancedTabChange}
      onCalculate={(patch, tab) => {
        onAdvancedTabChange(tab)
        onAnalyze({ ...options, ...patch }, 'advanced')
      }} />
    <ReportRows rows={report.rows} busy={busy} rulesChanged={rulesChanged} onReview={(reviews) => {
      onAnalyze({ ...options, reviews: [...(options.reviews ?? []), ...reviews].sort((a, b) => a.row - b.row) })
    }} />
  </article>
}

function SurveyStartControls({ projectId, materialId, workbook, busy, refreshingMaterials, onAnalyze, onRefreshMaterials }: {
  projectId: string
  materialId: string
  workbook: boolean
  busy: boolean
  refreshingMaterials: boolean
  onAnalyze: (options: SurveyOptions) => void
  onRefreshMaterials: () => void
}) {
  const id = useId()
  const [sheetList, setSheetList] = useState<{ sheets: string[] } | { error: string }>()
  const [sheet, setSheet] = useState('')
  const [reload, setReload] = useState(0)
  const sheets = sheetList && 'sheets' in sheetList ? sheetList.sheets : []
  const canStart = !busy && !!materialId && (!workbook || sheets.includes(sheet))

  useEffect(() => {
    if (!workbook) return
    const controller = new AbortController()
    void listSurveySheets(projectId, materialId, controller.signal).then((result) => {
      if (!controller.signal.aborted) setSheetList(result)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setSheetList({ error: errorMessage(cause) })
    })
    return () => controller.abort()
  }, [projectId, materialId, workbook, reload])

  return <>
    {workbook && <>
      <div className="field">
        <label htmlFor={id}>工作表</label>
        <select id={id} value={sheet} disabled={busy || !sheets.length} onChange={(event) => setSheet(event.currentTarget.value)}>
          <option value="">请选择工作表</option>
          {sheets.map((name) => <option key={name} value={name}>{name}</option>)}
        </select>
      </div>
      {!sheetList && <p className="survey-note" role="status">正在加载工作表…</p>}
      {sheetList && 'error' in sheetList && <div className="survey-actions">
        <p className="form-error" role="alert">{sheetList.error}</p>
        <button className="button outline" type="button" disabled={busy} onClick={() => {
          setSheetList(undefined)
          setReload((value) => value + 1)
        }}>重试加载工作表</button>
      </div>}
    </>}
    <div className="survey-actions">
      <button className="button primary" type="button" disabled={!canStart}
        onClick={() => onAnalyze(workbook ? { sheet } : {})}>开始分析</button>
      <button className="button outline" type="button" disabled={refreshingMaterials} onClick={onRefreshMaterials}>刷新文件列表</button>
    </div>
  </>
}

const materialIdOf = (item: { materialId: string }) => item.materialId
const analysisIdOf = (item: { analysisId: string }) => item.analysisId
const isWorkbook = (name: string) => name.toLowerCase().endsWith('.xlsx')

export function SurveyPanel({ projectId }: { projectId: string }) {
  return <ProjectSurveys key={projectId} projectId={projectId} />
}

function ProjectSurveys({ projectId }: { projectId: string }) {
  const id = useId()
  const loadMaterials = useCallback((signal: AbortSignal, cursor?: string) => listMaterials(projectId, signal, cursor), [projectId])
  const loadReports = useCallback((signal: AbortSignal, cursor?: string) => listSurveys(projectId, signal, cursor), [projectId])
  const materials = useSurveyList(loadMaterials, materialIdOf)
  const reports = useSurveyList(loadReports, analysisIdOf)
  const surveyFiles = materials.items.filter(({ name }) => name.toLowerCase().endsWith('.csv') || isWorkbook(name))
  const [materialId, setMaterialId] = useState('')
  const [run, setRun] = useState<SurveyRun>()
  const [busy, setBusy] = useState<SurveyOperation>()
  const [failure, setFailure] = useState<{ kind: SurveyOperation; message: string }>()
  const [activeAdvancedTab, setActiveAdvancedTab] = useState<AdvancedStatMethod>('multiple-regression')
  const active = useRef<AbortController | null>(null)
  const attempt = useRef<{ key: string; requestId: string } | null>(null)
  const currentMaterial = surveyFiles.find((file) => file.materialId === materialId)
  const currentMaterialId = currentMaterial?.materialId ?? ''
  const originalIndex = run ? surveyFiles.findIndex((file) => file.materialId === run.materialId) : -1
  // Material pages are ordered by upload. The report timestamp is the fallback
  // when the source upload is beyond the pages loaded so far.
  const newerMaterial = !!run && surveyFiles.some((file, index) => file.name === run.materialName
    && file.sha256 !== run.materialSha256
    && (originalIndex >= 0 ? index < originalIndex : file.createdAt > run.createdAt))
  useEffect(() => () => active.current?.abort(), [])

  async function open(kind: SurveyOperation, request: (signal: AbortSignal) => Promise<SurveyRun>) {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    setBusy(kind)
    setFailure(undefined)
    try {
      const next = await request(controller.signal)
      if (controller.signal.aborted) return
      setRun(next)
      if (kind !== 'read') {
        attempt.current = null
        reports.refresh()
      }
    } catch (cause) {
      if (!controller.signal.aborted) setFailure({ kind, message: errorMessage(cause) })
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  function submit(kind: SurveyCalculation | 'map-settings', key: string, request: (requestId: string, signal: AbortSignal) => Promise<SurveyRun>) {
    if (active.current) return
    if (attempt.current?.key !== key) attempt.current = { key, requestId: crypto.randomUUID() }
    const { requestId } = attempt.current
    void open(kind, (signal) => request(requestId, signal))
  }

  function analyze(materialId: string, options: SurveyOptions, kind: SurveyCalculation = 'analyze') {
    submit(kind, JSON.stringify({ materialId, options }),
      (requestId, signal) => analyzeSurvey(projectId, { requestId, materialId, options }, signal))
  }

  function applySettings(sourceAnalysisId: string, targetAnalysisId: string, fields: SurveyFieldPair[]) {
    submit('map-settings', JSON.stringify({ sourceAnalysisId, targetAnalysisId, fields }),
      (requestId, signal) => mapSurveySettings(projectId, sourceAnalysisId, { requestId, targetAnalysisId, fields }, signal))
  }

  return <section className="survey-panel" aria-labelledby={id}>
    <h4 id={id}>问卷分析</h4>
    <div className="field">
      <label htmlFor={`${id}-csv`}>
        {surveyFiles.some(({ name }) => isWorkbook(name)) ? '已上传的问卷 CSV 或 XLSX' : '已上传的问卷 CSV'}
      </label>
      <select id={`${id}-csv`} value={currentMaterialId} disabled={!!busy || !surveyFiles.length}
        onChange={(event) => setMaterialId(event.currentTarget.value)}>
        <option value="">请选择文件</option>
        {surveyFiles.map(({ materialId, name, createdAt }) => (
          <option key={materialId} value={materialId}>{name} · {reportDate.format(new Date(createdAt))}</option>
        ))}
      </select>
    </div>
    <SurveyStartControls key={currentMaterialId} projectId={projectId} materialId={currentMaterialId}
      workbook={!!currentMaterial && isWorkbook(currentMaterial.name)} busy={!!busy} refreshingMaterials={materials.loading}
      onAnalyze={(options) => analyze(currentMaterialId, options)} onRefreshMaterials={materials.refresh} />
    <ListControls list={materials} label="文件" />
    {!materials.loading && !materials.error && !surveyFiles.length && <p className="survey-note">
      此页未找到 CSV 或 XLSX。上传后可刷新文件列表，有更多文件时可继续加载。
    </p>}
    <details className="survey-details">
      <summary>已保存的报告</summary>
      <div className="survey-section">
        {reports.items.length > 0 && <ul className="survey-history" role="list">
          {reports.items.map((report) => <li key={report.analysisId}>
            <button type="button" className="survey-report-link" aria-current={run?.analysisId === report.analysisId ? 'true' : undefined}
              disabled={!!busy} onClick={() => void open('read', (signal) => readSurvey(projectId, report.analysisId, signal))}>
              <strong dir="auto">{report.materialName}</strong>
              <span className="survey-note">
                <time dateTime={report.createdAt}>{reportDate.format(new Date(report.createdAt))}</time>
                {' · '}{report.valid}/{report.total} 份有效{report.pending > 0 ? ` · ${report.pending} 份待复核` : ''}
                {report.invalid > 0 ? ` · ${report.invalid} 份无效` : ''}{report.demo ? ' · 演示数据' : ''}
              </span>
            </button>
          </li>)}
        </ul>}
        <ListControls list={reports} label="报告" />
        {!reports.loading && !reports.error && !reports.items.length && <p className="survey-note">暂无保存的报告。</p>}
      </div>
    </details>
    {busy && <p className="survey-note" role="status">{surveyOperationLabels[busy]}</p>}
    {failure && (failure.kind === 'analyze' || failure.kind === 'read') && <p className="form-error" role="alert">{failure.message}</p>}
    {run && <SurveyAnalysis key={run.analysisId} run={run} projectId={projectId} busy={!!busy} newerMaterial={newerMaterial}
      settingsMapping={<SurveyFieldMapping projectId={projectId} target={run} reports={reports.items}
        historyControls={<ListControls list={reports} label="报告" />} loadingHistory={reports.loading} busy={!!busy}
        error={failure?.kind === 'map-settings' ? failure.message : undefined}
        onEdit={() => setFailure((current) => current?.kind === 'map-settings' ? undefined : current)}
        onApply={(sourceId, fields) => applySettings(sourceId, run.analysisId, fields)} />}
      calculationError={failure} activeAdvancedTab={activeAdvancedTab} onAdvancedTabChange={setActiveAdvancedTab}
      onAnalyze={(options, kind) => analyze(run.materialId, options, kind)} />}
  </section>
}
