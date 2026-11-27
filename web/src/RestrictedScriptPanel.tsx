import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ProjectDate } from './ProjectDetails'
import { errorMessage } from './projects'
import {
  cancelScriptRun, createScriptRun, getScriptRun, isScriptRunActive, listScriptRuns, publishScriptArtifact,
  scriptRunStatusLabels,
} from './scriptRuns'
import type { ScriptRun, ScriptRunLimits } from './scriptRuns'
import { listFiles } from './workspace'
import type { FileRecord } from './workspace'
import './workspace.css'
import './tasks.css'

function newRequestId() {
  return `script-${crypto.randomUUID()}`
}

function formatKiB(bytes: number) {
  if (bytes >= 1024 * 1024 && bytes % (1024 * 1024) === 0) return `${bytes / (1024 * 1024)} MiB`
  if (bytes >= 1024 && bytes % 1024 === 0) return `${bytes / 1024} KiB`
  return `${bytes} 字节`
}

function mergeRun(items: ScriptRun[], updated: ScriptRun) {
  const index = items.findIndex((item) => item.runId === updated.runId)
  if (index === -1) return [updated, ...items]
  return items.map((item) => item.runId === updated.runId ? updated : item)
}

export function RestrictedScriptPanel({ projectId, onArtifactPublished }: {
  projectId: string
  onArtifactPublished?: (path: string) => void
}) {
  const id = useId()
  const [open, setOpen] = useState(false)
  const [configured, setConfigured] = useState(false)
  const [limits, setLimits] = useState<ScriptRunLimits>()
  const [runs, setRuns] = useState<ScriptRun[]>([])
  const [files, setFiles] = useState<FileRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [scriptVersionId, setScriptVersionId] = useState('')
  const [inputVersionId, setInputVersionId] = useState('')
  const [timeoutSeconds, setTimeoutSeconds] = useState('10')
  const [confirmed, setConfirmed] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [actionError, setActionError] = useState('')
  const [cancellingId, setCancellingId] = useState('')
  const [publishingKey, setPublishingKey] = useState('')
  const [published, setPublished] = useState<Record<string, FileRecord>>({})
  const [publishErrors, setPublishErrors] = useState<Record<string, string>>({})
  const pendingRequestId = useRef('')
  const mutationController = useRef<AbortController | null>(null)

  const pythonFiles = useMemo(() => files.filter(({ path }) => /\.py$/i.test(path)), [files])
  const activeRun = useMemo(() => runs.find((run) => isScriptRunActive(run.status)), [runs])

  const loadAll = useCallback(async (signal: AbortSignal) => {
    setLoading(true)
    setLoadError('')
    try {
      const [runsResult, filesResult] = await Promise.all([
        listScriptRuns(projectId, signal),
        listFiles(projectId, signal),
      ])
      if (signal.aborted) return
      setConfigured(runsResult.configured)
      setLimits(runsResult.limits)
      setRuns(runsResult.items)
      setFiles(filesResult.items)
      const pyFiles = filesResult.items.filter(({ path }) => /\.py$/i.test(path))
      setScriptVersionId((current) => pyFiles.some(({ versionId }) => versionId === current) ? current : (pyFiles[0]?.versionId ?? ''))
      setInputVersionId((current) => filesResult.items.some(({ versionId }) => versionId === current) ? current : '')
    } catch (cause: unknown) {
      if (!signal.aborted) setLoadError(errorMessage(cause))
    } finally {
      if (!signal.aborted) setLoading(false)
    }
  }, [projectId])

  useEffect(() => {
    setRuns([])
    setFiles([])
    setScriptVersionId('')
    setInputVersionId('')
    setConfirmed(false)
    setActionError('')
    setLoadError('')
    setPublished({})
    setPublishErrors({})
    pendingRequestId.current = ''
  }, [projectId])

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    void loadAll(controller.signal)
    return () => controller.abort()
  }, [open, loadAll])

  useEffect(() => {
    if (!open || !activeRun) return
    const controller = new AbortController()
    const timer = window.setInterval(() => {
      void getScriptRun(projectId, activeRun.runId, controller.signal).then((updated) => {
        if (!controller.signal.aborted) setRuns((current) => mergeRun(current, updated))
      }).catch(() => {
        // Keep polling on transient read failures until the user closes or refreshes.
      })
    }, 500)
    return () => {
      window.clearInterval(timer)
      controller.abort()
    }
  }, [open, projectId, activeRun?.runId])

  useEffect(() => () => mutationController.current?.abort(), [])

  function refresh() {
    const controller = new AbortController()
    void loadAll(controller.signal)
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!configured || submitting || !scriptVersionId || !confirmed || activeRun) return
    const parsedTimeout = Number.parseInt(timeoutSeconds, 10)
    const minTimeout = limits?.minTimeoutSeconds ?? 1
    const maxTimeout = limits?.maxTimeoutSeconds ?? 30
    if (!Number.isInteger(parsedTimeout) || parsedTimeout < minTimeout || parsedTimeout > maxTimeout) {
      setActionError(`超时时间须为 ${minTimeout} 至 ${maxTimeout} 秒之间的整数。`)
      return
    }
    if (!pendingRequestId.current) pendingRequestId.current = newRequestId()
    const controller = new AbortController()
    mutationController.current = controller
    setSubmitting(true)
    setActionError('')
    try {
      const created = await createScriptRun(projectId, {
        requestId: pendingRequestId.current,
        scriptVersionId,
        inputVersionId: inputVersionId || undefined,
        timeoutSeconds: parsedTimeout,
        confirmed: true,
      }, controller.signal)
      if (controller.signal.aborted) return
      pendingRequestId.current = ''
      setConfirmed(false)
      setRuns((current) => mergeRun(current, created))
    } catch (cause: unknown) {
      if (!controller.signal.aborted) setActionError(errorMessage(cause))
    } finally {
      mutationController.current = null
      if (!controller.signal.aborted) setSubmitting(false)
    }
  }

  async function handleCancel(runId: string) {
    if (cancellingId) return
    const controller = new AbortController()
    setCancellingId(runId)
    setActionError('')
    try {
      const updated = await cancelScriptRun(projectId, runId, controller.signal)
      setRuns((current) => mergeRun(current, updated))
    } catch (cause: unknown) {
      setActionError(errorMessage(cause))
    } finally {
      setCancellingId('')
    }
  }

  async function handlePublish(runId: string, name: string, sha256: string) {
    const key = `${runId}:${name}`
    if (publishingKey) return
    const controller = new AbortController()
    setPublishingKey(key)
    setPublishErrors((current) => {
      const next = { ...current }
      delete next[key]
      return next
    })
    try {
      const file = await publishScriptArtifact(projectId, runId, name, sha256, controller.signal)
      setPublished((current) => ({ ...current, [key]: file }))
      onArtifactPublished?.(file.path)
    } catch (cause: unknown) {
      setPublishErrors((current) => ({ ...current, [key]: errorMessage(cause) }))
    } finally {
      setPublishingKey('')
    }
  }

  return (
    <section className="workspace-panel" aria-labelledby={`${id}-heading`}>
      <details className="task-input-disclosure" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary id={`${id}-heading`}>受限本机脚本</summary>
        <div className="task-input-body">
          <div className="workspace-actions">
            <button className="button outline" type="button" disabled={loading} onClick={refresh}>
              {loading ? '正在刷新…' : '刷新文件与记录'}
            </button>
          </div>
          {loadError && <p className="form-error" role="alert">{loadError}</p>}
          {!loading && !loadError && !configured && (
            <p className="workspace-note" role="status">
              服务端尚未启用受限本机脚本运行器。请由部署方显式配置环境变量 <code>RESEARCH_SCRIPT_PYTHON</code> 后重启服务。
            </p>
          )}
          {configured && limits && (
            <>
              <p className="workspace-note">
                隔离运行器（Python <code>-I -B</code>、禁用网络） · 超时 {limits.minTimeoutSeconds}–{limits.maxTimeoutSeconds} 秒 ·
                脚本上限 {formatKiB(limits.maxScriptBytes)} · 输入上限 {formatKiB(limits.maxInputBytes)} ·
                日志各上限 {formatKiB(limits.maxLogBytes)} · 产物最多 {limits.maxOutputFiles} 份（单份 {formatKiB(limits.maxOutputFileBytes)}，总计 {formatKiB(limits.maxOutputTotalBytes)}）
              </p>
              <form className="create-form" onSubmit={(event) => void handleSubmit(event)} noValidate aria-busy={submitting}>
                <div className="field">
                  <label htmlFor={`${id}-script`}>脚本文件（.py 版本）</label>
                  <select id={`${id}-script`} value={scriptVersionId} disabled={submitting || !pythonFiles.length}
                    onChange={(event) => {
                      setScriptVersionId(event.currentTarget.value)
                      pendingRequestId.current = ''
                      setActionError('')
                    }}>
                    {!pythonFiles.length && <option value="">请先在文本工作区保存 .py 脚本</option>}
                    {pythonFiles.map((file) => (
                      <option key={file.versionId} value={file.versionId}>
                        {file.path}（版本 {file.versionId.slice(0, 8)} · {file.sha256.slice(0, 8)}）
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field">
                  <label htmlFor={`${id}-input`}>可选输入文件版本</label>
                  <select id={`${id}-input`} value={inputVersionId} disabled={submitting}
                    onChange={(event) => {
                      setInputVersionId(event.currentTarget.value)
                      pendingRequestId.current = ''
                      setActionError('')
                    }}>
                    <option value="">不使用输入文件</option>
                    {files.map((file) => (
                      <option key={file.versionId} value={file.versionId}>
                        {file.path}（版本 {file.versionId.slice(0, 8)} · {file.sha256.slice(0, 8)}）
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field">
                  <label htmlFor={`${id}-timeout`}>时限（秒，{limits.minTimeoutSeconds}–{limits.maxTimeoutSeconds}）</label>
                  <input id={`${id}-timeout`} type="number" min={limits.minTimeoutSeconds} max={limits.maxTimeoutSeconds}
                    value={timeoutSeconds} disabled={submitting}
                    onChange={(event) => {
                      setTimeoutSeconds(event.currentTarget.value)
                      pendingRequestId.current = ''
                      setActionError('')
                    }} />
                </div>
                <label className="task-input-option" htmlFor={`${id}-confirm`}>
                  <input id={`${id}-confirm`} type="checkbox" checked={confirmed} disabled={submitting}
                    onChange={(event) => {
                      setConfirmed(event.currentTarget.checked)
                      setActionError('')
                    }} />
                  <span>我已核对脚本与输入版本，明确授权在本机无网络临时沙箱中运行</span>
                </label>
                {actionError && <p className="form-error" role="alert">{actionError}</p>}
                {activeRun && <p className="workspace-note" role="status">当前项目已有运行中的脚本（{activeRun.runId.slice(0, 8)}），请等待完成或先停止。</p>}
                <div className="workspace-actions">
                  <button className="button primary" type="submit"
                    disabled={submitting || !scriptVersionId || !confirmed || Boolean(activeRun)}>
                    {submitting ? '正在提交…' : '运行脚本'}
                  </button>
                </div>
              </form>
            </>
          )}

          {runs.length > 0 && (
            <ul className="task-list" aria-label="受限脚本运行记录">
              {runs.map((run) => {
                const active = isScriptRunActive(run.status)
                return (
                  <li key={run.runId} className="task-card" data-script-run-id={run.runId} data-script-run-status={run.status}>
                    <div className="task-card-header">
                      <div className="task-status" role="status">
                        <strong>{scriptRunStatusLabels[run.status]}</strong>
                        {run.phase && <span className="task-note"> · {run.phase}</span>}
                      </div>
                      <div className="task-card-meta task-note">
                        <span>时限 {run.timeoutSeconds} 秒</span>
                        {run.exitCode !== undefined && <span>退出码 {run.exitCode}</span>}
                        <ProjectDate value={run.createdAt} withTime />
                      </div>
                    </div>
                    <p className="task-note">
                      脚本：<bdi>{run.scriptPath}</bdi> · SHA-256 <code title={run.scriptSha256}>{run.scriptSha256.slice(0, 12)}</code>
                      {run.inputPath && (
                        <> · 输入：<bdi>{run.inputPath}</bdi> · SHA-256 <code title={run.inputSha256}>{run.inputSha256?.slice(0, 12)}</code></>
                      )}
                    </p>
                    <p className="task-note">
                      运行编号 <code>{run.runId}</code>
                      {run.startedAt && <> · 开始 <ProjectDate value={run.startedAt} withTime /></>}
                      {run.finishedAt && <> · 结束 <ProjectDate value={run.finishedAt} withTime /></>}
                    </p>
                    {run.errorMessage && <p className="form-error" role="alert">{run.errorMessage}</p>}
                    {active && (
                      <div className="task-actions">
                        <button className="button outline" type="button"
                          disabled={cancellingId === run.runId || run.status === 'CANCELLING'}
                          onClick={() => void handleCancel(run.runId)}>
                          {cancellingId === run.runId || run.status === 'CANCELLING' ? '正在停止…' : '停止脚本'}
                        </button>
                      </div>
                    )}
                    {run.stdout && (
                      <div>
                        <p className="task-note">标准输出（stdout）{run.stdoutTruncated ? '（已按 16 KiB 截断）' : ''}</p>
                        <pre className="workspace-text">{run.stdout}</pre>
                      </div>
                    )}
                    {run.stderr && (
                      <div>
                        <p className="task-note">标准错误（stderr）{run.stderrTruncated ? '（已按 16 KiB 截断）' : ''}</p>
                        <pre className="workspace-text">{run.stderr}</pre>
                      </div>
                    )}
                    {run.artifacts.length > 0 && (
                      <div className="workspace-bundle">
                        <h5>待发布产物（{run.artifacts.length}）</h5>
                        <ul className="task-input-list">
                          {run.artifacts.map((artifact) => {
                            const key = `${run.runId}:${artifact.name}`
                            const saved = published[key]
                            const pubErr = publishErrors[key]
                            return (
                              <li key={artifact.name} data-script-artifact-name={artifact.name}>
                                <div>
                                  <strong><bdi>{artifact.name}</bdi></strong>
                                  <span className="task-note"> · {artifact.size} 字节 · SHA-256 <code data-artifact-sha256={artifact.sha256}>{artifact.sha256}</code></span>
                                </div>
                                <div className="workspace-actions">
                                  <button className="button outline" type="button"
                                    disabled={run.status !== 'SUCCEEDED' || publishingKey === key}
                                    onClick={() => void handlePublish(run.runId, artifact.name, artifact.sha256)}>
                                    {publishingKey === key ? '正在校验发布…' : '校验并发布到工作区'}
                                  </button>
                                  {saved && (
                                    <span className="task-note" role="status">
                                      已发布至 <bdi>{saved.path}</bdi>（版本 {saved.versionId.slice(0, 8)}）
                                    </span>
                                  )}
                                </div>
                                {pubErr && <p className="form-error" role="alert">{pubErr}</p>}
                              </li>
                            )
                          })}
                        </ul>
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </details>
    </section>
  )
}
