import { useId, useState } from 'react'
import {
  cfaConstructLimit, cfaItemLimit, conjointAttributeLimit, factorAnalysisColumnLimit,
  multipleRegressionPredictorLimit,
} from './surveys'
import type {
  SurveyCFA, SurveyCFAConstructOptions, SurveyCFAOptions,
  SurveyConjoint, SurveyConjointOptions,
  SurveyFactorAnalysis, SurveyFactorAnalysisOptions,
  SurveyKano, SurveyKanoOptions,
  SurveyMultipleRegression, SurveyMultipleRegressionOptions,
  SurveyOptions, SurveyPSM, SurveyPSMOptions, SurveyReport, SurveySummary,
} from './surveys'

export type AdvancedStatMethod =
  | 'multiple-regression'
  | 'factor-analysis'
  | 'kano'
  | 'psm'
  | 'conjoint'
  | 'cfa'

const numberFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 6 })
const scientificFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 4, notation: 'scientific' })
const percentFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 })

function fmtNum(value: number) {
  const abs = Math.abs(value)
  if (abs >= 1e6 || (abs > 0 && abs < 1e-4)) return scientificFormat.format(value)
  return numberFormat.format(value)
}

function fmtPct(ratio: number) {
  return `${percentFormat.format(ratio * 100)}%`
}

const unavailableLabels: Record<string, string> = {
  INSUFFICIENT_ROWS: '有效且完整的答卷数量不足，无法计算。',
  COLLINEAR_PREDICTORS: '所选自变量存在完全多重共线性或常量列，无法拟合。',
  CONSTANT_COLUMN: '所选字段中存在无变异的常量列，无法计算相关矩阵。',
  SINGULAR_MATRIX: '相关矩阵不可逆（存在完全共线性或奇异），无法计算 KMO 与 Bartlett 检验。',
  INVALID_LEVEL_COUNT: '每个分类属性须包含 2 至 8 个水平，且水平名称不超过 128 个 UTF-8 字节。',
  COLLINEAR_DESIGN: '属性水平组合存在完全共线性，无法估计各水平效用。',
  CONSTANT_CONSTRUCT: '潜变量得分为常量，无法计算区分效度相关矩阵。',
  OUT_OF_RANGE: '计算结果超出可表示的数值范围。',
}

function unavailableText(reason?: string) {
  if (!reason) return '当前数据无法完成计算。'
  return unavailableLabels[reason] ?? `无法完成计算（${reason}）。`
}

function MultipleRegressionView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyMultipleRegression
  busy: boolean
  onCalculate: (opts: SurveyMultipleRegressionOptions) => void
}) {
  const id = useId()
  const [outcome, setOutcome] = useState(result?.outcomeColumn ?? '')
  const [predictors, setPredictors] = useState<string[]>(() => result?.predictorColumns ?? [])
  const atLimit = predictors.length >= multipleRegressionPredictorLimit
  const validPredictors = predictors.filter((c) => c !== outcome && columns.includes(c))
  const canRun = !busy && columns.includes(outcome) && validPredictors.length >= 2

  return <div className="survey-section" data-survey-adv-panel="multiple-regression">
    {result && <div className="survey-section" role="status" data-survey-adv-result="multiple-regression">
      <p className="survey-note">
        因变量 <bdi>{result.outcomeColumn}</bdi>，自变量 <bdi>{result.predictorColumns.join('、')}</bdi>。
        纳入 {result.included} 份，缺失 {result.missing} 份，非数值 {result.nonNumeric} 份。
      </p>
      {result.fit ? <>
        <dl className="survey-counts" aria-label="多元线性回归汇总">
          {result.fit.rSquared !== undefined && <div><dt>R²</dt><dd>{fmtNum(result.fit.rSquared)}</dd></div>}
          {result.fit.adjustedRSquared !== undefined && <div><dt>调整 R²</dt><dd>{fmtNum(result.fit.adjustedRSquared)}</dd></div>}
          {result.fit.fStat !== undefined && <div><dt>F 值</dt><dd>{fmtNum(result.fit.fStat)}</dd></div>}
          <div><dt>自由度</dt><dd>{result.fit.dfModel} / {result.fit.dfResidual}</dd></div>
        </dl>
        <div className="survey-cross-tab-table" role="region" aria-label="多元线性回归系数表" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">变量</th><th scope="col">系数</th><th scope="col">标准误</th><th scope="col">t 值</th>
            </tr></thead>
            <tbody>
              {[result.fit.intercept, ...result.fit.predictors].map((item) => <tr key={item.name}>
                <th scope="row" dir="auto">{item.name}</th>
                <td>{fmtNum(item.coefficient)}</td>
                <td>{fmtNum(item.stdError)}</td>
                <td>{item.tStat === undefined ? '—' : fmtNum(item.tStat)}</td>
              </tr>)}
            </tbody>
          </table>
        </div>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 3 ? <>
      <div className="field">
        <label htmlFor={`${id}-outcome`}>因变量</label>
        <select id={`${id}-outcome`} data-survey-mr-outcome value={outcome} disabled={busy}
          onChange={(e) => {
            const next = e.currentTarget.value
            setOutcome(next)
            setPredictors((cur) => cur.filter((c) => c !== next))
          }}>
          <option value="">请选择因变量</option>
          {columns.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </div>
      <fieldset className="survey-scale-items" disabled={busy}>
        <legend>自变量（已选 {validPredictors.length} 个，需 2 至 {multipleRegressionPredictorLimit} 个）</legend>
        {columns.map((c) => <label key={c}>
          <input type="checkbox" data-survey-mr-predictor={c} checked={validPredictors.includes(c)}
            disabled={c === outcome || (atLimit && !validPredictors.includes(c))}
            onChange={(e) => {
              const checked = e.currentTarget.checked
              setPredictors((cur) => checked ? [...cur, c] : cur.filter((x) => x !== c))
            }} />
          <bdi>{c}</bdi>
        </label>)}
      </fieldset>
      <button className="button outline" type="button" data-survey-mr-submit disabled={!canRun}
        onClick={() => onCalculate({ outcomeColumn: outcome, predictorColumns: validPredictors })}>计算多元回归</button>
    </> : <p className="survey-note">至少需要 3 个可统计字段。</p>}
  </div>
}

function FactorAnalysisView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyFactorAnalysis
  busy: boolean
  onCalculate: (opts: SurveyFactorAnalysisOptions) => void
}) {
  const id = useId()
  const [selected, setSelected] = useState<string[]>(() => result?.columns ?? [])
  const [factorsInput, setFactorsInput] = useState(() => (result?.factors ? String(result.factors) : ''))
  const atLimit = selected.length >= factorAnalysisColumnLimit
  const parsedFactors = factorsInput.trim() === '' ? 0 : Number(factorsInput.trim())
  const validFactors = Number.isInteger(parsedFactors) && parsedFactors >= 0 && parsedFactors <= selected.length
  const canRun = !busy && selected.length >= 3 && validFactors

  return <div className="survey-section" data-survey-adv-panel="factor-analysis">
    {result && <div className="survey-section" role="status" data-survey-adv-result="factor-analysis">
      <p className="survey-note">
        题项 <bdi>{result.columns.join('、')}</bdi>。纳入 {result.included} 份，缺失 {result.missing} 份，非数值 {result.nonNumeric} 份。
      </p>
      {result.result ? <>
        <dl className="survey-counts" aria-label="效度与因子分析核心指标">
          <div><dt>KMO</dt><dd>{fmtNum(result.result.kmo)}</dd></div>
          <div><dt>Bartlett χ²</dt><dd>{fmtNum(result.result.bartlettChiSquare)}</dd></div>
          <div><dt>自由度</dt><dd>{result.result.bartlettDf}</dd></div>
          <div><dt>近似 p 值</dt><dd>{fmtNum(result.result.bartlettPValue)}</dd></div>
          <div><dt>提取因子数</dt><dd>{result.result.factorCount}{result.result.autoFactors ? '（特征值>1）' : ''}</dd></div>
        </dl>
        <div className="survey-cross-tab-table" role="region" aria-label="因子特征值与方差解释率" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">因子</th><th scope="col">初始特征值</th><th scope="col">方差解释率</th><th scope="col">累计解释率</th><th scope="col">旋转后平方和</th><th scope="col">旋转后解释率</th>
            </tr></thead>
            <tbody>{result.result.factors.map((f) => <tr key={f.factor}>
              <th scope="row">因子 {f.factor}</th>
              <td>{fmtNum(f.eigenvalue)}</td>
              <td>{percentFormat.format(f.variancePercent)}%</td>
              <td>{percentFormat.format(f.cumulativeVariancePercent)}%</td>
              <td>{fmtNum(f.rotatedSSLoadings)}</td>
              <td>{percentFormat.format(f.rotatedVariancePercent)}%</td>
            </tr>)}</tbody>
          </table>
        </div>
        <div className="survey-cross-tab-table" role="region" aria-label="Varimax 旋转后因子载荷与共同度" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">题项</th>
              {result.result.factors.map((f) => <th key={f.factor} scope="col">因子 {f.factor}</th>)}
              <th scope="col">共同度</th>
              <th scope="col">MSA</th>
            </tr></thead>
            <tbody>{result.result.variables.map((v) => <tr key={v.column}>
              <th scope="row" dir="auto">{v.column}</th>
              {v.loadings.map((l, idx) => <td key={idx}>{fmtNum(l)}</td>)}
              <td>{fmtNum(v.communality)}</td>
              <td>{fmtNum(v.msa)}</td>
            </tr>)}</tbody>
          </table>
        </div>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 3 ? <>
      <fieldset className="survey-scale-items" disabled={busy}>
        <legend>分析题项（已选 {selected.length} 项，需 3 至 {factorAnalysisColumnLimit} 项）</legend>
        {columns.map((c) => <label key={c}>
          <input type="checkbox" data-survey-fa-column={c} checked={selected.includes(c)}
            disabled={atLimit && !selected.includes(c)}
            onChange={(e) => {
              const checked = e.currentTarget.checked
              setSelected((cur) => checked ? [...cur, c] : cur.filter((x) => x !== c))
            }} />
          <bdi>{c}</bdi>
        </label>)}
      </fieldset>
      <div className="field">
        <label htmlFor={`${id}-factors`}>提取因子数（留空则按特征值大于 1 自动确定）</label>
        <input id={`${id}-factors`} data-survey-fa-factors type="number" min={1} max={Math.max(1, selected.length)} step={1}
          disabled={busy} value={factorsInput} onChange={(e) => setFactorsInput(e.currentTarget.value)} />
      </div>
      <button className="button outline" type="button" data-survey-fa-submit disabled={!canRun}
        onClick={() => onCalculate(parsedFactors > 0 ? { columns: selected, factors: parsedFactors } : { columns: selected })}>
        计算效度与因子分析
      </button>
    </> : <p className="survey-note">至少需要 3 个可统计字段。</p>}
  </div>
}

function KanoView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyKano
  busy: boolean
  onCalculate: (opts: SurveyKanoOptions) => void
}) {
  const id = useId()
  const [functionalColumn, setFunctionalColumn] = useState(result?.functionalColumn ?? '')
  const [dysfunctionalColumn, setDysfunctionalColumn] = useState(result?.dysfunctionalColumn ?? '')
  const canRun = !busy && columns.includes(functionalColumn) && columns.includes(dysfunctionalColumn) && functionalColumn !== dysfunctionalColumn

  return <div className="survey-section" data-survey-adv-panel="kano">
    {result && <div className="survey-section" role="status" data-survey-adv-result="kano">
      <p className="survey-note">
        正向题 <bdi>{result.functionalColumn}</bdi>，负向题 <bdi>{result.dysfunctionalColumn}</bdi>。
        纳入 {result.included} 份，缺失 {result.missing} 份，无法识别 {result.invalid} 份。
      </p>
      {result.result ? <>
        <dl className="survey-counts" aria-label="Kano 模型核心结果">
          <div><dt>主导类别</dt><dd>{result.result.dominantCode}（{result.result.dominantLabel}）</dd></div>
          {result.result.better !== undefined && <div><dt>Better 系数</dt><dd>{fmtNum(result.result.better)}</dd></div>}
          {result.result.worse !== undefined && <div><dt>Worse 系数</dt><dd>{fmtNum(result.result.worse)}</dd></div>}
        </dl>
        <div className="survey-cross-tab-table" role="region" aria-label="Kano 归类分布表" tabIndex={0}>
          <table>
            <thead><tr><th scope="col">类别</th><th scope="col">说明</th><th scope="col">份数</th><th scope="col">占比</th></tr></thead>
            <tbody>{result.result.categories.map((cat) => <tr key={cat.code}>
              <th scope="row">{cat.code}</th>
              <td>{cat.label}</td>
              <td>{cat.count}</td>
              <td>{fmtPct(cat.share)}</td>
            </tr>)}</tbody>
          </table>
        </div>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 2 ? <>
      <div className="survey-cross-tab-fields">
        <div className="field">
          <label htmlFor={`${id}-func`}>正向题（具备该功能）</label>
          <select id={`${id}-func`} data-survey-kano-functional value={functionalColumn} disabled={busy}
            onChange={(e) => setFunctionalColumn(e.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((c) => <option key={c} value={c} disabled={c === dysfunctionalColumn}>{c}</option>)}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-dys`}>负向题（不具备该功能）</label>
          <select id={`${id}-dys`} data-survey-kano-dysfunctional value={dysfunctionalColumn} disabled={busy}
            onChange={(e) => setDysfunctionalColumn(e.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((c) => <option key={c} value={c} disabled={c === functionalColumn}>{c}</option>)}
          </select>
        </div>
      </div>
      <p className="survey-note">支持 1–5 编码或标准 5 级选项文本（很喜欢、理所当然、无所谓、可以忍受、不喜欢）。</p>
      <button className="button outline" type="button" data-survey-kano-submit disabled={!canRun}
        onClick={() => onCalculate({ functionalColumn, dysfunctionalColumn })}>计算 Kano 模型</button>
    </> : <p className="survey-note">至少需要 2 个可统计字段。</p>}
  </div>
}

function PSMView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyPSM
  busy: boolean
  onCalculate: (opts: SurveyPSMOptions) => void
}) {
  const id = useId()
  const [tc, setTC] = useState(result?.tooCheapColumn ?? '')
  const [c, setC] = useState(result?.cheapColumn ?? '')
  const [e, setE] = useState(result?.expensiveColumn ?? '')
  const [te, setTE] = useState(result?.tooExpensiveColumn ?? '')
  const chosen = [tc, c, e, te]
  const uniqueChosen = new Set(chosen.filter(Boolean))
  const canRun = !busy && uniqueChosen.size === 4 && chosen.every((col) => columns.includes(col))

  return <div className="survey-section" data-survey-adv-panel="psm">
    {result && <div className="survey-section" role="status" data-survey-adv-result="psm">
      <p className="survey-note">
        纳入 {result.included} 份单调有效答卷，缺失 {result.missing} 份，非数值或负值 {result.nonNumeric} 份，价格顺序矛盾 {result.inconsistent} 份。
      </p>
      {result.result ? <>
        <dl className="survey-counts" aria-label="PSM 关键价格点">
          <div><dt>最低可接受价（PMC）</dt><dd>{fmtNum(result.result.pmc)}</dd></div>
          <div><dt>最优价格点（OPP）</dt><dd>{fmtNum(result.result.opp)}</dd></div>
          <div><dt>无差异价格点（IDP）</dt><dd>{fmtNum(result.result.idp)}</dd></div>
          <div><dt>最高可接受价（PME）</dt><dd>{fmtNum(result.result.pme)}</dd></div>
        </dl>
        <p className="survey-note">
          可接受价格区间：[{fmtNum(result.result.acceptableRange[0])}, {fmtNum(result.result.acceptableRange[1])}]。
        </p>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 4 ? <>
      <div className="survey-cross-tab-fields">
        {([
          ['tc', '太便宜（质疑质量）', tc, setTC],
          ['c', '便宜（划算）', c, setC],
          ['e', '偏贵（开始犹豫）', e, setE],
          ['te', '太贵（绝不购买）', te, setTE],
        ] as const).map(([key, label, value, setter]) => <div className="field" key={key}>
          <label htmlFor={`${id}-${key}`}>{label}</label>
          <select id={`${id}-${key}`} data-survey-psm={key} value={value} disabled={busy}
            onChange={(ev) => setter(ev.currentTarget.value)}>
            <option value="">请选择价格字段</option>
            {columns.map((col) => <option key={col} value={col} disabled={uniqueChosen.has(col) && value !== col}>{col}</option>)}
          </select>
        </div>)}
      </div>
      <button className="button outline" type="button" data-survey-psm-submit disabled={!canRun}
        onClick={() => onCalculate({ tooCheapColumn: tc, cheapColumn: c, expensiveColumn: e, tooExpensiveColumn: te })}>
        计算 PSM 价格敏感度
      </button>
    </> : <p className="survey-note">至少需要 4 个可统计字段。</p>}
  </div>
}

function ConjointView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyConjoint
  busy: boolean
  onCalculate: (opts: SurveyConjointOptions) => void
}) {
  const id = useId()
  const [ratingColumn, setRatingColumn] = useState(result?.ratingColumn ?? '')
  const [attrs, setAttrs] = useState<string[]>(() => result?.attributeColumns ?? [])
  const validAttrs = attrs.filter((c) => c !== ratingColumn && columns.includes(c))
  const atLimit = validAttrs.length >= conjointAttributeLimit
  const canRun = !busy && columns.includes(ratingColumn) && validAttrs.length >= 1

  return <div className="survey-section" data-survey-adv-panel="conjoint">
    {result && <div className="survey-section" role="status" data-survey-adv-result="conjoint">
      <p className="survey-note">
        评分列 <bdi>{result.ratingColumn}</bdi>，属性列 <bdi>{result.attributeColumns.join('、')}</bdi>。
        纳入 {result.included} 份，缺失 {result.missing} 份，非数值 {result.nonNumeric} 份。
      </p>
      {result.result ? <>
        <dl className="survey-counts" aria-label="联合分析拟合汇总">
          <div><dt>基准截距</dt><dd>{fmtNum(result.result.intercept)}</dd></div>
          {result.result.rSquared !== undefined && <div><dt>模型 R²</dt><dd>{fmtNum(result.result.rSquared)}</dd></div>}
        </dl>
        <div className="survey-cross-tab-table" role="region" aria-label="联合分析属性相对重要性与部分价值效用" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">属性</th><th scope="col">相对重要性</th><th scope="col">效用极差</th><th scope="col">水平与部分价值效用</th>
            </tr></thead>
            <tbody>{result.result.attributes.map((attr) => <tr key={attr.column}>
              <th scope="row" dir="auto">{attr.column}</th>
              <td>{percentFormat.format(attr.importancePercent)}%</td>
              <td>{fmtNum(attr.range)}</td>
              <td dir="auto">{attr.levels.map((lv) => `${lv.level}: ${fmtNum(lv.partWorth)} (${lv.count})`).join('；')}</td>
            </tr>)}</tbody>
          </table>
        </div>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 2 ? <>
      <div className="field">
        <label htmlFor={`${id}-rating`}>偏好 / 评分列</label>
        <select id={`${id}-rating`} data-survey-cj-rating value={ratingColumn} disabled={busy}
          onChange={(e) => {
            const next = e.currentTarget.value
            setRatingColumn(next)
            setAttrs((cur) => cur.filter((c) => c !== next))
          }}>
          <option value="">请选择评分字段</option>
          {columns.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </div>
      <fieldset className="survey-scale-items" disabled={busy}>
        <legend>分类属性列（已选 {validAttrs.length} 个，需 1 至 {conjointAttributeLimit} 个）</legend>
        {columns.map((c) => <label key={c}>
          <input type="checkbox" data-survey-cj-attr={c} checked={validAttrs.includes(c)}
            disabled={c === ratingColumn || (atLimit && !validAttrs.includes(c))}
            onChange={(e) => {
              const checked = e.currentTarget.checked
              setAttrs((cur) => checked ? [...cur, c] : cur.filter((x) => x !== c))
            }} />
          <bdi>{c}</bdi>
        </label>)}
      </fieldset>
      <button className="button outline" type="button" data-survey-cj-submit disabled={!canRun}
        onClick={() => onCalculate({ ratingColumn, attributeColumns: validAttrs })}>计算联合分析</button>
    </> : <p className="survey-note">至少需要 2 个可统计字段。</p>}
  </div>
}

function CFAView({ columns, result, busy, onCalculate }: {
  columns: string[]
  result?: SurveyCFA
  busy: boolean
  onCalculate: (opts: SurveyCFAOptions) => void
}) {
  const id = useId()
  const [constructs, setConstructs] = useState<SurveyCFAConstructOptions[]>(() =>
    result?.constructs?.length ? result.constructs : [
      { name: '潜变量 1', columns: [] },
      { name: '潜变量 2', columns: [] },
    ],
  )
  const usedCols = new Map<string, number>()
  constructs.forEach((c, idx) => {
    for (const col of c.columns) usedCols.set(col, idx)
  })
  const names = constructs.map((c) => c.name.trim())
  const uniqueNames = new Set(names.map((n) => n.toLowerCase()))
  const canRun = !busy && constructs.length >= 2 && constructs.length <= cfaConstructLimit
    && names.every((n) => n.length > 0) && uniqueNames.size === constructs.length
    && constructs.every((c) => c.columns.length >= 2 && c.columns.length <= cfaItemLimit)

  return <div className="survey-section" data-survey-adv-panel="cfa">
    {result && <div className="survey-section" role="status" data-survey-adv-result="cfa">
      <p className="survey-note">
        纳入 {result.included} 份，缺失 {result.missing} 份，非数值 {result.nonNumeric} 份。
      </p>
      {result.result ? <>
        <p className="survey-note">
          Fornell-Larcker 区分效度总体结论：
          <strong>{result.result.discriminantValid ? '通过（各潜变量 √AVE 均大于其与其他潜变量相关系数绝对值）' : '未全部通过（存在 √AVE 不大于潜变量间相关系数的情况）'}</strong>
        </p>
        <div className="survey-cross-tab-table" role="region" aria-label="CFA 收敛与区分效度汇总" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">潜变量</th><th scope="col">CR</th><th scope="col">AVE</th><th scope="col">√AVE</th><th scope="col">最大因子间 |r|</th><th scope="col">收敛效度</th><th scope="col">区分效度</th><th scope="col">观测项标准化载荷</th>
            </tr></thead>
            <tbody>{result.result.constructs.map((c) => <tr key={c.name}>
              <th scope="row" dir="auto">{c.name}</th>
              <td>{fmtNum(c.cr)}</td>
              <td>{fmtNum(c.ave)}</td>
              <td>{fmtNum(c.sqrtAve)}</td>
              <td>{fmtNum(c.maxInterConstructAbsCorr)}</td>
              <td>{c.convergentValid ? '达标' : '偏低'}</td>
              <td>{c.discriminantValid ? '通过' : '未通过'}</td>
              <td dir="auto">{c.items.map((it) => `${it.column}: ${fmtNum(it.loading)}`).join('；')}</td>
            </tr>)}</tbody>
          </table>
        </div>
        <div className="survey-cross-tab-table" role="region" aria-label="潜变量相关矩阵与 √AVE" tabIndex={0}>
          <table>
            <thead><tr>
              <th scope="col">潜变量</th>
              {result.result.constructs.map((c) => <th key={c.name} scope="col" dir="auto">{c.name}</th>)}
            </tr></thead>
            <tbody>{result.result.constructs.map((rowC, rIdx) => <tr key={rowC.name}>
              <th scope="row" dir="auto">{rowC.name}</th>
              {result.result!.constructs.map((colC, cIdx) => <td key={colC.name}>
                {rIdx === cIdx ? `√AVE=${fmtNum(rowC.sqrtAve)}` : fmtNum(result.result!.correlationMatrix[rIdx][cIdx])}
              </td>)}
            </tr>)}</tbody>
          </table>
        </div>
      </> : <p className="survey-note">{unavailableText(result.unavailableReason)}</p>}
    </div>}
    {columns.length >= 4 ? <>
      {constructs.map((construct, index) => <div className="survey-adv-construct" key={index}>
        <div className="survey-heading">
          <div className="field">
            <label htmlFor={`${id}-c-${index}`}>潜变量 {index + 1} 名称</label>
            <input id={`${id}-c-${index}`} data-survey-cfa-name={index} type="text" maxLength={128} disabled={busy}
              value={construct.name} onChange={(e) => {
                const val = e.currentTarget.value
                setConstructs((cur) => cur.map((item, i) => i === index ? { ...item, name: val } : item))
              }} />
          </div>
          {constructs.length > 2 && <button type="button" className="button outline" disabled={busy}
            onClick={() => setConstructs((cur) => cur.filter((_, i) => i !== index))}>移除</button>}
        </div>
        <fieldset className="survey-scale-items" disabled={busy}>
          <legend>观测题项（已选 {construct.columns.length} 项，需 2 至 {cfaItemLimit} 项）</legend>
          {columns.map((col) => {
            const owner = usedCols.get(col)
            const checked = construct.columns.includes(col)
            const blocked = (owner !== undefined && owner !== index) || (!checked && construct.columns.length >= cfaItemLimit)
            return <label key={col}>
              <input type="checkbox" data-survey-cfa-col={`${index}:${col}`} checked={checked} disabled={blocked}
                onChange={(e) => {
                  const isChecked = e.currentTarget.checked
                  setConstructs((cur) => cur.map((item, i) => i === index
                    ? { ...item, columns: isChecked ? [...item.columns, col] : item.columns.filter((x) => x !== col) }
                    : item))
                }} />
              <bdi>{col}</bdi>
            </label>
          })}
        </fieldset>
      </div>)}
      <div className="survey-actions">
        {constructs.length < cfaConstructLimit && <button type="button" className="button outline" disabled={busy}
          onClick={() => setConstructs((cur) => [...cur, { name: `潜变量 ${cur.length + 1}`, columns: [] }])}>
          添加潜变量
        </button>}
        <button type="button" className="button outline" data-survey-cfa-submit disabled={!canRun}
          onClick={() => onCalculate({ constructs: constructs.map((c) => ({ name: c.name.trim(), columns: c.columns })) })}>
          计算基础 CFA
        </button>
      </div>
    </> : <p className="survey-note">至少需要 4 个可统计字段（2 个潜变量，各 2 个观测题项）。</p>}
  </div>
}

const tabs: { id: AdvancedStatMethod; label: string; hasResult: (report: SurveyReport) => boolean }[] = [
  { id: 'multiple-regression', label: '多元线性回归', hasResult: (r) => !!r.multipleRegression },
  { id: 'factor-analysis', label: '效度与因子分析', hasResult: (r) => !!r.factorAnalysis },
  { id: 'kano', label: 'Kano 模型', hasResult: (r) => !!r.kano },
  { id: 'psm', label: 'PSM 价格敏感度', hasResult: (r) => !!r.psm },
  { id: 'conjoint', label: '联合分析', hasResult: (r) => !!r.conjoint },
  { id: 'cfa', label: '基础 CFA 效度', hasResult: (r) => !!r.cfa },
]

export function ReportAdvancedStats({
  summaries, report, activeTab, busy, error, onTabChange, onCalculate,
}: {
  summaries: SurveySummary[]
  report: SurveyReport
  activeTab: AdvancedStatMethod
  busy: boolean
  error?: string
  onTabChange: (tab: AdvancedStatMethod) => void
  onCalculate: (patch: Partial<SurveyOptions>, tab: AdvancedStatMethod) => void
}) {
  const columns = summaries.map(({ column }) => column)
  const completedCount = tabs.filter((t) => t.hasResult(report)).length
  const [open, setOpen] = useState(() => completedCount > 0)

  return <details className="survey-details" data-survey-advanced-stats open={open}
    onToggle={(e) => setOpen(e.currentTarget.open)}>
    <summary>高级统计分析{completedCount > 0 ? `（已计算 ${completedCount} 项）` : ''}</summary>
    <div className="survey-section">
      <p className="survey-note">
        所有高级统计仅纳入清洗和人工复核后的有效答卷。点击计算会沿用当前规则与复核决定并保存独立报告，后续复核将自动按相同配置重算。
      </p>
      <div className="survey-adv-tabs" role="tablist" aria-label="高级统计方法">
        {tabs.map((tab) => <button key={tab.id} type="button" role="tab" className="survey-adv-tab"
          data-survey-adv-tab={tab.id} aria-selected={activeTab === tab.id}
          onClick={() => onTabChange(tab.id)}>
          {tab.label}{tab.hasResult(report) ? ' ✓' : ''}
        </button>)}
      </div>
      {activeTab === 'multiple-regression' && <MultipleRegressionView columns={columns} result={report.multipleRegression} busy={busy}
        onCalculate={(multipleRegression) => onCalculate({ multipleRegression }, 'multiple-regression')} />}
      {activeTab === 'factor-analysis' && <FactorAnalysisView columns={columns} result={report.factorAnalysis} busy={busy}
        onCalculate={(factorAnalysis) => onCalculate({ factorAnalysis }, 'factor-analysis')} />}
      {activeTab === 'kano' && <KanoView columns={columns} result={report.kano} busy={busy}
        onCalculate={(kano) => onCalculate({ kano }, 'kano')} />}
      {activeTab === 'psm' && <PSMView columns={columns} result={report.psm} busy={busy}
        onCalculate={(psm) => onCalculate({ psm }, 'psm')} />}
      {activeTab === 'conjoint' && <ConjointView columns={columns} result={report.conjoint} busy={busy}
        onCalculate={(conjoint) => onCalculate({ conjoint }, 'conjoint')} />}
      {activeTab === 'cfa' && <CFAView columns={columns} result={report.cfa} busy={busy}
        onCalculate={(cfa) => onCalculate({ cfa }, 'cfa')} />}
      {error && <p className="form-error" role="alert">{error}</p>}
    </div>
  </details>
}
