import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage } from './projects'
import { executeBatchRestore, listFiles, listVersions, previewBatchRestore } from './workspace'
import type { BatchRestorePreviewItem, BatchRestoreResult, FileRecord } from './workspace'

const versionDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' })

type FileEntry = {
  file: FileRecord
  versions: FileRecord[]
  selected: boolean
  targetVersionId: string
}

export function WorkspaceBatchRestore({ projectId, onRestored }: {
  projectId: string
  onRestored: (restoredPaths: string[]) => void
}) {
  const headingId = useId()
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [reloadTick, setReloadTick] = useState(0)
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [preview, setPreview] = useState<BatchRestorePreviewItem[]>([])
  const [receipt, setReceipt] = useState<BatchRestoreResult | null>(null)
  const [busy, setBusy] = useState<'preview' | 'restore' | 'rollback' | null>(null)
  const [error, setError] = useState('')
  const activeReq = useRef<AbortController | null>(null)

  useEffect(() => () => activeReq.current?.abort(), [])

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setLoading(true)
    setError('')
    void (async () => {
      try {
        const page = await listFiles(projectId, signal)
        if (controller.signal.aborted) return
        const files = page.items.slice(0, 24)
        const loaded = await Promise.all(files.map(async (file) => {
          const versionsPage = await listVersions(projectId, file.path, signal)
          const versions = versionsPage.items
          const historical = versions.find((v) => v.versionId !== file.versionId)
          return {
            file,
            versions,
            selected: false,
            targetVersionId: historical?.versionId ?? file.versionId,
          }
        }))
        if (!controller.signal.aborted) {
          setEntries(loaded)
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      } finally {
        if (!controller.signal.aborted) setLoading(false)
      }
    })()
    return () => controller.abort()
  }, [projectId, open, reloadTick])

  const selectedItems = entries.filter((entry) => entry.selected && entry.targetVersionId)

  async function runPreview() {
    if (!selectedItems.length || busy) return
    const controller = new AbortController()
    activeReq.current = controller
    setBusy('preview')
    setError('')
    setReceipt(null)
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
      const result = await previewBatchRestore(
        projectId,
        selectedItems.map((entry) => ({ path: entry.file.path, targetVersionId: entry.targetVersionId })),
        signal,
      )
      if (!controller.signal.aborted) setPreview(result.items)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      activeReq.current = null
      if (!controller.signal.aborted) setBusy(null)
    }
  }

  async function runRestore() {
    if (!preview.length || busy) return
    const controller = new AbortController()
    activeReq.current = controller
    setBusy('restore')
    setError('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
      const result = await executeBatchRestore(
        projectId,
        preview.map((item) => ({
          path: item.path,
          targetVersionId: item.targetVersionId,
          expectedCurrentSha256: item.currentSha256,
        })),
        signal,
      )
      if (!controller.signal.aborted) {
        setReceipt(result)
        setPreview([])
        setReloadTick((v) => v + 1)
        onRestored(result.restoredFiles.map((file) => file.path))
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      activeReq.current = null
      if (!controller.signal.aborted) setBusy(null)
    }
  }

  async function rollbackToBeforeSnapshots() {
    if (!receipt || !receipt.beforeSnapshots.length || busy) return
    const controller = new AbortController()
    activeReq.current = controller
    setBusy('rollback')
    setError('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
      const restoredByPath = new Map(receipt.restoredFiles.map((file) => [file.path, file]))
      const items = receipt.beforeSnapshots
        .map((snap) => {
          const current = restoredByPath.get(snap.path)
          return current ? {
            path: snap.path,
            targetVersionId: snap.versionId,
            expectedCurrentSha256: current.sha256,
          } : null
        })
        .filter((item): item is NonNullable<typeof item> => item !== null)
      const rolledBack = await executeBatchRestore(projectId, items, signal)
      if (!controller.signal.aborted) {
        setReceipt(rolledBack)
        setReloadTick((v) => v + 1)
        onRestored(rolledBack.restoredFiles.map((file) => file.path))
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      activeReq.current = null
      if (!controller.signal.aborted) setBusy(null)
    }
  }

  return (
    <details
      className="workspace-subpanel"
      aria-labelledby={headingId}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary id={headingId}>整段版本恢复</summary>
      <div className="workspace-subpanel-body">
        <div className="workspace-actions">
          <button
            className="button outline"
            type="button"
            disabled={loading || !!busy}
            onClick={() => {
              setPreview([])
              setReloadTick((v) => v + 1)
            }}
          >
            {loading ? '正在刷新文件版本…' : '刷新文件与历史版本'}
          </button>
          <button
            className="button outline"
            type="button"
            disabled={loading || !!busy || selectedItems.length === 0}
            onClick={() => void runPreview()}
          >
            {busy === 'preview' ? '正在预览影响…' : `预览恢复影响（已选 ${selectedItems.length} 项）`}
          </button>
        </div>
        <p className="workspace-note">
          勾选多个工作区文件及各自的历史版本，先预览差异与基线摘要，再原子执行整段恢复；恢复前版本会自动留存快照供安全回滚。
        </p>
        {error && <p className="form-error" role="alert">{error}</p>}
        {!loading && entries.length === 0 && <p className="workspace-note">暂无可恢复的工作区文件。</p>}
        {entries.length > 0 && (
          <ul className="workspace-diagnostic-list" role="list" aria-label="批量恢复文件选择">
            {entries.map((entry) => (
              <li key={entry.file.path} data-batch-restore-path={entry.file.path}>
                <label className="workflow-checkpoint">
                  <input
                    type="checkbox"
                    checked={entry.selected}
                    disabled={!!busy || entry.versions.length < 2}
                    onChange={(event) => {
                      const checked = event.currentTarget.checked
                      setEntries((prev) => prev.map((item) => item.file.path === entry.file.path
                        ? { ...item, selected: checked } : item))
                      setPreview([])
                    }}
                  />
                  <span><bdi>{entry.file.path}</bdi></span>
                </label>
                <select
                  aria-label={`选择恢复版本：${entry.file.path}`}
                  value={entry.targetVersionId}
                  disabled={!!busy || entry.versions.length < 2}
                  onChange={(event) => {
                    const nextId = event.currentTarget.value
                    setEntries((prev) => prev.map((item) => item.file.path === entry.file.path
                      ? { ...item, targetVersionId: nextId } : item))
                    setPreview([])
                  }}
                >
                  {entry.versions.map((ver, idx) => (
                    <option key={ver.versionId} value={ver.versionId}>
                      {idx === 0 ? '当前版本' : `历史版本 #${entry.versions.length - idx}`} · {ver.versionId.slice(-8)} · {versionDate.format(new Date(ver.createdAt))}（{ver.size} B）
                    </option>
                  ))}
                </select>
                {entry.versions.length < 2 && <span className="workspace-note">仅 1 个版本</span>}
              </li>
            ))}
          </ul>
        )}
        {preview.length > 0 && (
          <div className="workspace-check-report" role="region" aria-label="整段恢复影响预览">
            <h5>恢复影响预览（{preview.length} 项）</h5>
            <ul className="workspace-diagnostic-list" role="list">
              {preview.map((item) => (
                <li key={item.path} data-restore-preview-path={item.path}>
                  <strong><bdi>{item.path}</bdi></strong>
                  <span className="workspace-note">
                    当前 <code>{item.currentVersionId.slice(-8)}</code>（SHA-256 <code>{item.currentSha256.slice(0, 8)}</code>）
                    → 目标 <code>{item.targetVersionId.slice(-8)}</code>（SHA-256 <code>{item.targetSha256.slice(0, 8)}</code>）
                  </span>
                  <span>{item.summary}</span>
                </li>
              ))}
            </ul>
            <div className="workspace-actions">
              <button
                className="button primary"
                type="button"
                disabled={!!busy}
                onClick={() => void runRestore()}
              >
                {busy === 'restore' ? '正在执行整段恢复…' : '执行整段恢复'}
              </button>
              <button
                className="button outline"
                type="button"
                disabled={!!busy}
                onClick={() => setPreview([])}
              >
                取消预览
              </button>
            </div>
          </div>
        )}
        {receipt && (
          <div className="workspace-check-report" role="status" aria-label="整段恢复快照回执">
            <h5>整段恢复已完成（已保留恢复前快照）</h5>
            <ul className="workspace-diagnostic-list" role="list">
              {receipt.beforeSnapshots.map((snap, idx) => {
                const restored = receipt.restoredFiles[idx]
                return (
                  <li key={snap.path}>
                    <strong><bdi>{snap.path}</bdi></strong>
                    <span className="workspace-note">
                      恢复前快照 <code>{snap.versionId.slice(-8)}</code>（<code>{snap.sha256.slice(0, 8)}</code>）
                      {restored && <> → 新当前版本 <code>{restored.versionId.slice(-8)}</code>（<code>{restored.sha256.slice(0, 8)}</code>）</>}
                    </span>
                  </li>
                )
              })}
            </ul>
            <div className="workspace-actions">
              <button
                className="button outline"
                type="button"
                disabled={!!busy}
                onClick={() => void rollbackToBeforeSnapshots()}
              >
                {busy === 'rollback' ? '正在回滚…' : '回滚到恢复前快照'}
              </button>
            </div>
          </div>
        )}
      </div>
    </details>
  )
}
