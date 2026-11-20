import { useId, useRef, useState } from 'react'
import type { SkipWorkflowInput } from './workflow'

export type SkipTarget = Omit<SkipWorkflowInput, 'requestId'>
const skipReasonLimit = 300

export function WorkflowSkipStep({ target, stepTitle, disabled, pending, retryReason, onConfirm }: {
  target: Omit<SkipTarget, 'reason'>
  stepTitle: string
  disabled: boolean
  pending: boolean
  retryReason?: string
  onConfirm: (target: SkipTarget) => void
}) {
  const id = useId()
  const opener = useRef<HTMLButtonElement>(null)
  const [confirmation, setConfirmation] = useState<Omit<SkipTarget, 'reason'>>()
  const [reason, setReason] = useState('')
  const cleanedReason = reason.trim()
  const tooLong = Array.from(cleanedReason).length > skipReasonLimit

  return <div>
    <button className="button outline" type="button" ref={opener} disabled={disabled}
      aria-expanded={!!confirmation} aria-controls={`${id}-confirmation`}
      onClick={() => setConfirmation((previous) => previous ? undefined : { ...target })}>
      跳过此步骤
    </button>
    <div id={`${id}-confirmation`} hidden={!confirmation}>
      {confirmation && <div className="workflow-skip-confirm field" role="group" aria-labelledby={`${id}-help`}>
        <p className="workflow-note" id={`${id}-help`}>将「{stepTitle}」标为已跳过。以后可单独运行此步骤。</p>
        <label htmlFor={`${id}-reason`}>跳过理由</label>
        <input id={`${id}-reason`} type="text" value={reason} required autoFocus readOnly={pending}
          aria-describedby={`${id}-limit`} aria-invalid={tooLong || undefined}
          onChange={(event) => setReason(event.currentTarget.value)} />
        <p id={`${id}-limit`} className={tooLong ? 'form-error' : 'workflow-note'}>理由必填，最多 {skipReasonLimit} 个字符。</p>
        <div className="workflow-actions">
          <button className="button outline" type="button" disabled={(!pending && disabled) || !cleanedReason || tooLong}
            aria-disabled={pending} onClick={() => {
              if (!disabled && !pending && cleanedReason && !tooLong) onConfirm({ ...confirmation, reason: cleanedReason })
            }}>{pending ? '正在提交…' : retryReason === cleanedReason ? '重试跳过' : '确认跳过'}</button>
          <button className="button outline" type="button" disabled={pending} onClick={() => {
            setConfirmation(undefined)
            opener.current?.focus()
          }}>返回</button>
        </div>
      </div>}
    </div>
  </div>
}
