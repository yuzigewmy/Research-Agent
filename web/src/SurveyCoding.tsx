import { useId, useRef, useState } from 'react'
import { categoryMappingLimit, categoryMappingValueByteLimit } from './surveys'
import type { SurveyCategoryMapping, SurveySummary } from './surveys'

const byteEncoder = new TextEncoder()
const categoryMappingColumnByteLimit = 256

type DraftMapping = SurveyCategoryMapping & { id: number }

function trimMappingText(value: string) {
  // Match Go's strings.TrimSpace for both rules and source answers.
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
}

function isCategoryValue(value: string) {
  return value !== '' && byteEncoder.encode(value).length <= categoryMappingValueByteLimit && !/\p{Cc}/u.test(value)
}

function columnKey(name: string) {
  // Go lowercases each rune independently and maps U+0130 to a single i.
  return Array.from(name, (character) => character === '\u0130' ? 'i' : character.toLowerCase()).join('')
}

function savedColumn(column: string, columns: string[]) {
  const name = trimMappingText(column)
  if (columns.includes(name)) return name
  const key = columnKey(name)
  return columns.find((candidate) => columnKey(candidate) === key) ?? name
}

export function ReportCoding({ summaries, mappings, busy, error, onApply }: {
  summaries: SurveySummary[]
  mappings?: SurveyCategoryMapping[]
  busy: boolean
  error?: string
  onApply: (mappings: SurveyCategoryMapping[]) => void
}) {
  const id = useId()
  const columns = summaries.map(({ column }) => column)
  const [saved] = useState(() => (mappings ?? []).map(({ column, from, to }) => ({
    column: savedColumn(column, columns), from: trimMappingText(from), to: trimMappingText(to),
  })))
  const [draft, setDraft] = useState<DraftMapping[]>(() => saved.map((mapping, id) => ({ ...mapping, id })))
  const nextId = useRef(draft.length)
  const nextFocus = useRef<number | 'add'>(null)
  const [validationError, setValidationError] = useState('')
  const rules = draft.map(({ column, from, to }) => ({
    column: trimMappingText(column), from: trimMappingText(from), to: trimMappingText(to),
  }))
  const changed = rules.length !== saved.length || rules.some(({ column, from, to }, index) => {
    const previous = saved[index]
    return column !== previous.column || from !== previous.from || to !== previous.to
  })

  function addMapping() {
    const mapping = { id: nextId.current++, column: '', from: '', to: '' }
    nextFocus.current = mapping.id
    setDraft((current) => current.length < categoryMappingLimit ? [...current, mapping] : current)
    setValidationError('')
  }

  function updateMapping(id: number, key: keyof SurveyCategoryMapping, value: string) {
    setDraft((current) => current.map((mapping) => mapping.id === id ? { ...mapping, [key]: value } : mapping))
    setValidationError('')
  }

  function submitMappings() {
    const seen = new Set<string>()
    for (const [index, { column, from, to }] of rules.entries()) {
      if (!columns.includes(column) || byteEncoder.encode(column).length > categoryMappingColumnByteLimit) {
        setValidationError(`请选择第 ${index + 1} 条规则的统计列。`)
        return
      }
      if (!isCategoryValue(from) || !isCategoryValue(to)) {
        setValidationError(`请填写第 ${index + 1} 条规则的原值和目标类别，每项最多 ${categoryMappingValueByteLimit} 个 UTF-8 字节，且不能含控制字符。`)
        return
      }
      const key = JSON.stringify([column, from])
      if (seen.has(key)) {
        setValidationError(`第 ${index + 1} 条规则与同列的原值重复。`)
        return
      }
      seen.add(key)
    }
    setValidationError('')
    onApply(rules)
  }

  const message = validationError || error
  return <details className="survey-details" data-survey-coding>
    <summary>分类编码（已应用 {saved.length} 条）</summary>
    <form className="survey-section" aria-label="分类编码" aria-describedby={`${id}-help`} onSubmit={(event) => {
      event.preventDefault()
      if (!busy && changed) submitMappings()
    }}>
      <p className="survey-note" id={`${id}-help`}>
        编码用于答卷频次和交叉统计。去除首尾空白后按原值精确匹配，区分大小写，每个回答只查找一次。未匹配的回答沿用原类别。
        最多可设 {categoryMappingLimit} 条规则，原值和目标类别各限 {categoryMappingValueByteLimit} 个 UTF-8 字节。应用后会重新计算并保存新报告。
      </p>
      {draft.length > 0 ? <ol className="survey-rows" role="list" aria-label="分类编码规则">
        {draft.map(({ id: rowId, column, from, to }, index) => {
          const row = `${id}-${rowId}`
          return <li key={rowId} data-survey-coding-rule>
            <div className="survey-cross-tab-fields survey-review">
              <div className="field">
                <label htmlFor={`${row}-column`}>统计列 <span className="survey-note">第 {index + 1} 条</span></label>
                <select id={`${row}-column`} value={column} disabled={busy} data-survey-coding-column
                  ref={(select) => {
                    if (select && nextFocus.current === rowId) {
                      nextFocus.current = null
                      select.focus()
                    }
                  }}
                  onChange={(event) => updateMapping(rowId, 'column', event.currentTarget.value)}>
                  <option value="">请选择字段</option>
                  {columns.map((name) => <option key={name} value={name}>{name}</option>)}
                </select>
              </div>
              <div className="field">
                <label htmlFor={`${row}-from`}>原值 <span className="survey-note">第 {index + 1} 条</span></label>
                <input id={`${row}-from`} type="text" value={from} readOnly={busy} dir="auto" data-survey-coding-from
                  onChange={(event) => updateMapping(rowId, 'from', event.currentTarget.value)} />
              </div>
              <div className="field">
                <label htmlFor={`${row}-to`}>目标类别 <span className="survey-note">第 {index + 1} 条</span></label>
                <input id={`${row}-to`} type="text" value={to} readOnly={busy} dir="auto" data-survey-coding-to
                  onChange={(event) => updateMapping(rowId, 'to', event.currentTarget.value)} />
              </div>
            </div>
            <div className="survey-actions">
              <button className="button outline" type="button" disabled={busy} aria-label={`删除第 ${index + 1} 条规则`}
                data-survey-coding-remove onClick={() => {
                  const neighbor = draft[index + 1] ?? draft[index - 1]
                  nextFocus.current = neighbor?.id ?? 'add'
                  setDraft((current) => current.filter((mapping) => mapping.id !== rowId))
                  setValidationError('')
                }}>删除规则</button>
            </div>
          </li>
        })}
      </ol> : <p className="survey-note">{columns.length ? '暂无分类编码规则。' : '此报告没有可编码的统计列。'}</p>}
      {message && <p className="form-error" id={`${id}-error`} role="alert">{message}</p>}
      <div className="survey-actions">
        <button className="button outline" type="button" disabled={busy || !columns.length || draft.length >= categoryMappingLimit}
          data-survey-coding-add ref={(button) => {
            if (button && nextFocus.current === 'add') {
              nextFocus.current = null
              button.focus()
            }
          }} onClick={addMapping}>添加规则</button>
        <button className="button outline" type="submit" disabled={busy || !changed}
          data-survey-coding-apply aria-describedby={message ? `${id}-error` : undefined}>应用编码并保存报告</button>
      </div>
    </form>
  </details>
}
