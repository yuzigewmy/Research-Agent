import { useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { readText, saveText } from './workspace'
import type { Document } from './workspace'

type LatexPreset = 'zh_article' | 'en_conference' | 'zh_thesis' | 'modeling_contest'

const latexPresets: ReadonlyArray<readonly [LatexPreset, string]> = [
  ['zh_article', '中文期刊论文（ctexart）'],
  ['en_conference', '英文会议双栏（article）'],
  ['zh_thesis', '中文学位论文（ctexrep）'],
  ['modeling_contest', '数学建模竞赛（ctexart）'],
]

interface LatexDiagnostic {
  line: number
  sourceLine?: number
  message: string
}

interface LatexExportResponse {
  versionId: string
  sourcePath: string
  sourceSha256: string
  preset: LatexPreset
  texFilename: string
  texContent: string
  bibContent: string
  texSha256: string
  compiled: boolean
  pdfFilename?: string
  pdfSha256?: string
  pdfSize?: number
  pdfBase64?: string
  compileError?: string
  diagnostics?: LatexDiagnostic[]
}

function isLatexDiagnostic(value: unknown): value is LatexDiagnostic {
  const item = value as Partial<LatexDiagnostic> | null
  return !!item && typeof item.line === 'number' && Number.isSafeInteger(item.line)
    && typeof item.message === 'string'
    && (item.sourceLine === undefined || (typeof item.sourceLine === 'number' && Number.isSafeInteger(item.sourceLine)))
}

function isLatexExportResponse(value: unknown): value is LatexExportResponse {
  const resp = value as Partial<LatexExportResponse> | null
  return !!resp && typeof resp.versionId === 'string' && typeof resp.sourceSha256 === 'string'
    && typeof resp.preset === 'string' && typeof resp.texFilename === 'string'
    && typeof resp.texContent === 'string' && typeof resp.bibContent === 'string'
    && typeof resp.texSha256 === 'string' && typeof resp.compiled === 'boolean'
    && (resp.diagnostics === undefined || (Array.isArray(resp.diagnostics) && resp.diagnostics.every(isLatexDiagnostic)))
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const link = window.document.createElement('a')
  link.href = url
  link.download = filename
  window.document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1_000)
}

function base64ToBlob(encoded: string, mimeType: string) {
  const binary = atob(encoded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return new Blob([bytes], { type: mimeType })
}

function workspaceTexPath(sourcePath: string) {
  return sourcePath.replace(/\.(md|markdown)$/i, '') + '.tex'
}

export function LatexExportPanel({ projectId, file, dirty, onSavedTex, onJumpToLine }: {
  projectId: string
  file: Document
  dirty: boolean
  onSavedTex: (path: string) => void
  onJumpToLine: (line: number) => void
}) {
  const id = useId()
  const [preset, setPreset] = useState<LatexPreset>('zh_article')
  const [result, setResult] = useState<LatexExportResponse>()
  const [busy, setBusy] = useState<'export' | 'compile' | 'zip' | 'save'>()
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const active = useRef<AbortController | null>(null)

  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(file.versionId)}/latex`

  async function runExport(compilePdf: boolean) {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(35_000)])
    setBusy(compilePdf ? 'compile' : 'export')
    setError('')
    setNotice('')
    try {
      const next = await request(route, isLatexExportResponse, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ preset, compilePdf }),
        signal,
      })
      if (!controller.signal.aborted) {
        setResult(next)
        if (compilePdf && next.compiled) setNotice(`PDF 编译完成（${next.pdfSize ?? 0} 字节）。`)
        else if (!compilePdf) setNotice('已生成 LaTeX 源码。')
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  async function downloadZip() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(20_000)])
    setBusy('zip')
    setError('')
    setNotice('')
    try {
      const response = await fetch(`${route}/zip`, {
        method: 'POST',
        mode: 'same-origin',
        cache: 'no-store',
        headers: { 'Content-Type': 'application/json', Accept: 'application/zip' },
        body: JSON.stringify({ preset }),
        signal,
      })
      if (!response.ok) {
        const body = await response.json().catch(() => null) as { error?: { message?: string } } | null
        throw new ProjectApiError(body?.error?.message || `无法下载 LaTeX ZIP（HTTP ${response.status}）。`, response.status)
      }
      const blob = await response.blob()
      const zipName = (result?.texFilename ?? 'manuscript.tex').replace(/\.tex$/i, '') + '-latex.zip'
      downloadBlob(blob, zipName)
      setNotice(`已下载 ${zipName}。`)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  async function saveTexToWorkspace() {
    if (!result || active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setBusy('save')
    setError('')
    setNotice('')
    const targetPath = workspaceTexPath(file.path)
    try {
      let baseSha = ''
      try {
        const existing = await readText(projectId, targetPath, signal)
        baseSha = existing.sha256
      } catch (cause) {
        if (!(cause instanceof ProjectApiError && cause.status === 404)) throw cause
      }
      const saved = await saveText(projectId, targetPath, result.texContent, baseSha, signal)
      if (!controller.signal.aborted) {
        onSavedTex(saved.path)
        setNotice(`已保存至工作区 ${saved.path}（SHA-256 ${saved.sha256.slice(0, 12)}）。`)
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  const stale = !!result && result.sourceSha256 !== file.sha256

  return <details className="workspace-subpanel workspace-latex-export">
    <summary>版式与 LaTeX 导出</summary>
    <div className="workspace-subpanel-body">
      <div className="workspace-actions">
        <div className="field">
          <label htmlFor={`${id}-preset`}>版式预设</label>
          <select id={`${id}-preset`} value={preset} disabled={!!busy}
            onChange={(event) => setPreset(event.currentTarget.value as LatexPreset)}>
            {latexPresets.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </div>
        <button className="button outline" type="button" disabled={!!busy}
          onClick={() => void runExport(false)}>
          {busy === 'export' ? '正在生成 LaTeX…' : '生成 LaTeX 源码'}
        </button>
        <button className="button outline" type="button" disabled={!!busy}
          onClick={() => void runExport(true)}>
          {busy === 'compile' ? '正在编译 PDF…' : '尝试编译 PDF'}
        </button>
        <button className="button outline" type="button" disabled={!!busy}
          onClick={() => void downloadZip()}>
          {busy === 'zip' ? '正在打包 ZIP…' : '下载 LaTeX ZIP'}
        </button>
      </div>
      {dirty && <p className="workspace-note">导出基于上次保存的版本（SHA-256 <code>{file.sha256.slice(0, 12)}</code>），保存修改后可导出新版。</p>}
      {stale && <p className="workspace-stale-banner" role="alert">当前预览的 LaTeX 源码对应旧版本，请重新生成以同步最新稿件。</p>}
      {error && <p className="form-error" role="alert">{error}</p>}
      {notice && <p className="workspace-note" role="status">{notice}</p>}
      {result && <div className="workspace-preview">
        <p className="workspace-note">
          文件 <code>{result.texFilename}</code> · 来源 SHA-256 <code>{result.sourceSha256.slice(0, 12)}</code> · 源码 SHA-256 <code>{result.texSha256.slice(0, 12)}</code>
        </p>
        <div className="workspace-actions">
          <button className="button outline" type="button" disabled={!!busy} onClick={() => {
            downloadBlob(new Blob([result.texContent], { type: 'application/x-tex;charset=utf-8' }), result.texFilename)
          }}>下载 .tex</button>
          <button className="button outline" type="button" disabled={!!busy}
            onClick={() => void saveTexToWorkspace()}>
            {busy === 'save' ? '正在保存 .tex…' : '保存 .tex 到工作区'}
          </button>
          {result.compiled && result.pdfBase64 && result.pdfFilename && (
            <button className="button primary" type="button" onClick={() => {
              downloadBlob(base64ToBlob(result.pdfBase64!, 'application/pdf'), result.pdfFilename!)
            }}>下载编译 PDF</button>
          )}
        </div>
        {result.compiled && result.pdfSha256 && (
          <p className="workspace-note">PDF SHA-256 <code>{result.pdfSha256.slice(0, 12)}</code> · 大小 {result.pdfSize} 字节</p>
        )}
        {result.compileError && <p className="form-error" role="alert">{result.compileError}</p>}
        {result.diagnostics && result.diagnostics.length > 0 && (
          <div className="workspace-diagnostics">
            <h6>编译诊断（点击行号定位编辑器）</h6>
            <ul className="workspace-diagnostic-list" role="list">
              {result.diagnostics.map((diag, idx) => {
                const targetLine = diag.sourceLine && diag.sourceLine > 0 ? diag.sourceLine : diag.line
                return <li key={`${diag.line}-${idx}`}>
                  <button className="button outline workspace-line-jump" type="button"
                    onClick={() => onJumpToLine(targetLine)}>
                    第 {targetLine} 行{diag.sourceLine && diag.sourceLine !== diag.line ? `（TeX L${diag.line}）` : ''}
                  </button>
                  <span>{diag.message}</span>
                </li>
              })}
            </ul>
          </div>
        )}
        <pre className="workspace-text" tabIndex={0} aria-label="LaTeX 源码预览">{result.texContent}</pre>
        {result.bibContent.trim() && <details>
          <summary>查看联动文献库 references.bib</summary>
          <pre className="workspace-text" tabIndex={0} aria-label="BibTeX 预览">{result.bibContent}</pre>
        </details>}
      </div>}
    </div>
  </details>
}
