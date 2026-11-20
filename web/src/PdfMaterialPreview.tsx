import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { AnnotationMode, getDocument, GlobalWorkerOptions, InvalidPDFException, PasswordException, TextLayer } from 'pdfjs-dist'
import type { PageViewport, PDFDocumentLoadingTask, PDFDocumentProxy, PDFPageProxy, RenderTask } from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'
import './pdf-preview.css'

GlobalWorkerOptions.workerSrc = workerUrl

const canvasPixelLimit = 8_000_000
const canvasSideLimit = 4_096
const pageSideLimit = 8_192

type ReadState<T> = { value: T; error?: never } | { value?: never; error: string }

function PdfTextLayer({ page, viewport }: { page: PDFPageProxy; viewport: PageViewport }) {
  const ref = useRef<HTMLDivElement>(null)
  const [message, setMessage] = useState('')

  useEffect(() => {
    const container = ref.current
    if (!container) return
    let active = true
    let layer: TextLayer | undefined
    async function renderText() {
      try {
        layer = new TextLayer({ textContentSource: page.streamTextContent(), container: container!, viewport })
        await layer.render()
        if (active && !layer.textDivs.some((span) => span.textContent?.trim())) setMessage('此页没有可选择的文字。')
      } catch {
        if (active) setMessage('无法读取此页文字。')
      }
    }
    container.style.setProperty('--total-scale-factor', String(viewport.scale * viewport.userUnit))
    void renderText()
    return () => {
      active = false
      layer?.cancel()
      container.replaceChildren()
    }
  }, [page, viewport])

  return <>
    <div ref={ref} className="pdf-text-layer" data-page-number={page.pageNumber} role="group"
      aria-label={`第 ${page.pageNumber} 页文字`} hidden={!!message} />
    {message && <p className="material-note pdf-text-note" role="status">{message}</p>}
  </>
}

function PdfPageSurface({ page, scale }: { page: PDFPageProxy; scale: number }) {
  const viewport = useMemo(() => page.getViewport({ scale }), [page, scale])
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    let active = true
    let task: RenderTask | undefined
    const { width, height } = viewport
    const pixelRatio = Math.min(window.devicePixelRatio || 1, 2,
      Math.sqrt(canvasPixelLimit / (width * height)), canvasSideLimit / Math.max(width, height))
    canvas.width = Math.max(1, Math.floor(width * pixelRatio))
    canvas.height = Math.max(1, Math.floor(height * pixelRatio))
    async function draw(target: HTMLCanvasElement) {
      try {
        task = page.render({ canvas: target, viewport, annotationMode: AnnotationMode.DISABLE,
          transform: [target.width / width, 0, 0, target.height / height, 0, 0] })
        await task.promise
        if (active) setStatus('ready')
      } catch {
        if (active) setStatus('error')
      }
    }
    void draw(canvas)
    return () => {
      active = false
      task?.cancel()
      canvas.width = canvas.height = 0
    }
  }, [page, viewport])

  if (status === 'error') return <p className="form-error pdf-text-note" role="alert">无法绘制此页，可下载原件查看。</p>
  return <>
    {status === 'loading' && <p className="material-note pdf-text-note" role="status">正在绘制页面…</p>}
    <div className="pdf-page" style={{ width: viewport.width, height: viewport.height }}>
      <canvas ref={canvasRef} role="img" aria-label={`PDF 第 ${page.pageNumber} 页`} />
      {status === 'ready' && <PdfTextLayer page={page} viewport={viewport} />}
    </div>
  </>
}

function PdfPage({ pdf, pageNumber, availableWidth, zoom }: {
  pdf: PDFDocumentProxy
  pageNumber: number
  availableWidth: number
  zoom: number
}) {
  const [result, setResult] = useState<ReadState<PDFPageProxy>>()

  useEffect(() => {
    let active = true
    let openedPage: PDFPageProxy | undefined
    void pdf.getPage(pageNumber).then((page) => {
      if (!active) return
      openedPage = page
      setResult({ value: page })
    }).catch(() => {
      if (active) setResult({ error: '无法读取此页，可下载原件查看。' })
    })
    return () => {
      active = false
      // The child effects cancel canvas and text rendering before page cleanup.
      if (openedPage) queueMicrotask(() => openedPage?.cleanup())
    }
  }, [pdf, pageNumber])

  if (result?.error) return <p className="form-error pdf-text-note" role="alert">{result.error}</p>
  if (!result?.value || availableWidth <= 0) return <p className="material-note pdf-text-note" role="status">正在读取页面…</p>
  const page = result.value
  const { width, height } = page.getViewport({ scale: 1 })
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return <p className="form-error pdf-text-note" role="alert">此页尺寸无效，可下载原件查看。</p>
  }
  const fittedScale = Math.min(availableWidth / width, 4 / 3)
  const scale = Math.min(fittedScale * zoom / 100, pageSideLimit / Math.max(width, height))
  return <PdfPageSurface key={scale} page={page} scale={scale} />
}

function PdfDocumentView({ pdf }: { pdf: PDFDocumentProxy }) {
  const viewport = useRef<HTMLDivElement>(null)
  const [pageNumber, setPageNumber] = useState(1)
  const [zoom, setZoom] = useState(100)
  const [width, setWidth] = useState(0)

  useEffect(() => {
    const container = viewport.current
    if (!container) return
    const observer = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)))
    observer.observe(container)
    return () => observer.disconnect()
  }, [])

  function goToPage(next: number) {
    if (!Number.isInteger(next) || next < 1 || next > pdf.numPages || next === pageNumber) return
    setPageNumber(next)
    viewport.current?.scrollTo(0, 0)
  }

  function jump(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    goToPage(Number(new FormData(event.currentTarget).get('page')))
    event.currentTarget.reset()
  }

  return <div className="pdf-preview">
    <div className="pdf-toolbar">
      <div className="pdf-controls">
        <button className="button outline" type="button" disabled={pageNumber <= 1}
          onClick={() => goToPage(pageNumber - 1)}>上一页</button>
        <button className="button outline" type="button" disabled={pageNumber >= pdf.numPages}
          onClick={() => goToPage(pageNumber + 1)}>下一页</button>
        <span className="material-note" role="status">第 {pageNumber} 页，共 {pdf.numPages} 页</span>
      </div>
      {pdf.numPages > 1 && <form className="pdf-controls" onSubmit={jump}>
        <input type="number" name="page" min={1} max={pdf.numPages} required inputMode="numeric"
          aria-label="跳转页码" placeholder="页码" />
        <button className="button outline" type="submit">跳转</button>
      </form>}
      <label className="pdf-controls">缩放
        <select value={zoom} onChange={(event) => setZoom(Number(event.target.value))}>
          <option value={75}>75%</option>
          <option value={100}>适应宽度</option>
          <option value={125}>125%</option>
          <option value={150}>150%</option>
          <option value={200}>200%</option>
        </select>
      </label>
    </div>
    <div className="pdf-viewport" tabIndex={0} role="group" aria-label="PDF 页面滚动区" ref={viewport}>
      <PdfPage key={pageNumber} pdf={pdf} pageNumber={pageNumber} availableWidth={width} zoom={zoom} />
    </div>
  </div>
}

export default function PdfMaterialPreview({ url }: { url: string }) {
  const [result, setResult] = useState<ReadState<PDFDocumentProxy>>()

  useEffect(() => {
    let active = true
    let task: PDFDocumentLoadingTask | undefined
    function dispose() {
      if (!task) return
      void task.destroy().catch(() => {})
      task = undefined
    }
    async function open() {
      try {
        task = getDocument({ url, enableXfa: false, disableStream: true, disableAutoFetch: true,
          cMapUrl: '/pdf-resources/cmaps/', iccUrl: '/pdf-resources/iccs/',
          standardFontDataUrl: '/pdf-resources/standard_fonts/', wasmUrl: '/pdf-resources/wasm/',
          canvasMaxAreaInBytes: 32 * 1024 * 1024 })
        const pdf = await task.promise
        if (active) setResult({ value: pdf })
      } catch (error) {
        dispose()
        if (!active) return
        const message = error instanceof PasswordException ? '此 PDF 需要密码，请下载原件后解锁。'
          : error instanceof InvalidPDFException ? '此文件无法解析为 PDF，可下载原件检查。'
            : '无法打开 PDF，可重新打开预览或下载原件。'
        setResult({ error: message })
      }
    }
    void open()
    return () => {
      active = false
      dispose()
    }
  }, [url])

  if (result?.error) return <p className="form-error" role="alert">{result.error}</p>
  if (!result?.value) return <p className="material-note" role="status">正在打开 PDF…</p>
  return <PdfDocumentView pdf={result.value} />
}
