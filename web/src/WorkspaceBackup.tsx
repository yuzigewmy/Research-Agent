import { useEffect, useId, useRef, useState } from 'react'
import { t, useLocale } from './i18n'
import { errorMessage, request } from './projects'
import { WorkspaceRestore } from './WorkspaceRestore'
import './workspace-backup.css'

type StorageUsage = {
  directory: string
  schemaVersion: number
  databaseBytes: number
  objectBytes: number
  objectCount: number
  cacheBytes: number
  cacheCount: number
  cacheUnavailable?: boolean
  totalBytes: number
}

type UsageState =
  | { status: 'loading'; usage?: StorageUsage }
  | { status: 'ready'; usage: StorageUsage }
  | { status: 'error'; usage?: StorageUsage; message: string }

type CacheClearReceipt = { removedBytes: number; removedCount: number }

type CacheClearState =
  | { status: 'idle' }
  | { status: 'confirming'; message?: string }
  | { status: 'clearing' }
  | { status: 'done'; receipt: CacheClearReceipt }

function isNonNegativeInteger(value: unknown): boolean {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isStorageUsage(value: unknown): value is StorageUsage {
  const usage = value as Partial<StorageUsage> | null
  return !!usage && typeof usage.directory === 'string'
    && (usage.cacheUnavailable === undefined || typeof usage.cacheUnavailable === 'boolean')
    && [usage.schemaVersion, usage.databaseBytes, usage.objectBytes, usage.objectCount,
      usage.cacheBytes, usage.cacheCount, usage.totalBytes].every(isNonNegativeInteger)
}

function isCacheClearReceipt(value: unknown): value is CacheClearReceipt {
  const receipt = value as Partial<CacheClearReceipt> | null
  return !!receipt && isNonNegativeInteger(receipt.removedBytes) && isNonNegativeInteger(receipt.removedCount)
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
  const [cacheClear, setCacheClear] = useState<CacheClearState>({ status: 'idle' })
  const activeClear = useRef<AbortController | null>(null)
  const refreshButton = useRef<HTMLButtonElement>(null)
  const clearButton = useRef<HTMLButtonElement>(null)
  const confirmButton = useRef<HTMLButtonElement>(null)
  const confirmationId = useId()
  const { usage } = result
  const clearing = cacheClear.status === 'clearing'
  const showingConfirmation = cacheClear.status === 'confirming' || clearing
  const refreshDisabled = result.status === 'loading' || clearing
  const clearDisabled = result.status !== 'ready' || clearing || usage?.cacheUnavailable

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    void request('/api/v1/workspace-storage', isStorageUsage, { signal, cache: 'no-store' }).then((usage) => {
      signal.throwIfAborted()
      setResult({ status: 'ready', usage })
      if (!usage.cacheCount || usage.cacheUnavailable) {
        setCacheClear((previous) => previous.status === 'confirming' ? { status: 'idle' } : previous)
      }
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setResult((previous) => ({ status: 'error', usage: previous.usage, message: errorMessage(cause) }))
      }
    })
    return () => controller.abort()
  }, [attempt])

  useEffect(() => () => activeClear.current?.abort(), [])

  function refreshUsage() {
    setResult((previous) => ({ status: 'loading', usage: previous.usage }))
    setAttempt((value) => value + 1)
  }

  async function clearCache() {
    if (cacheClear.status !== 'confirming' || activeClear.current || clearDisabled || !usage?.cacheCount) return
    const controller = new AbortController()
    activeClear.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setCacheClear({ status: 'clearing' })
    try {
      const receipt = await request('/api/v1/workspace-cache/clear', isCacheClearReceipt, { method: 'POST', signal })
      signal.throwIfAborted()
      if (document.activeElement === confirmButton.current || document.activeElement === clearButton.current) {
        refreshButton.current?.focus()
      }
      setCacheClear({ status: 'done', receipt })
      refreshUsage()
    } catch (cause) {
      if (!controller.signal.aborted) setCacheClear({ status: 'confirming', message: errorMessage(cause) })
    } finally {
      activeClear.current = null
    }
  }

  return <div className="workspace-storage" role="group" aria-label="资料库用量">
    {result.status === 'loading' && <p role="status">正在读取资料库用量…</p>}
    {result.status === 'error' && <p className="form-error" role="alert">读取用量失败。{result.message}</p>}
    {usage && <dl>
      <div className="workspace-storage-path"><dt>资料库目录</dt><dd><bdi>{usage.directory}</bdi></dd></div>
      <div><dt>资料库版本</dt><dd>{usage.schemaVersion}</dd></div>
      <div><dt>数据库文件</dt><dd>{formatSize(usage.databaseBytes)}</dd></div>
      <div><dt>资料对象</dt><dd>{formatSize(usage.objectBytes)}（{usage.objectCount} 份）</dd></div>
      <div><dt>鉴别材料 PDF 缓存</dt><dd>{usage.cacheUnavailable ? '用量暂不可读' : `${formatSize(usage.cacheBytes)}（${usage.cacheCount} 个文件）`}</dd></div>
      <div><dt>{usage.cacheUnavailable ? '数据库与资料对象小计' : '合计'}</dt><dd>{formatSize(usage.totalBytes)}</dd></div>
    </dl>}
    <div className="workspace-storage-actions">
      <button className="button outline" type="button" ref={refreshButton} aria-disabled={refreshDisabled}
        onClick={() => { if (!refreshDisabled) refreshUsage() }}>
        {result.status === 'error' ? '重试读取用量' : '刷新用量'}
      </button>
      {!!usage?.cacheCount && <button className="button outline" type="button" ref={clearButton}
        aria-disabled={clearDisabled} aria-expanded={showingConfirmation}
        aria-controls={showingConfirmation ? confirmationId : undefined} onClick={() => {
          if (!clearDisabled) setCacheClear({ status: showingConfirmation ? 'idle' : 'confirming' })
        }}>清理 PDF 缓存</button>}
    </div>
    {showingConfirmation && <div className="workspace-cache-confirmation field" id={confirmationId}
      role="group" aria-labelledby={`${confirmationId}-note`}>
      <p id={`${confirmationId}-note`}>仅删除当前工作区中可重建的鉴别材料 PDF 渲染缓存，保留原稿、资料和备份。</p>
      <div className="workspace-storage-actions">
        <button className="button outline" type="button" autoFocus disabled={clearing} onClick={() => {
          clearButton.current?.focus()
          setCacheClear({ status: 'idle' })
        }}>取消</button>
        <button className="button outline" type="button" ref={confirmButton} aria-disabled={clearDisabled}
          onClick={() => void clearCache()}>
          {cacheClear.status === 'confirming' && cacheClear.message ? '重试清理 PDF 缓存' : '确认清理 PDF 缓存'}
        </button>
      </div>
      {cacheClear.status === 'confirming' && cacheClear.message && <p className="form-error" role="alert">
        未能确认清理结果。{cacheClear.message}
      </p>}
    </div>}
    {clearing && <p role="status">正在清理 PDF 缓存…</p>}
    {cacheClear.status === 'done' && <p role="status">
      已移除 {cacheClear.receipt.removedCount} 个 PDF 缓存文件（{formatSize(cacheClear.receipt.removedBytes)}）。
    </p>}
  </div>
}

export function WorkspaceBackup() {
  const locale = useLocale()
  const [open, setOpen] = useState(false)
  return <details className="workspace-backup" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t(locale, 'section.workspaceBackup')}</summary>
    {open && <WorkspaceStorageUsage />}
    <p>下载包含项目、答卷、对话和资料各版本的 ZIP。备份包括整个工作区，请妥善保存。</p>
    <form action="/api/v1/workspace-backup" method="post" target="_blank" rel="noopener">
      <button className="button outline" type="submit">{t(locale, 'section.workspaceBackupDownload')}</button>
    </form>
    <WorkspaceRestore />
    <p>使用启动程序的 <code>--restore</code> 选项可恢复更大的备份。</p>
  </details>
}
