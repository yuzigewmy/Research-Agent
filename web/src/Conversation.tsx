import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from './projects'
import { listTurns, sendTurn, turnsPath } from './turns'
import type { Turn, TurnEvent, TurnInput } from './turns'

type StreamTarget = { id: string; after: number }

function readAttempt(key: string): TurnInput | null {
  const value = sessionStorage.getItem(key)
  return value ? JSON.parse(value) as TurnInput : null
}

export function Conversation({ projectId }: { projectId: string }) {
  const attemptKey = `research.turn.${projectId}`
  const draftKey = `research.draft.${projectId}`
  const attempt = useRef<TurnInput | null>(null)
  const submission = useRef<AbortController | null>(null)
  const [turns, setTurns] = useState<Turn[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState(false)
  const [draft, setDraft] = useState(() => sessionStorage.getItem(draftKey) ?? readAttempt(attemptKey)?.text ?? '')
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

  useEffect(() => {
    const controller = new AbortController()
    attempt.current = readAttempt(attemptKey)
    listTurns(projectId, controller.signal).then((page) => {
      if (controller.signal.aborted) return
      const saved = page.items.find((turn) => turn.requestId === attempt.current?.requestId)
      if (saved) {
        sessionStorage.removeItem(attemptKey)
        attempt.current = null
        if (sessionStorage.getItem(draftKey) === saved.text) sessionStorage.removeItem(draftKey)
        setDraft((current) => current === saved.text ? '' : current)
      }
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
  }, [attemptKey, draftKey, projectId, reload])

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
        return { ...turn, status: event.status, message: event.message, afterSeq: event.seq }
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
    const input = attempt.current?.text === draft ? attempt.current : { requestId: crypto.randomUUID(), text: draft }
    attempt.current = input
    sessionStorage.setItem(attemptKey, JSON.stringify(input))
    const controller = new AbortController()
    submission.current = controller
    setSending(true)
    setError('')
    try {
      const turn = await sendTurn(projectId, input, controller.signal)
      if (controller.signal.aborted) return
      sessionStorage.removeItem(attemptKey)
      sessionStorage.removeItem(draftKey)
      attempt.current = null
      setDraft('')
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

  return (
    <section className="conversation" aria-labelledby="conversation-title">
      <h4 id="conversation-title">项目对话</h4>
      {loading && <p role="status">正在读取会话…</p>}
      {cursor && <button className="button outline" type="button" disabled={loadingEarlier} onClick={() => void earlier()}>
        {loadingEarlier ? '正在读取…' : '更早的对话'}
      </button>}
      <ol className="turns" aria-label="对话记录" aria-live="polite" aria-busy={stream !== null}>
        {turns.map((turn) => (
          <li key={turn.turnId} className="turn">
            <p className="turn-role">你</p>
            <p className="turn-content" dir="auto">{turn.text}</p>
            <p className="turn-role">研究助手</p>
            {turn.answer && <p className="turn-content" dir="auto">{turn.answer}</p>}
            {turn.status === 'RUNNING' && <p className="turn-status" role="status">{connectionLost ? '连接中断，正在续接…' : '正在回复…'}</p>}
            {turn.message && <p className="turn-status">{turn.message}</p>}
          </li>
        ))}
      </ol>
      {error && <p className="form-error" role="alert">{error}</p>}
      {error && <button className="button outline" type="button" disabled={loading || sending} onClick={refresh}>刷新会话</button>}
      {!loading && !configured && !error && <p className="turn-status">配置对话模型后即可提问。</p>}
      {configured && (
        <form className="create-form" onSubmit={send} aria-busy={sending}>
          <div className="field">
            <label htmlFor="turn-message">消息</label>
            <textarea id="turn-message" rows={3} maxLength={16_384} value={draft} readOnly={sending}
              onChange={(event) => updateDraft(event.currentTarget.value)} />
          </div>
          <div className="form-actions">
            <button className="button primary" type="submit" disabled={loading || sending || stream !== null || !draft.trim()}>发送</button>
          </div>
        </form>
      )}
    </section>
  )
}
