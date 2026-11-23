import { useId, useLayoutEffect, useRef, useState } from 'react'
import { ProjectApiError } from './projects'
import { readWorkflowPatentWord, workflowPatentFigurePath } from './workflow'

type FigureArtifact = { projectId: string; runId: string; artifactId: string }
const wordLabel = '下载带附图的 Word'

export function WorkflowPatentFigureLinks({ projectId, runId, artifactId, wordEnabled, versionLabelId }: FigureArtifact & {
  wordEnabled?: boolean
  versionLabelId?: string
}) {
  return <>
    <a className="workflow-download" href={workflowPatentFigurePath(projectId, runId, artifactId, 'system')} download>
      下载系统框图 SVG
    </a>
    <a className="workflow-download" href={workflowPatentFigurePath(projectId, runId, artifactId, 'process')} download>
      下载操作流程图 SVG
    </a>
    {wordEnabled && <PatentWordDownload key={JSON.stringify([projectId, runId, artifactId])}
      projectId={projectId} runId={runId} artifactId={artifactId} versionLabelId={versionLabelId} />}
  </>
}

function PatentWordDownload({ projectId, runId, artifactId, versionLabelId }: FigureArtifact & { versionLabelId?: string }) {
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
      const word = await readWorkflowPatentWord(projectId, runId, artifactId, signal)
      signal.throwIfAborted()
      const link = document.createElement('a')
      const url = URL.createObjectURL(word)
      link.href = url
      link.download = 'patent-disclosure-with-figures.docx'
      link.hidden = true
      try {
        document.body.append(link)
        link.click()
      } finally {
        link.remove()
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
      }
    } catch (cause) {
      if (controller.signal.aborted) return
      if (signal.aborted) setError('生成带附图的 Word 超时，请重试。')
      else if (cause instanceof ProjectApiError) setError(cause.message)
      else setError('无法下载带附图的 Word，请重试。')
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  const describedBy = [versionLabelId, error ? id : undefined].filter(Boolean).join(' ') || undefined
  return <>
    <button className="button outline" type="button" disabled={busy} aria-busy={busy}
      aria-describedby={describedBy} onClick={() => void download()}>
      {busy ? '正在生成 Word…' : wordLabel}
    </button>
    {error && <p className="form-error" id={id} role="alert">{error}</p>}
  </>
}
