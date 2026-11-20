import { lazy, Suspense, useEffect, useId, useRef, useState } from 'react'
import type { ChangeEvent } from 'react'
import { getMaterialPreview, listMaterials, materialDownloadPath, uploadMaterial } from './materials'
import type { Material, MaterialPreview } from './materials'
import { errorMessage } from './projects'
import { turnReferenceLimit } from './turns'
import './materials.css'

const materialDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const sizeFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 })
const readTimeout = 10_000
const PdfPreview = lazy(() => import('./PdfMaterialPreview').catch(() => ({
  default: () => <p className="form-error" role="alert">无法加载 PDF 查看器，请刷新页面或下载原件。</p>,
})))

type ReadStatus = { kind: 'loading' } | { kind: 'error'; message: string }
type ListStatus = ReadStatus | { kind: 'ready' }
type PreviewStatus = ReadStatus | { kind: 'ready'; preview: MaterialPreview }
type UploadFailure = { name: string; message: string }

function formatSize(size: number) {
  if (size >= 1_048_576) return `${sizeFormat.format(size / 1_048_576)} MiB`
  if (size >= 1_024) return `${sizeFormat.format(size / 1_024)} KiB`
  return `${size} B`
}

function mergeMaterials(current: Material[], incoming: Material[]) {
  const byId = new Map(current.map((material) => [material.materialId, material]))
  for (const material of incoming) byId.set(material.materialId, material)
  return [...byId.values()].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
}

function MaterialPreviewContent({ projectId, material }: { projectId: string; material: Material }) {
  const [status, setStatus] = useState<PreviewStatus>({ kind: 'loading' })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(readTimeout)])
    void getMaterialPreview(projectId, material.materialId, signal).then((preview) => {
      if (!controller.signal.aborted) setStatus({ kind: 'ready', preview })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setStatus({ kind: 'error', message: errorMessage(error) })
    })
    return () => controller.abort()
  }, [projectId, material.materialId, attempt])

  if (status.kind === 'loading') return <p className="material-note" role="status">正在加载预览…</p>
  if (status.kind === 'error') return (
    <div className="request-error">
      <p role="alert">{status.message}</p>
      <button className="button outline" type="button" onClick={() => {
        setStatus({ kind: 'loading' })
        setAttempt((value) => value + 1)
      }}>重试</button>
    </div>
  )
  const { text, truncated } = status.preview
  const isWord = material.format === 'word'
  return (
    <>
      {text ? <pre className="material-text" tabIndex={0} aria-label={`文本预览：${material.name}`} dir="auto">{text}</pre>
        : <p className="material-note" role="status">{isWord ? '未读取到正文或表格文字。' : '文件无文本内容'}</p>}
      {truncated && <p className="material-note" role="status">
        {isWord ? '文字节选已截断，可下载 Word 原件。' : '预览已截断，可下载原件查看全文。'}
      </p>}
    </>
  )
}

interface MaterialPanelProps {
  projectId: string
  onReference?: (material: Material) => void
  referenceIds?: string[]
}

export function MaterialPanel({ projectId, onReference, referenceIds = [] }: MaterialPanelProps) {
  const headingId = useId()
  const [items, setItems] = useState<Material[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [status, setStatus] = useState<ListStatus>({ kind: 'loading' })
  const [pageRequest, setPageRequest] = useState<{ cursor?: string }>({})
  const [previewId, setPreviewId] = useState<string>()
  const [uploading, setUploading] = useState(false)
  const [uploadedCount, setUploadedCount] = useState(0)
  const [failures, setFailures] = useState<UploadFailure[]>([])
  const fileInput = useRef<HTMLInputElement>(null)
  const uploadRequest = useRef<AbortController | null>(null)
  const referenceLimitReached = referenceIds.length >= turnReferenceLimit
  const referenceLimitHint = `${headingId}-reference-limit`

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(readTimeout)])
    void listMaterials(projectId, signal, pageRequest.cursor).then((page) => {
      if (controller.signal.aborted) return
      setItems((current) => mergeMaterials(current, page.items))
      setNextCursor(page.nextCursor)
      setStatus({ kind: 'ready' })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setStatus({ kind: 'error', message: errorMessage(error) })
    })
    return () => controller.abort()
  }, [projectId, pageRequest])

  useEffect(() => () => uploadRequest.current?.abort(), [projectId])

  function loadPage(cursor?: string) {
    if (status.kind === 'loading') return
    setStatus({ kind: 'loading' })
    setPageRequest({ cursor })
  }

  async function uploadFiles(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.currentTarget.files ?? [])
    event.currentTarget.value = ''
    if (!files.length || uploadRequest.current) return

    const controller = new AbortController()
    uploadRequest.current = controller
    setUploading(true)
    setUploadedCount(0)
    setFailures([])
    try {
      for (const file of files) {
        if (controller.signal.aborted) return
        try {
          const material = await uploadMaterial(projectId, file, controller.signal)
          if (controller.signal.aborted) return
          setItems((current) => mergeMaterials(current, [material]))
          setUploadedCount((count) => count + 1)
        } catch (error) {
          if (controller.signal.aborted) return
          setFailures((current) => [...current, { name: file.name, message: errorMessage(error) }])
        }
      }
    } finally {
      uploadRequest.current = null
      if (!controller.signal.aborted) setUploading(false)
    }
  }

  return (
    <section className="material-panel" aria-labelledby={headingId}>
      <div className="material-heading">
        <h4 id={headingId}>项目资料</h4>
        <input ref={fileInput} type="file" multiple hidden onChange={(event) => void uploadFiles(event)} />
        <button className="button primary" type="button" aria-disabled={uploading}
          onClick={() => { if (!uploadRequest.current) fileInput.current?.click() }}>
          {uploading ? '正在上传…' : '上传文件'}
        </button>
      </div>
      {onReference && referenceLimitReached && <p id={referenceLimitHint} className="material-note" role="status">
        已选满 {turnReferenceLimit} 份资料，移除一份后可继续选择。
      </p>}
      {uploadedCount > 0 && !uploading && <p className="material-note" role="status">上传成功 {uploadedCount} 个文件</p>}
      {failures.length > 0 && <div role="alert">
        <ul className="material-failures form-error">
          {failures.map(({ name, message }, index) => <li key={index}><bdi>{name}</bdi> 上传失败：{message}</li>)}
        </ul>
      </div>}
      {items.length > 0 && <ul className="material-list" role="list">
        {items.map((material) => {
          const isOpen = previewId === material.materialId
          const previewRegion = `${headingId}-preview-${material.materialId}`
          const isPdf = material.format === 'pdf'
          const isWord = material.format === 'word'
          const hasText = !isPdf && (material.kind === 'text' || isWord)
          const canPreview = isPdf || hasText
          const previewLabel = isOpen ? '收起预览' : isPdf ? '预览 PDF' : '预览'
          const downloadPath = materialDownloadPath(projectId, material.materialId)
          const wordScopeId = isWord ? `${previewRegion}-scope` : undefined
          const referenced = referenceIds.includes(material.materialId)
          const atLimit = referenceLimitReached && !referenced
          return (
            <li className="material-row" key={material.materialId}>
              <div className="material-info">
                <strong dir="auto">{material.name}</strong>
                <div className="material-meta material-note">
                  <span>{isPdf ? 'PDF' : isWord ? 'Word' : material.kind === 'text' ? '文本' : '文件'}</span>
                  <span>{formatSize(material.size)}</span>
                  <time dateTime={material.createdAt}>{materialDate.format(new Date(material.createdAt))}</time>
                  {canPreview && <span>原文件 SHA-256 <code title={material.sha256}>{material.sha256.slice(0, 8)}</code></span>}
                </div>
                {isWord && <p className="material-note" id={wordScopeId}>正文和表格文字节选（预览最多 64 KiB，UTF-8）</p>}
              </div>
              <div className="material-actions">
                {canPreview && <button className="button outline" type="button"
                  aria-label={`${previewLabel}：${material.name}`} aria-expanded={isOpen} aria-controls={previewRegion}
                  aria-describedby={wordScopeId} onClick={() => setPreviewId(isOpen ? undefined : material.materialId)}>{previewLabel}</button>}
                <a className="button outline" href={downloadPath}
                  aria-label={`下载原件：${material.name}`} download>下载原件</a>
                {hasText && onReference && <button className="button outline" type="button"
                  aria-label={`引用资料：${material.name}`} aria-pressed={referenced} aria-disabled={atLimit}
                  aria-describedby={atLimit ? referenceLimitHint : undefined}
                  onClick={() => { if (!atLimit) onReference(material) }}>引用</button>}
              </div>
              {canPreview && <div className="material-preview" id={previewRegion} hidden={!isOpen}>
                {isOpen && (isPdf ? (
                  <Suspense fallback={<p className="material-note" role="status">正在加载 PDF 查看器…</p>}>
                    <PdfPreview key={downloadPath} url={downloadPath} />
                  </Suspense>
                ) : <MaterialPreviewContent material={material} projectId={projectId} />)}
              </div>}
            </li>
          )
        })}
      </ul>}
      {status.kind === 'loading' && !nextCursor && <p className="material-note" role="status">正在加载资料…</p>}
      {status.kind === 'ready' && items.length === 0 && <p className="material-note" role="status">暂无资料</p>}
      {status.kind === 'error' && (
        <div className="request-error">
          <p role="alert">{status.message}</p>
          <button className="button outline" type="button" onClick={() => loadPage(pageRequest.cursor)}>重试</button>
        </div>
      )}
      {nextCursor && status.kind !== 'error' && <button className="button outline" type="button"
        aria-disabled={status.kind === 'loading'} onClick={() => loadPage(nextCursor)}>
        {status.kind === 'loading' ? '正在加载…' : '加载更多'}
      </button>}
    </section>
  )
}
