import { useEffect, useId, useRef, useState } from 'react'
import type { ReviewLink } from './notificationApi'
import { errorMessage, ProjectApiError, request } from './projects'
import { readVersion } from './workspace'
import type { FileRecord } from './workspace'

const statusLabels = {
  QUEUED: '排队中', RUNNING: '评审中', CANCELLING: '正在取消', CANCELLED: '已取消',
  SUCCEEDED: '已完成', FAILED: '评审失败', INTERRUPTED: '已中断',
} as const

type SavedRound = { number: number; path: string; versionId: string }
type Review = {
  reviewId: string
  sourceSha256: string
  status: keyof typeof statusLabels
  message?: string
  rounds?: SavedRound[]
}
type Result = { link: ReviewLink } & (
  { kind: 'ready'; review: Review; source: FileRecord } | { kind: 'error'; message: string }
)

function isSavedRound(value: unknown, index: number): value is SavedRound {
  const round = value as Partial<SavedRound> | null
  return !!round && round.number === index + 1 && typeof round.path === 'string' && round.path.length > 0
    && typeof round.versionId === 'string' && round.versionId.length > 0
}

function isReview(value: unknown, link: ReviewLink): value is Review {
  const review = value as Partial<Review> | null
  return !!review && review.reviewId === link.reviewId
    && typeof review.sourceSha256 === 'string' && /^[a-f\d]{64}$/i.test(review.sourceSha256)
    && typeof review.status === 'string' && Object.hasOwn(statusLabels, review.status)
    && (review.message === undefined || typeof review.message === 'string')
    && (review.rounds === undefined || (Array.isArray(review.rounds) && review.rounds.every(isSavedRound)))
}

export function ReviewNoticeDetail({ projectId, reviewLink }: { projectId: string; reviewLink: ReviewLink }) {
  const headingId = useId()
  const focusTarget = useRef<HTMLElement>(null)
  const [result, setResult] = useState<Result>()
  const [attempt, setAttempt] = useState(0)
  const detail = result?.link === reviewLink ? result : undefined
  const { reviewId, reviewVersionId, reviewKind } = reviewLink
  const automatic = reviewKind === 'automatic'
  const collection = automatic ? 'automatic-reviews' : 'reviews'
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(reviewVersionId)}/${collection}/${encodeURIComponent(reviewId)}`

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void Promise.all([
      readVersion(projectId, reviewVersionId, signal),
      request(route, (value): value is Review => isReview(value, reviewLink), { cache: 'no-store', signal }),
    ]).then(([source, review]) => {
      if (controller.signal.aborted) return
      if (source.versionId !== reviewVersionId || source.sha256 !== review.sourceSha256) {
        throw new ProjectApiError('评审与链接指定的稿件版本不匹配。', 409)
      }
      setResult({ link: reviewLink, kind: 'ready', source, review })
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setResult({ link: reviewLink, kind: 'error', message: errorMessage(cause) })
      controller.abort()
    })
    return () => controller.abort()
  }, [attempt, projectId, reviewLink, reviewVersionId, route])

  useEffect(() => {
    if (detail) focusTarget.current?.focus()
  }, [detail])

  return <section className="workspace-panel" id="project-review" aria-labelledby={headingId}
    data-review-id={reviewId} data-review-version-id={reviewVersionId} data-review-kind={reviewKind}
    ref={focusTarget} tabIndex={-1}>
    <h4 id={headingId}>评审详情</h4>
    {!detail && <p className="workspace-note" role="status">正在读取评审详情…</p>}
    {detail?.kind === 'error' && <div className="request-error">
      <p role="alert">读取评审详情失败。{detail.message}</p>
      <button className="button outline" type="button" onClick={() => {
        setResult(undefined)
        setAttempt((value) => value + 1)
      }}>重试读取评审详情</button>
    </div>}
    {detail?.kind === 'ready' && <>
      <dl className="project-properties">
        <div><dt>原稿路径</dt><dd dir="auto">{detail.source.path}</dd></div>
        <div><dt>原稿版本</dt><dd><code>{detail.source.versionId}</code></dd></div>
        <div><dt>评审类型</dt><dd>{automatic ? '多轮审稿' : '模拟评审'}</dd></div>
        <div><dt>报告状态</dt><dd data-review-status={detail.review.status}>{statusLabels[detail.review.status]}</dd></div>
      </dl>
      {detail.review.message && <p className="workspace-note turn-content" dir="auto">{detail.review.message}</p>}
      {detail.review.status === 'SUCCEEDED' && <div className="workspace-actions">
        <a className="button outline" download href={`${route}/download`}>
          {automatic ? '下载多轮审稿报告' : '下载评审报告'}
        </a>
      </div>}
      {automatic && !!detail.review.rounds?.length && <div className="workspace-history">
        <h5>已保存的审稿轮次</h5>
        <ol className="workspace-history" aria-label="已保存的审稿轮次">
          {detail.review.rounds.map(({ number, path, versionId }) => <li key={number} data-review-round={number}>
            <p className="workspace-note turn-content"><bdi>{path}</bdi> · 版本 <code>{versionId}</code></p>
            <a className="button outline" download href={`${route}/rounds/${number}/manuscript`}>
              下载第 {number} 轮审稿稿件
            </a>
          </li>)}
        </ol>
      </div>}
    </>}
  </section>
}
