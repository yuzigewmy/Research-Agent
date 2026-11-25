import { useId, useLayoutEffect, useRef, useState } from 'react'
import { isMaterial, materialDownloadPath } from './materials'
import type { Material } from './materials'
import { errorMessage, ProjectApiError, request } from './projects'
import { isTaskActive } from './tasks'

const statuses = {
  QUEUED: '已排队',
  RUNNING: '正在生成图片',
  CANCELLING: '正在停止生成',
  SUCCEEDED: '图片已保存到项目资料',
  FAILED: '图片生成失败',
  INTERRUPTED: '图片生成已中断',
  CANCELLED: '图片生成已取消',
} as const
const sizes = ['1024x1024', '1536x1024', '1024x1536'] as const
const promptLimit = 8 * 1024
const timeout = 10_000
const encoder = new TextEncoder()

type ImageSize = typeof sizes[number]
type GenerationInput = { requestId: string; prompt: string; size: ImageSize }
type SubmitError = { kind: 'busy' | 'retry' | 'rejected'; message: string }

interface Generation {
  generationId: string
  status: keyof typeof statuses
  prompt: string
  size: string
  quality: string
  model: string
  createdAt: string
  message?: string
  material?: Material
}

interface GenerationResponse {
  configured: boolean
  model?: string
  quality?: string
  generation: Generation | null
}

function matchesGeneration(value: unknown, generationId?: string): value is GenerationResponse {
  const body = value as Partial<GenerationResponse> | null
  if (!body || typeof body.configured !== 'boolean'
    || (body.model !== undefined && typeof body.model !== 'string')
    || (body.quality !== undefined && typeof body.quality !== 'string')) return false
  const generation = body.generation
  if (generation === null) return generationId === undefined
  return !!generation && typeof generation.generationId === 'string' && !!generation.generationId
    && (generationId === undefined || generation.generationId === generationId)
    && typeof generation.status === 'string' && Object.hasOwn(statuses, generation.status)
    && typeof generation.prompt === 'string' && typeof generation.size === 'string'
    && typeof generation.quality === 'string' && typeof generation.model === 'string'
    && typeof generation.createdAt === 'string' && Number.isFinite(Date.parse(generation.createdAt))
    && (generation.message === undefined || typeof generation.message === 'string')
    && (generation.status === 'SUCCEEDED' ? isMaterial(generation.material) : generation.material === undefined)
}

export function MaterialImageGeneration({ projectId, onMaterial }: { projectId: string; onMaterial: (material: Material) => void }) {
  const id = useId()
  const path = `/api/v1/projects/${encodeURIComponent(projectId)}/image-generations`
  const [prompt, setPrompt] = useState('')
  const [size, setSize] = useState<ImageSize>(sizes[0])
  const [response, setResponse] = useState<GenerationResponse>()
  const [lookup, setLookup] = useState<{ generationId?: string } | null>({})
  const [reading, setReading] = useState(true)
  const [readError, setReadError] = useState('')
  const [submitError, setSubmitError] = useState<SubmitError>()
  const [submitting, setSubmitting] = useState(false)
  const read = useRef<AbortController | null>(null)
  const submission = useRef<AbortController | null>(null)
  const attempt = useRef<GenerationInput | undefined>(undefined)
  const notified = useRef(new Set<string>())

  const generation = response?.generation
  const material = generation?.material
  const active = !!generation && isTaskActive(generation.status)
  const normalizedPrompt = prompt.trim()
  const promptError = encoder.encode(normalizedPrompt).length > promptLimit ? '图片说明不能超过 8 KiB。'
    : normalizedPrompt.includes('\0') ? '图片说明不能包含空字符。' : ''
  const retry = submitError?.kind === 'retry'
  const canSubmit = response?.configured && !reading && !submitting && !active && submitError?.kind !== 'busy'
    && (retry || (!!normalizedPrompt && !promptError))

  useLayoutEffect(() => {
    if (!lookup) return
    const controller = new AbortController()
    read.current = controller
    let timer: number | undefined
    let generationId = lookup.generationId

    async function poll() {
      if (controller.signal.aborted) return
      const url = generationId ? `${path}?${new URLSearchParams({ generationId })}` : path
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)])
      try {
        const value = await request(url, (data): data is GenerationResponse => matchesGeneration(data, generationId), {
          signal, cache: 'no-store',
        })
        signal.throwIfAborted()
        setResponse(value)
        setReadError('')
        setSubmitError((current) => current?.kind === 'busy' ? undefined : current)
        if (value.generation && isTaskActive(value.generation.status)) {
          generationId = value.generation.generationId
          timer = window.setTimeout(poll, 1_500)
        }
      } catch (error) {
        if (!controller.signal.aborted) setReadError(signal.aborted ? '查询超时，请刷新生成记录。' : errorMessage(error))
      } finally {
        if (!controller.signal.aborted) setReading(false)
      }
    }

    void poll()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [path, lookup])

  useLayoutEffect(() => () => submission.current?.abort(), [])
  useLayoutEffect(() => {
    if (!material || notified.current.has(material.materialId)) return
    notified.current.add(material.materialId)
    onMaterial(material)
  }, [material, onMaterial])

  function refresh(generationId?: string) {
    read.current?.abort()
    setReading(true)
    setReadError('')
    setLookup({ generationId })
  }

  async function submit() {
    if (!canSubmit || submission.current) return
    read.current?.abort()
    setLookup(null)
    setReadError('')
    setSubmitError(undefined)
    const controller = new AbortController()
    submission.current = controller
    setSubmitting(true)
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)])
    try {
      attempt.current ??= { requestId: crypto.randomUUID(), prompt: normalizedPrompt, size }
      const value = await request(path, (data): data is GenerationResponse => matchesGeneration(data) && !!data.generation, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(attempt.current), signal,
      })
      signal.throwIfAborted()
      attempt.current = undefined
      setResponse(value)
      if (value.generation && isTaskActive(value.generation.status)) refresh(value.generation.generationId)
    } catch (error) {
      if (controller.signal.aborted) return
      const apiError = !signal.aborted && error instanceof ProjectApiError ? error : undefined
      if (apiError?.status === 409 && apiError.code === 'IMAGE_GENERATION_BUSY') {
        attempt.current = undefined
        setSubmitError({ kind: 'busy', message: errorMessage(error) })
      } else if (apiError && ((apiError.status >= 400 && apiError.status < 500 && apiError.status !== 408)
        || apiError.code === 'IMAGE_GENERATION_UNAVAILABLE')) {
        attempt.current = undefined
        setSubmitError({ kind: 'rejected', message: errorMessage(error) })
      } else {
        setSubmitError({ kind: 'retry', message: '提交结果尚未确认，可以重试同一请求。' })
      }
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSubmitting(false)
    }
  }

  return <div className="field">
    <p className="material-note" role="status">
      {generation ? statuses[generation.status] : reading ? '正在查询生成记录…'
        : response ? '暂无生成记录' : '暂时无法查询生成记录'}
    </p>
    {response?.configured && <p className="material-note">
      {response.model && <>生图模型 <bdi>{response.model}</bdi> · </>}质量 <bdi>{response.quality ?? 'auto'}</bdi>
    </p>}
    {response && !response.configured && <p className="material-note">请先在服务端配置生图模型。</p>}
    {generation && <p className="material-note">
      本次图片：{generation.size} · <bdi>{generation.model}</bdi> · 质量 <bdi>{generation.quality}</bdi>
      {' · '}<bdi>{generation.prompt}</bdi>
    </p>}
    {generation?.message && <p className="material-note" dir="auto">{generation.message}</p>}
    {generation?.status === 'INTERRUPTED' && <p className="material-note">
      无法确认生图服务是否已完成上一次请求。再次点击「生成图片」会发起新的任务。
    </p>}
    {response?.configured && <>
      <label htmlFor={`${id}-prompt`}>图片说明</label>
      <textarea className="workspace-text" id={`${id}-prompt`} rows={3} value={prompt}
        readOnly={submitting || retry} aria-invalid={!!promptError} aria-describedby={`${id}-prompt-hint`}
        onChange={(event) => setPrompt(event.currentTarget.value)} />
      <p className="material-note" id={`${id}-prompt-hint`}>{promptError || '最多 8 KiB（UTF-8）。提交后，说明将发送给已配置的生图模型。'}</p>
      <label htmlFor={`${id}-size`}>图片尺寸</label>
      <select id={`${id}-size`} value={size} disabled={submitting || retry}
        onChange={(event) => setSize(event.currentTarget.value as ImageSize)}>
        {sizes.map((option) => <option key={option} value={option}>{option.replace('x', ' × ')}</option>)}
      </select>
    </>}
    {submitError && <p className="form-error" role="alert">{submitError.message}</p>}
    {readError && <p className="form-error" role="alert">{readError}</p>}
    <div className="material-actions">
      {response?.configured && <button className="button primary" type="button" aria-disabled={!canSubmit} onClick={() => void submit()}>
        {submitting ? '正在提交…' : retry ? '重试同一请求' : '生成图片'}
      </button>}
      <button className="button outline" type="button" aria-disabled={reading || submitting}
        onClick={() => { if (!reading && !submission.current) refresh(active ? generation?.generationId : undefined) }}>
        刷新生成记录
      </button>
      {material && <a className="button outline" href={materialDownloadPath(projectId, material.materialId)} download>下载生成图片</a>}
    </div>
  </div>
}
