import { useEffect, useRef, useState } from 'react'
import { ExperimentReportHandoff } from './ExperimentReportHandoff'
import { isMaterial, materialDownloadPath } from './materials'
import type { Material } from './materials'
import { ProjectDate } from './ProjectDetails'
import { errorMessage, ProjectApiError, request } from './projects'

const statusLabels = {
  PENDING: '待提交', RUNNING: '运行中', CANCELLING: '正在停止', CANCELLED: '已取消',
  SUCCEEDED: '已完成', FAILED: '失败', INTERRUPTED: '已中断',
} as const
const actionLabels = {
  snapshot: '读取本机记录', submit: '提交远程复算', refresh: '刷新远程状态',
  cancel: '请求停止远程复算', retrieve: '取回报告', result: '下载远程结果',
  artifacts: '查看远程成果', 'retrieve-scatter': '取回远程散点图', 'download-scatter': '下载远程散点图',
  'save-scatter': '保存散点图到项目资料',
} as const
const sha256Pattern = /^[a-f0-9]{64}$/i
const resultFileName = 'paired-comparison.json'
const scatterFileName = 'scatter.svg'

type RemoteJob = {
  jobId: string
  inputSha256: string
  status: keyof typeof statusLabels
  startedAt?: string
  finishedAt?: string
  resultSha256?: string
}
type RemoteScatter = { size: number; sha256: string; retrieved: boolean }
type Snapshot = { configured: boolean; job?: RemoteJob; reportRetrieved: boolean; scatter?: RemoteScatter }
type Action = keyof typeof actionLabels

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isDigest(value: unknown) {
  return typeof value === 'string' && sha256Pattern.test(value)
}

function isOptionalDate(value: unknown) {
  return value === undefined || (typeof value === 'string' && Number.isFinite(Date.parse(value)))
}

function isSnapshot(value: unknown): value is Snapshot {
  if (!isRecord(value) || typeof value.configured !== 'boolean' || typeof value.reportRetrieved !== 'boolean') return false
  const { job, scatter } = value
  if (scatter !== undefined && (!isRecord(scatter) || typeof scatter.size !== 'number'
    || !Number.isSafeInteger(scatter.size) || scatter.size <= 0 || !isDigest(scatter.sha256)
    || typeof scatter.retrieved !== 'boolean')) return false
  return job === undefined || (isRecord(job)
    && typeof job.jobId === 'string' && job.jobId.trim().length > 0
    && isDigest(job.inputSha256)
    && typeof job.status === 'string' && Object.hasOwn(statusLabels, job.status)
    && isOptionalDate(job.startedAt) && isOptionalDate(job.finishedAt)
    && (job.resultSha256 === undefined || isDigest(job.resultSha256)))
}

function readSnapshot(path: string, action: Exclude<Action, 'result' | 'download-scatter' | 'save-scatter'>, signal: AbortSignal) {
  const init = action === 'snapshot' ? { method: 'GET' } : {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
  }
  return request(action === 'snapshot' ? path : `${path}/${action}`, isSnapshot, {
    ...init, cache: 'no-store', redirect: 'error', signal,
  })
}

async function downloadResult(path: string, expectedDigest: string, signal: AbortSignal) {
  const response = await fetch(path, {
    method: 'GET', mode: 'same-origin', redirect: 'error', cache: 'no-store', headers: { Accept: 'application/json' }, signal,
  })
  if (!response.ok) throw new ProjectApiError(`项目服务返回错误（HTTP ${response.status}）。`, response.status)
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase()
  if (contentType !== 'application/json') throw new ProjectApiError('项目服务未返回 JSON 文件。', 422)

  const bytes = await response.arrayBuffer()
  await verifyDownloadDigest(response, bytes, expectedDigest, signal)
  saveDownload(new Blob([bytes], { type: 'application/json' }), resultFileName)
}

async function verifyDownloadDigest(response: Response, bytes: ArrayBuffer, expectedDigest: string, signal: AbortSignal) {
  if (!globalThis.crypto?.subtle) throw new ProjectApiError('当前浏览器无法校验远程结果的 SHA-256。', 0)
  const hash = await crypto.subtle.digest('SHA-256', bytes)
  const digest = Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, '0')).join('')
  const headerDigest = response.headers.get('X-Result-SHA256')
  if (digest !== expectedDigest.toLowerCase() || (headerDigest !== null && digest !== headerDigest.toLowerCase())) {
    throw new ProjectApiError('结果的 SHA-256 与回执不符。', 422)
  }
  signal.throwIfAborted()
}

async function downloadScatter(path: string, scatter: RemoteScatter, signal: AbortSignal) {
  const response = await fetch(path, {
    method: 'GET', mode: 'same-origin', redirect: 'error', cache: 'no-store', headers: { Accept: 'image/svg+xml' }, signal,
  })
  if (!response.ok) throw new ProjectApiError(`项目服务返回错误（HTTP ${response.status}）。`, response.status)
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase()
  const disposition = response.headers.get('Content-Disposition')?.split(';', 1)[0].trim().toLowerCase()
  if (contentType !== 'image/svg+xml' || disposition !== 'attachment') {
    throw new ProjectApiError('项目服务未返回 SVG 附件。', 422)
  }
  const bytes = await response.arrayBuffer()
  if (bytes.byteLength !== scatter.size) throw new ProjectApiError('散点图的文件大小与成果清单不符。', 422)
  await verifyDownloadDigest(response, bytes, scatter.sha256, signal)
  saveDownload(new Blob([bytes], { type: 'application/octet-stream' }), scatterFileName)
}

function saveDownload(blob: Blob, fileName: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = fileName
  link.hidden = true
  document.body.append(link)
  try {
    link.click()
  } finally {
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
  }
}

export function RemoteComparison({ projectId, taskId, onReportSaved }: {
  projectId: string
  taskId: string
  onReportSaved?: (path: string) => void
}) {
  const path = `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/remote-comparison`
  const resultPath = `${path}/result`
  const active = useRef<AbortController | null>(null)
  const automaticRefresh = useRef<AbortController | null>(null)
  const [snapshot, setSnapshot] = useState<Snapshot>()
  const [open, setOpen] = useState(false)
  const [refreshDelayed, setRefreshDelayed] = useState(false)
  const [confirmed, setConfirmed] = useState(false)
  const [listedJobId, setListedJobId] = useState<string>()
  const [savedScatter, setSavedScatter] = useState<Material>()
  const [busy, setBusy] = useState<Action>()
  const [error, setError] = useState<{ action: Action; message: string }>()
  const { configured, job, reportRetrieved } = snapshot ?? {}
  const scatter = job?.status === 'SUCCEEDED' ? snapshot?.scatter : undefined
  const canListArtifacts = configured === true && job?.status === 'SUCCEEDED'
  const canRetrieveScatter = canListArtifacts && !!scatter && !scatter.retrieved
  const scatterMissing = job?.status === 'SUCCEEDED' && job.jobId === listedJobId && !scatter
  const canSubmit = configured === true && (!job || job.status === 'PENDING')
  const canCancel = configured === true
    && (job?.status === 'PENDING' || job?.status === 'RUNNING' || job?.status === 'CANCELLING')
  const canRefresh = configured === true && (!!job || error?.action === 'submit' || error?.action === 'refresh')
  const canRetrieve = configured === true && job?.status === 'SUCCEEDED' && !reportRetrieved
  const resultDigest = job?.status === 'SUCCEEDED' && reportRetrieved ? job.resultSha256 : undefined
  const watchStatus = configured && (job?.status === 'RUNNING' || job?.status === 'CANCELLING')

  useEffect(() => () => active.current?.abort(), [])

  useEffect(() => {
    if (!open || !watchStatus) return
    const interval = 2_000
    let delay = interval
    let timer: number | undefined
    function schedule() {
      if (!document.hidden) timer = window.setTimeout(refresh, delay)
    }
    async function refresh() {
      timer = undefined
      if (document.hidden) return
      if (active.current) {
        schedule()
        return
      }
      const controller = new AbortController()
      automaticRefresh.current = controller
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
      try {
        const next = await readSnapshot(path, 'refresh', signal)
        signal.throwIfAborted()
        delay = interval
        setRefreshDelayed(false)
        setSnapshot(next)
      } catch {
        if (!controller.signal.aborted) {
          delay = Math.min(delay * 2, 16_000)
          setRefreshDelayed(true)
        }
      } finally {
        if (automaticRefresh.current === controller) {
          automaticRefresh.current = null
          schedule()
        }
      }
    }
    function reset() {
      window.clearTimeout(timer)
      automaticRefresh.current?.abort()
      automaticRefresh.current = null
    }
    function changeVisibility() {
      reset()
      delay = interval
      schedule()
    }
    schedule()
    document.addEventListener('visibilitychange', changeVisibility)
    window.addEventListener('online', changeVisibility)
    return () => {
      reset()
      document.removeEventListener('visibilitychange', changeVisibility)
      window.removeEventListener('online', changeVisibility)
    }
  }, [open, path, watchStatus])

  async function run(action: Action) {
    if (active.current) return
    if (action === 'submit' && (!confirmed || !canSubmit)) return
    if (action === 'refresh' && !canRefresh) return
    if (action === 'cancel' && !canCancel) return
    if (action === 'retrieve' && !canRetrieve) return
    if (action === 'artifacts' && !canListArtifacts) return
    if (action === 'retrieve-scatter' && !canRetrieveScatter) return
    automaticRefresh.current?.abort()
    const controller = new AbortController()
    const timeout = action === 'snapshot' || action === 'result' || action === 'download-scatter' ? 10_000 : 30_000
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)])
    active.current = controller
    setBusy(action)
    setError(undefined)
    setConfirmed(false)
    try {
      if (action === 'result') {
        if (resultDigest) await downloadResult(resultPath, resultDigest, signal)
      } else if (action === 'download-scatter') {
        if (scatter?.retrieved) await downloadScatter(`${path}/scatter`, scatter, signal)
      } else if (action === 'save-scatter') {
        if (!scatter?.retrieved) return
        const material = await request(`${path}/scatter/material`, (value): value is Material =>
          isMaterial(value) && value.sha256 === scatter.sha256 && value.size === scatter.size, { method: 'POST', signal })
        signal.throwIfAborted()
        setSavedScatter(material)
      } else {
        const next = await readSnapshot(path, action, signal)
        signal.throwIfAborted()
        if (action === 'artifacts') setListedJobId(next.job?.jobId)
        setRefreshDelayed(false)
        setSnapshot(next)
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        const label = actionLabels[action]
        const message = signal.aborted ? `${label}超时，请重试。` : `${label}失败。${errorMessage(cause)}`
        setError({ action, message })
        if (action === 'submit') {
          try {
            const latest = await readSnapshot(path, 'snapshot', AbortSignal.any([controller.signal, AbortSignal.timeout(5_000)]))
            if (!controller.signal.aborted) setSnapshot(latest)
          } catch {
            // Keep the submit error if the local snapshot cannot be read.
          }
        }
      }
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  return <details className="task-input-disclosure" data-remote-comparison={taskId} onToggle={(event) => {
    if (event.currentTarget !== event.target) return
    const expanded = event.currentTarget.open
    setOpen(expanded)
    if (expanded) void run('snapshot')
    else setConfirmed(false)
  }}>
    <summary>远程配对复算</summary>
    <div className="task-input-body" aria-busy={!!busy}>
      {busy && <p className="task-note" role="status">正在{actionLabels[busy]}…</p>}
      {error && <p className="form-error" role="alert" data-remote-comparison-error>{error.message}</p>}
      {watchStatus && refreshDelayed && <p className="task-note" role="status" data-remote-comparison-delayed>
        暂时无法查询节点，正在重试原作业。
      </p>}
      {configured === false && <p className="task-note">计算节点未配置。请在首页的「计算节点」中查看配置说明。</p>}
      {configured && !job && <p className="task-note">本机尚无远程作业回执。</p>}
      {job && <div className="task-input-body" data-remote-comparison-job={job.jobId} data-status={job.status}>
        <p className="task-note" role="status">远程状态：<strong>{statusLabels[job.status]}</strong></p>
        <p className="task-note">作业编号 <bdi>{job.jobId}</bdi></p>
        <p className="task-note">输入 SHA-256 <code>{job.inputSha256}</code></p>
        {(job.startedAt || job.finishedAt) && <p className="task-note">
          {job.startedAt && <>开始 <ProjectDate value={job.startedAt} withTime /></>}
          {job.startedAt && job.finishedAt && ' · '}
          {job.finishedAt && <>结束 <ProjectDate value={job.finishedAt} withTime /></>}
        </p>}
        {job.resultSha256 && <p className="task-note">报告{snapshot?.reportRetrieved ? '已取回' : '待取回'} · SHA-256 <code>{job.resultSha256}</code></p>}
      </div>}
      {canSubmit && <>
        {job?.status === 'PENDING' && <p className="task-note">再次提交会沿用当前作业编号。</p>}
        <label className="task-input-option task-note">
          <input type="checkbox" checked={confirmed} disabled={!!busy} data-remote-comparison-confirm
            onChange={(event) => setConfirmed(event.currentTarget.checked)} />
          我同意把本任务确认时锁定的原始 CSV 上传到部署配置的计算节点。
        </label>
      </>}
      {canRefresh && <p className="task-note">刷新状态只查询节点，不上传 CSV。</p>}
      {scatter && <p className="task-note" role="status" data-remote-comparison-scatter data-retrieved={scatter.retrieved}>
        远程散点图{scatter.retrieved ? '已取回' : '待取回'} · {scatter.size.toLocaleString('zh-CN')} 字节 · SHA-256 <code>{scatter.sha256}</code>
      </p>}
      {scatterMissing && <p className="task-note" role="status" data-remote-comparison-scatter-empty>此作业没有远程散点图。</p>}
      <div className="task-actions">
        {!snapshot && error && <button className="button outline" type="button" data-remote-comparison-retry
          disabled={!!busy} onClick={() => void run('snapshot')}>重新读取本机记录</button>}
        {canSubmit && <button className="button primary" type="button" data-remote-comparison-submit
          disabled={!!busy || !confirmed} onClick={() => void run('submit')}>
          {job ? '再次提交复算' : '提交远程复算'}
        </button>}
        {canCancel && <button className="button outline" type="button" data-remote-comparison-cancel
          disabled={!!busy} onClick={() => void run('cancel')}>
          {job?.status === 'CANCELLING' ? '重试停止' : '请求停止'}
        </button>}
        {canRefresh && <button className="button outline" type="button" data-remote-comparison-refresh
          disabled={!!busy} onClick={() => void run('refresh')}>刷新状态</button>}
        {canRetrieve && <button className="button primary" type="button" data-remote-comparison-retrieve
          disabled={!!busy} onClick={() => void run('retrieve')}>取回报告</button>}
        {resultDigest && <a className="button outline" href={resultPath} download={resultFileName}
          data-remote-comparison-result aria-disabled={!!busy} onClick={(event) => {
            event.preventDefault()
            void run('result')
          }}>下载远程结果 JSON</a>}
        {resultDigest && onReportSaved && <ExperimentReportHandoff projectId={projectId} taskId={taskId}
          remoteDigest={resultDigest} onSaved={onReportSaved} />}
        {canListArtifacts && <button className="button outline" type="button" data-remote-comparison-artifacts
          disabled={!!busy} onClick={() => void run('artifacts')}>查看远程成果</button>}
        {canRetrieveScatter && <button className="button primary" type="button" data-remote-comparison-retrieve-scatter
          disabled={!!busy} onClick={() => void run('retrieve-scatter')}>取回远程散点图</button>}
        {scatter?.retrieved && <button className="button outline" type="button" data-remote-comparison-download-scatter
          disabled={!!busy} onClick={() => void run('download-scatter')}>下载远程散点图 SVG</button>}
        {scatter?.retrieved && !savedScatter && <button className="button outline" type="button" data-remote-comparison-save-scatter
          disabled={!!busy} onClick={() => void run('save-scatter')}>保存散点图到项目资料</button>}
        {savedScatter && <a className="button outline" href={materialDownloadPath(projectId, savedScatter.materialId)} download>
          下载已保存的散点图</a>}
      </div>
    </div>
  </details>
}
