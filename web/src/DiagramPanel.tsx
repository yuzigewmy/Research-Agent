import { useCallback, useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { DiagramCanvas } from './DiagramCanvas'
import {
  diagramPath, diagramText, edgeKey, isCoordinate, isDiagram, isDiagramPath, isNodeLabel, maxDiagramEdges,
  maxDiagramNodes, maxNodeX, maxNodeY, newNode, parseDiagram,
} from './diagram'
import type { Diagram, DiagramNode } from './diagram'
import { errorMessage, ProjectApiError } from './projects'
import { diagramSvgPath, listFiles, listVersions, readText, readVersion, restoreVersion, saveText } from './workspace'
import type { Document, FilePage, FileRecord } from './workspace'
import './diagram.css'

const versionDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const undoLimit = 50

type Editor = {
  path: string
  draft: Diagram
  saved?: { file: FileRecord; diagram: Diagram }
  undo: Diagram[]
}

type Destination = { kind: 'create' | 'open' | 'reload'; path: string }
type Operation = 'open' | 'save' | 'copy' | 'restore'
type LoadPage = (signal: AbortSignal, cursor?: string) => Promise<FilePage>

const destinationLabels = { create: '新建', open: '打开', reload: '重新加载' } as const

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function useDiagramPage(load: LoadPage, key: 'path' | 'versionId') {
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
        const byKey = new Map(current.items.map((file) => [file[key], file]))
        for (const file of next.items) byKey.set(file[key], file)
        return { items: [...byKey.values()], nextCursor: next.nextCursor }
      })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [load, key, query])

  function loadPage(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  return { ...page, loading, error, refresh: () => loadPage(), retry: () => loadPage(query.cursor), more: () => loadPage(page.nextCursor) }
}

function PageControls({ page, label }: { page: ReturnType<typeof useDiagramPage>; label: string }) {
  if (page.loading) return <p className="workspace-note" role="status">正在加载{label}…</p>
  if (page.error) return <div className="request-error">
    <p className="form-error" role="alert">{page.error}</p>
    <button className="button outline" type="button" onClick={page.retry}>重试加载{label}</button>
  </div>
  return page.nextCursor && <button className="button outline" type="button" onClick={page.more}>加载更多{label}</button>
}

function NodeFields({ node, disabled, onChange }: {
  node: DiagramNode
  disabled: boolean
  onChange: (change: Partial<Pick<DiagramNode, 'label' | 'x' | 'y'>>) => void
}) {
  const id = useId()
  const validLabel = isNodeLabel(node.label)
  const validX = isCoordinate(node.x, maxNodeX)
  const validY = isCoordinate(node.y, maxNodeY)

  return <div className="workspace-preview" role="group" aria-label="节点属性">
    <div className="field">
      <label htmlFor={`${id}-label`}>节点标签</label>
      <input id={`${id}-label`} type="text" dir="auto" value={node.label} maxLength={72} disabled={disabled}
        aria-invalid={!validLabel || undefined} aria-describedby={`${id}-label-hint`}
        onChange={(event) => onChange({ label: event.currentTarget.value })} />
      <p id={`${id}-label-hint`} className={validLabel ? 'workspace-note' : 'form-error'}>请输入 1 至 36 个字符的标签。画布每 12 个字符换行。</p>
    </div>
    <div className="workspace-actions">
      <div className="field">
        <label htmlFor={`${id}-x`}>横坐标</label>
        <input id={`${id}-x`} type="number" min={0} max={maxNodeX} step={1} disabled={disabled}
          value={Number.isFinite(node.x) ? node.x : ''} aria-invalid={!validX || undefined} aria-describedby={`${id}-position-hint`}
          onChange={(event) => onChange({ x: event.currentTarget.valueAsNumber })} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-y`}>纵坐标</label>
        <input id={`${id}-y`} type="number" min={0} max={maxNodeY} step={1} disabled={disabled}
          value={Number.isFinite(node.y) ? node.y : ''} aria-invalid={!validY || undefined} aria-describedby={`${id}-position-hint`}
          onChange={(event) => onChange({ y: event.currentTarget.valueAsNumber })} />
      </div>
    </div>
    <p className={validX && validY ? 'workspace-note' : 'form-error'} id={`${id}-position-hint`}>
      坐标为整数，横坐标 0 至 {maxNodeX}，纵坐标 0 至 {maxNodeY}。
    </p>
  </div>
}

function EdgeEditor({ diagram, disabled, onChange }: {
  diagram: Diagram
  disabled: boolean
  onChange: (apply: (current: Diagram) => Diagram) => void
}) {
  const id = useId()
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const nodes = new Map(diagram.nodes.map((node) => [node.id, node]))
  const source = nodes.has(from) ? from : ''
  const destination = nodes.has(to) ? to : ''
  const editable = !disabled && nodes.size > 1 && diagram.edges.length < maxDiagramEdges
  const duplicate = diagram.edges.some((edge) => edge.from === source && edge.to === destination)
  const canAdd = editable && source && destination && source !== destination && !duplicate
  const label = (nodeId: string) => `${nodes.get(nodeId)?.label || nodeId}（${nodeId}）`

  return <section className="workspace-preview" aria-labelledby={`${id}-heading`}>
    <h5 id={`${id}-heading`}>连线</h5>
    <div className="workspace-actions">
      <div className="field">
        <label htmlFor={`${id}-from`}>连线起点</label>
        <select id={`${id}-from`} value={source} disabled={!editable} onChange={(event) => setFrom(event.currentTarget.value)}>
          <option value="">选择起点</option>
          {diagram.nodes.map((node) => <option key={node.id} value={node.id}>{label(node.id)}</option>)}
        </select>
      </div>
      <div className="field">
        <label htmlFor={`${id}-to`}>连线终点</label>
        <select id={`${id}-to`} value={destination} disabled={!editable} onChange={(event) => setTo(event.currentTarget.value)}>
          <option value="">选择终点</option>
          {diagram.nodes.map((node) => <option key={node.id} value={node.id} disabled={node.id === source}>{label(node.id)}</option>)}
        </select>
      </div>
      <button className="button outline" type="button" disabled={!canAdd} onClick={() => {
        if (canAdd) onChange((current) => ({ ...current, edges: [...current.edges, { from: source, to: destination }] }))
      }}>添加连线</button>
    </div>
    {duplicate && <p className="workspace-note">该方向的连线已存在。</p>}
    {diagram.edges.length === maxDiagramEdges && <p className="workspace-note">每张流程图最多 {maxDiagramEdges} 条连线。</p>}
    {diagram.edges.length > 0 && <ul className="diagram-edge-list" aria-label="流程图连线">
      {diagram.edges.map((edge) => <li key={edgeKey(edge)}>
        <div className="workspace-actions">
          <span><bdi>{label(edge.from)}</bdi> → <bdi>{label(edge.to)}</bdi></span>
          <button className="button outline" type="button" disabled={disabled}
            aria-label={`删除连线 ${edge.from} → ${edge.to}`}
            onClick={() => onChange((current) => ({ ...current, edges: current.edges.filter((item) => item !== edge) }))}>删除连线</button>
        </div>
      </li>)}
    </ul>}
  </section>
}

function VersionPreview({ projectId, version, current, restoreDisabled, onRestore }: {
  projectId: string
  version: FileRecord
  current: boolean
  restoreDisabled: boolean
  onRestore: () => void
}) {
  const [preview, setPreview] = useState<Diagram>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const { path, versionId, sha256 } = version

  useEffect(() => {
    const controller = new AbortController()
    void readVersion(projectId, versionId, requestSignal(controller)).then((document) => {
      if (controller.signal.aborted) return
      if (document.path !== path || document.versionId !== versionId || document.sha256 !== sha256) {
        throw new ProjectApiError('无法核对此流程图版本。', 422)
      }
      setPreview(parseDiagram(document.content))
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    })
    return () => controller.abort()
  }, [projectId, path, versionId, sha256, retry])

  if (error) return <div className="request-error">
    <p className="form-error" role="alert">{error}</p>
    <button className="button outline" type="button" onClick={() => {
      setError('')
      setRetry((value) => value + 1)
    }}>重试查看版本</button>
  </div>
  if (!preview) return <p className="workspace-note" role="status">正在加载流程图版本…</p>
  return <section className="workspace-preview" aria-label="流程图版本预览">
    <p className="workspace-note">只读预览 · <time dateTime={version.createdAt}>{versionDate.format(new Date(version.createdAt))}</time>
      {' · SHA-256 '}<code title={sha256}>{sha256.slice(0, 12)}</code></p>
    <DiagramCanvas diagram={preview} />
    <div className="workspace-actions">
      <a className="button outline" download href={diagramSvgPath(projectId, versionId)}>下载此版本 SVG</a>
      {!current && <button className="button outline" type="button" disabled={restoreDisabled} onClick={onRestore}>恢复此版本</button>}
    </div>
  </section>
}

function DiagramHistory({ projectId, current, restoreDisabled, onRestore }: {
  projectId: string
  current: FileRecord
  restoreDisabled: boolean
  onRestore: (versionId: string) => void
}) {
  const path = current.path
  const load = useCallback((signal: AbortSignal, cursor?: string) => listVersions(projectId, path, signal, cursor), [projectId, path])
  const page = useDiagramPage(load, 'versionId')
  const [selectedId, setSelectedId] = useState<string>()
  const selected = page.items.find((version) => version.versionId === selectedId && version.path === path)

  return <section className="workspace-history" aria-label="流程图版本历史">
    {page.items.length > 0 && <ul className="workspace-list" aria-label="流程图版本列表" role="list">
      {page.items.filter((file) => file.path === path).map((file) => <li key={file.versionId}>
        <button className="workspace-row" type="button" aria-pressed={selectedId === file.versionId} onClick={() => setSelectedId(file.versionId)}>
          <time dateTime={file.createdAt}>{versionDate.format(new Date(file.createdAt))}</time>
          <span className="workspace-note">{file.versionId === current.versionId ? '当前版本' : `版本 ${file.versionId.slice(0, 8)}`}</span>
        </button>
      </li>)}
    </ul>}
    <PageControls page={page} label="版本" />
    {selected && <VersionPreview key={selected.versionId} projectId={projectId} version={selected}
      current={selected.versionId === current.versionId} restoreDisabled={restoreDisabled}
      onRestore={() => onRestore(selected.versionId)} />}
  </section>
}

function DiagramWorkspace({ projectId }: { projectId: string }) {
  const id = useId()
  const load = useCallback((signal: AbortSignal, cursor?: string) => listFiles(projectId, signal, cursor, 'diagram'), [projectId])
  const page = useDiagramPage(load, 'path')
  const operation = useRef<AbortController | null>(null)
  const editorHeading = useRef<HTMLHeadingElement>(null)
  const discardPrompt = useRef<HTMLDivElement>(null)
  const [editor, setEditor] = useState<Editor>()
  const [name, setName] = useState('')
  const [destination, setDestination] = useState<Destination>()
  const [selectedId, setSelectedId] = useState<string>()
  const [zoom, setZoom] = useState(100)
  const [busy, setBusy] = useState<Operation>()
  const [error, setError] = useState('')
  const [nameError, setNameError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [history, setHistory] = useState(false)
  const saved = editor?.saved
  const draft = editor?.draft
  const dirty = !!editor && (!saved || diagramText(saved.diagram) !== diagramText(editor.draft))
  const validDraft = !!draft && isDiagram(draft)
  const canSave = validDraft && dirty && !busy && !conflict
  const selected = draft?.nodes.find((node) => node.id === selectedId) ?? draft?.nodes[0]
  const paths = new Set(page.items.map(({ path }) => path).filter(isDiagramPath))
  if (saved) paths.add(saved.file.path)

  useEffect(() => () => operation.current?.abort(), [])
  useEffect(() => { if (editor?.path) editorHeading.current?.focus() }, [editor?.path])
  useEffect(() => { if (destination) discardPrompt.current?.focus() }, [destination])

  function change(apply: (current: Diagram) => Diagram) {
    if (operation.current) return
    setEditor((current) => {
      if (!current) return current
      const next = apply(current.draft)
      if (diagramText(next) === diagramText(current.draft)) return current
      return { ...current, draft: next, undo: [...current.undo.slice(1 - undoLimit), current.draft] }
    })
  }

  function undo() {
    if (operation.current) return
    setEditor((current) => {
      const previous = current?.undo.at(-1)
      return current && previous ? { ...current, draft: previous, undo: current.undo.slice(0, -1) } : current
    })
  }

  async function run(kind: Operation, path: string, send: (signal: AbortSignal) => Promise<Document>) {
    if (operation.current) return
    const controller = new AbortController()
    operation.current = controller
    setBusy(kind)
    setError('')
    setDestination(undefined)
    try {
      const file = await send(requestSignal(controller))
      if (controller.signal.aborted) return
      if (file.path !== path) throw new ProjectApiError('无法核对此流程图文件。', 422)
      const diagram = parseDiagram(file.content)
      setEditor((current) => ({ path, draft: diagram, saved: { file, diagram },
        undo: kind === 'save' || kind === 'copy' ? current?.undo ?? [] : [] }))
      setConflict(false)
      if (kind === 'open' || kind === 'restore') setSelectedId(diagram.nodes[0]?.id)
      if (kind === 'open' || kind === 'copy') setHistory(false)
      if (kind === 'copy') setName('')
      if (kind !== 'open') page.refresh()
    } catch (cause) {
      if (controller.signal.aborted) return
      if (kind !== 'open' && cause instanceof ProjectApiError && cause.code === 'REVISION_CONFLICT') {
        setConflict(true)
        if (kind === 'copy') setNameError('此路径已有流程图，请更换名称后另存。')
      } else setError(errorMessage(cause))
    } finally {
      operation.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  function goTo(next: Destination) {
    if (operation.current) return
    setDestination(undefined)
    if (next.kind !== 'create') {
      void run('open', next.path, (signal) => readText(projectId, next.path, signal))
      return
    }
    const first = newNode([])
    setEditor({ path: next.path, draft: { version: 1, nodes: [first], edges: [] }, undo: [] })
    setSelectedId(first.id)
    setZoom(100)
    setName('')
    setError('')
    setConflict(false)
    setHistory(false)
  }

  function navigate(next: Destination) {
    if (operation.current) return
    if (next.kind === 'open' && next.path === editor?.path) {
      setDestination(undefined)
      editorHeading.current?.focus()
      return
    }
    if (dirty) setDestination(next)
    else goTo(next)
  }

  function submitNew(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const copy = (event.nativeEvent as SubmitEvent).submitter?.getAttribute('value') === 'copy'
    setNameError('')
    try {
      const path = diagramPath(name)
      if (paths.has(path) || path === editor?.path) {
        setNameError('此名称已在使用，请选择其他名称。')
        return
      }
      if (copy) {
        if (conflict && draft && isDiagram(draft)) void run('copy', path, (signal) => saveText(projectId, path, diagramText(draft), '', signal))
      } else navigate({ kind: 'create', path })
    } catch (cause) {
      setNameError(errorMessage(cause))
    }
  }

  function save() {
    if (!editor || !canSave) return
    const { path, draft: content } = editor
    void run('save', path, (signal) => saveText(projectId, path, diagramText(content), saved?.file.sha256 ?? '', signal))
  }

  function restore(versionId: string) {
    if (!saved || dirty || conflict || versionId === saved.file.versionId) return
    void run('restore', saved.file.path, (signal) => restoreVersion(projectId, versionId, saved.file.sha256, signal))
  }

  function changeNode(nodeId: string, fields: Partial<Pick<DiagramNode, 'label' | 'x' | 'y'>>) {
    change((current) => ({ ...current, nodes: current.nodes.map((node) => node.id === nodeId ? { ...node, ...fields } : node) }))
  }

  return <div className="workspace-editor diagram-editor">
    <form className="field" onSubmit={submitNew}>
      <label htmlFor={`${id}-name`}>流程图名称</label>
      <div className="workspace-create-row">
        <input id={`${id}-name`} autoComplete="off" dir="auto" value={name} disabled={!!busy}
          aria-describedby={`${id}-name-hint`} aria-invalid={!!nameError || undefined} onChange={(event) => {
            setName(event.currentTarget.value)
            setNameError('')
          }} />
        <button className="button outline" type="submit" disabled={!!busy || !name.trim()}>新建流程图</button>
        {conflict && <button className="button outline" type="submit" value="copy"
          disabled={!!busy || !name.trim() || !draft || !isDiagram(draft)}>另存当前草稿</button>}
      </div>
      <p id={`${id}-name-hint`} className="workspace-note">保存至 diagrams/，名称最多 256 字节（UTF-8）。</p>
      {nameError && <p className="form-error" role="alert">{nameError}</p>}
    </form>
    {paths.size > 0 && <ul className="workspace-list" aria-label="已保存流程图" role="list">
      {[...paths].map((path) => <li key={path}>
        <button className="workspace-row" type="button" aria-current={editor?.path === path ? 'true' : undefined}
          disabled={!!busy} onClick={() => navigate({ kind: 'open', path })}><bdi>{path}</bdi></button>
      </li>)}
    </ul>}
    <PageControls page={page} label="流程图" />
    {!page.loading && !page.error && paths.size === 0 && <p className="workspace-note">暂无已保存的流程图。</p>}
    {destination && <div className="workspace-discard" role="group" aria-label="处理未保存的流程图" tabIndex={-1} ref={discardPrompt}>
      <p>流程图有未保存的修改。{destinationLabels[destination.kind]} <bdi>{destination.path}</bdi> 前，请先保存或放弃修改。</p>
      <div className="workspace-actions">
        <button className="button outline" type="button" onClick={() => {
          setDestination(undefined)
          editorHeading.current?.focus()
        }}>继续编辑</button>
        <button className="button outline" type="button" onClick={() => goTo(destination)}>放弃修改并{destinationLabels[destination.kind]}</button>
      </div>
    </div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {conflict && <p className="form-error" role="alert">同一路径已有更新。当前草稿仍保留，可填写新名称并另存。</p>}
    {busy && <p className="workspace-note" role="status">{busy === 'open' ? '正在打开流程图…'
      : busy === 'restore' ? '正在恢复流程图版本…' : '正在保存流程图…'}</p>}
    {editor && draft && selected && <section className="workspace-editor" aria-labelledby={`${id}-editor`}>
      <div className="workspace-heading">
        <h4 id={`${id}-editor`} ref={editorHeading} tabIndex={-1}><bdi>{editor.path}</bdi></h4>
        <span className="diagram-status" role="status">{dirty ? '有未保存的修改' : '已保存'}</span>
      </div>
      <div className="workspace-actions">
        <button className="button primary" type="button" disabled={!canSave} onClick={save}>保存流程图</button>
        <button className="button outline" type="button" disabled={!!busy || !editor.undo.length} onClick={undo}>撤销</button>
        {saved && <button className="button outline" type="button" disabled={!!busy} aria-expanded={history} aria-controls={`${id}-history`}
          onClick={() => setHistory((open) => !open)}>流程图版本历史</button>}
        {(saved || conflict) && <button className="button outline" type="button" disabled={!!busy}
          onClick={() => navigate({ kind: 'reload', path: editor.path })}>重新加载流程图</button>}
        {saved
          ? <a className="button outline" download href={diagramSvgPath(projectId, saved.file.versionId)}
            aria-describedby={`${id}-svg-source`}>下载 SVG</a>
          : <button className="button outline" type="button" disabled>下载 SVG</button>}
      </div>
      <p className="workspace-note" id={`${id}-svg-source`}>
        {saved ? <>SVG 来自已保存版本 <code title={saved.file.sha256}>{saved.file.sha256.slice(0, 12)}</code>。
          {dirty && ' 保存修改后可下载新版。'}</> : '保存流程图后可下载 SVG。'}
      </p>
      {!validDraft && <p className="form-error" role="status">请修正节点标签或坐标后再保存。</p>}
      <div className="workspace-actions" role="group" aria-label="画布缩放">
        <button className="button outline" type="button" disabled={zoom === 50} onClick={() => setZoom((value) => value - 25)}>缩小</button>
        <output className="workspace-note" aria-label="当前画布缩放">{zoom}%</output>
        <button className="button outline" type="button" disabled={zoom === 200} onClick={() => setZoom((value) => value + 25)}>放大</button>
      </div>
      <DiagramCanvas key={`canvas:${editor.path}`} diagram={draft} zoom={zoom} selectedId={selected.id} onSelect={setSelectedId}
        onMove={busy ? undefined : changeNode} />
      <div className="workspace-actions">
        <div className="field">
          <label htmlFor={`${id}-node`}>节点</label>
          <select id={`${id}-node`} value={selected.id} disabled={!!busy} onChange={(event) => setSelectedId(event.currentTarget.value)}>
            {draft.nodes.map((node) => <option key={node.id} value={node.id}>{node.label || '未命名节点'}（{node.id}）</option>)}
          </select>
        </div>
        <button className="button outline" type="button" disabled={!!busy || draft.nodes.length >= maxDiagramNodes} onClick={() => {
          const node = newNode(draft.nodes)
          change((current) => ({ ...current, nodes: [...current.nodes, node] }))
          setSelectedId(node.id)
        }}>添加节点</button>
        <button className="button outline" type="button" disabled={!!busy || draft.nodes.length <= 1} onClick={() => {
          change((current) => ({ ...current, nodes: current.nodes.filter((node) => node.id !== selected.id),
            edges: current.edges.filter(({ from, to }) => from !== selected.id && to !== selected.id) }))
          setSelectedId(undefined)
        }}>删除所选节点</button>
      </div>
      <NodeFields node={selected} disabled={!!busy} onChange={(fields) => changeNode(selected.id, fields)} />
      <EdgeEditor key={editor.path} diagram={draft} disabled={!!busy} onChange={change} />
      <div id={`${id}-history`} hidden={!history}>
        {history && saved && <DiagramHistory key={saved.file.versionId} projectId={projectId} current={saved.file}
          restoreDisabled={dirty || conflict || !!busy} onRestore={restore} />}
      </div>
    </section>}
  </div>
}

export function DiagramPanel({ projectId }: { projectId: string }) {
  const [visited, setVisited] = useState(false)
  return <details className="diagram-panel" id="project-diagrams" onToggle={(event) => {
    if (event.target === event.currentTarget && event.currentTarget.open) setVisited(true)
  }}>
    <summary>图表工作区</summary>
    {visited && <DiagramWorkspace key={projectId} projectId={projectId} />}
  </details>
}
