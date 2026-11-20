import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { isReviewFinding } from './ReviewFindingEdit'
import type { ReviewFinding } from './ReviewFindingEdit'

const statusLabels = {
  QUEUED: '排队中', RUNNING: '审稿中', SUCCEEDED: '已完成', FAILED: '审稿失败', INTERRUPTED: '已中断',
} as const
const stopLabels = {
  TARGET_MET: '达到目标分数', MAX_ROUNDS: '达到轮次上限', NO_PROGRESS: '没有可继续的修改',
} as const
const reviewDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const encoder = new TextEncoder()
const pollInterval = 2_000

interface ReviewOptions {
  target: string
  maxRounds: number
  targetScore: number
  criteria?: string
}

interface ReviewRound {
  number: number
  score: number
  summary: string
  versionId: string
  sha256: string
  path: string
  findings: ReviewFinding[]
}

interface AutoReview extends ReviewOptions {
  reviewId: string
  status: keyof typeof statusLabels
  revision: number
  sourceSha256: string
  createdAt: string
  message?: string
  stopReason?: keyof typeof stopLabels
  report?: string
  rounds?: ReviewRound[]
}

type ReviewPage = { items: AutoReview[]; nextCursor?: string; modelConfigured: boolean }
type Selection = { id: string }
type Action = 'create' | 'retry'
type Attempt = { key: string; requestId: string }

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function integerInRange(value: unknown, minimum: number, maximum: number): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= minimum && value <= maximum
}

function isRound(value: unknown, index: number): value is ReviewRound {
  const round = value as Partial<ReviewRound> | null
  return !!round && round.number === index + 1 && typeof round.score === 'number' && round.score >= 1 && round.score <= 10
    && typeof round.summary === 'string' && isPathId(round.versionId) && typeof round.path === 'string'
    && typeof round.sha256 === 'string' && /^[a-f\d]{64}$/i.test(round.sha256)
    && Array.isArray(round.findings) && round.findings.every(isReviewFinding)
}

function isReview(value: unknown, sha256: string, reviewId?: string): value is AutoReview {
  const review = value as Partial<AutoReview> | null
  return !!review && isPathId(review.reviewId) && (!reviewId || review.reviewId === reviewId) && review.sourceSha256 === sha256
    && typeof review.target === 'string' && integerInRange(review.maxRounds, 1, 10) && integerInRange(review.targetScore, 5, 9)
    && typeof review.status === 'string' && Object.hasOwn(statusLabels, review.status)
    && integerInRange(review.revision, 1, Number.MAX_SAFE_INTEGER)
    && typeof review.createdAt === 'string' && Number.isFinite(Date.parse(review.createdAt))
    && [review.criteria, review.message, review.report].every((text) => text === undefined || typeof text === 'string')
    && (review.stopReason === undefined || (typeof review.stopReason === 'string' && Object.hasOwn(stopLabels, review.stopReason)))
    && (review.rounds === undefined || (Array.isArray(review.rounds) && review.rounds.length <= review.maxRounds
      && review.rounds.every(isRound)))
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
}

function isActive(review: AutoReview) {
  return review.status === 'QUEUED' || review.status === 'RUNNING'
}

function canRetry(review: AutoReview) {
  return review.status === 'FAILED' || review.status === 'INTERRUPTED'
}

function reviewPath(route: string, reviewId: string) {
  return `${route}/${encodeURIComponent(reviewId)}`
}

async function readReview(route: string, reviewId: string, sourceSha256: string, signal: AbortSignal) {
  const review = await request(reviewPath(route, reviewId), (value): value is AutoReview =>
    isReview(value, sourceSha256, reviewId), { cache: 'no-store', signal })
  return { ...review, rounds: review.rounds ?? [] }
}

function mergeReviews(current: AutoReview[], incoming: AutoReview[]) {
  const byId = new Map(current.map((review) => [review.reviewId, review]))
  for (const review of incoming) {
    const previous = byId.get(review.reviewId)
    if (!previous || previous.revision <= review.revision) {
      // List entries omit round detail and the report, including at the same revision.
      byId.set(review.reviewId, { ...review, rounds: review.rounds ?? previous?.rounds, report: review.report ?? previous?.report })
    }
  }
  return [...byId.values()].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
}

function ReviewForm({ id, dirty, configured, pending, error, onCreate }: {
  id: string
  dirty: boolean
  configured?: boolean
  pending?: Action
  error?: string
  onCreate: (options: ReviewOptions) => Promise<boolean>
}) {
  const [target, setTarget] = useState('')
  const [maxRounds, setMaxRounds] = useState('4')
  const [targetScore, setTargetScore] = useState('6')
  const [criteria, setCriteria] = useState('')
  const invalidTarget = Array.from(target.trim()).length > 200 || /[\p{Cc}\p{Zl}\p{Zp}]/u.test(target.trim())
  const invalidOptions = !integerInRange(Number(maxRounds), 1, 10) || !integerInRange(Number(targetScore), 5, 9)
  const invalidCriteria = encoder.encode(criteria.trim()).length > 4096 || criteria.includes('\0')
  const blocked = dirty || configured !== true || !!pending || !target.trim() || invalidTarget || invalidOptions || invalidCriteria

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (blocked) return
    if (await onCreate({ target: target.trim(), maxRounds: Number(maxRounds), targetScore: Number(targetScore),
      criteria: criteria.trim() || undefined })) setTarget('')
  }

  return <form className="workspace-preview" onSubmit={(event) => void submit(event)} aria-busy={pending === 'create'}>
    <div className="field">
      <label htmlFor={`${id}-target`}>投稿目标</label>
      <input id={`${id}-target`} type="text" value={target} required maxLength={400} readOnly={!!pending}
        aria-invalid={invalidTarget || undefined} onChange={(event) => setTarget(event.currentTarget.value)} />
      {invalidTarget && <p className="form-error" role="alert">请输入一行投稿目标，最多 200 个字符。</p>}
    </div>
    <div className="workspace-actions">
      <div className="field">
        <label htmlFor={`${id}-rounds`}>最多审稿轮次</label>
        <input id={`${id}-rounds`} type="number" min={1} max={10} value={maxRounds} required readOnly={!!pending}
          aria-invalid={!integerInRange(Number(maxRounds), 1, 10) || undefined} onChange={(event) => setMaxRounds(event.currentTarget.value)} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-score`}>目标分数</label>
        <input id={`${id}-score`} type="number" min={5} max={9} value={targetScore} required readOnly={!!pending}
          aria-invalid={!integerInRange(Number(targetScore), 5, 9) || undefined} onChange={(event) => setTargetScore(event.currentTarget.value)} />
      </div>
    </div>
    {invalidOptions && <p className="form-error" role="alert">轮次须为 1 至 10 的整数，目标分数须为 5 至 9 的整数。</p>}
    <div className="field">
      <label htmlFor={`${id}-criteria`}>补充评审标准</label>
      <textarea id={`${id}-criteria`} className="workspace-text" rows={2} dir="auto" value={criteria} readOnly={!!pending}
        aria-invalid={invalidCriteria || undefined} aria-describedby={`${id}-criteria-hint`}
        onChange={(event) => setCriteria(event.currentTarget.value)} />
      <p id={`${id}-criteria-hint`} className={invalidCriteria ? 'form-error' : 'workspace-note'}>
        {invalidCriteria ? '补充评审标准须在 4 KiB 以内且不含空字符。' : '可选，最多 4 KiB（UTF-8）。'}
      </p>
    </div>
    <p className="workspace-note" id={`${id}-authorization`}>
      开始后，模型会读取此版已保存的稿件和评审标准。授权仅限自动修改审稿副本，原稿会保留。
      达到目标分数、轮次上限或没有可继续的修改时结束。
    </p>
    {dirty && <p className="workspace-note">请先保存稿件修改，再提交多轮审稿。</p>}
    {configured === false && <p className="workspace-note">配置模型后可提交多轮审稿。</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="workspace-actions">
      <button className="button outline" type="submit" disabled={blocked} aria-describedby={`${id}-authorization`}>
        {pending === 'create' ? '正在提交…' : '授权并开始多轮审稿'}
      </button>
    </div>
  </form>
}

function ReviewRoundDetails({ round, route }: { round: ReviewRound; route: string }) {
  return <li className="workspace-preview">
    <strong>第 {round.number} 轮 · {round.score} / 10</strong>
    <p className="turn-content" dir="auto">{round.summary}</p>
    <p className="workspace-note turn-content">稿件 <bdi>{round.path}</bdi> · 版本 <code>{round.versionId}</code>
      {' · SHA-256 '}<code title={round.sha256}>{round.sha256.slice(0, 12)}</code>
    </p>
    <div className="workspace-actions">
      <a className="button outline" download href={`${route}/rounds/${round.number}/manuscript`}>下载第 {round.number} 轮审稿稿件</a>
    </div>
    {round.findings.length ? <details>
      <summary>审稿发现（{round.findings.length} 条）</summary>
      <ul className="workspace-history">
        {round.findings.map((finding) => <li key={finding.number} className="workspace-preview">
          <strong>意见 {finding.number} · 第 {finding.line} 行 · {finding.severity === 'major' ? '主要问题' : '次要问题'}</strong>
          <blockquote className="turn-content workspace-note" dir="auto">{finding.quote}</blockquote>
          <p className="turn-content" dir="auto">{finding.finding}</p>
          <p className="turn-content">修改建议 <bdi>{finding.suggestion}</bdi></p>
        </li>)}
      </ul>
    </details> : <p className="workspace-note">本轮未列出具体修改事项。</p>}
  </li>
}

export function AutomaticReview({ projectId, versionId, sourceSha256, dirty }: {
  projectId: string
  versionId: string
  sourceSha256: string
  dirty: boolean
}) {
  const id = useId()
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/automatic-reviews`
  const [reviews, setReviews] = useState<AutoReview[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState<boolean>()
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loadingPage, setLoadingPage] = useState(true)
  const [pageError, setPageError] = useState('')
  const [selection, setSelection] = useState<Selection>()
  const [loadingDetail, setLoadingDetail] = useState(false)
  const [detailFailure, setDetailFailure] = useState<{ reviewId: string; message: string }>()
  const [pollError, setPollError] = useState(false)
  const [pending, setPending] = useState<Action>()
  const [actionError, setActionError] = useState<{ action: Action; message: string }>()
  const mutation = useRef<AbortController | null>(null)
  const attempts = useRef<Partial<Record<Action, Attempt>>>({})
  const selected = reviews.find((review) => review.reviewId === selection?.id)
  const detailError = detailFailure?.reviewId === selected?.reviewId ? detailFailure?.message : undefined
  const detailLoading = loadingDetail || (!!selected && selected.rounds === undefined && !detailError)
  const hasActive = reviews.some(isActive)

  useEffect(() => {
    const controller = new AbortController()
    const suffix = query.cursor ? `?${new URLSearchParams({ cursor: query.cursor })}` : ''
    void request(route + suffix, (value): value is ReviewPage => {
      const page = value as Partial<ReviewPage> | null
      return !!page && typeof page.modelConfigured === 'boolean'
        && (page.nextCursor === undefined || typeof page.nextCursor === 'string')
        && Array.isArray(page.items) && page.items.every((review) => isReview(review, sourceSha256))
    }, { cache: 'no-store', signal: requestSignal(controller) }).then((page) => {
      if (controller.signal.aborted) return
      setReviews((current) => mergeReviews(current, page.items))
      setCursor(page.nextCursor)
      setConfigured(page.modelConfigured)
      if (!query.cursor) {
        const first = page.items.find(isActive) ?? page.items[0]
        // Refresh the selected detail after the list, which only contains summaries.
        setSelection((current) => current ? { id: current.id } : first && { id: first.reviewId })
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setPageError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoadingPage(false)
    })
    return () => controller.abort()
  }, [route, sourceSha256, query])

  useEffect(() => {
    if (!selection) return
    const controller = new AbortController()
    const reviewId = selection.id
    setLoadingDetail(true)
    void readReview(route, reviewId, sourceSha256, requestSignal(controller)).then((review) => {
      if (controller.signal.aborted) return
      setReviews((current) => mergeReviews(current, [review]))
      setDetailFailure(undefined)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setDetailFailure({ reviewId, message: errorMessage(error) })
    }).finally(() => {
      if (!controller.signal.aborted) setLoadingDetail(false)
    })
    return () => controller.abort()
  }, [route, sourceSha256, selection])

  const pollReview = useEffectEvent(async (index: number, signal: AbortSignal) => {
    const active = reviews.filter((review) => isActive(review) && (!detailLoading || review.reviewId !== selection?.id))
    if (!active.length) return
    const { reviewId } = active[index % active.length]
    const review = await readReview(route, reviewId, sourceSha256, signal)
    if (signal.aborted) return
    setReviews((current) => mergeReviews(current, [review]))
    setDetailFailure((current) => current?.reviewId === reviewId ? undefined : current)
  })

  useEffect(() => {
    if (!hasActive) return
    const controller = new AbortController()
    let index = 0
    let timer = window.setTimeout(() => void poll(), pollInterval)
    async function poll() {
      try {
        await pollReview(index++, requestSignal(controller))
        if (!controller.signal.aborted) setPollError(false)
      } catch {
        if (!controller.signal.aborted) setPollError(true)
      } finally {
        if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), pollInterval)
      }
    }
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [route, sourceSha256, hasActive])

  useEffect(() => () => mutation.current?.abort(), [])

  function loadPage(nextCursor?: string) {
    setPageError('')
    setLoadingPage(true)
    setQuery({ cursor: nextCursor })
  }

  function selectReview(review: AutoReview) {
    setDetailFailure(undefined)
    setLoadingDetail(true)
    setSelection({ id: review.reviewId })
  }

  async function submit(input: ReviewOptions | AutoReview) {
    if (dirty || configured !== true || mutation.current) return false
    const retry = 'reviewId' in input ? input : undefined
    if (retry && (!canRetry(retry) || detailLoading || detailError)) return false
    const action: Action = retry ? 'retry' : 'create'
    const path = retry ? `${reviewPath(route, retry.reviewId)}/retry` : route
    const payload = retry ? { expectedRevision: retry.revision } : input
    const key = JSON.stringify([path, payload])
    const previous = attempts.current[action]
    const requestId = previous?.key === key ? previous.requestId
      : Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join('')
    attempts.current[action] = { key, requestId }
    const controller = new AbortController()
    mutation.current = controller
    setPending(action)
    setActionError(undefined)
    try {
      const review = await request(path, (value): value is AutoReview => isReview(value, sourceSha256, retry?.reviewId), {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...payload, requestId }), signal: requestSignal(controller),
      })
      if (controller.signal.aborted) return false
      delete attempts.current[action]
      setReviews((current) => mergeReviews(current, [review]))
      selectReview(review)
      return true
    } catch (error) {
      if (controller.signal.aborted) return false
      if (error instanceof ProjectApiError && error.status === 409) {
        delete attempts.current[action]
        if (retry) selectReview(retry)
      }
      setActionError({ action, message: errorMessage(error) })
      if (error instanceof ProjectApiError && error.code === 'MODEL_UNAVAILABLE') setConfigured(false)
      return false
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  return <section className="workspace-history" aria-labelledby={`${id}-heading`}>
    <strong id={`${id}-heading`}>多轮审稿</strong>
    <ReviewForm id={id} dirty={dirty} configured={configured} pending={pending}
      error={actionError?.action === 'create' ? actionError.message : undefined} onCreate={submit} />
    {reviews.length > 0 && <ul className="workspace-list" role="list" aria-label="此版本的多轮审稿记录">
      {reviews.map((review) => <li key={review.reviewId}>
        <button className="workspace-row" type="button" aria-pressed={selection?.id === review.reviewId} aria-controls={`${id}-detail`}
          disabled={!!pending} onClick={() => {
            if (actionError?.action === 'retry') setActionError(undefined)
            selectReview(review)
          }}>
          <bdi>{review.target}</bdi>
          <span className="workspace-note">{statusLabels[review.status]} · <time dateTime={review.createdAt}>
            {reviewDate.format(new Date(review.createdAt))}
          </time></span>
        </button>
      </li>)}
    </ul>}
    {loadingPage && <p className="workspace-note" role="status">正在读取多轮审稿记录…</p>}
    {pageError && <p className="form-error" role="alert">{pageError}</p>}
    {hasActive && pollError && <p className="workspace-note" role="status">暂时无法更新审稿状态，稍后自动重试。</p>}
    {!loadingPage && !pageError && !reviews.length && <p className="workspace-note">此版本暂无多轮审稿记录。</p>}
    {!loadingPage && <div className="workspace-actions">
      <button className="button outline" type="button" onClick={() => loadPage()}>刷新审稿记录</button>
      {cursor && <button className="button outline" type="button" onClick={() => loadPage(cursor)}>加载更多审稿记录</button>}
    </div>}
    <div id={`${id}-detail`} className="workspace-preview" hidden={!selected}>
      {selected && <>
        <p className="workspace-note" role="status"><bdi>{selected.target}</bdi> · {statusLabels[selected.status]}
          {selected.rounds && <> · 已审 {selected.rounds.length} / {selected.maxRounds} 轮</>}
          {' · 目标 '}{selected.targetScore} / 10
        </p>
        {selected.criteria && <p className="workspace-note turn-content">补充评审标准 <bdi>{selected.criteria}</bdi></p>}
        {selected.message && <p className="workspace-note" dir="auto">{selected.message}</p>}
        {selected.stopReason && <p className="workspace-note">结束原因 · {stopLabels[selected.stopReason]}</p>}
        {detailLoading && <p className="workspace-note" role="status">正在查询多轮审稿…</p>}
        {detailError && <p className="form-error" role="alert">{detailError}</p>}
        {actionError?.action === 'retry' && <p className="form-error" role="alert">{actionError.message}</p>}
        {canRetry(selected) && <p className="workspace-note">重试会从已保存的进度继续，已完成的轮次会保留。</p>}
        {(detailError || canRetry(selected) || selected.status === 'SUCCEEDED') && <div className="workspace-actions">
          {detailError && <button className="button outline" type="button" disabled={detailLoading || !!pending}
            onClick={() => selectReview(selected)}>重新查询多轮审稿</button>}
          {canRetry(selected) && <button className="button outline" type="button" onClick={() => void submit(selected)}
            disabled={dirty || configured !== true || !!pending || detailLoading || !!detailError}>
            {pending === 'retry' ? '正在重试…' : '重试多轮审稿'}
          </button>}
          {selected.status === 'SUCCEEDED' && <a className="button outline" download
            href={`${reviewPath(route, selected.reviewId)}/download`}>下载多轮审稿报告</a>}
        </div>}
        {!!selected.rounds?.length && <ol className="workspace-history" aria-label="已完成审稿轮次">
          {selected.rounds.map((round) => <ReviewRoundDetails key={round.number} round={round} route={reviewPath(route, selected.reviewId)} />)}
        </ol>}
        {selected.report && <details>
          <summary>查看多轮审稿报告</summary>
          <pre className="workspace-text" dir="auto" tabIndex={0} aria-label="多轮审稿报告 Markdown 原文">{selected.report}</pre>
        </details>}
      </>}
    </div>
  </section>
}
