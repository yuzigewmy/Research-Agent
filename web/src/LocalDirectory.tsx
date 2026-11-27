import { useEffect, useId, useRef, useState } from 'react'
import { materialSizeLimit, uploadMaterial } from './materials'
import type { Material } from './materials'
import { errorMessage } from './projects'

type Entry = FileSystemFileHandle | Directory
interface Directory extends FileSystemDirectoryHandle {
  values(): AsyncIterableIterator<Entry>
}
type DirectoryPicker = (options: { mode: 'read' }) => Promise<Directory>
type SavePicker = (options: { suggestedName: string; startIn: Directory }) => Promise<FileSystemFileHandle>
type Folder = { chain: Directory[]; entries: Entry[] }
type FilePreview = { handle: FileSystemFileHandle; file: File; text?: string }

const entryLimit = 500
const previewLimit = 64 * 1024

async function readFolder(chain: Directory[], signal: AbortSignal): Promise<Folder> {
  const entries: Entry[] = []
  for await (const entry of chain[chain.length - 1].values()) {
    signal.throwIfAborted()
    if (entries.length === entryLimit) throw new Error('此层目录超过 500 项，请直接选择较小的子目录。')
    entries.push(entry)
  }
  entries.sort((a, b) => a.kind === b.kind ? a.name.localeCompare(b.name, 'zh-CN') : a.kind === 'directory' ? -1 : 1)
  return { chain, entries }
}

async function previewFile(handle: FileSystemFileHandle): Promise<FilePreview> {
  const file = await handle.getFile()
  const preview = { handle, file }
  if (file.size > previewLimit) return preview
  const bytes = await file.arrayBuffer()
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    return text.includes('\0') ? preview : { ...preview, text }
  } catch (error) {
    if (error instanceof TypeError) return preview
    throw error
  }
}

export function LocalDirectory({ projectId, onImported }: { projectId: string; onImported: (material: Material) => void }) {
  const { showDirectoryPicker: picker, showSaveFilePicker: savePicker } = window as Window & {
    showDirectoryPicker?: DirectoryPicker
    showSaveFilePicker?: SavePicker
  }
  const titleId = useId()
  const [folder, setFolder] = useState<Folder>()
  const [preview, setPreview] = useState<FilePreview>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState<{ action: '已导入' | '已复制'; name: string }>()
  const active = useRef<AbortController | null>(null)
  useEffect(() => () => active.current?.abort(), [])

  async function run(action: (signal: AbortSignal) => Promise<void>, accessError = '无法读取此目录或文件，请重新选择目录。') {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    setBusy(true)
    setError('')
    setReceipt(undefined)
    try {
      await action(controller.signal)
    } catch (cause) {
      if (controller.signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return
      setError(cause instanceof DOMException && (cause.name === 'NotAllowedError' || cause.name === 'NotFoundError' || cause.name === 'NotReadableError')
        ? accessError : errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  async function openFolder(chain: Directory[], signal: AbortSignal) {
    const next = await readFolder(chain, signal)
    signal.throwIfAborted()
    setFolder(next)
    setPreview(undefined)
  }

  function selectDirectory() {
    if (!picker) return
    void run(async signal => {
      const root = await picker.call(window, { mode: 'read' })
      signal.throwIfAborted()
      await openFolder([root], signal)
    })
  }

  function openEntry(entry: Entry) {
    if (!folder) return
    if (entry.kind === 'directory') {
      void run(signal => openFolder([...folder.chain, entry], signal))
    } else {
      setPreview(undefined)
      void run(async signal => {
        const result = await previewFile(entry)
        signal.throwIfAborted()
        setPreview(result)
      })
    }
  }

  function importFile(file: File) {
    void run(async signal => {
      const material = await uploadMaterial(projectId, file, AbortSignal.any([signal, AbortSignal.timeout(60_000)]))
      signal.throwIfAborted()
      onImported(material)
      setReceipt({ action: '已导入', name: material.name })
    })
  }

  function copyFile({ handle, file }: FilePreview) {
    if (!savePicker || !folder) return
    void run(async signal => {
      const target = await savePicker.call(window, { suggestedName: `副本-${file.name}`, startIn: folder.chain.at(-1)! })
      signal.throwIfAborted()
      const sameFile = await target.isSameEntry(handle)
      signal.throwIfAborted()
      if (sameFile) {
        setError('请选择另一个文件保存副本。')
        return
      }
      const writable = await target.createWritable()
      await file.stream().pipeTo(writable, { signal })
      signal.throwIfAborted()
      setReceipt({ action: '已复制', name: target.name })
    }, '无法访问源文件或保存目标，请重新选择文件后重试。')
  }

  return <details className="material-local-directory" aria-labelledby={titleId}>
    <summary id={titleId}>本机资料目录</summary>
    <div className="material-local-directory-body">
      <p className="material-note">在当前项目和页面内以只读方式浏览所选目录。点击导入后，所选文件会复制到项目资料。</p>
      {picker ? <div className="material-actions">
        <button className="button outline" type="button" disabled={busy} onClick={selectDirectory}>{folder ? '更换目录' : '选择目录'}</button>
        {folder && <button className="button outline" type="button" disabled={busy} onClick={() => {
          setFolder(undefined)
          setPreview(undefined)
          setError('')
          setReceipt(undefined)
        }}>卸载目录</button>}
      </div> : <p className="material-note">当前浏览器无法选择目录。可使用「上传文件」导入资料。</p>}
      {folder && <>
        <p className="material-note">当前目录 <bdi>{folder.chain.map(entry => entry.name).join('/')}</bdi></p>
        {folder.chain.length > 1 && <div className="material-actions">
          <button className="button outline" type="button" disabled={busy}
            onClick={() => void run(signal => openFolder(folder.chain.slice(0, -1), signal))}>返回上一级</button>
        </div>}
        {folder.entries.length ? <ul className="workspace-list" role="list" aria-label="本机目录内容">
          {folder.entries.map(entry => <li key={entry.name}>
            <button className="workspace-row" type="button" disabled={busy} onClick={() => openEntry(entry)}>
              <bdi>{entry.name}</bdi><span className="material-note">{entry.kind === 'directory' ? '目录' : '文件'}</span>
            </button>
          </li>)}
        </ul> : <p className="material-note">此目录为空。</p>}
      </>}
      {preview && <section className="material-local-directory-body" aria-label="选中的本机文件">
        <p><bdi>{preview.file.name}</bdi> · {preview.file.size.toLocaleString('zh-CN')} 字节</p>
        {preview.text !== undefined
          ? <pre className="material-text" tabIndex={0} aria-label="本机文件预览" dir="auto">{preview.text}</pre>
          : <p className="material-note">可预览 64 KiB 以内的 UTF-8 文本。</p>}
        <div className="material-actions">
          <button className="button primary" type="button" disabled={busy || preview.file.size > materialSizeLimit}
            onClick={() => importFile(preview.file)}>导入选中文件</button>
          {savePicker && <button className="button outline" type="button" disabled={busy}
            onClick={() => copyFile(preview)}>保存本机副本</button>}
        </div>
        {preview.file.size > materialSizeLimit && <p className="material-note">单个文件导入上限为 64 MiB。</p>}
      </section>}
      {busy && <p className="material-note" role="status">正在处理本机资料…</p>}
      {receipt && <p className="material-note" role="status">{receipt.action} <bdi>{receipt.name}</bdi></p>}
      {error && <p className="form-error" role="alert">{error}</p>}
    </div>
  </details>
}
