import { useId, useLayoutEffect, useRef, useState } from 'react'
import { diagramHeight, diagramWidth } from './diagram'
import { ProjectApiError } from './projects'
import { diagramPdfPath, diagramSvgPath } from './workspace'
import type { FileRecord } from './workspace'

type Props = { projectId: string; file: FileRecord; format: 'png' | 'pdf'; historical?: boolean }

async function readPdf(projectId: string, file: FileRecord, signal: AbortSignal) {
  const response = await fetch(diagramPdfPath(projectId, file.versionId), {
    mode: 'same-origin', redirect: 'error', cache: 'no-store',
    headers: { Accept: 'application/pdf' }, signal,
  })
  if (!response.ok) {
    const body = await response.json().catch(() => null) as { error?: { message?: unknown } } | null
    const message = body?.error?.message
    throw new ProjectApiError(typeof message === 'string' ? message : `无法读取流程图 PDF（HTTP ${response.status}）。`, response.status)
  }
  if (response.headers.get('X-Source-Version-ID') !== file.versionId
    || response.headers.get('X-Source-SHA256')?.toLowerCase() !== file.sha256.toLowerCase()) {
    throw new ProjectApiError('流程图的来源摘要与所选版本不符。', 409)
  }
  const pdf = await response.blob()
  if (pdf.type !== 'application/pdf' || await pdf.slice(0, 5).text() !== '%PDF-') {
    throw new ProjectApiError('流程图服务返回的文件不是 PDF。', 422)
  }
  return pdf
}

async function readSvg(projectId: string, file: FileRecord, signal: AbortSignal) {
  const response = await fetch(diagramSvgPath(projectId, file.versionId), {
    mode: 'same-origin', redirect: 'error', cache: 'no-store',
    headers: { Accept: 'image/svg+xml' }, signal,
  })
  if (!response.ok) throw new ProjectApiError(`无法读取流程图 SVG（HTTP ${response.status}）。`, response.status)
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase()
  if (contentType !== 'image/svg+xml') throw new ProjectApiError('流程图服务返回的文件不是 SVG。', 422)
  if (response.headers.get('X-Source-SHA256')?.toLowerCase() !== file.sha256.toLowerCase()) {
    throw new ProjectApiError('流程图的来源摘要与所选版本不符。', 409)
  }
  return response.blob()
}

async function rasterize(svg: Blob, signal: AbortSignal) {
  signal.throwIfAborted()
  const canvas = document.createElement('canvas')
  canvas.width = diagramWidth * 2
  canvas.height = diagramHeight * 2
  const context = canvas.getContext('2d')
  if (!context) throw new ProjectApiError('浏览器无法创建 PNG 画布。', 0)

  const image = new Image()
  const objectUrl = URL.createObjectURL(svg)
  const clearSource = () => image.removeAttribute('src')
  signal.addEventListener('abort', clearSource, { once: true })
  try {
    image.src = objectUrl
    try {
      await image.decode()
    } catch {
      signal.throwIfAborted()
      throw new ProjectApiError('浏览器无法读取 SVG 图像。', 422)
    }
    signal.throwIfAborted()
    context.drawImage(image, 0, 0, canvas.width, canvas.height)
    const pngUrl = canvas.toDataURL('image/png')
    if (!pngUrl.startsWith('data:image/png;')) throw new ProjectApiError('浏览器无法生成 PNG。', 0)
    return pngUrl
  } finally {
    signal.removeEventListener('abort', clearSource)
    clearSource()
    URL.revokeObjectURL(objectUrl)
  }
}

function saveDownload(output: string | Blob, path: string, format: Props['format']) {
  const name = path.split('/').at(-1)?.replace(/\.flow\.json$/, '') || '流程图'
  const url = typeof output === 'string' ? output : URL.createObjectURL(output)
  const link = document.createElement('a')
  link.href = url
  link.download = `${name}.${format}`
  link.hidden = true
  document.body.append(link)
  try {
    link.click()
  } finally {
    link.remove()
    if (typeof output !== 'string') window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
  }
}

export function DiagramDownload(props: Props) {
  const { projectId, file, format } = props
  return <VersionDownload key={JSON.stringify([projectId, file.versionId, file.sha256, file.path, format])} {...props} />
}

function VersionDownload({ projectId, file, format, historical }: Props) {
  const id = useId()
  const currentRequest = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useLayoutEffect(() => () => currentRequest.current?.abort(), [])

  async function download() {
    if (currentRequest.current) return
    const controller = new AbortController()
    currentRequest.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setBusy(true)
    setError('')
    try {
      const output = format === 'pdf' ? await readPdf(projectId, file, signal)
        : await rasterize(await readSvg(projectId, file, signal), signal)
      signal.throwIfAborted()
      saveDownload(output, file.path, format)
    } catch (cause) {
      if (!controller.signal.aborted) {
        const message = cause instanceof ProjectApiError ? cause.message : `无法完成 ${format.toUpperCase()} 下载。`
        setError(signal.aborted ? '流程图导出超时。' : message)
      }
    } finally {
      currentRequest.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  const label = `${historical ? '下载此版本' : '下载'} ${format.toUpperCase()}`
  return <>
    <button className="button outline" type="button" disabled={busy} aria-busy={busy}
      aria-describedby={error ? id : undefined} onClick={() => void download()}>
      {busy ? `正在生成 ${format.toUpperCase()}…` : label}
    </button>
    {error && <p className="form-error" id={id} role="alert">{error}请再次点击「{label}」重试。</p>}
  </>
}
