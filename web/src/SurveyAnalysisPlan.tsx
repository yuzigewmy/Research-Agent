import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage } from './projects'
import {
  calculateSampleSizePreview, createSurveyPlan, defaultPlannedStatisticalMethods,
  sampleSizeConfidenceLevels, sampleSizeMethodCochran,
} from './surveys'
import type { SampleSizeConfidenceLevel, SurveyAnalysisPlan, SurveySampleSizeInput } from './surveys'

const planDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })
const samplingSuggestions = ['简单随机抽样', '分层随机抽样', '整群抽样', '系统抽样', '配额抽样', '方便抽样']

interface PlanDraft {
  title: string
  objective: string
  targetPopulation: string
  samplingMethod: string
  plannedMethods: string[]
  missingStrategy: string
  notes: string
  confidenceLevel: SampleSizeConfidenceLevel
  expectedProportion: string
  marginOfError: string
  populationSize: string
  responseRate: string
}

const initialDraft: PlanDraft = {
  title: '',
  objective: '',
  targetPopulation: '',
  samplingMethod: '简单随机抽样',
  plannedMethods: ['描述统计'],
  missingStrategy: '必答列缺失超过 20% 判为无效，其余统计按完整配对纳入',
  notes: '',
  confidenceLevel: 95,
  expectedProportion: '0.5',
  marginOfError: '0.05',
  populationSize: '',
  responseRate: '1',
}

function draftFromPlan(plan: SurveyAnalysisPlan): PlanDraft {
  return {
    title: plan.title,
    objective: plan.objective,
    targetPopulation: plan.targetPopulation,
    samplingMethod: plan.samplingMethod,
    plannedMethods: [...plan.plannedMethods],
    missingStrategy: plan.missingStrategy,
    notes: plan.notes ?? '',
    confidenceLevel: plan.sampleSizeInput.confidenceLevel,
    expectedProportion: String(plan.sampleSizeInput.expectedProportion),
    marginOfError: String(plan.sampleSizeInput.marginOfError),
    populationSize: plan.sampleSizeInput.populationSize ? String(plan.sampleSizeInput.populationSize) : '',
    responseRate: String(plan.sampleSizeInput.responseRate),
  }
}

function parseSampleSizeDraft(draft: PlanDraft): { input: SurveySampleSizeInput } | { error: string } {
  const pText = draft.expectedProportion.trim()
  const eText = draft.marginOfError.trim()
  const rText = draft.responseRate.trim()
  const popText = draft.populationSize.trim()
  if (!pText || !eText || !rText) {
    return { error: '请填写预期比例、允许误差和预期有效回收率。' }
  }
  const expectedProportion = Number(pText)
  const marginOfError = Number(eText)
  const responseRate = Number(rText)
  let populationSize: number | undefined
  if (popText !== '' && popText !== '0') {
    if (!/^\d+$/.test(popText)) {
      return { error: '总体规模须为正整数，留空或填 0 表示无限总体。' }
    }
    populationSize = Number(popText)
  }
  return {
    input: {
      method: sampleSizeMethodCochran,
      confidenceLevel: draft.confidenceLevel,
      expectedProportion,
      marginOfError,
      populationSize,
      responseRate,
    },
  }
}

export function SurveyAnalysisPlanSection({
  projectId,
  plans,
  latestVersion,
  loading,
  loadError,
  selectedPlanId,
  disabled,
  onRefresh,
  onCreated,
  onSelectPlan,
}: {
  projectId: string
  plans: SurveyAnalysisPlan[]
  latestVersion: number
  loading: boolean
  loadError: string
  selectedPlanId: string
  disabled: boolean
  onRefresh: () => void
  onCreated: (plan: SurveyAnalysisPlan) => void
  onSelectPlan: (planId: string) => void
}) {
  const id = useId()
  const [draft, setDraft] = useState<PlanDraft>(initialDraft)
  const [viewingPlanId, setViewingPlanId] = useState<string>('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [statusMessage, setStatusMessage] = useState('')
  const initialized = useRef(false)
  const active = useRef<AbortController | null>(null)
  const attempt = useRef<{ key: string; requestId: string } | null>(null)

  useEffect(() => () => active.current?.abort(), [])

  useEffect(() => {
    if (!initialized.current && plans.length > 0) {
      initialized.current = true
      setDraft(draftFromPlan(plans[0]))
      setViewingPlanId(plans[0].planId)
    }
  }, [plans])

  const parsed = parseSampleSizeDraft(draft)
  const preview = 'error' in parsed ? parsed : calculateSampleSizePreview(parsed.input)
  const viewingPlan = plans.find((item) => item.planId === viewingPlanId)
  const methodOptions = [...new Set([...defaultPlannedStatisticalMethods, ...draft.plannedMethods])]
  const busy = disabled || saving

  function updateDraft(patch: Partial<PlanDraft>) {
    setDraft((current) => ({ ...current, ...patch }))
    setSaveError('')
    setStatusMessage('')
  }

  function toggleMethod(method: string, checked: boolean) {
    setDraft((current) => ({
      ...current,
      plannedMethods: checked
        ? [...current.plannedMethods, method]
        : current.plannedMethods.filter((item) => item !== method),
    }))
    setSaveError('')
    setStatusMessage('')
  }

  function loadHistoryVersion(plan: SurveyAnalysisPlan) {
    setDraft(draftFromPlan(plan))
    setViewingPlanId(plan.planId)
    setSaveError('')
    setStatusMessage(`已回填第 ${plan.version} 版计划，可直接查看或修改后保存为第 ${latestVersion + 1} 版。`)
  }

  async function savePlan() {
    if (active.current || saving) return
    if ('error' in parsed) {
      setSaveError(parsed.error)
      return
    }
    if ('error' in preview) {
      setSaveError(preview.error)
      return
    }
    const payload = {
      expectedVersion: latestVersion,
      title: draft.title.trim(),
      objective: draft.objective.trim(),
      targetPopulation: draft.targetPopulation.trim(),
      samplingMethod: draft.samplingMethod.trim(),
      plannedMethods: draft.plannedMethods,
      missingStrategy: draft.missingStrategy.trim(),
      notes: draft.notes.trim() || undefined,
      sampleSizeInput: parsed.input,
    }
    if (!payload.title || !payload.objective || !payload.targetPopulation || !payload.samplingMethod || !payload.missingStrategy) {
      setSaveError('请完整填写计划标题、研究目标、目标总体、抽样方式和缺失值处理策略。')
      return
    }
    if (payload.plannedMethods.length === 0) {
      setSaveError('请至少选择一项计划统计方法。')
      return
    }

    const key = JSON.stringify(payload)
    if (attempt.current?.key !== key) {
      attempt.current = { key, requestId: crypto.randomUUID() }
    }
    const requestId = attempt.current.requestId
    const controller = new AbortController()
    active.current = controller
    setSaving(true)
    setSaveError('')
    setStatusMessage('')
    try {
      const saved = await createSurveyPlan(projectId, { requestId, ...payload }, controller.signal)
      if (controller.signal.aborted) return
      attempt.current = null
      initialized.current = true
      setDraft(draftFromPlan(saved))
      setViewingPlanId(saved.planId)
      setStatusMessage(`已保存第 ${saved.version} 版分析计划（计划发放 ${saved.sampleSizeResult.plannedInvitations} 份），并设为新分析默认关联计划。`)
      onCreated(saved)
    } catch (cause: unknown) {
      if (!controller.signal.aborted) setSaveError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setSaving(false)
    }
  }

  return <details className="survey-details" data-survey-plan-section>
    <summary data-survey-plan-summary>
      样本量与分析计划（{latestVersion > 0 ? `第 ${latestVersion} 版` : '未保存'}）
    </summary>
    <div className="survey-section">
      <p className="survey-note">
        采用 Cochran 比例抽样口径（<code>{sampleSizeMethodCochran}</code>），保存后生成不可变版本号，可供问卷分析报告关联溯源。
      </p>
      {viewingPlan && <p className="survey-note" data-survey-plan-viewing>
        当前表单对应：<strong>第 {viewingPlan.version} 版</strong>（{viewingPlan.title}）。修改后保存将生成第 {latestVersion + 1} 版。
      </p>}

      <div className="survey-plan-grid">
        <div className="field">
          <label htmlFor={`${id}-confidence`}>置信水平</label>
          <select id={`${id}-confidence`} data-survey-plan-confidence value={draft.confidenceLevel} disabled={busy}
            onChange={(event) => updateDraft({ confidenceLevel: Number(event.currentTarget.value) as SampleSizeConfidenceLevel })}>
            {sampleSizeConfidenceLevels.map((level) => (
              <option key={level} value={level}>{level}%</option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-proportion`}>预期比例 p</label>
          <input id={`${id}-proportion`} data-survey-plan-proportion type="number" step="0.01" min="0.01" max="0.99"
            value={draft.expectedProportion} disabled={busy}
            onChange={(event) => updateDraft({ expectedProportion: event.currentTarget.value })} />
        </div>
        <div className="field">
          <label htmlFor={`${id}-margin`}>允许误差 e</label>
          <input id={`${id}-margin`} data-survey-plan-margin type="number" step="0.005" min="0.001" max="0.499"
            value={draft.marginOfError} disabled={busy}
            onChange={(event) => updateDraft({ marginOfError: event.currentTarget.value })} />
        </div>
        <div className="field">
          <label htmlFor={`${id}-population`}>总体规模 N（留空为无限总体）</label>
          <input id={`${id}-population`} data-survey-plan-population type="number" step="1" min="0" max="1000000000"
            placeholder="例如 1000" value={draft.populationSize} disabled={busy}
            onChange={(event) => updateDraft({ populationSize: event.currentTarget.value })} />
        </div>
        <div className="field">
          <label htmlFor={`${id}-response-rate`}>预期有效回收率 r</label>
          <input id={`${id}-response-rate`} data-survey-plan-response-rate type="number" step="0.05" min="0.01" max="1"
            value={draft.responseRate} disabled={busy}
            onChange={(event) => updateDraft({ responseRate: event.currentTarget.value })} />
        </div>
      </div>

      {'result' in preview ? <div className="survey-section" data-survey-plan-preview>
        <dl className="survey-counts">
          <div><dt>临界值 z</dt><dd data-survey-plan-z>{preview.result.zScore.toFixed(4)}</dd></div>
          <div><dt>基础样本量 n₀</dt><dd data-survey-plan-unadjusted>{preview.result.unadjustedSampleSize}</dd></div>
          <div><dt>有限总体校正</dt><dd data-survey-plan-fpc>{preview.result.finitePopulationAdjusted}</dd></div>
          <div><dt>计划发放量</dt><dd data-survey-plan-planned>{preview.result.plannedInvitations}</dd></div>
        </dl>
        <p className="survey-note survey-plan-formula" data-survey-plan-formula>{preview.result.formulaSummary}</p>
      </div> : <p className="form-error" role="alert" data-survey-plan-preview-error>{preview.error}</p>}

      <div className="field">
        <label htmlFor={`${id}-title`}>计划标题</label>
        <input id={`${id}-title`} data-survey-plan-title type="text" maxLength={200} value={draft.title} disabled={busy}
          placeholder="例如：春季校园调研样本量与分析计划"
          onChange={(event) => updateDraft({ title: event.currentTarget.value })} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-objective`}>研究目标</label>
        <textarea id={`${id}-objective`} className="survey-plan-textarea" data-survey-plan-objective rows={2}
          value={draft.objective} disabled={busy} placeholder="说明本次问卷调研要验证的核心问题或假设"
          onChange={(event) => updateDraft({ objective: event.currentTarget.value })} />
      </div>
      <div className="survey-plan-grid">
        <div className="field">
          <label htmlFor={`${id}-target-population`}>目标总体</label>
          <input id={`${id}-target-population`} data-survey-plan-target-population type="text" maxLength={500}
            value={draft.targetPopulation} disabled={busy} placeholder="例如：在校本科生与研究生"
            onChange={(event) => updateDraft({ targetPopulation: event.currentTarget.value })} />
        </div>
        <div className="field">
          <label htmlFor={`${id}-sampling`}>抽样方式</label>
          <input id={`${id}-sampling`} data-survey-plan-sampling type="text" maxLength={200} list={`${id}-sampling-list`}
            value={draft.samplingMethod} disabled={busy}
            onChange={(event) => updateDraft({ samplingMethod: event.currentTarget.value })} />
          <datalist id={`${id}-sampling-list`}>
            {samplingSuggestions.map((name) => <option key={name} value={name} />)}
          </datalist>
        </div>
      </div>

      <fieldset className="survey-scale-items" disabled={busy}>
        <legend>计划统计方法（已选 {draft.plannedMethods.length} 项）</legend>
        {methodOptions.map((method) => (
          <label key={method}>
            <input type="checkbox" data-survey-plan-method={method} checked={draft.plannedMethods.includes(method)}
              onChange={(event) => toggleMethod(method, event.currentTarget.checked)} />
            <span>{method}</span>
          </label>
        ))}
      </fieldset>

      <div className="field">
        <label htmlFor={`${id}-missing`}>缺失值与质量控制策略</label>
        <textarea id={`${id}-missing`} className="survey-plan-textarea" data-survey-plan-missing rows={2}
          value={draft.missingStrategy} disabled={busy}
          onChange={(event) => updateDraft({ missingStrategy: event.currentTarget.value })} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-notes`}>补充说明（可选）</label>
        <textarea id={`${id}-notes`} className="survey-plan-textarea" data-survey-plan-notes rows={2}
          value={draft.notes} disabled={busy} placeholder="记录预调研调整、样本配额或特殊口径说明"
          onChange={(event) => updateDraft({ notes: event.currentTarget.value })} />
      </div>

      <div className="survey-actions">
        <button className="button primary" type="button" data-survey-plan-save
          disabled={busy || 'error' in preview} onClick={() => void savePlan()}>
          {saving ? '正在保存计划版本…' : `保存计划版本（第 ${latestVersion + 1} 版）`}
        </button>
        <button className="button outline" type="button" data-survey-plan-refresh disabled={loading || saving}
          onClick={() => {
            attempt.current = null
            setSaveError('')
            onRefresh()
          }}>刷新计划版本</button>
      </div>

      {statusMessage && <p className="survey-note" role="status" data-survey-plan-status>{statusMessage}</p>}
      {saveError && <p className="form-error" role="alert" data-survey-plan-error>{saveError}</p>}
      {loading && <p className="survey-note" role="status">正在加载历史计划版本…</p>}
      {loadError && <div className="survey-actions">
        <p className="form-error" role="alert">{loadError}</p>
        <button className="button outline" type="button" onClick={onRefresh}>重试加载计划</button>
      </div>}

      {plans.length > 0 && <section className="survey-section" aria-labelledby={`${id}-history`}>
        <h6 id={`${id}-history`}>历史计划版本（共 {plans.length} 版）</h6>
        <ul className="survey-history" role="list" data-survey-plan-history>
          {plans.map((plan) => (
            <li key={plan.planId} className="survey-plan-history-item" data-survey-plan-version={plan.version}>
              <button type="button" className="survey-report-link"
                aria-current={viewingPlanId === plan.planId ? 'true' : undefined}
                data-survey-plan-load-version={plan.version}
                onClick={() => loadHistoryVersion(plan)}>
                <strong dir="auto">第 {plan.version} 版 · {plan.title}</strong>
                <span className="survey-note">
                  <time dateTime={plan.createdAt}>{planDate.format(new Date(plan.createdAt))}</time>
                  {' · '}基础 {plan.sampleSizeResult.unadjustedSampleSize}
                  {' · '}校正 {plan.sampleSizeResult.finitePopulationAdjusted}
                  {' · '}计划发放 {plan.sampleSizeResult.plannedInvitations} 份
                  {selectedPlanId === plan.planId ? ' · 当前新分析默认关联' : ''}
                </span>
              </button>
              {selectedPlanId !== plan.planId && <button type="button" className="button outline survey-plan-bind-btn"
                data-survey-plan-bind-version={plan.version}
                onClick={() => onSelectPlan(plan.planId)}>用于新分析</button>}
            </li>
          ))}
        </ul>
      </section>}
    </div>
  </details>
}
