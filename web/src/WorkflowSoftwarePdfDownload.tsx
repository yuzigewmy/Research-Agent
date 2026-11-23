import { useId, useLayoutEffect, useRef, useState } from 'react'
import { ProjectApiError } from './projects'
import { readWorkflowSoftwarePdf } from './workflow'
import type { SoftwarePdfStep } from './workflow'

type DownloadProps = {
  projectId: string
  runId: string
  stepKey: SoftwarePdfStep
  stepTitle: string
  artifactId: string
  versionLabel?: string
}

export function WorkflowSoftwarePdfDownload(props: DownloadProps) {
  const { projectId, runId, stepKey, artifactId } = props
  return <ArtifactPdfDownload key={JSON.stringify([projectId, runId, stepKey, artifactId])} {...props} />
}

function ArtifactPdfDownload({ projectId, runId, stepKey, stepTitle, artifactId, versionLabel }: DownloadProps) {
  const id = useId()
  const active = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const label = versionLabel ? '下载此版本 PDF' : '下载 PDF'
  useLayoutEffect(() => () => active.current?.abort(), [])

  async function download() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)])
    setBusy(true)
    setError('')
    try {
      const pdf = await readWorkflowSoftwarePdf(projectId, runId, stepKey, artifactId, signal)
      signal.throwIfAborted()
      const url = URL.createObjectURL(pdf)
      const link = document.createElement('a')
      link.href = url
      link.download = `software-registration-${stepKey}.pdf`
      link.hidden = true
      document.body.append(link)
      try {
        link.click()
      } finally {
        link.remove()
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        const message = cause instanceof ProjectApiError ? cause.message : '无法完成 PDF 下载。'
        setError(signal.aborted ? '下载 PDF 超时。' : message)
      }
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <>
    <button className="button outline" type="button" disabled={busy} aria-busy={busy}
      aria-label={busy ? undefined : `${label} · ${stepTitle}${versionLabel ? ` · ${versionLabel}` : ''}`}
      aria-describedby={error ? id : undefined} onClick={() => void download()}>
      {busy ? '正在读取 PDF…' : label}
    </button>
    {error && <p className="form-error" id={id} role="alert">{error}请再次点击「{label}」重试。</p>}
  </>
}
