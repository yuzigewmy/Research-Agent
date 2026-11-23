import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import { engineeringPreviewDocumentPath, getEngineeringPreview, listFiles, startEngineeringPreview, stopEngineeringPreview } from './workspace'
import type { EngineeringPreviewRecord, FilePage } from './workspace'
import './workspace.css'

type PreviewAction = { kind: 'start'; versionId: string } | { kind: 'stop'; previewId: string }

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

export function EngineeringPreview({ projectId, archived }: { projectId: string; archived: boolean }) {
  const id = useId()
  const [preview, setPreview] = useState<EngineeringPreviewRecord | null>(null)
  const [previewLoading, setPreviewLoading] = useState(true)
  const [previewReload, setPreviewReload] = useState(0)
  const [previewError, setPreviewError] = useState('')
  const [needsRefresh, setNeedsRefresh] = useState(false)
  const [page, setPage] = useState<FilePage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [selectedPath, setSelectedPath] = useState('')
  const [filesLoading, setFilesLoading] = useState(true)
  const [filesError, setFilesError] = useState('')
  const [pending, setPending] = useState<PreviewAction['kind']>()
  const mutation = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    void getEngineeringPreview(projectId, requestSignal(controller)).then(({ preview: next }) => {
      if (controller.signal.aborted) return
      setPreview(next)
      setNeedsRefresh(false)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setPreviewError(errorMessage(cause))
      setNeedsRefresh(true)
    }).finally(() => {
      if (!controller.signal.aborted) setPreviewLoading(false)
    })
    return () => controller.abort()
  }, [projectId, previewReload])

  useEffect(() => {
    const controller = new AbortController()
    void listFiles(projectId, requestSignal(controller), query.cursor, 'html').then((next) => {
      if (controller.signal.aborted) return
      setPage((current) => {
        if (!query.cursor) return next
        const seen = new Set(current.items.map(({ path }) => path))
        const extra = next.items.filter(({ path }) => !seen.has(path))
        return { ...next, items: [...current.items, ...extra] }
      })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setFilesError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setFilesLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  useEffect(() => () => mutation.current?.abort(), [])

  const selectedFile = page.items.find(({ path }) => path === selectedPath)
    ?? page.items.find(({ path }) => path === preview?.file.path) ?? page.items[0]
  const previewBlocked = previewLoading || needsRefresh || pending !== undefined
  const startBlocked = previewBlocked || archived || filesLoading || !!filesError || !selectedFile
    || selectedFile.versionId === preview?.file.versionId

  function refreshFiles(cursor?: string) {
    setFilesLoading(true)
    setFilesError('')
    setQuery({ cursor })
  }

  function refreshPreview() {
    setPreviewLoading(true)
    setPreviewError('')
    setPreviewReload((value) => value + 1)
  }

  async function changePreview(action: PreviewAction) {
    if (mutation.current || previewBlocked || (action.kind === 'start' && startBlocked)) return
    const controller = new AbortController()
    mutation.current = controller
    setPending(action.kind)
    setPreviewError('')
    try {
      const signal = requestSignal(controller)
      const result = action.kind === 'start'
        ? await startEngineeringPreview(projectId, action.versionId, signal)
        : await stopEngineeringPreview(projectId, action.previewId, signal)
      if (controller.signal.aborted) return
      setPreview(result.preview)
      if (result.preview) setSelectedPath(result.preview.file.path)
    } catch (cause) {
      if (controller.signal.aborted) return
      setPreviewError(errorMessage(cause))
      if (cause instanceof ProjectApiError && cause.code === 'PREVIEW_STATE_CONFLICT') setNeedsRefresh(true)
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  return <section className="workspace-panel" aria-labelledby={`${id}-heading`}>
    <h4 id={`${id}-heading`}>工程预览</h4>
    <p className="workspace-note" id={`${id}-help`}>将脚本和样式内联到单个 HTML 文件中，保存后选择 .html 或 .htm 文件预览。</p>
    <div className="field">
      <label htmlFor={`${id}-file`}>HTML 文件</label>
      <select id={`${id}-file`} value={selectedFile?.path ?? ''} aria-describedby={`${id}-help`}
        disabled={archived || filesLoading || pending !== undefined || !page.items.length}
        onChange={(event) => setSelectedPath(event.currentTarget.value)}>
        {!page.items.length && <option value="">暂无可选文件</option>}
        {page.items.map((file) => <option key={file.path} value={file.path}>{file.path}</option>)}
      </select>
    </div>
    <div className="workspace-actions">
      <button className="button outline" type="button" disabled={filesLoading || pending !== undefined} onClick={() => refreshFiles()}>
        刷新文件列表
      </button>
      {page.nextCursor && <button className="button outline" type="button" disabled={filesLoading || pending !== undefined}
        onClick={() => refreshFiles(page.nextCursor)}>加载更多文件</button>}
      <button className="button primary" type="button" disabled={startBlocked} onClick={() => {
        if (selectedFile) void changePreview({ kind: 'start', versionId: selectedFile.versionId })
      }}>{pending === 'start' ? '正在启动…' : preview ? '切换预览' : '启动预览'}</button>
      {preview && !archived && <button className="button outline" type="button" disabled={previewBlocked}
        onClick={() => void changePreview({ kind: 'stop', previewId: preview.previewId })}>
        {pending === 'stop' ? '正在停止…' : '停止预览'}
      </button>}
    </div>
    {filesLoading && <p className="workspace-note" role="status">正在读取文件列表…</p>}
    {filesError && <p className="form-error" role="alert">{filesError}</p>}
    {previewError && <p className="form-error" role="alert">{previewError}</p>}
    {(needsRefresh || !!previewError) && <button className="button outline" type="button" disabled={previewLoading || pending !== undefined}
      onClick={refreshPreview}>刷新预览状态</button>}
    {previewLoading && <p className="workspace-note" role="status">正在读取预览状态…</p>}
    {archived && <p className="workspace-note">恢复项目后可启动预览。</p>}
    {!archived && preview && <>
      <p className="workspace-note" role="status">
        固定版本 <bdi>{preview.file.path}</bdi> · <bdi>{preview.file.versionId}</bdi> · SHA-256 <code title={preview.file.sha256}>{preview.file.sha256.slice(0, 12)}</code>
      </p>
      <iframe key={preview.previewId} title="工程预览" src={engineeringPreviewDocumentPath(projectId, preview.previewId)}
        sandbox="allow-scripts" referrerPolicy="no-referrer" className="engineering-preview-frame" />
    </>}
  </section>
}
