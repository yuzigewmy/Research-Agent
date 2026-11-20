import { useId, useState } from 'react'
import type { SelectWorkflowTopicInput, WorkflowTopicChoiceData } from './workflow'

export type TopicSelectionTarget = Omit<SelectWorkflowTopicInput, 'requestId'>

export function WorkflowSelectedTopic({ choice }: { choice?: WorkflowTopicChoiceData }) {
  const selected = choice?.candidates.find(({ number }) => number === choice.selectedNumber)
  if (!selected) return null
  return <div className="workflow-topic-selected workflow-note">
    <p>已选题目 <strong dir="auto">{selected.title}</strong></p>
    <p dir="auto">{selected.summary}</p>
  </div>
}

export function WorkflowTopicChoice({ choice, target, disabled, pending, retryNumber, onConfirm }: {
  choice: WorkflowTopicChoiceData
  target: Omit<TopicSelectionTarget, 'candidateNumber'>
  disabled: boolean
  pending: boolean
  retryNumber?: number
  onConfirm: (target: TopicSelectionTarget) => void
}) {
  const id = useId()
  const [selected, setSelected] = useState(retryNumber)
  const { candidates, recommendedNumber, recommendationReason } = choice
  const recommended = candidates.find(({ number }) => number === recommendedNumber)

  function confirm(candidateNumber: number) {
    if (!disabled && !pending && candidates.some(({ number }) => number === candidateNumber)) {
      setSelected(candidateNumber)
      onConfirm({ ...target, candidateNumber })
    }
  }

  return <fieldset className="workflow-topic-choice" aria-describedby={`${id}-scope`}>
    <legend>入围选题</legend>
    <p className="workflow-note" id={`${id}-scope`}>新颖性分数是基于本轮文献的初评。</p>
    <ul className="workflow-topic-list" role="list">
      {candidates.map((candidate) => (
        <li key={candidate.number}>
          <label className="workflow-topic-option">
            <input type="radio" name={id} checked={selected === candidate.number} disabled={disabled || pending}
              onChange={() => setSelected(candidate.number)} />
            <strong dir="auto">{candidate.title}</strong>
          </label>
          <p className="workflow-note" dir="auto">{candidate.summary}</p>
          <dl className="workflow-topic-evaluation workflow-note">
            <div><dt>新颖性初评</dt><dd>{candidate.noveltyScore}/10</dd></div>
            <div><dt>优势</dt><dd dir="auto">{candidate.strength}</dd></div>
            <div><dt>风险</dt><dd dir="auto">{candidate.risk}</dd></div>
            <div><dt>预计投入</dt><dd dir="auto">{candidate.effort}</dd></div>
            {candidate.evidenceKeys && candidate.evidenceKeys.length > 0 && (
              <div><dt>文献键</dt><dd>{candidate.evidenceKeys.join('、')}</dd></div>
            )}
          </dl>
        </li>
      ))}
    </ul>
    {recommended && <p className="workflow-note" id={`${id}-recommendation`}>
      推荐选题 <bdi>{recommended.title}</bdi>。<bdi>{recommendationReason}</bdi>
    </p>}
    <div className="workflow-actions">
      <button className="button primary" type="button" disabled={(disabled && !pending) || selected === undefined}
        aria-disabled={pending} onClick={() => { if (selected !== undefined) confirm(selected) }}>
        {pending ? '正在确认…' : '确认所选题目'}
      </button>
      {recommended && <button className="button outline" type="button" disabled={disabled || pending}
        aria-describedby={`${id}-recommendation`} onClick={() => confirm(recommended.number)}>采用推荐选题</button>}
    </div>
  </fieldset>
}
