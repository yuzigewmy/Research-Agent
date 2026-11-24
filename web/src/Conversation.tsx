import { useEffect, useEffectEvent, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ConversationTasks } from './ConversationTasks'
import { ConversationWorkflowProposal } from './ConversationWorkflowProposal'
import { materialDownloadPath } from './materials'
import { errorMessage, isTurnMode, ProjectApiError, turnModeHelp, turnModeLabels } from './projects'
import type { TurnMode } from './projects'
import type { TaskPanelSync } from './TaskPanel'
import { conversationTurnAnchor, taskArtifactPath } from './tasks'
import {
  isConversationModelId, isManuscriptSelectionInput, isManuscriptSelectionInputs, isPdfQuotes, isProposedTaskId,
  isWorkflowProposal, listTurns, manuscriptSelectionInput, manuscriptSelectionsOf, matchesTurnModel,
  sameManuscriptSelections, sendTurn, stopTurn, turnsPath,
} from './turns'
import type {
  ConversationModelId, ConversationModelOption, ManuscriptSelection, ManuscriptSelectionInput, PdfQuote, Turn, TurnEvent,
  TurnInput, TurnReference, WorkflowProposal,
} from './turns'

type StopState = { turnId: string; kind: 'pending' } | { turnId: string; kind: 'error'; message: string }

function latestTurn(current: Turn | undefined, snapshot: Turn) {
  // On a tie, the full snapshot can supply text missing from the local event stream.
  const latest = current && current.afterSeq > snapshot.afterSeq ? current : snapshot
  // A proposal decision is saved separately from the turn's event sequence.
  const decided = [current?.workflowProposal, snapshot.workflowProposal]
    .find((proposal) => proposal && proposal.status !== 'PROPOSED')
  return decided && latest.workflowProposal?.status === 'PROPOSED' ? { ...latest, workflowProposal: decided } : latest
}

function mergeTurns(current: Turn[], snapshots: Turn[]) {
  const byId = new Map(current.map((turn) => [turn.turnId, turn]))
  for (const snapshot of snapshots) byId.set(snapshot.turnId, latestTurn(byId.get(snapshot.turnId), snapshot))
  return [...byId.values()]
}

function readAttempt(key: string): TurnInput | null {
  try {
    const saved: unknown = JSON.parse(sessionStorage.getItem(key) ?? 'null')
    if (!saved || typeof saved !== 'object') return null
    const input = saved as Partial<TurnInput>
    if (typeof input.requestId !== 'string' || typeof input.text !== 'string'
      || (input.mode !== undefined && !isTurnMode(input.mode))
      || (input.modelId !== undefined && !isConversationModelId(input.modelId))
      || (input.referenceIds !== undefined && (!Array.isArray(input.referenceIds)
        || !input.referenceIds.every((id) => typeof id === 'string')))
      || (input.pdfQuotes !== undefined && !isPdfQuotes(input.pdfQuotes, input.referenceIds))
      || (input.manuscriptSelection !== undefined && !isManuscriptSelectionInput(input.manuscriptSelection))
      || (input.manuscriptSelections !== undefined && (input.manuscriptSelection !== undefined
        || !isManuscriptSelectionInputs(input.manuscriptSelections)))) return null
    return {
      requestId: input.requestId, text: input.text, referenceIds: input.referenceIds,
      pdfQuotes: input.pdfQuotes, mode: input.mode ?? 'CHAT', modelId: input.modelId,
      manuscriptSelection: input.manuscriptSelection ? manuscriptSelectionInput(input.manuscriptSelection) : undefined,
      manuscriptSelections: input.manuscriptSelections?.map(manuscriptSelectionInput),
    }
  } catch {
    return null
  }
}

function readModelPreference(key: string): ConversationModelId | undefined {
  try {
    const value = sessionStorage.getItem(key)
    return isConversationModelId(value) ? value : undefined
  } catch {
    return undefined
  }
}

function matchesSentReference(input: TurnInput, reference: TurnReference) {
  if (!input.referenceIds?.includes(reference.materialId)) return false
  const selected = input.pdfQuotes?.[reference.materialId]
  if (!selected) return reference.page === undefined
  return selected.page === reference.page && selected.sha256 === reference.sha256 && selected.text === reference.quote
}

function PdfReferenceDetails({ page, quote }: { page?: number; quote?: string }) {
  if (page === undefined) return null
  return <>
    <span className="turn-status">第 {page} 页</span>
    <details className="turn-quote">
      <summary>查看选区</summary>
      <blockquote className="turn-content" dir="auto">{quote}</blockquote>
    </details>
  </>
}

function ManuscriptQuotes({ selections, onRemove }: {
  selections: readonly ManuscriptSelection[]
  onRemove?: (selection: ManuscriptSelection) => void
}) {
  if (!selections.length) return null
  const label = onRemove ? '已选稿件选区' : '随消息发送的稿件选区'
  return <div className={onRemove ? 'turn-references draft-references' : 'turn-references'}
    aria-labelledby={onRemove ? 'draft-manuscript-title' : undefined}>
    {onRemove ? <>
      <p id="draft-manuscript-title">{label}</p>
      <p className="turn-status">发送消息时，所选稿件文字会发送给研究模型。</p>
    </> : <p className="turn-status">{label}</p>}
    <ul aria-label={label}>
      {selections.map((selection) => {
        const { path, versionId, sha256, text } = selection
        return <li key={path}>
          <bdi>{path}</bdi>
          <span className="turn-status">版本 <bdi>{versionId}</bdi> · SHA-256 <code title={sha256}>{sha256.slice(0, 12)}</code></span>
          {onRemove && <button className="button outline" type="button"
            aria-label={selections.length === 1 ? '移除稿件选区' : `移除稿件选区：${path}`}
            onClick={() => onRemove(selection)}>移除</button>}
          <details className="turn-quote">
            <summary>查看稿件选区</summary>
            <blockquote className="turn-content" dir="auto">{text}</blockquote>
          </details>
        </li>
      })}
    </ul>
  </div>
}

interface ConversationProps {
  projectId: string
  defaultTurnMode?: TurnMode
  references: TurnReference[]
  onClearReferences: (matches: (reference: TurnReference) => boolean) => void
  manuscriptSelections: readonly ManuscriptSelection[]
  onClearManuscriptSelections: (sent: readonly ManuscriptSelectionInput[]) => void
  onWorkflowConfirmed?: () => void
  taskSync?: TaskPanelSync
}

export function Conversation({ projectId, defaultTurnMode = 'CHAT', references, onClearReferences, manuscriptSelections,
  onClearManuscriptSelections, onWorkflowConfirmed, taskSync }: ConversationProps) {
  const attemptKey = `research.turn.${projectId}`
  const draftKey = `research.draft.${projectId}`
  const modelPreferenceKey = `research.conversation-model.${projectId}`
  const submission = useRef<AbortController | null>(null)
  const earlierRequest = useRef<AbortController | null>(null)
  const stopRequests = useRef(new Map<string, AbortController>())
  const [turns, setTurns] = useState<Turn[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState(false)
  const [modelOptions, setModelOptions] = useState<ConversationModelOption[]>([])
  const [preferredModelId, setPreferredModelId] = useState(() => readModelPreference(modelPreferenceKey))
  const [attempt, setAttempt] = useState(() => readAttempt(attemptKey))
  const [draft, setDraft] = useState(() => sessionStorage.getItem(draftKey) ?? attempt?.text ?? '')
  const [modeOverride, setModeOverride] = useState<TurnMode>()
  const [loading, setLoading] = useState(true)
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const [connectionLost, setConnectionLost] = useState(false)
  const [stopState, setStopState] = useState<StopState>()
  const [reload, setReload] = useState(0)
  const runningTurn = turns.find((turn) => turn.status === 'RUNNING')
  const automaticTurnId = turns.filter((turn) => turn.workflowProposal?.automatic).at(-1)?.turnId
  const streamId = loading ? undefined : runningTurn?.turnId
  const matchingAttempt = attempt && attempt.text === draft && (attempt.referenceIds?.length ?? 0) === references.length
    && references.every((ref, index) => attempt.referenceIds?.[index] === ref.materialId && matchesSentReference(attempt, ref))
    && sameManuscriptSelections(manuscriptSelectionsOf(attempt), manuscriptSelections) ? attempt : null
  const retryMode = matchingAttempt ? matchingAttempt.mode ?? 'CHAT' : undefined
  const inheritedMode = retryMode ?? defaultTurnMode
  const mode = modeOverride ?? inheritedMode
  const retry = matchingAttempt && retryMode === mode ? matchingAttempt : null
  const preferredModel = modelOptions.find(({ id }) => id === preferredModelId) ?? modelOptions[0]
  const selectedModelId = retry ? retry.modelId ?? 'default' : preferredModel?.id

  function discardAttempt() {
    setAttempt(null)
    sessionStorage.removeItem(attemptKey)
  }

  function discardAttemptKeepingDraft() {
    updateDraft(draft)
    setModeOverride(mode === defaultTurnMode ? undefined : mode)
    discardAttempt()
  }

  function changeModel(modelId: string) {
    if (!isConversationModelId(modelId) || modelId === selectedModelId || submission.current) return
    setPreferredModelId(modelId)
    if (attempt) discardAttemptKeepingDraft()
    setError('')
    try {
      sessionStorage.setItem(modelPreferenceKey, modelId)
    } catch {
      // The selection still applies while this project is open.
    }
  }

  function finishAttempt(input: TurnInput) {
    discardAttempt()
    if (sessionStorage.getItem(draftKey) === input.text) sessionStorage.removeItem(draftKey)
    setDraft((current) => current === input.text ? '' : current)
    setModeOverride(undefined)
    if (input.referenceIds?.length) onClearReferences((reference) => matchesSentReference(input, reference))
    const sent = manuscriptSelectionsOf(input)
    if (sent.length) onClearManuscriptSelections(sent)
  }

  const reconcileAttempt = useEffectEvent((items: Turn[]) => {
    if (!attempt) return
    const sent = manuscriptSelectionsOf(attempt)
    if (items.some((turn) => turn.requestId === attempt.requestId && matchesTurnModel(turn, attempt)
      && sameManuscriptSelections(manuscriptSelectionsOf(turn), sent))) finishAttempt(attempt)
  })

  useEffect(() => {
    const controller = new AbortController()
    listTurns(projectId, controller.signal).then((page) => {
      if (controller.signal.aborted) return
      reconcileAttempt(page.items)
      setTurns((items) => [...page.items].reverse().map((snapshot) =>
        latestTurn(items.find((turn) => turn.turnId === snapshot.turnId), snapshot)))
      setCursor(page.nextCursor)
      setConfigured(page.modelConfigured)
      setModelOptions(page.modelOptions ?? [])
      setConnectionLost(false)
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(err))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, reload])

  useEffect(() => {
    const requests = stopRequests.current
    return () => {
      submission.current?.abort()
      earlierRequest.current?.abort()
      for (const controller of requests.values()) controller.abort()
    }
  }, [])

  const streamCursor = useEffectEvent((id: string) => turns.find((turn) => turn.turnId === id)?.afterSeq ?? 0)

  useEffect(() => {
    if (automaticTurnId) onWorkflowConfirmed?.()
  }, [automaticTurnId, onWorkflowConfirmed])

  useEffect(() => {
    if (!streamId) return
    const source = new EventSource(`${turnsPath(projectId)}/${encodeURIComponent(streamId)}/stream?after=${streamCursor(streamId)}`)
    source.onopen = () => setConnectionLost(false)
    source.onerror = () => {
      setConnectionLost(true)
      if (source.readyState === EventSource.CLOSED) {
        setLoading(true)
        setReload((revision) => revision + 1)
      }
    }
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as TurnEvent
      setTurns((items) => items.map((turn) => {
        if (turn.turnId !== streamId || turn.status !== 'RUNNING' || event.seq <= turn.afterSeq) return turn
        if (event.kind === 'delta') return { ...turn, answer: turn.answer + event.text, afterSeq: event.seq }
        const workflowProposal = event.status === 'COMPLETED' && isWorkflowProposal(event.workflowProposal)
          ? event.workflowProposal : undefined
        const proposedTaskId = event.status === 'COMPLETED' && !workflowProposal && isProposedTaskId(event.proposedTaskId)
          ? event.proposedTaskId : undefined
        return { ...turn, status: event.status, message: event.message, afterSeq: event.seq, proposedTaskId, workflowProposal }
      }))
      if (event.kind === 'done') {
        source.onerror = null
        source.close()
        setConnectionLost(false)
      }
    }
    return () => {
      source.onopen = null
      source.onerror = null
      source.onmessage = null
      source.close()
    }
  }, [projectId, streamId])

  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (loading || submission.current || runningTurn || !configured || !draft.trim()) return
    if (new TextEncoder().encode(draft).length > 16_384) {
      setError('这条消息超过 16 KiB，请拆成几条发送。')
      return
    }
    const pdfQuotes: Record<string, PdfQuote> = {}
    for (const { materialId, page, quote, sha256 } of references) {
      if (page !== undefined && quote !== undefined) pdfQuotes[materialId] = { page, text: quote, sha256 }
    }
    const selections = manuscriptSelections.map(manuscriptSelectionInput)
    const input: TurnInput = retry ?? {
      requestId: crypto.randomUUID(), text: draft, mode, modelId: preferredModel?.id,
      referenceIds: references.length ? references.map(({ materialId }) => materialId) : undefined,
      pdfQuotes: Object.keys(pdfQuotes).length ? pdfQuotes : undefined,
      manuscriptSelection: selections.length === 1 ? selections[0] : undefined,
      manuscriptSelections: selections.length > 1 ? selections : undefined,
    }
    setAttempt(input)
    sessionStorage.setItem(attemptKey, JSON.stringify(input))
    const controller = new AbortController()
    submission.current = controller
    setSending(true)
    setError('')
    try {
      const turn = await sendTurn(projectId, input, controller.signal)
      if (controller.signal.aborted) return
      finishAttempt(input)
      setTurns((items) => mergeTurns(items, [turn]))
      setConnectionLost(false)
    } catch (err) {
      if (controller.signal.aborted) return
      if (err instanceof ProjectApiError && err.status === 422 && err.code === 'CONVERSATION_MODEL_UNAVAILABLE') {
        discardAttemptKeepingDraft()
      }
      setError(errorMessage(err))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSending(false)
    }
  }

  async function stop(turnId: string) {
    if (loading || runningTurn?.turnId !== turnId || stopRequests.current.has(turnId)) return
    const controller = new AbortController()
    stopRequests.current.set(turnId, controller)
    setStopState({ turnId, kind: 'pending' })
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      const turn = await stopTurn(projectId, turnId, signal)
      if (controller.signal.aborted) return
      setTurns((items) => mergeTurns(items, [turn]))
    } catch (err) {
      if (!controller.signal.aborted) {
        setStopState((current) => current?.turnId === turnId
          ? { turnId, kind: 'error', message: errorMessage(err) } : current)
      }
    } finally {
      stopRequests.current.delete(turnId)
      if (!controller.signal.aborted) {
        setStopState((current) => current?.turnId === turnId && current.kind === 'pending' ? undefined : current)
      }
    }
  }

  async function earlier() {
    if (!cursor || earlierRequest.current) return
    const controller = new AbortController()
    earlierRequest.current = controller
    setError('')
    setLoadingEarlier(true)
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      const page = await listTurns(projectId, signal, cursor)
      if (controller.signal.aborted) return
      setTurns((items) => mergeTurns([...page.items].reverse(), items))
      setCursor(page.nextCursor)
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err))
    } finally {
      earlierRequest.current = null
      if (!controller.signal.aborted) setLoadingEarlier(false)
    }
  }

  function updateDraft(text: string) {
    sessionStorage.setItem(draftKey, text)
    setDraft(text)
  }

  function updateWorkflowProposal(turnId: string, proposal: WorkflowProposal) {
    setTurns((items) => items.map((turn) => turn.turnId === turnId ? { ...turn, workflowProposal: proposal } : turn))
    if (proposal.status === 'CONFIRMED') onWorkflowConfirmed?.()
  }

  function refresh() {
    setError('')
    setLoading(true)
    setReload((revision) => revision + 1)
  }

  const visibleTurnIds = new Set(turns.map(({ turnId }) => turnId))

  return (
    <section className="conversation" aria-labelledby="conversation-title">
      <h4 id="conversation-title" tabIndex={-1}>项目对话</h4>
      {loading && <p role="status">正在读取会话…</p>}
      {cursor && <button className="button outline" type="button" disabled={loadingEarlier} onClick={() => void earlier()}>
        {loadingEarlier ? '正在读取…' : '更早的对话'}
      </button>}
      <ol className="turns" aria-label="对话记录" aria-live="polite" aria-busy={!!runningTurn}>
        {turns.map((turn) => (
          <li key={turn.turnId} className="turn" id={conversationTurnAnchor(turn.turnId)} tabIndex={-1}>
            <p className="turn-role">你</p>
            <p className="turn-content" dir="auto">{turn.text}</p>
            {!!turn.references?.length && <div className="turn-references">
              <p className="turn-status">随消息发送的资料</p>
              <ul aria-label="随消息发送的资料">
                {turn.references.map(({ materialId, name, sha256, page, quote }) => <li key={materialId}>
                  <a href={materialDownloadPath(projectId, materialId)} download
                    aria-label={`下载原件：${name}`}><bdi>{name}</bdi></a>
                  <code className="turn-status" title={sha256}>{sha256.slice(0, 8)}</code>
                  <PdfReferenceDetails page={page} quote={quote} />
                </li>)}
              </ul>
            </div>}
            <ManuscriptQuotes selections={manuscriptSelectionsOf(turn)} />
            {!!turn.taskResults?.length && <div className="turn-references">
              <p className="turn-status">本轮参考的任务成果</p>
              <ul aria-label="本轮参考的任务成果">
                {turn.taskResults.map(({ taskId, sourceTurnId, instruction }) => (
                  <li key={taskId}>
                    <bdi className="turn-status">{instruction}</bdi>
                    <a href={taskArtifactPath(projectId, taskId)} download>下载 Markdown</a>
                    {visibleTurnIds.has(sourceTurnId) && (
                      <a href={`#${encodeURIComponent(conversationTurnAnchor(sourceTurnId))}`}>回到原消息</a>
                    )}
                  </li>
                ))}
              </ul>
            </div>}
            <p className="turn-role">研究助手</p>
            {turn.model && <p className="turn-status">模型 <bdi>{turn.model}</bdi> · 推理档 <bdi>{turn.reasoningEffort || '默认'}</bdi></p>}
            {turn.answer && <p className="turn-content" dir="auto">{turn.answer}</p>}
            {turn.status === 'RUNNING' && <>
              <p className="turn-status" role="status">{connectionLost ? '连接中断，正在续接…' : '正在回复…'}</p>
              <div className="form-actions">
                <button className="button outline" type="button"
                  disabled={loading || (stopState?.turnId === turn.turnId && stopState.kind === 'pending')}
                  onClick={() => void stop(turn.turnId)}>
                  {stopState?.turnId === turn.turnId && stopState.kind === 'pending' ? '正在停止…' : '停止回复'}
                </button>
              </div>
              {stopState?.turnId === turn.turnId && stopState.kind === 'error' && (
                <p className="form-error" role="alert">{stopState.message}</p>
              )}
            </>}
            {turn.status === 'STOPPED'
              ? <p className="turn-status" role="status">{turn.message || '已停止回复。已生成的内容已保留。'}</p>
              : turn.message && <p className="turn-status">{turn.message}</p>}
            {turn.status === 'COMPLETED' && turn.workflowProposal && (
              <ConversationWorkflowProposal projectId={projectId} turnId={turn.turnId} proposal={turn.workflowProposal}
                onChanged={(proposal) => updateWorkflowProposal(turn.turnId, proposal)} />
            )}
            {turn.status === 'COMPLETED' && <ConversationTasks key={turn.proposedTaskId ?? turn.turnId}
              projectId={projectId} turnId={turn.turnId} text={turn.text}
              proposedTaskId={turn.proposedTaskId} sync={taskSync} />}
          </li>
        ))}
      </ol>
      {error && <p className="form-error" role="alert">{error}</p>}
      {error && <button className="button outline" type="button" disabled={loading || sending} onClick={refresh}>刷新会话</button>}
      {!loading && !configured && !error && <p className="turn-status">配置对话模型后即可提问。</p>}
      {references.length > 0 && <div className="turn-references draft-references" aria-labelledby="draft-references-title">
        <p id="draft-references-title">已选资料</p>
        <p className="turn-status">发送消息时，模型会收到已选资料的文字节选。PDF 只发送所选文字和页码。</p>
        <ul aria-labelledby="draft-references-title">
          {references.map(({ materialId, name, sha256, page, quote }) => <li key={materialId}>
            <bdi>{name}</bdi>
            <span className="turn-status">原文件 SHA-256 <code title={sha256}>{sha256.slice(0, 8)}</code></span>
            <button className="button outline" type="button" aria-label={`移除资料：${name}`}
              onClick={() => onClearReferences((reference) => reference.materialId === materialId)}>移除</button>
            <PdfReferenceDetails page={page} quote={quote} />
          </li>)}
        </ul>
      </div>}
      <ManuscriptQuotes selections={manuscriptSelections} onRemove={(selection) => onClearManuscriptSelections([selection])} />
      {configured && (
        <form className="create-form" onSubmit={send} aria-busy={sending}>
          {modelOptions.length > 1 && <div className="field">
            <label htmlFor="turn-model">对话模型</label>
            <select id="turn-model" value={selectedModelId ?? ''} disabled={loading || sending}
              onChange={(event) => changeModel(event.currentTarget.value)}>
              {modelOptions.map(({ id, model, reasoningEffort }) => (
                <option key={id} value={id}>{model} · 推理档 {reasoningEffort || '默认'}</option>
              ))}
            </select>
          </div>}
          <div className="field">
            <label htmlFor="turn-mode">本轮协作</label>
            <select id="turn-mode" value={mode} disabled={loading || sending} aria-describedby="turn-mode-help" onChange={(event) => {
              const nextMode = event.currentTarget.value
              if (isTurnMode(nextMode)) setModeOverride(nextMode === inheritedMode ? undefined : nextMode)
            }}>
              {Object.entries(turnModeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
            </select>
            <p className="turn-status" id="turn-mode-help">{turnModeHelp[mode]}</p>
          </div>
          <div className="field">
            <label htmlFor="turn-message">消息</label>
            <textarea id="turn-message" rows={3} maxLength={16_384} value={draft} readOnly={sending}
              aria-describedby="turn-task-results-hint" onChange={(event) => updateDraft(event.currentTarget.value)} />
            <p className="turn-status" id="turn-task-results-hint">
              发送消息时，本项目已完成且尚未同步的关联任务成果节选（最多 4 项）会随本轮发送给研究模型。
              {mode !== 'CHAT' && ' 建议任务时，模型会收到最近保存的最多 20 份 Markdown 稿件的路径、版本号和摘要。'}
            </p>
          </div>
          <div className="form-actions">
            <button className="button primary" type="submit" disabled={loading || sending || !!runningTurn || !draft.trim()}>发送</button>
          </div>
        </form>
      )}
    </section>
  )
}
