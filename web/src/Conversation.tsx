import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ConversationTasks } from './ConversationTasks'
import { materialDownloadPath } from './materials'
import { errorMessage } from './projects'
import type { TaskPanelSync } from './TaskPanel'
import { conversationTurnAnchor, taskArtifactPath } from './tasks'
import { isProposedTaskId, listTurns, sendTurn, turnsPath } from './turns'
import type { Turn, TurnEvent, TurnInput, TurnMode, TurnReference } from './turns'

type StreamTarget = { id: string; after: number }

function readAttempt(key: string): TurnInput | null {
  const value = sessionStorage.getItem(key)
  return value ? JSON.parse(value) as TurnInput : null
}

function sameReferenceIds(first: string[] = [], second: string[]) {
  return first.length === second.length && first.every((id, index) => id === second[index])
}

interface ConversationProps {
  projectId: string
  references: TurnReference[]
  onClearReferences: (ids: string[]) => void
  taskSync?: TaskPanelSync
}

export function Conversation({ projectId, references, onClearReferences, taskSync }: ConversationProps) {
  const attemptKey = `research.turn.${projectId}`
  const draftKey = `research.draft.${projectId}`
  const attempt = useRef<TurnInput | null>(null)
  const submission = useRef<AbortController | null>(null)
  const [turns, setTurns] = useState<Turn[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState(false)
  const [draft, setDraft] = useState(() => sessionStorage.getItem(draftKey) ?? readAttempt(attemptKey)?.text ?? '')
  const [mode, setMode] = useState<TurnMode>(() => readAttempt(attemptKey)?.mode === 'CONFIRM' ? 'CONFIRM' : 'CHAT')
  const [loading, setLoading] = useState(true)
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const [connectionLost, setConnectionLost] = useState(false)
  const [stream, setStream] = useState<StreamTarget | null>(null)
  const [reload, setReload] = useState(0)

  function accept(turn: Turn) {
    setTurns((items) => [...items.filter((item) => item.turnId !== turn.turnId), turn])
    setConnectionLost(false)
    setStream(turn.status === 'RUNNING' ? { id: turn.turnId, after: turn.afterSeq } : null)
  }

  const finishAttempt = useCallback((input: TurnInput) => {
    attempt.current = null
    sessionStorage.removeItem(attemptKey)
    if (sessionStorage.getItem(draftKey) === input.text) sessionStorage.removeItem(draftKey)
    setDraft((current) => current === input.text ? '' : current)
    setMode('CHAT')
    if (input.referenceIds?.length) onClearReferences(input.referenceIds)
  }, [attemptKey, draftKey, onClearReferences])

  useEffect(() => {
    const controller = new AbortController()
    attempt.current = readAttempt(attemptKey)
    listTurns(projectId, controller.signal).then((page) => {
      if (controller.signal.aborted) return
      const pending = attempt.current
      if (pending && page.items.some((turn) => turn.requestId === pending.requestId)) finishAttempt(pending)
      const running = page.items.find((turn) => turn.status === 'RUNNING')
      setTurns(page.items.reverse())
      setCursor(page.nextCursor)
      setConfigured(page.modelConfigured)
      setConnectionLost(false)
      setStream(running ? { id: running.turnId, after: running.afterSeq } : null)
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(err))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [attemptKey, finishAttempt, projectId, reload])

  useEffect(() => () => submission.current?.abort(), [])

  useEffect(() => {
    if (!stream) return
    const source = new EventSource(`${turnsPath(projectId)}/${encodeURIComponent(stream.id)}/stream?after=${stream.after}`)
    source.onopen = () => setConnectionLost(false)
    source.onerror = () => {
      setConnectionLost(true)
      if (source.readyState === EventSource.CLOSED) {
        setStream(null)
        setLoading(true)
        setReload((revision) => revision + 1)
      }
    }
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as TurnEvent
      setTurns((items) => items.map((turn) => {
        if (turn.turnId !== stream.id || event.seq <= turn.afterSeq) return turn
        if (event.kind === 'delta') return { ...turn, answer: turn.answer + event.text, afterSeq: event.seq }
        const proposedTaskId = event.status === 'COMPLETED' && isProposedTaskId(event.proposedTaskId)
          ? event.proposedTaskId : undefined
        return { ...turn, status: event.status, message: event.message, afterSeq: event.seq, proposedTaskId }
      }))
      if (event.kind === 'done') {
        source.close()
        setStream(null)
        setConnectionLost(false)
      }
    }
    return () => source.close()
  }, [projectId, stream])

  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (loading || submission.current || stream || !configured || !draft.trim()) return
    if (new TextEncoder().encode(draft).length > 16_384) {
      setError('这条消息超过 16 KiB，请拆成几条发送。')
      return
    }
    const referenceIds = references.map(({ materialId }) => materialId)
    const previous = attempt.current
    const input: TurnInput = previous?.text === draft && (previous.mode ?? 'CHAT') === mode
      && sameReferenceIds(previous.referenceIds, referenceIds)
      ? previous
      : { requestId: crypto.randomUUID(), text: draft, mode, referenceIds: referenceIds.length ? referenceIds : undefined }
    attempt.current = input
    sessionStorage.setItem(attemptKey, JSON.stringify(input))
    const controller = new AbortController()
    submission.current = controller
    setSending(true)
    setError('')
    try {
      const turn = await sendTurn(projectId, input, controller.signal)
      if (controller.signal.aborted) return
      finishAttempt(input)
      accept(turn)
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSending(false)
    }
  }

  async function earlier() {
    setError('')
    setLoadingEarlier(true)
    try {
      const page = await listTurns(projectId, AbortSignal.timeout(10_000), cursor)
      const older = page.items.reverse()
      setTurns((items) => [...older, ...items])
      setCursor(page.nextCursor)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoadingEarlier(false)
    }
  }

  function updateDraft(text: string) {
    sessionStorage.setItem(draftKey, text)
    setDraft(text)
  }

  function refresh() {
    setError('')
    setLoading(true)
    setReload((revision) => revision + 1)
  }

  const visibleTurnIds = new Set(turns.map(({ turnId }) => turnId))

  return (
    <section className="conversation" aria-labelledby="conversation-title">
      <h4 id="conversation-title">项目对话</h4>
      {loading && <p role="status">正在读取会话…</p>}
      {cursor && <button className="button outline" type="button" disabled={loadingEarlier} onClick={() => void earlier()}>
        {loadingEarlier ? '正在读取…' : '更早的对话'}
      </button>}
      <ol className="turns" aria-label="对话记录" aria-live="polite" aria-busy={stream !== null}>
        {turns.map((turn) => (
          <li key={turn.turnId} className="turn" id={conversationTurnAnchor(turn.turnId)} tabIndex={-1}>
            <p className="turn-role">你</p>
            <p className="turn-content" dir="auto">{turn.text}</p>
            {!!turn.references?.length && <div className="turn-references">
              <p className="turn-status">随消息发送的资料</p>
              <ul aria-label="随消息发送的资料">
                {turn.references.map(({ materialId, name, sha256 }) => <li key={materialId}>
                  <a href={materialDownloadPath(projectId, materialId)} download
                    aria-label={`下载原件：${name}`}><bdi>{name}</bdi></a>
                  <code className="turn-status" title={sha256}>{sha256.slice(0, 8)}</code>
                </li>)}
              </ul>
            </div>}
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
            {turn.answer && <p className="turn-content" dir="auto">{turn.answer}</p>}
            {turn.status === 'RUNNING' && <p className="turn-status" role="status">{connectionLost ? '连接中断，正在续接…' : '正在回复…'}</p>}
            {turn.message && <p className="turn-status">{turn.message}</p>}
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
        <p className="turn-status">发送时，已选资料的文字节选会一并发给模型。Word 节选来自正文和表格。</p>
        <ul aria-labelledby="draft-references-title">
          {references.map(({ materialId, name, sha256 }) => <li key={materialId}>
            <bdi>{name}</bdi>
            <span className="turn-status">原文件 SHA-256 <code title={sha256}>{sha256.slice(0, 8)}</code></span>
            <button className="button outline" type="button" aria-label={`移除资料：${name}`}
              onClick={() => onClearReferences([materialId])}>移除</button>
          </li>)}
        </ul>
      </div>}
      {configured && (
        <form className="create-form" onSubmit={send} aria-busy={sending}>
          <div className="field">
            <label htmlFor="turn-mode">本轮协作</label>
            <select id="turn-mode" value={mode} disabled={loading || sending}
              onChange={(event) => setMode(event.currentTarget.value === 'CONFIRM' ? 'CONFIRM' : 'CHAT')}>
              <option value="CHAT">仅问答</option>
              <option value="CONFIRM">建议任务，确认后运行</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="turn-message">消息</label>
            <textarea id="turn-message" rows={3} maxLength={16_384} value={draft} readOnly={sending}
              aria-describedby="turn-task-results-hint" onChange={(event) => updateDraft(event.currentTarget.value)} />
            <p className="turn-status" id="turn-task-results-hint">
              发送消息时，本项目已完成且尚未同步的关联任务成果节选（最多 4 项）会随本轮发送给研究模型。
            </p>
          </div>
          <div className="form-actions">
            <button className="button primary" type="submit" disabled={loading || sending || stream !== null || !draft.trim()}>发送</button>
          </div>
        </form>
      )}
    </section>
  )
}
