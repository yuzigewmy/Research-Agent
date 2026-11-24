import { useEffect, useEffectEvent, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { AutomaticReview } from './AutomaticReview'
import { errorMessage, ProjectApiError, request } from './projects'
import { isReviewFinding, ReviewFindings } from './ReviewFindingEdit'
import type { RevisionActions, ReviewFinding } from './ReviewFindingEdit'

const statusLabels = {
  QUEUED: '排队中', RUNNING: '评审中', CANCELLING: '正在取消', CANCELLED: '已取消',
  SUCCEEDED: '已完成', FAILED: '评审失败', INTERRUPTED: '已中断',
} as const
const reviewDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const pollInterval = 2_000
const targetLimit = 200

type Review = {
  reviewId: string
  target: string
  status: keyof typeof statusLabels
  revision: number
  sourceSha256: string
  createdAt: string
  model?: string
  reasoningEffort?: string
  message?: string
  report?: string
  findings?: ReviewFinding[]
}

type ReviewPage = { items: Review[]; nextCursor?: string; modelConfigured: boolean }
type Selection = { id: string; active: boolean }
type ReviewAction = 'create' | 'retry'
type RetryAttempt = { reviewId: string; requestId: string; expectedRevision: number }

type Props = {
  projectId: string
  versionId: string
  sourceSha256: string
  dirty?: boolean
  revisionActions?: RevisionActions
  initialFinding?: { reviewId: string; number: number }
}

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isReview(value: unknown, sourceSha256: string, reviewId?: string): value is Review {
  const review = value as Partial<Review> | null
  return !!review && isPathId(review.reviewId) && (!reviewId || review.reviewId === reviewId)
    && typeof review.target === 'string' && typeof review.status === 'string' && Object.hasOwn(statusLabels, review.status)
    && typeof review.revision === 'number' && Number.isSafeInteger(review.revision) && review.revision >= 1
    && review.sourceSha256 === sourceSha256
    && typeof review.createdAt === 'string' && Number.isFinite(Date.parse(review.createdAt))
    && (review.model === undefined || typeof review.model === 'string')
    && (review.reasoningEffort === undefined || typeof review.reasoningEffort === 'string')
    && (review.message === undefined || typeof review.message === 'string')
    && (review.report === undefined || typeof review.report === 'string')
    && (review.findings === undefined || (Array.isArray(review.findings) && review.findings.every(isReviewFinding)))
}

function isReviewPage(value: unknown, sourceSha256: string): value is ReviewPage {
  const page = value as Partial<ReviewPage> | null
  return !!page && Array.isArray(page.items) && page.items.every((review) => isReview(review, sourceSha256))
    && (page.nextCursor === undefined || typeof page.nextCursor === 'string') && typeof page.modelConfigured === 'boolean'
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
}

function isActive(review: Review) {
  return review.status === 'QUEUED' || review.status === 'RUNNING' || review.status === 'CANCELLING'
}

function canRetry(review: Review) {
  return review.status === 'FAILED' || review.status === 'INTERRUPTED' || review.status === 'CANCELLED'
}

function reviewPath(route: string, reviewId: string) {
  return `${route}/${encodeURIComponent(reviewId)}`
}

function readReview(route: string, reviewId: string, sourceSha256: string, signal: AbortSignal) {
  return request(reviewPath(route, reviewId), (value): value is Review => isReview(value, sourceSha256, reviewId), { signal })
}

function mergeReviews(current: Review[], incoming: Review[]) {
  const byId = new Map(current.map((review) => [review.reviewId, review]))
  for (const review of incoming) {
    const previous = byId.get(review.reviewId)
    // A list may be older than a mutation response or omit the report from a detail response.
    if (!previous || review.revision > previous.revision) {
      byId.set(review.reviewId, review)
    } else if (review.revision === previous.revision) {
      byId.set(review.reviewId, { ...previous, ...review })
    }
  }
  return [...byId.values()].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
}

export function ManuscriptReview({ projectId, versionId, sourceSha256, dirty = false, revisionActions, initialFinding }: Props) {
  if (!isPathId(projectId) || !isPathId(versionId)) return <p className="form-error" role="alert">稿件版本链接无效。</p>
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/reviews`
  const key = JSON.stringify([route, sourceSha256, initialFinding?.reviewId, initialFinding?.number])
  return <>
    <VersionReviews key={key} route={route} projectId={projectId} sourceSha256={sourceSha256}
      dirty={dirty} revisionActions={revisionActions} initialFinding={initialFinding} />
    <AutomaticReview key={JSON.stringify([projectId, versionId, sourceSha256])} projectId={projectId}
      versionId={versionId} sourceSha256={sourceSha256} dirty={dirty} />
  </>
}

function VersionReviews({ route, projectId, sourceSha256, dirty, revisionActions, initialFinding }: Omit<Props, 'versionId' | 'dirty'> & {
  route: string
  dirty: boolean
}) {
  const id = useId()
  const [reviews, setReviews] = useState<Review[]>([])
  const [cursor, setCursor] = useState<string>()
  const [configured, setConfigured] = useState<boolean>()
  const [pageQuery, setPageQuery] = useState<{ cursor?: string }>({})
  const [loadingPage, setLoadingPage] = useState(true)
  const [pageError, setPageError] = useState('')
  const initialReviewId = isPathId(initialFinding?.reviewId) ? initialFinding.reviewId : undefined
  const [selection, setSelection] = useState<Selection | undefined>(() => initialReviewId ? { id: initialReviewId, active: false } : undefined)
  const [loadingDetail, setLoadingDetail] = useState(!!initialReviewId)
  const [detailError, setDetailError] = useState('')
  const [pollError, setPollError] = useState('')
  const [target, setTarget] = useState('')
  const [pending, setPending] = useState<ReviewAction>()
  const [actionError, setActionError] = useState<{ action: ReviewAction; message: string }>()
  const [retryConflict, setRetryConflict] = useState<string>()
  const [applyingFinding, setApplyingFinding] = useState(false)
  const findingActions = revisionActions && {
    ...revisionActions,
    disabled: revisionActions.disabled || dirty || loadingDetail || !!pending || applyingFinding,
    onApplying(applying: boolean) {
      setApplyingFinding(applying)
      revisionActions.onApplying(applying)
    },
  }
  const mutation = useRef<AbortController | null>(null)
  const retryAttempt = useRef<RetryAttempt | undefined>(undefined)
  const selected = reviews.find((review) => review.reviewId === selection?.id)
  const hasOtherActive = reviews.some((review) => review.reviewId !== selection?.id && isActive(review))

  useEffect(() => {
    const controller = new AbortController()
    const query = pageQuery.cursor ? `?${new URLSearchParams({ cursor: pageQuery.cursor })}` : ''
    void request(route + query, (value): value is ReviewPage => isReviewPage(value, sourceSha256), {
      signal: requestSignal(controller),
    }).then((page) => {
      if (controller.signal.aborted) return
      setReviews((current) => mergeReviews(current, page.items))
      setConfigured(page.modelConfigured)
      setCursor(page.nextCursor)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setPageError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoadingPage(false)
    })
    return () => controller.abort()
  }, [route, sourceSha256, pageQuery])

  useEffect(() => {
    if (!selection) return
    const controller = new AbortController()
    const reviewId = selection.id
    let active = selection.active
    let timer: number | undefined

    async function refresh() {
      try {
        const review = await readReview(route, reviewId, sourceSha256, requestSignal(controller))
        if (controller.signal.aborted) return
        setReviews((current) => mergeReviews(current, [review]))
        setDetailError('')
        setRetryConflict((current) => current === reviewId ? undefined : current)
        active = isActive(review)
      } catch (error) {
        if (!controller.signal.aborted) setDetailError(errorMessage(error))
      } finally {
        if (!controller.signal.aborted) {
          setLoadingDetail(false)
          if (active) timer = window.setTimeout(() => void refresh(), pollInterval)
        }
      }
    }

    void refresh()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [route, sourceSha256, selection])

  const refreshOtherReview = useEffectEvent(async (index: number, signal: AbortSignal) => {
    const active = reviews.filter((review) => review.reviewId !== selection?.id && isActive(review))
    if (!active.length) return
    const current = active[index % active.length]
    const updated = await readReview(route, current.reviewId, sourceSha256, signal)
    if (!signal.aborted) setReviews((previous) => mergeReviews(previous, [updated]))
  })

  useEffect(() => {
    if (!hasOtherActive) return
    const controller = new AbortController()
    let index = 0
    let timer = window.setTimeout(() => void poll(), pollInterval)
    async function poll() {
      try {
        await refreshOtherReview(index++, requestSignal(controller))
        if (!controller.signal.aborted) setPollError('')
      } catch {
        if (!controller.signal.aborted) setPollError('暂时无法更新评审状态，稍后自动重试。')
      } finally {
        if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), pollInterval)
      }
    }
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [hasOtherActive])

  useEffect(() => () => mutation.current?.abort(), [])

  function loadPage(nextCursor?: string) {
    setPageError('')
    setLoadingPage(true)
    setPageQuery({ cursor: nextCursor })
  }

  function showReview(reviewId: string, active = false) {
    if (applyingFinding) return
    setActionError((current) => current?.action === 'retry' ? undefined : current)
    setDetailError('')
    setLoadingDetail(true)
    setSelection({ id: reviewId, active })
  }

  async function submitReview(input: { target: string } | { review: Review }) {
    if (mutation.current || !configured || applyingFinding || ('target' in input && dirty)) return
    const retry = 'review' in input ? input.review : undefined
    if (retry && (!canRetry(retry) || loadingDetail || detailError || retryConflict === retry.reviewId)) return
    const action = retry ? 'retry' : 'create'
    const controller = new AbortController()
    mutation.current = controller
    setPending(action)
    setActionError(undefined)
    try {
      let path = route
      let body: { target: string } | { requestId: string; expectedRevision: number }
      if ('review' in input) {
        const { reviewId, revision } = input.review
        const previous = retryAttempt.current
        const attempt = previous?.reviewId === reviewId && previous.expectedRevision === revision ? previous : {
          reviewId, expectedRevision: revision,
          requestId: Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join(''),
        }
        retryAttempt.current = attempt
        path = `${reviewPath(route, reviewId)}/retry`
        body = { requestId: attempt.requestId, expectedRevision: attempt.expectedRevision }
      } else body = input
      const review = await request(path, (value): value is Review => isReview(value, sourceSha256, retry?.reviewId), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal: requestSignal(controller),
      })
      if (controller.signal.aborted) return
      if (retry) retryAttempt.current = undefined
      else setTarget('')
      setReviews((current) => mergeReviews(current, [review]))
      showReview(review.reviewId, isActive(review))
    } catch (error) {
      if (controller.signal.aborted) return
      let message = errorMessage(error)
      if (retry && error instanceof ProjectApiError && error.status === 409) {
        retryAttempt.current = undefined
        if (error.code !== 'REVIEW_MODEL_CONNECTION_CHANGED') {
          setRetryConflict(retry.reviewId)
          message = '评审状态已更新，请重新查询后再试。'
        }
      }
      setActionError({ action, message })
      if (error instanceof ProjectApiError && error.code === 'MODEL_UNAVAILABLE') setConfigured(false)
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const cleaned = target.trim()
    if (!cleaned || Array.from(cleaned).length > targetLimit || /\p{Cc}/u.test(cleaned)) {
      setActionError({ action: 'create', message: '请输入一行投稿目标，最多 200 个字符。' })
      return
    }
    void submitReview({ target: cleaned })
  }

  return <section className="workspace-history" aria-labelledby={`${id}-heading`}>
    <strong id={`${id}-heading`}>投稿模拟评审</strong>
    <form className="field" onSubmit={create}>
      <label htmlFor={`${id}-target`}>投稿目标</label>
      <div className="workspace-create-row">
        <input id={`${id}-target`} type="text" value={target} required maxLength={targetLimit * 2}
          readOnly={!!pending} aria-describedby={`${id}-disclosure`} onChange={(event) => {
            setTarget(event.target.value)
            if (actionError?.action === 'create') setActionError(undefined)
          }} />
        <button className="button outline" type="submit" disabled={dirty || !configured || !!pending || applyingFinding || !target.trim()}>
          {pending === 'create' ? '正在提交…' : '提交模拟评审'}
        </button>
      </div>
      <p className="workspace-note" id={`${id}-disclosure`}>提交或重试会将此版已保存的稿件和投稿目标发送给模型。</p>
      {dirty && <p className="workspace-note">请先保存稿件修改，再提交评审。</p>}
      {configured === false && <p className="workspace-note">配置模型后可提交评审。</p>}
      {actionError?.action === 'create' && <p className="form-error" role="alert">{actionError.message}</p>}
    </form>
    {reviews.length > 0 && <ul className="workspace-list" role="list" aria-label="此版本的评审记录">
      {reviews.map((review) => <li key={review.reviewId}>
        <button className="workspace-row" type="button" aria-pressed={selection?.id === review.reviewId}
          aria-controls={`${id}-detail`} disabled={!!pending || applyingFinding}
          onClick={() => showReview(review.reviewId, isActive(review))}>
          <bdi>{review.target}</bdi>
          <span className="workspace-note">{statusLabels[review.status]} · <time dateTime={review.createdAt}>{reviewDate.format(new Date(review.createdAt))}</time></span>
        </button>
      </li>)}
    </ul>}
    {loadingPage && <p className="workspace-note" role="status">正在加载评审记录…</p>}
    {pageError && <p className="form-error" role="alert">{pageError}</p>}
    {hasOtherActive && pollError && <p className="workspace-note" role="status">{pollError}</p>}
    {!loadingPage && !pageError && !reviews.length && <p className="workspace-note">此版本暂无评审记录。</p>}
    {!loadingPage && (pageError || cursor) && <div className="workspace-actions">
      <button className="button outline" type="button" onClick={() => loadPage(pageError ? pageQuery.cursor : cursor)}>
        {pageError ? '重新加载评审记录' : '加载更多评审'}
      </button>
    </div>}
    <div id={`${id}-detail`} hidden={!selection}>
      {selection && <div className="workspace-preview">
        {selected && <p className="workspace-note" role="status"><bdi>{selected.target}</bdi> · {statusLabels[selected.status]}</p>}
        {selected?.model && <p className="workspace-note">
          审稿模型 <bdi>{selected.model}</bdi> · 推理档 <bdi>{selected.reasoningEffort || '默认'}</bdi>
        </p>}
        {selected?.message && <p className="workspace-note" dir="auto">{selected.message}</p>}
        {loadingDetail && <p className="workspace-note" role="status">正在查询评审…</p>}
        {detailError && <p className="form-error" role="alert">{detailError}</p>}
        {actionError?.action === 'retry' && <p className="form-error" role="alert">{actionError.message}</p>}
        {selected?.report && <pre className="workspace-text" dir="auto" tabIndex={0} aria-label="评审报告 Markdown 原文">{selected.report}</pre>}
        {(detailError || retryConflict === selection.id || (selected && (canRetry(selected) || selected.status === 'SUCCEEDED'))) && (
          <div className="workspace-actions">
            {(detailError || retryConflict === selection.id) && <button className="button outline" type="button"
              disabled={!!pending || applyingFinding || loadingDetail}
              onClick={() => showReview(selection.id, selected ? isActive(selected) : false)}>重新查询评审</button>}
            {selected && canRetry(selected) && <button className="button outline" type="button" onClick={() => void submitReview({ review: selected })}
              disabled={!configured || !!pending || applyingFinding || loadingDetail || !!detailError || retryConflict === selected.reviewId}>
              {pending === 'retry' ? '正在重试…' : '重试评审'}
            </button>}
            {selected?.status === 'SUCCEEDED' && <a className="button outline" download
              href={`${reviewPath(route, selected.reviewId)}/download`}>下载评审报告</a>}
          </div>
        )}
        {selected && !!selected.findings?.length && <ReviewFindings key={selected.reviewId}
          projectId={projectId} reviewId={selected.reviewId} route={reviewPath(route, selected.reviewId)}
          findings={selected.findings} revisionActions={findingActions} applying={applyingFinding} modelConfigured={configured}
          initialNumber={initialFinding?.reviewId === selected.reviewId ? initialFinding.number : undefined} />}
        {selected?.report && selected.findings === undefined && <p className="workspace-note">
          此报告未保存结构化意见。可在编辑器手工选中要修改的文字。
        </p>}
      </div>}
    </div>
  </section>
}
