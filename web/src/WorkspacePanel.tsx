import { useCallback, useEffect, useId, useRef, useState } from 'react'
import type { ChangeEvent, ComponentProps, FormEvent } from 'react'
import { ManuscriptReview } from './ManuscriptReview'
import { SelectionEdit } from './SelectionEdit'
import type { SelectionEditRange } from './SelectionEdit'
import { WordExport } from './WordExport'
import { errorMessage, ProjectApiError } from './projects'
import { researchBriefPath, researchDocumentTemplates } from './researchDocuments'
import type { ResearchDocumentTemplate } from './researchDocuments'
import { isManuscriptSelection, manuscriptSelectionByteLimit, manuscriptSelectionLimit, sameManuscriptSelection } from './turns'
import type { ManuscriptSelection } from './turns'
import { listFiles, listVersions, readText, readVersion, restoreVersion, saveText } from './workspace'
import type { Document, FilePage } from './workspace'
import './workspace.css'

const versionDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })
const encoder = new TextEncoder()
const markdownExtension = /\.(md|markdown)$/i
const manuscriptSizeLimit = 64 * 1024
const textZoomOptions = ['85', '100', '115', '130'] as const
const textZoomKey = 'research-agent.workspace.text-zoom'
type TextZoom = typeof textZoomOptions[number]

function isTextZoom(value: string | null): value is TextZoom {
  return textZoomOptions.some((option) => option === value)
}

function savedTextZoom(): TextZoom {
  try {
    const value = localStorage.getItem(textZoomKey)
    if (isTextZoom(value)) return value
  } catch {
    // 本地存储不可用时使用默认字号。
  }
  return '100'
}

async function readManuscript(file: File) {
  let content: string
  try {
    // Keep a UTF-8 BOM as part of the original text and reject invalid bytes.
    content = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(await file.arrayBuffer())
  } catch {
    throw new ProjectApiError('无法读取稿件，请选择 UTF-8 编码的 Markdown 文件。', 400)
  }
  if (!content.trim()) throw new ProjectApiError('稿件为空，请选择有内容的 Markdown 文件。', 400)
  if (content.includes('\0')) throw new ProjectApiError('稿件包含空字符，请检查文件内容。', 400)
  return content
}

// The textarea uses LF even when a saved line ending occupies two code units.
function savedOffset(source: string, offset: number) {
  for (const breakPair of source.matchAll(/\r\n/g)) {
    if (breakPair.index >= offset) break
    offset++
  }
  return offset
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function useFilePage(load: (signal: AbortSignal, cursor?: string) => Promise<FilePage>, key: 'path' | 'versionId') {
  const [page, setPage] = useState<FilePage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    void load(requestSignal(controller), query.cursor).then((next) => {
      if (controller.signal.aborted) return
      setPage((current) => {
        if (!query.cursor) return next
        const combined = new Map(current.items.map((item) => [item[key], item]))
        for (const item of next.items) combined.set(item[key], item)
        return { items: [...combined.values()], nextCursor: next.nextCursor }
      })
      setLoading(false)
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      setError(errorMessage(error))
      setLoading(false)
    })
    return () => controller.abort()
  }, [key, load, query])

  function loadPage(cursor?: string) {
    setError('')
    setLoading(true)
    setQuery({ cursor })
  }

  return { ...page, loading, error, retry: () => loadPage(query.cursor), more: () => loadPage(page.nextCursor) }
}

function PageControls({ page, label }: { page: ReturnType<typeof useFilePage>; label: string }) {
  if (page.loading) return <p className="workspace-note" role="status">正在加载{label}…</p>
  if (page.error) return <>
    <p className="form-error" role="alert">{page.error}</p>
    <button className="button outline" type="button" onClick={page.retry}>重试加载{label}</button>
  </>
  if (page.nextCursor) return <button className="button outline" type="button" onClick={page.more}>加载更多{label}</button>
  return null
}

type RevisionActions = NonNullable<ComponentProps<typeof ManuscriptReview>['revisionActions']>
type InitialFinding = ComponentProps<typeof ManuscriptReview>['initialFinding']

type VersionPreviewProps = {
  projectId: string
  versionId: string
  restoreBlocked: string | undefined
  isCurrent: boolean
  onRestore: (versionId: string) => void
  revisionActions: RevisionActions
  initialFinding: InitialFinding
}

function VersionPreview({ projectId, versionId, restoreBlocked, isCurrent, onRestore, revisionActions, initialFinding }: VersionPreviewProps) {
  const [document, setDocument] = useState<Document>()
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void readVersion(projectId, versionId, requestSignal(controller)).then((document) => {
      if (!controller.signal.aborted) setDocument(document)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(error))
    })
    return () => controller.abort()
  }, [projectId, versionId, attempt])

  if (error) return <>
    <p className="form-error" role="alert">{error}</p>
    <button className="button outline" type="button" onClick={() => {
      setError('')
      setAttempt((value) => value + 1)
    }}>重试查看版本</button>
  </>
  if (!document) return <p className="workspace-note" role="status">正在加载版本内容…</p>

  return <div className="workspace-preview">
    <p className="workspace-note">只读预览 · <time dateTime={document.createdAt}>{versionDate.format(new Date(document.createdAt))}</time></p>
    {document.content
      ? <pre className="workspace-text" tabIndex={0} aria-label="版本内容" dir="auto">{document.content}</pre>
      : <p className="workspace-note">此版本为空文件。</p>}
    {!isCurrent && document.content.trim() && markdownExtension.test(document.path) && <>
      <WordExport key={document.versionId} projectId={projectId} versionId={document.versionId} sourceSha256={document.sha256} />
      <ManuscriptReview projectId={projectId} versionId={document.versionId} sourceSha256={document.sha256}
        revisionActions={revisionActions} initialFinding={initialFinding} />
    </>}
    {!isCurrent && <>
      {restoreBlocked && <p className="workspace-note">{restoreBlocked}</p>}
      <button className="button outline" type="button" disabled={!!restoreBlocked}
        onClick={() => onRestore(versionId)}>恢复此版本</button>
    </>}
  </div>
}

function VersionHistory({ projectId, document, restoreBlocked, onRestore, selected, onSelect, applying, revisionActions, initialFinding }: {
  projectId: string
  document: Document
  restoreBlocked: string | undefined
  onRestore: (versionId: string) => void
  selected?: string
  onSelect: (versionId: string) => void
  applying: boolean
  revisionActions: RevisionActions
  initialFinding: InitialFinding
}) {
  const { path, versionId: currentId } = document
  const load = useCallback((signal: AbortSignal, cursor?: string) => listVersions(projectId, path, signal, cursor), [projectId, path])
  const page = useFilePage(load, 'versionId')

  return <div className="workspace-history">
    {page.items.length > 0 && <ul className="workspace-list" role="list" aria-label="版本历史">
      {page.items.map(({ versionId, createdAt }) => (
        <li key={versionId}>
          <button className="workspace-row" type="button" aria-pressed={selected === versionId}
            disabled={applying} onClick={() => onSelect(versionId)}>
            <time dateTime={createdAt}>{versionDate.format(new Date(createdAt))}</time>
            <span className="workspace-note">{versionId === currentId ? '当前版本' : versionId.slice(-8)}</span>
          </button>
        </li>
      ))}
    </ul>}
    <PageControls page={page} label="版本" />
    {!page.loading && !page.error && !page.items.length && <p className="workspace-note">暂无版本。</p>}
    {selected && <VersionPreview key={selected} projectId={projectId} versionId={selected}
      restoreBlocked={restoreBlocked} isCurrent={selected === currentId} onRestore={onRestore}
      revisionActions={revisionActions} initialFinding={initialFinding} />}
  </div>
}

type Destination =
  | { kind: 'open'; path: string }
  | { kind: 'create'; path: string; content: string }
  | { kind: 'import'; path: string; file: File }
type Operation = Destination['kind'] | 'save' | 'reload' | 'restore'

const destinationLabel = { open: '打开', create: '创建', import: '导入' }
const progress: Record<Operation, string> = {
  open: '正在打开文件…', create: '正在创建文件…', import: '正在导入稿件…',
  save: '正在保存…', reload: '正在重新加载…', restore: '正在恢复版本…',
}

type WorkspaceProps = {
  projectId: string
  publishedPaths: string[]
  manuscriptIntake?: boolean
  manuscriptSelections: readonly ManuscriptSelection[]
  onManuscriptSelection: (selection: ManuscriptSelection) => void
}

export function WorkspacePanel(props: WorkspaceProps) {
  return <ProjectWorkspace key={props.projectId} {...props} />
}

function ProjectWorkspace({ projectId, publishedPaths, manuscriptIntake, manuscriptSelections, onManuscriptSelection }: WorkspaceProps) {
  const id = useId()
  const load = useCallback((signal: AbortSignal, cursor?: string) => listFiles(projectId, signal, cursor), [projectId])
  const page = useFilePage(load, 'path')
  const [createdPaths, setCreatedPaths] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [template, setTemplate] = useState<ResearchDocumentTemplate>()
  const [document, setDocument] = useState<Document>()
  const [draft, setDraft] = useState('')
  const [editorZoom, setEditorZoom] = useState(savedTextZoom)
  const [selection, setSelection] = useState<SelectionEditRange>()
  const [applying, setApplying] = useState(false)
  const [history, setHistory] = useState(false)
  const [historyVersion, setHistoryVersion] = useState<string>()
  const [historyFinding, setHistoryFinding] = useState<InitialFinding>()
  const [destination, setDestination] = useState<Destination>()
  const [busy, setBusy] = useState<Operation>()
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const operation = useRef<AbortController | null>(null)
  const editor = useRef<HTMLTextAreaElement>(null)
  const discardPrompt = useRef<HTMLDivElement>(null)
  const manuscriptInput = useRef<HTMLInputElement>(null)
  const currentPath = document?.path
  const dirty = document !== undefined && draft !== document.content
  const locked = !!busy || applying
  const isMarkdown = document !== undefined && markdownExtension.test(document.path)
  const quoteCandidate = document && isMarkdown && !dirty && selection
    ? { ...selection, path: document.path, versionId: document.versionId, sha256: document.sha256 }
    : undefined
  const selectedQuote = isManuscriptSelection(quoteCandidate) ? quoteCandidate : undefined
  const currentQuote = manuscriptSelections.find(({ path }) => path === currentPath)
  const alreadyQuoted = !!selectedQuote && sameManuscriptSelection(selectedQuote, currentQuote)
  const quoteLimitReached = manuscriptSelections.length >= manuscriptSelectionLimit && !currentQuote
  const brief = document?.path === researchBriefPath ? document : undefined
  const briefCandidate = brief && !dirty && !conflict ? {
    path: brief.path,
    versionId: brief.versionId,
    sha256: brief.sha256,
    text: brief.content,
    startByte: 0,
    endByte: brief.size,
  } : undefined
  const briefQuote = isManuscriptSelection(briefCandidate) ? briefCandidate : undefined
  const alreadyQuotedBrief = !!briefQuote && sameManuscriptSelection(briefQuote, currentQuote)
  let briefHint = currentQuote ? '引用全文会替换此文件在下一条消息中的已有引用。' : '将完整的已保存简报加入下一条消息。'
  if (conflict) briefHint = '请重新加载研究简报后引用。'
  else if (dirty) briefHint = '请保存研究简报后再引用。'
  else if (!briefQuote) briefHint = '研究简报须有正文，全文最多 8 KiB（UTF-8）。'
  const paths = [...new Set([...publishedPaths, ...createdPaths, ...page.items.map(({ path }) => path)])]
  const restoreBlocked = conflict ? '请先重新加载当前文件。'
    : dirty ? '请先保存草稿或放弃修改。'
      : applying ? '正在应用修改建议。' : busy ? progress[busy] : undefined

  useEffect(() => () => operation.current?.abort(), [])
  useEffect(() => { if (currentPath !== undefined) editor.current?.focus() }, [currentPath])
  useEffect(() => { if (destination) discardPrompt.current?.focus() }, [destination])

  function loadDocument(next: Document) {
    setDocument(next)
    setDraft(next.content)
    setSelection(undefined)
    setConflict(false)
  }

  function reviewActionsFor(reviewVersionId: string, baseVersionId: string): RevisionActions {
    return {
      versionId: baseVersionId,
      disabled: locked || dirty || conflict,
      onApplying: setApplying,
      onApplied: (next, reviewId, number) => {
        loadDocument(next)
        setHistoryVersion(reviewVersionId)
        setHistoryFinding({ reviewId, number })
        setHistory(true)
      },
    }
  }

  function selectText(control: HTMLTextAreaElement) {
    if (!document || dirty || control.selectionStart === control.selectionEnd) {
      setSelection(undefined)
      return
    }
    const source = document.content
    const start = savedOffset(source, control.selectionStart)
    const end = savedOffset(source, control.selectionEnd)
    const splitsCharacter = (offset: number) => {
      const codeUnit = source.charCodeAt(offset)
      return codeUnit >= 0xdc00 && codeUnit <= 0xdfff
    }
    if (splitsCharacter(start) || splitsCharacter(end)) {
      setSelection(undefined)
      return
    }
    const text = source.slice(start, end)
    const startByte = encoder.encode(source.slice(0, start)).length
    setSelection({ startByte, endByte: startByte + encoder.encode(text).length, text })
  }

  async function run(kind: Operation, send: (signal: AbortSignal) => Promise<Document>) {
    if (operation.current || applying) return
    const controller = new AbortController()
    operation.current = controller
    setBusy(kind)
    setError('')
    setDestination(undefined)
    try {
      const next = await send(requestSignal(controller))
      if (controller.signal.aborted) return
      loadDocument(next)
      if (kind === 'open' || kind === 'create' || kind === 'import') {
        setHistory(false)
        setHistoryVersion(undefined)
        setHistoryFinding(undefined)
      }
      if (kind === 'create' || kind === 'import') {
        setCreatedPaths((paths) => paths.includes(next.path) ? paths : [next.path, ...paths])
        setCreating(false)
        setName('')
        setTemplate(undefined)
      }
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof ProjectApiError && error.status === 409 && error.code === 'REVISION_CONFLICT') {
        if (kind === 'import') setError('同名稿件已存在，请更换文件名后重新导入。')
        else if (kind === 'create') setError('此路径已有文件，请选择其他路径。')
        else setConflict(true)
      } else setError(errorMessage(error))
    } finally {
      operation.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  function goTo(next: Destination) {
    const { kind, path } = next
    void run(kind, async (signal) => {
      if (kind === 'open') return readText(projectId, path, signal)
      const content = kind === 'import' ? await readManuscript(next.file) : next.content
      signal.throwIfAborted()
      return saveText(projectId, path, content, '', signal)
    })
  }

  function requestDestination(next: Destination) {
    if (operation.current || applying) return
    if (next.kind === 'open' && next.path === currentPath) {
      setDestination(undefined)
      editor.current?.focus()
    } else if (dirty) setDestination(next)
    else goTo(next)
  }

  function selectTemplate(templateId: string) {
    const next = researchDocumentTemplates.find(({ id }) => id === templateId)
    if (next) setName(next.path)
    else if (name === template?.path) setName('')
    setTemplate(next)
    if (destination?.kind === 'create') setDestination(undefined)
  }

  function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim()) return
    if (template && (name === currentPath || paths.includes(name))) {
      setDestination(undefined)
      setError('此路径已有文件，请选择其他路径。')
      return
    }
    requestDestination({ kind: 'create', path: name, content: template?.content ?? '' })
  }

  function importManuscript(event: ChangeEvent<HTMLInputElement>) {
    const file = event.currentTarget.files?.[0]
    event.currentTarget.value = ''
    if (!file || operation.current || applying || !manuscriptIntake) return

    setDestination(undefined)
    if (!markdownExtension.test(file.name)) {
      setError('请选择 .md 或 .markdown 稿件。')
      return
    }
    if (/[\\/:\p{Cc}]/u.test(file.name)) {
      setError('稿件文件名包含无效字符，请更换文件名。')
      return
    }
    if (file.size > manuscriptSizeLimit) {
      setError('单篇稿件不能超过 64 KiB。')
      return
    }
    setError('')
    requestDestination({ kind: 'import', path: `paper/${file.name}`, file })
  }

  function restore(versionId: string) {
    if (!document || dirty || conflict || versionId === document.versionId) return
    void run('restore', (signal) => restoreVersion(projectId, versionId, document.sha256, signal))
  }

  return <section className="workspace-panel" id="project-workspace" aria-labelledby={id}>
    <div className="workspace-heading">
      <h4 id={id}>文本工作区</h4>
      <div className="workspace-actions">
        {manuscriptIntake && <>
          <input ref={manuscriptInput} type="file" accept=".md,.markdown" hidden onChange={importManuscript} />
          <button className="button outline" type="button" disabled={locked} aria-describedby={`${id}-import-hint`}
            onClick={() => manuscriptInput.current?.click()}>导入审稿稿件</button>
        </>}
        <button className="button outline" type="button" aria-expanded={creating} aria-controls={`${id}-create`}
          onClick={() => {
            if (creating && destination?.kind === 'create') setDestination(undefined)
            setCreating(!creating)
          }} disabled={locked}>{creating ? '取消新建' : '新建文件'}</button>
      </div>
    </div>
    {manuscriptIntake && <p id={`${id}-import-hint`} className="workspace-note">
      导入 UTF-8 编码的 .md 或 .markdown 文件，单篇不超过 64 KiB。
    </p>}
    <div id={`${id}-create`} hidden={!creating}>
      {creating && <form className="field" onSubmit={create}>
        <label htmlFor={`${id}-template`}>文件模板</label>
        <select id={`${id}-template`} value={template?.id ?? ''} disabled={locked}
          onChange={(event) => selectTemplate(event.currentTarget.value)}>
          <option value="">空白文件</option>
          {researchDocumentTemplates.map(({ id, label }) => <option key={id} value={id}>{label}</option>)}
        </select>
        {template && <details>
          <summary>预览模板初稿</summary>
          <pre className="workspace-text" tabIndex={0} aria-label="模板初稿">{template.content}</pre>
        </details>}
        <label htmlFor={`${id}-name`}>文件路径</label>
        <div className="workspace-create-row">
          <input id={`${id}-name`} value={name} required autoFocus autoComplete="off" spellCheck={false}
            readOnly={locked} aria-describedby={`${id}-path-hint`} onChange={(event) => {
              setName(event.target.value)
              if (destination?.kind === 'create') setDestination(undefined)
            }} />
          <button className="button primary" type="submit" disabled={locked || !name.trim()}>创建文本文件</button>
        </div>
        <p id={`${id}-path-hint`} className="workspace-note">输入项目内的相对路径，例如 paper/main.md。</p>
      </form>}
    </div>
    {paths.length > 0 && <ul className="workspace-list" role="list" aria-label="工作区文件">
      {paths.map((path) => (
        <li key={path}>
          <button className="workspace-row" type="button" aria-current={currentPath === path ? 'true' : undefined}
            disabled={locked} onClick={() => requestDestination({ kind: 'open', path })}>
            <bdi>{path}</bdi>
          </button>
        </li>
      ))}
    </ul>}
    <PageControls page={page} label="文件" />
    {!page.loading && !page.error && !paths.length && <p className="workspace-note">
      {manuscriptIntake ? '暂无文本文件，可导入稿件或新建文件。' : '暂无文本文件，可新建文件开始编辑。'}
    </p>}
    {destination && <div className="workspace-discard" ref={discardPrompt} tabIndex={-1}
      role="group" aria-label="处理未保存的修改">
      <p>当前有未保存的修改。{destinationLabel[destination.kind]} <bdi>{destination.path}</bdi> 前，请先保存或放弃草稿。</p>
      <div className="workspace-actions">
        <button className="button outline" type="button" onClick={() => {
          setDestination(undefined)
          editor.current?.focus()
        }}>继续编辑</button>
        <button className="button outline" type="button" onClick={() => goTo(destination)}>
          放弃草稿并{destinationLabel[destination.kind]}
        </button>
      </div>
    </div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {conflict && <p className="form-error" role="alert">
      {dirty ? '文件已有更新，本地草稿仍保留。可先复制草稿，再重新加载。' : '文件已有更新，请重新加载后继续。'}
    </p>}
    {busy && <p className="workspace-note" role="status">{progress[busy]}</p>}
    {document && <div className="workspace-editor">
      <div className="workspace-heading">
        <label htmlFor={`${id}-text`}>文件内容 <bdi>{document.path}</bdi></label>
        <span className="workspace-note" role="status">{dirty ? '有未保存的修改' : '已保存'}</span>
      </div>
      <div className="field workspace-text-zoom">
        <label htmlFor={`${id}-text-zoom`}>文字大小</label>
        <select id={`${id}-text-zoom`} value={editorZoom} onChange={(event) => {
          const zoom = event.currentTarget.value
          if (!isTextZoom(zoom)) return
          setEditorZoom(zoom)
          try {
            localStorage.setItem(textZoomKey, zoom)
          } catch {
            // 本地存储不可用时，保留本次字号选择。
          }
        }}>
          {textZoomOptions.map((zoom) => <option key={zoom} value={zoom}>{zoom}%</option>)}
        </select>
      </div>
      <textarea className="workspace-text" id={`${id}-text`} ref={editor} rows={9} dir="auto" spellCheck={false}
        data-text-zoom={editorZoom} value={draft} readOnly={locked} onSelect={(event) => selectText(event.currentTarget)} onChange={(event) => {
          setDraft(event.currentTarget.value)
          setSelection(undefined)
        }} />
      <div className="workspace-actions">
        <button className="button primary" type="button" disabled={locked || !dirty || conflict}
          onClick={() => void run('save', (signal) => saveText(projectId, document.path, draft, document.sha256, signal))}>保存</button>
        <button className="button outline" type="button" disabled={locked}
          onClick={() => void run('reload', (signal) => readText(projectId, document.path, signal))}>
          {dirty ? '放弃草稿并重新加载' : '重新加载'}
        </button>
        <button className="button outline" type="button" aria-expanded={history} aria-controls={`${id}-history`}
          disabled={applying} onClick={() => setHistory((value) => !value)}>版本历史</button>
        {isMarkdown && <button className="button outline" type="button" aria-describedby={`${id}-quote-hint`}
          aria-disabled={alreadyQuoted || undefined} disabled={locked || !selectedQuote || quoteLimitReached} onClick={() => {
            if (!locked && selectedQuote && !alreadyQuoted && !quoteLimitReached) onManuscriptSelection(selectedQuote)
          }}>引用稿件选区</button>}
        {brief && <button className="button outline" type="button" aria-describedby={`${id}-brief-hint`}
          aria-disabled={alreadyQuotedBrief || undefined} disabled={locked || !briefQuote || quoteLimitReached} onClick={() => {
            if (!locked && briefQuote && !alreadyQuotedBrief && !quoteLimitReached) onManuscriptSelection(briefQuote)
          }}>引用研究简报</button>}
      </div>
      {brief && <p className="workspace-note" id={`${id}-brief-hint`} role="status">
        {alreadyQuotedBrief ? <>已将完整简报加入<a href="#conversation-title">项目对话</a>。</>
          : briefQuote && quoteLimitReached ? <>已引用 {manuscriptSelectionLimit} 份稿件，请先在<a href="#conversation-title">项目对话</a>中移除一份。</>
            : briefHint}
      </p>}
      {isMarkdown && <p className="workspace-note" id={`${id}-quote-hint`} role="status">
        {alreadyQuoted ? <>已将选区加入<a href="#conversation-title">项目对话</a>。</>
          : dirty ? '请保存稿件后选取要引用的文字。'
            : quoteLimitReached ? <>已引用 {manuscriptSelectionLimit} 份稿件，请先在<a href="#conversation-title">项目对话</a>中移除一份。</>
              : <>选取非空白文字，最多 {manuscriptSelectionByteLimit} 个 UTF-8 字节。{currentQuote && '再次引用会替换此稿件的选区。'}</>}
      </p>}
      {isMarkdown && document.content.trim() && <>
        <SelectionEdit projectId={projectId} versionId={document.versionId} selection={selection}
          disabled={locked || dirty || conflict} onApplying={setApplying} onApplied={loadDocument} />
        <WordExport key={document.versionId} projectId={projectId} versionId={document.versionId} sourceSha256={document.sha256} dirty={dirty} />
        <ManuscriptReview projectId={projectId} versionId={document.versionId} sourceSha256={document.sha256} dirty={dirty}
          revisionActions={reviewActionsFor(document.versionId, document.versionId)} />
      </>}
      <div id={`${id}-history`} hidden={!history}>
        {history && <VersionHistory key={document.versionId} projectId={projectId} document={document}
          restoreBlocked={restoreBlocked} onRestore={restore} selected={historyVersion} applying={applying}
          initialFinding={historyFinding} onSelect={(versionId) => {
            setHistoryVersion(versionId)
            setHistoryFinding(undefined)
          }}
          revisionActions={reviewActionsFor(historyVersion ?? document.versionId, document.versionId)} />}
      </div>
    </div>}
  </section>
}
