import { useEffect, useState } from 'react'
import { errorMessage, request } from './projects'
import './workspace-backup.css'

type StorageUsage = {
  directory: string
  schemaVersion: number
  databaseBytes: number
  objectBytes: number
  objectCount: number
  totalBytes: number
}

type UsageState = { status: 'loading' } | { status: 'ready'; usage: StorageUsage } | { status: 'error'; message: string }

function isStorageUsage(value: unknown): value is StorageUsage {
  const usage = value as Partial<StorageUsage> | null
  return !!usage && typeof usage.directory === 'string'
    && [usage.schemaVersion, usage.databaseBytes, usage.objectBytes, usage.objectCount, usage.totalBytes]
      .every((number) => typeof number === 'number' && Number.isSafeInteger(number) && number >= 0)
}

const sizeFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 })
const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

function formatSize(bytes: number) {
  const unit = bytes < 1024 ? 0 : Math.min(units.length - 1, Math.floor(Math.log2(bytes) / 10))
  return `${sizeFormat.format(bytes / 1024 ** unit)} ${units[unit]}`
}

function WorkspaceStorageUsage() {
  const [result, setResult] = useState<UsageState>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    void request('/api/v1/workspace-storage', isStorageUsage, { signal, cache: 'no-store' }).then((usage) => {
      if (!controller.signal.aborted) setResult({ status: 'ready', usage })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setResult({ status: 'error', message: errorMessage(cause) })
    })
    return () => controller.abort()
  }, [attempt])

  return <div className="workspace-storage" role="group" aria-label="资料库用量">
    {result.status === 'loading' && <p role="status">正在读取资料库用量…</p>}
    {result.status === 'error' && <p className="form-error" role="alert">{result.message}</p>}
    {result.status === 'ready' && <dl>
      <div className="workspace-storage-path"><dt>资料库目录</dt><dd><bdi>{result.usage.directory}</bdi></dd></div>
      <div><dt>资料库版本</dt><dd>{result.usage.schemaVersion}</dd></div>
      <div><dt>数据库文件</dt><dd>{formatSize(result.usage.databaseBytes)}</dd></div>
      <div><dt>资料对象</dt><dd>{formatSize(result.usage.objectBytes)}（{result.usage.objectCount} 份）</dd></div>
      <div><dt>合计</dt><dd>{formatSize(result.usage.totalBytes)}</dd></div>
    </dl>}
    <button className="button outline" type="button" disabled={result.status === 'loading'} onClick={() => {
      setResult({ status: 'loading' })
      setAttempt((value) => value + 1)
    }}>{result.status === 'error' ? '重试读取用量' : '刷新用量'}</button>
  </div>
}

export function WorkspaceBackup() {
  const [open, setOpen] = useState(false)
  return <details className="workspace-backup" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>工作区备份</summary>
    {open && <WorkspaceStorageUsage />}
    <p>下载包含项目、答卷、对话和资料各版本的 ZIP。备份包括整个工作区，请妥善保存。</p>
    <form action="/api/v1/workspace-backup" method="post" target="_blank" rel="noopener">
      <button className="button outline" type="submit">下载工作区备份</button>
    </form>
    <p>恢复时使用启动程序的 <code>--restore</code> 选项，将备份恢复到一个新的资料库目录。</p>
  </details>
}
