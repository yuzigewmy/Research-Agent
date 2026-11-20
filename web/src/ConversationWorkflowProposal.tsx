import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import { changeWorkflowProposal } from './turns'
import type { WorkflowProposal } from './turns'

type Decision = 'confirm' | 'cancel'

export function ConversationWorkflowProposal({ projectId, turnId, proposal, onChanged }: {
  projectId: string
  turnId: string
  proposal: WorkflowProposal
  onChanged: (proposal: WorkflowProposal) => void
}) {
  const headingId = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  const request = useRef<AbortController | null>(null)
  const [pending, setPending] = useState<Decision>()
  const [error, setError] = useState('')
  const [stale, setStale] = useState(false)
  const { steps, scope } = proposal
  const start = steps.findIndex((step) => step.key === scope.startStep)
  const end = steps.findIndex((step) => step.key === scope.endStep)
  const range = steps.slice(start, end + 1)

  useEffect(() => () => request.current?.abort(), [])

  async function decide(decision: Decision) {
    if (request.current || proposal.status !== 'PROPOSED' || (decision === 'confirm' && stale)) return
    const controller = new AbortController()
    const control = document.activeElement
    request.current = controller
    setPending(decision)
    setError('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      const updated = await changeWorkflowProposal(projectId, turnId, decision, signal)
      if (controller.signal.aborted) return
      if (updated.status !== 'PROPOSED' && document.activeElement === control) heading.current?.focus()
      onChanged(updated)
    } catch (cause) {
      if (controller.signal.aborted) return
      if (cause instanceof ProjectApiError && cause.code === 'WORKFLOW_PROPOSAL_STALE') {
        setStale(true)
        setError('工作流已有更新。请根据当前流程重新提议。')
      } else {
        setError(errorMessage(cause))
      }
    } finally {
      request.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  return <section className="workspace-discard" aria-labelledby={headingId}>
    <h4 id={headingId} tabIndex={-1} ref={heading}>助手建议的工作流</h4>
    <p className="turn-status">
      {proposal.action === 'start' ? '开始新一轮' : '继续本轮'} · {proposal.checkpoints ? '已启用检查点' : '未启用检查点'}
    </p>
    <p className="turn-content" dir="auto">{proposal.instruction}</p>
    <ol className="workflow-steps" aria-label="建议运行的步骤">
      {range.map(({ key, title }) => <li key={key} dir="auto">{title}</li>)}
    </ol>
    {error && proposal.status === 'PROPOSED' && <p className="form-error" role="alert">{error}</p>}
    {proposal.status === 'PROPOSED' ? (
      <div className="workflow-actions">
        <button className="button primary" type="button" disabled={stale} aria-disabled={!!pending}
          onClick={() => void decide('confirm')}>
          {pending === 'confirm' ? '正在确认…' : '确认运行工作流'}
        </button>
        <button className="button outline" type="button" aria-disabled={!!pending} onClick={() => void decide('cancel')}>
          {pending === 'cancel' ? '正在取消…' : '取消工作流建议'}
        </button>
      </div>
    ) : <p className="turn-status" role="status">
      {proposal.status === 'CONFIRMED' ? '工作流建议已确认' : '工作流建议已取消'}
    </p>}
  </section>
}
