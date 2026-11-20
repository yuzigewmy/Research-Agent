import { useId } from 'react'
import type { ProposalOptions, ProposalTargets, WorkflowRun } from './workflow'

const degreeLabels = { bachelor: '本科', master: '硕士', doctor: '博士' } as const
const degreeTypeLabels = { academic: '学术学位', professional: '专业学位' } as const
const defaults = {
  bachelor: { targetCharacters: 3000, targetSources: 15 },
  master: { targetCharacters: 5000, targetSources: 30 },
  doctor: { targetCharacters: 15000, targetSources: 50 },
} as const

const textFields = [
  { key: 'discipline', label: '学科方向' },
  { key: 'method', label: '研究方法' },
  { key: 'projectSource', label: '课题来源' },
] as const
const monthFields = [
  { key: 'proposalMonth', label: '开题月份' },
  { key: 'defenseMonth', label: '答辩月份' },
] as const
const quotaFields = [
  { key: 'targetCharacters', label: '目标正文字数', min: 500, max: 30000, unit: '字' },
  { key: 'targetSources', label: '目标文献数', min: 1, max: 80, unit: '条' },
] as const

type ProposalDraft = Omit<ProposalTargets, 'targetCharacters' | 'targetSources'> & Record<
  typeof textFields[number]['key'] | typeof monthFields[number]['key'] | typeof quotaFields[number]['key'] | 'requirements', string
>
type ProposalErrors = Partial<Record<keyof ProposalDraft, string>>

export const initialProposalDraft: ProposalDraft = {
  degree: 'master', degreeType: 'academic', discipline: '', method: '', projectSource: '',
  proposalMonth: '', defenseMonth: '', requirements: '', targetCharacters: '', targetSources: '',
}

export function prepareProposal(draft: ProposalDraft) {
  const errors: ProposalErrors = {}
  const options: ProposalOptions = { degree: draft.degree, degreeType: draft.degreeType }
  for (const { key } of textFields) {
    const text = draft[key].trim()
    if (Array.from(text).length > 200 || /\p{Cc}/u.test(text)) errors[key] = '最多 200 个字符，且不得包含控制字符。'
    if (text) options[key] = text
  }
  for (const { key } of monthFields) {
    const month = draft[key].trim()
    if (month && !/^\d{4}-(0[1-9]|1[0-2])$/.test(month)) errors[key] = '请输入 YYYY-MM 格式的有效月份。'
    if (month) options[key] = month
  }
  if (!errors.proposalMonth && !errors.defenseMonth && options.proposalMonth && options.defenseMonth
    && options.defenseMonth < options.proposalMonth) errors.defenseMonth = '答辩月份不能早于开题月份。'
  for (const { key, min, max, unit } of quotaFields) {
    const text = draft[key].trim()
    if (!text) continue
    const count = Number(text)
    if (!/^\d+$/.test(text) || count < min || count > max) errors[key] = `请输入 ${min} 至 ${max} ${unit}之间的整数。`
    else options[key] = count
  }
  const requirements = draft.requirements.trim()
  if (new TextEncoder().encode(requirements).length > 4 * 1024 || requirements.includes('\0')) {
    errors.requirements = '补充要求须为 4 KiB 以内且不含空字符。'
  }
  if (requirements) options.requirements = requirements
  return { options, errors, valid: Object.keys(errors).length === 0 }
}

export function WorkflowProposalOptions({ draft, errors, disabled, onChange }: {
  draft: ProposalDraft
  errors: ProposalErrors
  disabled: boolean
  onChange: (draft: ProposalDraft) => void
}) {
  const id = useId()
  const goals = defaults[draft.degree]

  function field(key: keyof ProposalDraft, label: string, placeholder?: string, numeric = false) {
    const inputId = `${id}-${key}`
    return <div className="field" key={key}>
      <label htmlFor={inputId}>{label}</label>
      <input id={inputId} value={draft[key]} readOnly={disabled} placeholder={placeholder}
        inputMode={numeric ? 'numeric' : undefined} aria-invalid={!!errors[key] || undefined}
        aria-describedby={errors[key] ? `${inputId}-error` : undefined}
        onChange={(event) => onChange({ ...draft, [key]: event.currentTarget.value })} />
      {errors[key] && <p id={`${inputId}-error`} className="form-error">{errors[key]}</p>}
    </div>
  }

  return <div className="create-form">
    <div className="workflow-actions">
      <div className="field">
        <label htmlFor={`${id}-degree`}>学历层次</label>
        <select id={`${id}-degree`} value={draft.degree} disabled={disabled} onChange={(event) => {
          onChange({ ...draft, degree: event.currentTarget.value as ProposalDraft['degree'] })
        }}>
          {Object.entries(degreeLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
      </div>
      <div className="field">
        <label htmlFor={`${id}-degree-type`}>学位类型</label>
        <select id={`${id}-degree-type`} value={draft.degreeType} disabled={disabled} onChange={(event) => {
          onChange({ ...draft, degreeType: event.currentTarget.value as ProposalDraft['degreeType'] })
        }}>
          {Object.entries(degreeTypeLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
      </div>
    </div>
    <div className="field">
      <div className="workflow-actions">
        {quotaFields.map(({ key, label }) => field(key, label, String(goals[key]), true))}
      </div>
      <p className="workflow-note">留空时按学历层次使用 {goals.targetCharacters} 字和 {goals.targetSources} 条文献。校方有要求时可自填。</p>
    </div>
    <div className="field">
      <div className="workflow-actions">
        {textFields.map(({ key, label }) => field(key, label))}
      </div>
      <p className="workflow-note">学科方向或研究方法留空时，按研究题目判断。</p>
    </div>
    <div className="field">
      <div className="workflow-actions">
        {monthFields.map(({ key, label }) => field(key, label, 'YYYY-MM'))}
      </div>
      <p className="workflow-note">月份可留空，届时使用相对时间安排。</p>
    </div>
    <div className="field">
      <label htmlFor={`${id}-requirements`}>补充要求</label>
      <textarea className="workspace-text" id={`${id}-requirements`} value={draft.requirements} rows={3}
        readOnly={disabled} aria-invalid={!!errors.requirements || undefined} aria-describedby={`${id}-requirements-help`}
        onChange={(event) => onChange({ ...draft, requirements: event.currentTarget.value })} />
      <p id={`${id}-requirements-help`} className={errors.requirements ? 'form-error' : 'workflow-note'}>
        {errors.requirements || '选填，最多 4 KiB（UTF-8）。开题设置和本轮文献元数据将发送给研究模型。'}
      </p>
    </div>
  </div>
}

export function WorkflowProposalSummary({ run }: { run: WorkflowRun }) {
  const settings = run.proposal
  if (!settings) return null
  return <p className="workflow-note">
    {degreeLabels[settings.degree]} · {degreeTypeLabels[settings.degreeType]}。目标正文 {settings.targetCharacters} 字，
    目标文献 {settings.targetSources} 条，实际纳入 {run.sourceCount ?? 0} 条。
  </p>
}
