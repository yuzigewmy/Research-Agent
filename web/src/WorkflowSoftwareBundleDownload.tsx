import { useId, useLayoutEffect, useRef, useState } from 'react'
import { ProjectApiError } from './projects'
import { readWorkflowSoftwareBundle } from './workflow'
import type { WorkflowRun } from './workflow'

const requiredSteps = ['sources', 'manuscript', 'program', 'application']
const label = '下载登记材料 ZIP'

type BundleTarget = { projectId: string; runId: string; revision: number }

export function WorkflowSoftwareBundleDownload({ projectId, run }: { projectId: string; run: WorkflowRun }) {
  if (!run.software || run.status !== 'COMPLETED' || !requiredSteps.every((key) =>
    run.steps.some((step) => step.key === key && step.status === 'SUCCEEDED' && !!step.artifactId))) return null

  return <RunBundleDownload key={JSON.stringify([projectId, run.runId, run.revision])}
    projectId={projectId} runId={run.runId} revision={run.revision} />
}

function RunBundleDownload({ projectId, runId, revision }: BundleTarget) {
  const id = useId()
  const active = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useLayoutEffect(() => () => active.current?.abort(), [])

  async function download() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)])
    setBusy(true)
    setError('')
    try {
      const bundle = await readWorkflowSoftwareBundle(projectId, runId, revision, signal)
      signal.throwIfAborted()
      const url = URL.createObjectURL(bundle)
      const link = document.createElement('a')
      link.href = url
      link.download = 'software-registration.zip'
      link.hidden = true
      document.body.append(link)
      try {
        link.click()
      } finally {
        link.remove()
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
      }
    } catch (cause) {
      if (controller.signal.aborted) return
      if (signal.aborted) setError('下载登记材料 ZIP 超时，请重试。')
      else if (cause instanceof ProjectApiError) setError(cause.message)
      else setError('无法完成登记材料 ZIP 下载，请重试。')
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <div className="workflow-actions">
    <button className="button outline" type="button" disabled={busy} aria-busy={busy}
      aria-describedby={error ? `${id}-help ${id}-error` : `${id}-help`} onClick={() => void download()}>
      {busy ? '正在打包…' : label}
    </button>
    <p className="workflow-note" id={`${id}-help`}>清单会注明因组件未配置而略过的 PDF 或 Word 格式。</p>
    {error && <p className="form-error" id={`${id}-error`} role="alert">{error}</p>}
  </div>
}
