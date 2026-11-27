import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { isFileRecord } from './workspace'
import type { FileRecord } from './workspace'

const editDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' })

export interface EditHistoryItem {
  editId: string
  kind: 'selection' | 'batch' | 'review_finding'
  path: string
  status: string
  instruction: string
  original: string
  replacement?: string
  reason?: string
  sourceVersionId: string
  sourceSha256: string
  appliedVersionId?: string
  appliedSha256?: string
  reviewId?: string
  findingNumber?: number
  createdAt: string
  canUndo: boolean
}

interface EditHistoryPage {
  items: EditHistoryItem[]
  hasMore: boolean
}

interface EditHistoryUndoResult {
  editId: string
  path: string
  sourceVersionId: string
  beforeSnapshot: FileRecord
  restoredVersion: FileRecord
}

const kindLabels: Record<EditHistoryItem['kind'], string> = {
  selection: '选区改稿',
  batch: '批量改稿',
  review_finding: '审稿定向修订',
}

const statusLabels: Record<string, string> = {
  APPLIED: '已应用',
  SUCCEEDED: '待应用',
  QUEUED: '排队中',
  RUNNING: '生成中',
  FAILED: '失败',
  CANCELLED: '已取消',
  INTERRUPTED: '已中断',
}

function isEditHistoryItem(value: unknown): value is EditHistoryItem {
  const item = value as Partial<EditHistoryItem> | null
  return !!item && typeof item.editId === 'string' && typeof item.path === 'string'
    && (item.kind === 'selection' || item.kind === 'batch' || item.kind === 'review_finding')
    && typeof item.status === 'string' && typeof item.sourceVersionId === 'string'
    && typeof item.sourceSha256 === 'string' && typeof item.createdAt === 'string'
    && typeof item.canUndo === 'boolean'
}

function isEditHistoryPage(value: unknown): value is EditHistoryPage {
  const page = value as Partial<EditHistoryPage> | null
  return !!page && Array.isArray(page.items) && page.items.every(isEditHistoryItem)
    && typeof page.hasMore === 'boolean'
}

function isEditHistoryUndoResult(value: unknown): value is EditHistoryUndoResult {
  const res = value as Partial<EditHistoryUndoResult> | null
  return !!res && typeof res.editId === 'string' && typeof res.path === 'string'
    && typeof res.sourceVersionId === 'string'
    && isFileRecord(res.beforeSnapshot) && isFileRecord(res.restoredVersion)
}

function listEditHistory(
  projectId: string,
  filters: { path: string; status: string },
  signal: AbortSignal,
) {
  const params = new URLSearchParams()
  if (filters.path) params.set('path', filters.path)
  if (filters.status) params.set('status', filters.status)
  params.set('limit', '50')
  return request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/edit-history?${params}`,
    isEditHistoryPage,
    { cache: 'no-store', signal },
  )
}

function undoEditHistory(
  projectId: string,
  editId: string,
  options: { force?: boolean },
  signal: AbortSignal,
) {
  return request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/edit-history/${encodeURIComponent(editId)}/undo`,
    isEditHistoryUndoResult,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(options),
      signal,
    },
  )
}

export function EditHistoryPanel({ projectId, onUndone }: {
  projectId: string
  onUndone: (path: string) => void
}) {
  const headingId = useId()
  const [open, setOpen] = useState(false)
  const [pathInput, setPathInput] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [appliedPath, setAppliedPath] = useState('')
  const [reloadTick, setReloadTick] = useState(0)
  const [loading, setLoading] = useState(false)
  const [items, setItems] = useState<EditHistoryItem[]>([])
  const [error, setError] = useState('')
  const [undoingId, setUndoingId] = useState<string | null>(null)
  const [forceConfirmId, setForceConfirmId] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const activeUndo = useRef<AbortController | null>(null)

  useEffect(() => () => activeUndo.current?.abort(), [])

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setLoading(true)
    setError('')
    void listEditHistory(projectId, { path: appliedPath, status: statusFilter }, signal).then((page) => {
      if (!controller.signal.aborted) setItems(page.items)
    }).catch((cause) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, open, appliedPath, statusFilter, reloadTick])

  async function runUndo(item: EditHistoryItem, force: boolean) {
    if (undoingId) return
    const controller = new AbortController()
    activeUndo.current = controller
    setUndoingId(item.editId)
    setError('')
    setNotice('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
      const result = await undoEditHistory(projectId, item.editId, { force }, signal)
      if (!controller.signal.aborted) {
        setForceConfirmId(null)
        setNotice(`已撤销「${result.path}」的此项改动，原稿内容已保存为新版本 ${result.restoredVersion.versionId.slice(-8)}（恢复前快照 ${result.beforeSnapshot.versionId.slice(-8)} 已保留）。`)
        setReloadTick((v) => v + 1)
        onUndone(result.path)
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        if (cause instanceof ProjectApiError && cause.status === 409 && cause.code === 'VERSION_CONFLICT') {
          setForceConfirmId(item.editId)
        }
        setError(errorMessage(cause))
      }
    } finally {
      activeUndo.current = null
      if (!controller.signal.aborted) setUndoingId(null)
    }
  }

  return (
    <details
      className="workspace-subpanel"
      aria-labelledby={headingId}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary id={headingId}>编辑历史</summary>
      <div className="workspace-subpanel-body">
        <p className="workspace-note">
          编辑历史独立持久化于项目记录，清空会话或开启新一轮对话后仍完整可查，支持按文件路径和状态筛选并逐项撤销已应用改动。
        </p>
        <form
          className="workspace-actions"
          onSubmit={(event) => {
            event.preventDefault()
            setAppliedPath(pathInput.trim())
          }}
        >
          <div className="field">
            <label htmlFor={`${headingId}-path`}>按文件路径筛选</label>
            <input
              id={`${headingId}-path`}
              type="search"
              value={pathInput}
              placeholder="例如 paper/main.md"
              onChange={(event) => setPathInput(event.currentTarget.value)}
            />
          </div>
          <div className="field">
            <label htmlFor={`${headingId}-status`}>按状态筛选</label>
            <select
              id={`${headingId}-status`}
              value={statusFilter}
              onChange={(event) => setStatusFilter(event.currentTarget.value)}
            >
              <option value="">全部状态</option>
              <option value="APPLIED">已应用（APPLIED）</option>
              <option value="SUCCEEDED">待应用（SUCCEEDED）</option>
              <option value="RUNNING">生成中（RUNNING）</option>
              <option value="FAILED">失败（FAILED）</option>
            </select>
          </div>
          <button className="button outline" type="submit" disabled={loading}>筛选</button>
          <button
            className="button outline"
            type="button"
            disabled={loading}
            onClick={() => setReloadTick((v) => v + 1)}
          >
            {loading ? '正在刷新…' : '刷新历史'}
          </button>
        </form>
        {error && <p className="form-error" role="alert">{error}</p>}
        {notice && <p className="workspace-note" role="status">{notice}</p>}
        {!loading && items.length === 0 && (
          <p className="workspace-note" role="status">当前筛选条件下暂无编辑历史记录。</p>
        )}
        {items.length > 0 && (
          <ul className="workspace-diagnostic-list" role="list" aria-label="编辑历史列表">
            {items.map((item) => (
              <li key={item.editId} data-edit-history-id={item.editId}>
                <div>
                  <strong><bdi>{item.path}</bdi></strong>{' '}
                  <span className="workspace-note">
                    {kindLabels[item.kind]} · {statusLabels[item.status] ?? item.status} ·{' '}
                    <time dateTime={item.createdAt}>{editDate.format(new Date(item.createdAt))}</time>
                  </span>
                </div>
                <div className="workspace-note">
                  原稿版本 <code>{item.sourceVersionId.slice(-8)}</code>
                  {item.appliedVersionId && <> → 应用后版本 <code>{item.appliedVersionId.slice(-8)}</code></>}
                  {item.instruction && <> · 修改要求：{item.instruction}</>}
                </div>
                {item.reason && <div className="workspace-note">修改说明：{item.reason}</div>}
                {item.canUndo && (
                  <div className="workspace-actions">
                    <button
                      className="button outline"
                      type="button"
                      disabled={undoingId !== null}
                      onClick={() => void runUndo(item, false)}
                    >
                      {undoingId === item.editId ? '正在撤销…' : '撤销此改动'}
                    </button>
                    {forceConfirmId === item.editId && (
                      <button
                        className="button outline"
                        type="button"
                        disabled={undoingId !== null}
                        onClick={() => void runUndo(item, true)}
                      >
                        确认忽略后续版本并恢复原稿
                      </button>
                    )}
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </details>
  )
}
