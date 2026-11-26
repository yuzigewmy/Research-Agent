import { useId, useLayoutEffect, useRef, useState } from 'react'
import type { ChangeEvent } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import { isCSVTaskPath } from './tasks'
import { readText, saveText } from './workspace'
import type { Document, FileRecord } from './workspace'

type Props = { projectId: string; disabled: boolean; onImported: (file: FileRecord) => void }
type Choice = { file: File; path: string }
type Csv = { path: string; size: number; sha256: string; content: string }
type Conflict = { kind: 'conflict'; csv: Csv; current?: FileRecord; readError?: string }
type ImportResult = Conflict | { kind: 'saved'; file: FileRecord } | { kind: 'error'; message: string }

const byteLimit = 512 * 1024
const pathByteLimit = 512
const choiceLimit = 200
const encoder = new TextEncoder()

function directoryChoices(files: FileList) {
  const root = files[0].webkitRelativePath.split('/')[0]
  const choices = new Map<string, Choice>()
  for (const file of files) {
    if (!isCSVTaskPath(file.name) || file.size === 0 || file.size > byteLimit) continue
    const relativePath = file.webkitRelativePath
    const path = `data/imports/${relativePath}`
    if (!root || !relativePath.startsWith(`${root}/`) || path.length > pathByteLimit
      || /[\\:\p{Cc}\p{Cs}]/u.test(relativePath) || encoder.encode(path).length > pathByteLimit) continue
    const segments = relativePath.split('/')
    if (segments.at(-1) !== file.name || segments.some((part) => !part || part === '.' || part === '..')) continue
    choices.set(relativePath, { file, path })
    if (choices.size > choiceLimit) {
      throw new ProjectApiError(`目录中可导入的 CSV 超过 ${choiceLimit} 份，请选择更小的目录。`, 400)
    }
  }
  if (!choices.size) {
    throw new ProjectApiError('目录中没有可导入的 CSV。请选择含非空 CSV 的目录，文件须在 512 KiB 以内且路径有效。', 400)
  }
  return [...choices.values()].sort((a, b) => a.path.localeCompare(b.path))
}

async function readBytes(file: File, signal: AbortSignal) {
  signal.throwIfAborted()
  const reader = new FileReader()
  const abort = () => reader.abort()
  signal.addEventListener('abort', abort, { once: true })
  try {
    return await new Promise<ArrayBuffer>((resolve, reject) => {
      reader.onload = () => resolve(reader.result as ArrayBuffer)
      reader.onerror = () => reject(new ProjectApiError('无法读取本机 CSV，请重新选择目录。', 400))
      reader.onabort = () => reject(signal.reason)
      reader.readAsArrayBuffer(file)
    })
  } finally {
    signal.removeEventListener('abort', abort)
  }
}

async function prepareCsv({ file, path }: Choice, signal: AbortSignal): Promise<Csv> {
  if (!globalThis.crypto?.subtle) throw new ProjectApiError('当前浏览器环境无法校验 CSV 摘要。', 0)
  const bytes = await readBytes(file, signal)
  signal.throwIfAborted()
  if (bytes.byteLength !== file.size) throw new ProjectApiError('本机 CSV 已变更，请重新选择目录。', 400)

  let content: string
  try {
    // Keep the UTF-8 BOM as text so the workspace API writes it back byte for byte.
    content = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
  } catch {
    throw new ProjectApiError('CSV 不是有效的 UTF-8 编码。', 400)
  }
  if (content.includes('\0')) throw new ProjectApiError('CSV 包含 NUL 空字符。', 400)

  const digest = await crypto.subtle.digest('SHA-256', bytes)
  const sha256 = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
  return { path, size: bytes.byteLength, sha256, content }
}

function requestSignal(signal: AbortSignal) {
  return AbortSignal.any([signal, AbortSignal.timeout(15_000)])
}

function verifiedResult({ path, versionId, sha256, size, createdAt, content }: Document, csv: Csv): ImportResult {
  if (path !== csv.path || sha256.toLowerCase() !== csv.sha256 || size !== csv.size || content !== csv.content) {
    throw new ProjectApiError('无法核对工作区文件的路径、摘要或内容。', 502)
  }
  return { kind: 'saved', file: { path, versionId, sha256: csv.sha256, size, createdAt } }
}

async function compareCurrent(projectId: string, csv: Csv, previous: FileRecord | undefined, signal: AbortSignal): Promise<ImportResult> {
  try {
    const current = await readText(projectId, csv.path, requestSignal(signal))
    if (current.path !== csv.path) throw new ProjectApiError('工作区返回了不同的文件路径。', 502)
    if (current.sha256.toLowerCase() === csv.sha256) return verifiedResult(current, csv)
    const { path, versionId, sha256, size, createdAt } = current
    return { kind: 'conflict', csv, current: { path, versionId, sha256, size, createdAt } }
  } catch (error) {
    signal.throwIfAborted()
    return { kind: 'conflict', csv, current: previous, readError: errorMessage(error) }
  }
}

async function saveCsv(projectId: string, csv: Csv, current: FileRecord | undefined, signal: AbortSignal) {
  signal.throwIfAborted()
  try {
    const saved = await saveText(projectId, csv.path, csv.content, current?.sha256 ?? '', requestSignal(signal))
    return verifiedResult(saved, csv)
  } catch (error) {
    if (error instanceof ProjectApiError && error.status === 409 && error.code === 'REVISION_CONFLICT') {
      signal.throwIfAborted()
      return compareCurrent(projectId, csv, current, signal)
    }
    throw error
  }
}

function ConflictComparison({ csv, current, readError }: Conflict) {
  return <div className="task-input-body">
    {readError ? <p className="form-error" role="alert">无法核对当前版本。{readError}</p>
      : <p className="task-note" role="status">此路径已有不同内容。确认后会保存为新版本。</p>}
    {current && <p className="task-note">{readError ? '上次核对的版本' : '当前版本'} SHA-256 <code>{current.sha256}</code></p>}
    <p className="task-note">本机文件 SHA-256 <code>{csv.sha256}</code></p>
  </div>
}

export function TaskCsvDirectoryImport(props: Props) {
  return <DirectoryImport key={props.projectId} {...props} />
}

function DirectoryImport({ projectId, disabled, onImported }: Props) {
  const selectId = useId()
  const folderInput = useRef<HTMLInputElement>(null)
  const folderButton = useRef<HTMLButtonElement>(null)
  const operation = useRef<AbortController>(null)
  const [choices, setChoices] = useState<Choice[]>([])
  const [relativePath, setRelativePath] = useState('')
  const [result, setResult] = useState<ImportResult>()
  const [busy, setBusy] = useState(false)

  useLayoutEffect(() => () => operation.current?.abort(), [])

  const selected = choices.find(({ file }) => file.webkitRelativePath === relativePath)
  const conflict = result?.kind === 'conflict' ? result : undefined
  const unavailable = disabled || busy

  function chooseDirectory(event: ChangeEvent<HTMLInputElement>) {
    const input = event.currentTarget
    if (disabled || operation.current || !input.files?.length) return
    setRelativePath('')
    setResult(undefined)
    try {
      setChoices(directoryChoices(input.files))
    } catch (error) {
      setChoices([])
      setResult({ kind: 'error', message: errorMessage(error) })
    } finally {
      input.value = ''
    }
  }

  function cancel() {
    if (operation.current) return
    setChoices([])
    setRelativePath('')
    setResult(undefined)
    folderButton.current?.focus()
  }

  async function run() {
    if (disabled || operation.current || !selected) return
    const controller = new AbortController()
    const { signal } = controller
    operation.current = controller
    setBusy(true)
    if (!conflict) setResult(undefined)

    let next: ImportResult
    try {
      const csv = conflict?.csv ?? await prepareCsv(selected, signal)
      next = conflict?.readError
        ? await compareCurrent(projectId, csv, conflict.current, signal)
        : await saveCsv(projectId, csv, conflict?.current, signal)
    } catch (error) {
      next = { kind: 'error', message: errorMessage(error) }
    } finally {
      if (operation.current === controller) operation.current = null
      if (!signal.aborted) setBusy(false)
    }
    if (signal.aborted) return
    setResult(next)
    if (next.kind === 'saved') onImported(next.file)
  }

  const actionLabel = conflict?.readError ? '重新核对当前版本' : conflict ? '保存为新版本' : '导入 CSV'
  return <div className="task-input-body">
    <input ref={folderInput} type="file" {...{ webkitdirectory: '' }} hidden disabled={unavailable} onChange={chooseDirectory} />
    <div className="task-actions">
      <button ref={folderButton} className="button outline" type="button" disabled={unavailable}
        onClick={() => folderInput.current?.click()}>选择本机目录</button>
    </div>
    <p className="task-note">每次导入一份 UTF-8 CSV，最多 512 KiB。</p>
    {choices.length > 0 && <div className="field">
      <label htmlFor={selectId}>选择要导入的 CSV</label>
      <select id={selectId} value={relativePath} disabled={unavailable} onChange={(event) => {
        setRelativePath(event.currentTarget.value)
        setResult(undefined)
      }}>
        <option value="">请选择一份 CSV</option>
        {choices.map(({ file, path }) => <option key={path} value={file.webkitRelativePath}>{file.webkitRelativePath}</option>)}
      </select>
    </div>}
    {selected && result?.kind !== 'saved' && <p className="task-note">工作区路径 <bdi>{selected.path}</bdi></p>}
    {busy && <p className="task-note" role="status">正在处理 CSV…</p>}
    {result?.kind === 'error' && <p className="form-error" role="alert">{result.message}</p>}
    {conflict && <ConflictComparison {...conflict} />}
    {result?.kind === 'saved' && <div className="task-input-body" role="status">
      <p className="task-note">CSV 已导入工作区。</p>
      <p className="task-note">工作区路径 <bdi>{result.file.path}</bdi></p>
      <p className="task-note">SHA-256 <code>{result.file.sha256}</code></p>
    </div>}
    <div className="task-actions">
      <button className="button outline" type="button" disabled={unavailable || !selected}
        onClick={() => void run()}>{actionLabel}</button>
      {(choices.length > 0 || result) && result?.kind !== 'saved' && <button className="button outline"
        type="button" disabled={busy} onClick={cancel}>取消导入</button>}
    </div>
  </div>
}
