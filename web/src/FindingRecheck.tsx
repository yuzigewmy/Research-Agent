import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, request } from './projects'

const statusLabels = {
  QUEUED: '复查已排队', RUNNING: '正在复查', SUCCEEDED: '复查已完成', FAILED: '复查失败', INTERRUPTED: '复查已中断',
} as const
const verdictLabels = { RESOLVED: '已处理', OPEN: '仍需处理', UNVERIFIABLE: '证据不足' } as const

type Recheck = {
  recheckId: string
  status: keyof typeof statusLabels
  appliedVersionId: string
  sourceSha256: string
  findingNumber: number
  message?: string
  verdict?: keyof typeof verdictLabels
  reason?: string
  line?: number
  quote?: string
}

function isRecheck(value: unknown, versionId: string, findingNumber: number): value is Recheck {
  const result = value as Partial<Recheck> | null
  return !!result && typeof result.recheckId === 'string' && result.appliedVersionId === versionId
    && result.findingNumber === findingNumber && typeof result.sourceSha256 === 'string'
    && typeof result.status === 'string' && Object.hasOwn(statusLabels, result.status)
    && (result.status !== 'SUCCEEDED' || (!!result.verdict && Object.hasOwn(verdictLabels, result.verdict) && typeof result.reason === 'string'))
}

export function FindingRecheck({ editPath, versionId, findingNumber, modelConfigured, currentVersionId }: {
  editPath: string
  versionId: string
  findingNumber: number
  modelConfigured: boolean | undefined
  currentVersionId?: string
}) {
  const id = useId()
  const route = `${editPath}/recheck`
  const [result, setResult] = useState<Recheck | null>()
  const [readError, setReadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [reload, setReload] = useState(0)
  const submission = useRef<AbortController | null>(null)
  const requestId = useRef<string | undefined>(undefined)

  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    async function read() {
      try {
        const next = await request(route, (value): value is Recheck | null =>
          value === null || isRecheck(value, versionId, findingNumber), {
          signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)]),
        })
        if (controller.signal.aborted) return
        setResult(next)
        setReadError('')
        if (modelConfigured && (next?.status === 'QUEUED' || next?.status === 'RUNNING')) {
          timer = window.setTimeout(() => void read(), 2_000)
        }
      } catch (error) {
        if (!controller.signal.aborted) setReadError(errorMessage(error))
      }
    }
    void read()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [route, versionId, findingNumber, modelConfigured, reload])

  useEffect(() => () => submission.current?.abort(), [])

  async function run() {
    if (submission.current) return
    const controller = new AbortController()
    submission.current = controller
    setSubmitting(true)
    setActionError('')
    try {
      requestId.current ??= crypto.randomUUID()
      const next = await request(route, (value): value is Recheck => isRecheck(value, versionId, findingNumber), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ requestId: requestId.current }),
        signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)]),
      })
      if (controller.signal.aborted) return
      requestId.current = undefined
      setResult(next)
      setReload((value) => value + 1)
    } catch (error) {
      if (!controller.signal.aborted) setActionError(errorMessage(error))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSubmitting(false)
    }
  }

  const canSubmit = !!modelConfigured && (result === null || result?.status === 'FAILED' || result?.status === 'INTERRUPTED')
  return <section className="workspace-preview" aria-labelledby={id}>
    <strong id={id}>意见复查</strong>
    {result === undefined && !readError && <p className="workspace-note" role="status">正在读取复查记录…</p>}
    {result === null && <p className="workspace-note">此修订尚无复查记录。</p>}
    {result && <>
      <p className="workspace-note" role="status">{result.verdict ? verdictLabels[result.verdict] : statusLabels[result.status]}</p>
      <p className="workspace-note">复查稿件摘要 <code title={result.sourceSha256}>{result.sourceSha256.slice(0, 12)}</code>
        {currentVersionId && currentVersionId !== versionId && ' · 对应历史版本'}</p>
      {result.reason && <p dir="auto">{result.reason}</p>}
      {result.line && result.quote && <blockquote className="workspace-text" dir="auto">第 {result.line} 行　{result.quote}</blockquote>}
      {result.message && <p className="workspace-note">{result.message}</p>}
    </>}
    {modelConfigured === false && (result === null || result?.status === 'QUEUED' || result?.status === 'FAILED' || result?.status === 'INTERRUPTED') &&
      <p className="workspace-note">配置模型后可运行复查。</p>}
    {readError && <p className="form-error" role="alert">{readError}</p>}
    {actionError && <p className="form-error" role="alert">{actionError}</p>}
    {readError ? <button className="button outline" type="button" onClick={() => {
      setReadError('')
      setReload((value) => value + 1)
    }}>重新读取复查</button> : canSubmit && <>
      <p className="workspace-note">复查会把这条意见和应用版本的局部摘录发送给模型。</p>
      <button className="button outline" type="button" disabled={submitting} onClick={() => void run()}>
        {submitting ? '正在提交…' : result ? '重试复查' : '复查此意见'}
      </button>
    </>}
  </section>
}
