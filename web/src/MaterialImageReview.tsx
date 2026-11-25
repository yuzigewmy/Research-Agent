import { useEffect, useRef, useState } from 'react'
import type { Material } from './materials'
import { errorMessage, request } from './projects'
import { isTaskActive, taskArtifactPath } from './tasks'

const reviewStatus = {
  QUEUED: '已排队',
  RUNNING: '正在复查图片',
  CANCELLING: '正在停止复查',
  CANCELLED: '复查已取消',
  INTERRUPTED: '复查已中断',
  FAILED: '复查未完成',
  SUCCEEDED: '复查已完成',
} as const

interface ImageReviewResult {
  verdict: 'PASS' | 'ISSUES'
  observedLabels: string[]
  relations: string[]
  issues: string[]
}

interface ImageReview {
  reviewId: string
  materialId: string
  sourceSha256: string
  status: keyof typeof reviewStatus
  model: string
  reasoningEffort?: string
  message?: string
  result?: ImageReviewResult
}

interface ReviewResponse {
  configured: boolean
  review: ImageReview | null
}

function isReviewResult(value: unknown): value is ImageReviewResult {
  const result = value as Partial<ImageReviewResult> | null
  return !!result && (result.verdict === 'PASS' || result.verdict === 'ISSUES')
    && [result.observedLabels, result.relations, result.issues].every((list) =>
      Array.isArray(list) && list.every((item: unknown) => typeof item === 'string'))
}

function matchesReview(value: unknown, materialId: string, digest: string, expectedId?: string): value is ReviewResponse {
  const body = value as Partial<ReviewResponse> | null
  if (!body || typeof body.configured !== 'boolean') return false
  const review = body.review
  if (review === null) return expectedId === undefined
  return !!review && review.materialId === materialId && review.sourceSha256 === digest
    && typeof review.reviewId === 'string' && !!review.reviewId
    && (expectedId === undefined || review.reviewId === expectedId)
    && typeof review.status === 'string' && Object.hasOwn(reviewStatus, review.status)
    && typeof review.model === 'string'
    && (review.reasoningEffort === undefined || typeof review.reasoningEffort === 'string')
    && (review.message === undefined || typeof review.message === 'string')
    && (review.status === 'SUCCEEDED' ? isReviewResult(review.result) : review.result === undefined)
}

export function MaterialImageReview({ projectId, material }: { projectId: string; material: Material }) {
  const { materialId, sha256 } = material
  const path = `/api/v1/projects/${encodeURIComponent(projectId)}/materials/${encodeURIComponent(materialId)}/image-review`
  const [response, setResponse] = useState<ReviewResponse>()
  const [refresh, setRefresh] = useState<{ reviewId?: string }>({})
  const [readError, setReadError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const read = useRef<AbortController | null>(null)
  const submission = useRef<AbortController | null>(null)
  const requestId = useRef<string | undefined>(undefined)
  const review = response?.review
  const active = !!review && isTaskActive(review.status)

  useEffect(() => {
    const controller = new AbortController()
    read.current = controller
    let timer: number | undefined
    let reviewId = refresh.reviewId
    async function poll() {
      const url = reviewId ? `${path}?${new URLSearchParams({ reviewId })}` : path
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      try {
        const value = await request(url, (data): data is ReviewResponse => matchesReview(data, materialId, sha256, reviewId), {
          signal, cache: 'no-store',
        })
        if (controller.signal.aborted) return
        setResponse(value)
        setReadError('')
        if (value.review && isTaskActive(value.review.status)) {
          reviewId = value.review.reviewId
          timer = window.setTimeout(poll, 1_500)
        }
      } catch (error) {
        if (!controller.signal.aborted) setReadError(signal.aborted ? '查询超时，请刷新复查记录。' : errorMessage(error))
      }
    }
    void poll()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [path, materialId, sha256, refresh])

  useEffect(() => () => submission.current?.abort(), [])

  async function start() {
    if (!response?.configured || active || review?.result || submission.current) return
    read.current?.abort()
    const controller = new AbortController()
    submission.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    requestId.current ??= crypto.randomUUID()
    setReadError('')
    setSubmitError('')
    setSubmitting(true)
    try {
      const value = await request(path, (data): data is ReviewResponse => matchesReview(data, materialId, sha256) && data.review !== null, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ requestId: requestId.current }), signal,
      })
      if (controller.signal.aborted) return
      requestId.current = undefined
      setResponse(value)
      if (value.review && isTaskActive(value.review.status)) setRefresh({ reviewId: value.review.reviewId })
    } catch (error) {
      if (!controller.signal.aborted) setSubmitError(signal.aborted ? '提交响应超时，可以重试同一请求。' : errorMessage(error))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSubmitting(false)
    }
  }

  const result = review?.result
  const sections = result ? [
    { title: '识别到的标签', items: result.observedLabels },
    { title: '观察到的关系', items: result.relations },
    { title: '需核对的问题', items: result.issues },
  ] : []
  return <div className="material-image-review">
    <p className="material-note" role="status">
      {result ? result.verdict === 'PASS' ? '复查通过' : '发现图片问题'
        : review ? reviewStatus[review.status] : response ? '暂无复查记录' : readError ? '暂时无法查询复查记录' : '正在查询复查记录…'}
    </p>
    {review && <p className="material-note">视觉模型 <bdi>{review.model}</bdi>{review.reasoningEffort && <> · 推理档 <bdi>{review.reasoningEffort}</bdi></>}</p>}
    {review?.message && <p className="material-note" dir="auto">{review.message}</p>}
    {sections.filter(({ items }) => items.length > 0).map(({ title, items }) => <div key={title}>
      <strong>{title}</strong>
      <ul>{items.map((item, index) => <li key={index} dir="auto">{item}</li>)}</ul>
    </div>)}
    {response && !response.configured && !result && <p className="material-note">请先在服务端配置视觉模型。</p>}
    {response?.configured && !active && !result && <p className="material-note">开始后会将这份图片原件发送给已配置的视觉模型。</p>}
    {(readError || (submitError && !active && !result)) && <p className="form-error" role="alert">{readError || submitError}</p>}
    <div className="material-actions">
      {response?.configured && !active && !result && <button className="button outline" type="button" disabled={submitting} onClick={() => void start()}>
        {submitting ? '正在提交…' : submitError ? '重试同一请求' : review ? '重试图片复查' : '开始图片复查'}
      </button>}
      {readError && <button className="button outline" type="button" disabled={submitting}
        onClick={() => { setReadError(''); setRefresh({ reviewId: review?.reviewId }) }}>刷新复查记录</button>}
      {result && review && <a className="button outline" href={taskArtifactPath(projectId, review.reviewId)} download>下载复查报告</a>}
    </div>
  </div>
}
