import { useId, useState } from 'react'
import type { SurveyRegression, SurveyRegressionOptions, SurveyRegressionUnavailableReason, SurveySummary } from './surveys'

const numberFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 6 })
const scientificFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 6, notation: 'scientific' })
const unavailableMessages: Record<SurveyRegressionUnavailableReason, string> = {
  INSUFFICIENT_ROWS: '完整数值配对不足两份，无法拟合。',
  CONSTANT_PREDICTOR: '完整配对中的自变量没有变异，无法拟合。',
  OUT_OF_RANGE: '拟合结果超出可表示的数值范围。',
}

function formatCoefficient(value: number) {
  const magnitude = Math.abs(value)
  return (magnitude >= 1e6 || (magnitude > 0 && magnitude < 1e-4) ? scientificFormat : numberFormat).format(value)
}

function RegressionResult({ result }: { result: SurveyRegression }) {
  const { fit, included, missing, nonNumeric, predictorColumn, outcomeColumn, weighted } = result

  return <div className="survey-section" role="status">
    <p className="survey-note">报告所用字段 <bdi>{predictorColumn}</bdi>（自变量）、<bdi>{outcomeColumn}</bdi>（因变量）</p>
    {fit ? <>
      <dl className="survey-counts" aria-label="回归拟合结果">
        <div><dt>截距</dt><dd>{formatCoefficient(fit.intercept)}</dd></div>
        <div><dt>斜率</dt><dd>{formatCoefficient(fit.slope)}</dd></div>
        {fit.rSquared !== undefined && <div><dt>R²</dt><dd>{formatCoefficient(fit.rSquared)}</dd></div>}
      </dl>
      {fit.rSquared === undefined && <p className="survey-note">因变量为常量，R² 未定义。</p>}
    </> : <p>{unavailableMessages[result.unavailableReason]}</p>}
    <p className="survey-note">纳入 {included} 份，排除缺失 {missing} 份、非数值 {nonNumeric} 份。</p>
    {weighted && <div className="survey-section" data-survey-regression-weighted>
      <p className="survey-note">
        <strong>加权最小二乘回归（WLS）</strong>：纳入 {weighted.included} 份，权重合计 {formatCoefficient(weighted.weightSum)}，缺失权重 {weighted.missingWeight} 份，无效权重 {weighted.invalidWeight} 份。
      </p>
      {weighted.fit ? <dl className="survey-counts" aria-label="加权回归拟合结果">
        <div><dt>加权截距</dt><dd>{formatCoefficient(weighted.fit.intercept)}</dd></div>
        <div><dt>加权斜率</dt><dd>{formatCoefficient(weighted.fit.slope)}</dd></div>
        {weighted.fit.rSquared !== undefined && <div><dt>加权 R²</dt><dd>{formatCoefficient(weighted.fit.rSquared)}</dd></div>}
      </dl> : <p className="survey-note">{unavailableMessages[weighted.unavailableReason]}</p>}
    </div>}
  </div>
}

export function ReportRegression({ summaries, result, busy, error, onCalculate }: {
  summaries: SurveySummary[]
  result?: SurveyRegression
  busy: boolean
  error?: string
  onCalculate: (options: SurveyRegressionOptions) => void
}) {
  const id = useId()
  const [predictorColumn, setPredictorColumn] = useState(result?.predictorColumn ?? '')
  const [outcomeColumn, setOutcomeColumn] = useState(result?.outcomeColumn ?? '')
  const columns = summaries.map(({ column }) => column)
  const canCalculate = !busy && columns.includes(predictorColumn) && columns.includes(outcomeColumn) && predictorColumn !== outcomeColumn

  return <section className="survey-section" aria-labelledby={`${id}-heading`}>
    <h6 id={`${id}-heading`}>一元线性回归</h6>
    {result && <RegressionResult result={result} />}
    {columns.length >= 2 ? <>
      <div className="survey-cross-tab-fields">
        <div className="field">
          <label htmlFor={`${id}-predictor`}>自变量</label>
          <select id={`${id}-predictor`} value={predictorColumn} disabled={busy}
            onChange={(event) => setPredictorColumn(event.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((column) => <option key={column} value={column} disabled={column === outcomeColumn}>{column}</option>)}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-outcome`}>因变量</label>
          <select id={`${id}-outcome`} value={outcomeColumn} disabled={busy}
            onChange={(event) => setOutcomeColumn(event.currentTarget.value)}>
            <option value="">请选择字段</option>
            {columns.map((column) => <option key={column} value={column} disabled={column === predictorColumn}>{column}</option>)}
          </select>
        </div>
      </div>
      <p className="survey-note" id={`${id}-help`}>
        沿用当前报告的清洗规则和复核决定。至少需要两份有效的完整数值配对，且自变量须有变异。
      </p>
      <button className="button outline" type="button" disabled={!canCalculate}
        aria-describedby={error ? `${id}-help ${id}-error` : `${id}-help`}
        onClick={() => onCalculate({ predictorColumn, outcomeColumn })}>计算回归</button>
    </> : <p className="survey-note">至少需要两个可统计字段。</p>}
    {error && <p className="form-error" id={`${id}-error`} role="alert">{error}</p>}
  </section>
}
