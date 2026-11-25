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
type GenerationSpec = { prompt: string; size: ImageSize; sourceGenerationId?: never }
  | { sourceGenerationId: string; prompt?: never; size?: never }
type GenerationInput = GenerationSpec & { requestId: string }
type SubmitError = { kind: 'busy' | 'rejected'; message: string }
  | { kind: 'retry'; message: string; input: GenerationInput }

interface Generation {
  generationId: string
  sourceGenerationId?: string
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
    && (generation.sourceGenerationId === undefined
      || (typeof generation.sourceGenerationId === 'string' && !!generation.sourceGenerationId))
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
  const [submitting, setSubmitting] = useState<GenerationSpec>()
  const [redrawSourceId, setRedrawSourceId] = useState<string>()
  const read = useRef<AbortController | null>(null)
  const submission = useRef<AbortController | null>(null)
  const redrawButton = useRef<HTMLButtonElement>(null)
  const confirmButton = useRef<HTMLButtonElement>(null)
  const refreshButton = useRef<HTMLButtonElement>(null)
  const notified = useRef(new Set<string>())

  const generation = response?.generation
  const material = generation?.material
  const active = !!generation && isTaskActive(generation.status)
  const normalizedPrompt = prompt.trim()
  const promptError = encoder.encode(normalizedPrompt).length > promptLimit ? '图片说明不能超过 8 KiB。'
    : normalizedPrompt.includes('\0') ? '图片说明不能包含空字符。' : ''
  const retry = submitError?.kind === 'retry' ? submitError.input : undefined
  const redrawRetry = retry?.sourceGenerationId !== undefined
  const newImageRetry = !!retry && !redrawRetry
  const submittingNewImage = !!submitting && submitting.sourceGenerationId === undefined
  const confirmingRedraw = generation && redrawSourceId === generation.generationId ? generation : undefined
  const idle = !reading && !submitting
  // The server checks for a saved receipt before checking capacity or configuration.
  const canRetry = idle && !!retry
  const canStartNew = idle && !!response?.configured && !active && !retry && submitError?.kind !== 'busy'
  const canSubmit = canStartNew && !confirmingRedraw && !!normalizedPrompt && !promptError
  const canRedraw = canStartNew && !!generation
  const formLocked = submittingNewImage || newImageRetry

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
    setRedrawSourceId(undefined)
    setReading(true)
    setReadError('')
    setLookup({ generationId })
  }

  async function submit(sourceGenerationId?: string) {
    if (submission.current) return
    if (retry) {
      if (!canRetry || sourceGenerationId !== retry.sourceGenerationId) return
    } else if (sourceGenerationId !== undefined) {
      if (!canRedraw || confirmingRedraw?.generationId !== sourceGenerationId) return
    } else if (!canSubmit) return

    const spec: GenerationSpec = sourceGenerationId === undefined ? { prompt: normalizedPrompt, size } : { sourceGenerationId }
    if (confirmButton.current && document.activeElement === confirmButton.current) refreshButton.current?.focus()
    read.current?.abort()
    setLookup(null)
    setRedrawSourceId(undefined)
    setReadError('')
    setSubmitError(undefined)
    const controller = new AbortController()
    submission.current = controller
    setSubmitting(spec)
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)])
    let input = retry
    try {
      input ??= { requestId: crypto.randomUUID(), ...spec }
      const value = await request(path, (data): data is GenerationResponse => matchesGeneration(data)
        && data.generation !== null && data.generation.sourceGenerationId === sourceGenerationId, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input), signal,
      })
      signal.throwIfAborted()
      setResponse(value)
      if (value.generation && isTaskActive(value.generation.status)) refresh(value.generation.generationId)
    } catch (error) {
      if (controller.signal.aborted) return
      const apiError = !signal.aborted && error instanceof ProjectApiError ? error : undefined
      if (apiError?.status === 409 && apiError.code === 'IMAGE_GENERATION_BUSY') {
        setSubmitError({ kind: 'busy', message: errorMessage(error) })
      } else if (!input || (apiError && ((apiError.status >= 400 && apiError.status < 500 && apiError.status !== 408)
        || apiError.code === 'IMAGE_GENERATION_UNAVAILABLE'))) {
        setSubmitError({ kind: 'rejected', message: errorMessage(error) })
      } else {
        setSubmitError({ kind: 'retry', input, message: '提交结果尚未确认，可以重试同一请求。' })
      }
      if (active) refresh(generation.generationId)
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setSubmitting(undefined)
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
    {generation?.sourceGenerationId && <p className="material-note">原生成记录 <bdi>{generation.sourceGenerationId}</bdi></p>}
    {generation?.message && <p className="material-note" dir="auto">{generation.message}</p>}
    {generation?.status === 'INTERRUPTED' && <p className="material-note">
      无法确认生图服务是否已完成上一次请求。生成新图片或确认重绘会发起新的任务。
    </p>}
    {response?.configured && <>
      <label htmlFor={`${id}-prompt`}>图片说明</label>
      <textarea className="workspace-text" id={`${id}-prompt`} rows={3} value={prompt}
        readOnly={formLocked} aria-invalid={!!promptError} aria-describedby={`${id}-prompt-hint`}
        onChange={(event) => setPrompt(event.currentTarget.value)} />
      <p className="material-note" id={`${id}-prompt-hint`}>{promptError || '最多 8 KiB（UTF-8）。提交后，说明将发送给已配置的生图模型。'}</p>
      <label htmlFor={`${id}-size`}>图片尺寸</label>
      <select id={`${id}-size`} value={size} disabled={formLocked}
        onChange={(event) => setSize(event.currentTarget.value as ImageSize)}>
        {sizes.map((option) => <option key={option} value={option}>{option.replace('x', ' × ')}</option>)}
      </select>
    </>}
    {submitting?.sourceGenerationId && <p className="material-note" role="status">正在提交重绘…</p>}
    {submitError && <p className="form-error" role="alert">{submitError.message}</p>}
    {readError && <p className="form-error" role="alert">{readError}</p>}
    <div className="material-actions">
      {(response?.configured || newImageRetry || submittingNewImage) && <button className="button primary" type="button"
        aria-disabled={!(canSubmit || (canRetry && newImageRetry))} onClick={() => void submit()}>
        {submittingNewImage ? '正在提交…' : newImageRetry ? '重试同一请求' : '生成图片'}
      </button>}
      {response?.configured && generation && !active && <button className="button outline" type="button" ref={redrawButton}
        aria-disabled={!canRedraw} aria-expanded={!!confirmingRedraw} aria-controls={confirmingRedraw ? `${id}-redraw` : undefined}
        onClick={() => {
          if (canRedraw && !submission.current) setRedrawSourceId(confirmingRedraw ? undefined : generation.generationId)
        }}>按原规格重绘</button>}
      {redrawRetry && <button className="button outline" type="button" ref={confirmButton} aria-disabled={!canRetry}
        onClick={() => void submit(retry.sourceGenerationId)}>重试同一重绘请求</button>}
      <button className="button outline" type="button" ref={refreshButton} aria-disabled={reading || !!submitting}
        onClick={() => { if (!reading && !submission.current) refresh(active ? generation?.generationId : undefined) }}>
        刷新生成记录
      </button>
      {material && <a className="button outline" href={materialDownloadPath(projectId, material.materialId)} download>下载生成图片</a>}
    </div>
    {confirmingRedraw && <div className="field" id={`${id}-redraw`} role="group" aria-label="重绘确认">
      <p className="material-note" id={`${id}-redraw-spec`}>
        沿用原图片尺寸 <bdi>{confirmingRedraw.size}</bdi> 和说明 <bdi>{confirmingRedraw.prompt}</bdi>。
      </p>
      <p className="material-note" id={`${id}-redraw-model`}>
        新任务使用当前生图模型{response?.model && <> <bdi>{response.model}</bdi></>} · 质量 <bdi>{response?.quality ?? 'auto'}</bdi>
      </p>
      <div className="material-actions">
        <button className="button outline" type="button" autoFocus onClick={() => {
          redrawButton.current?.focus()
          setRedrawSourceId(undefined)
        }}>取消</button>
        <button className="button primary" type="button" ref={confirmButton} aria-disabled={!canRedraw}
          aria-describedby={`${id}-redraw-spec ${id}-redraw-model`}
          onClick={() => void submit(confirmingRedraw.generationId)}>确认重绘</button>
      </div>
    </div>}
  </div>
}
