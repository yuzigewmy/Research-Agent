import { useEffect, useId, useState } from 'react'
import { errorMessage } from './projects'
import { listFiles, readVersion } from './workspace'
import type { Document, FilePage, FileRecord } from './workspace'
import type { AcceptWorkflowInput } from './workflow'

export type AcceptanceTarget = Omit<AcceptWorkflowInput, 'requestId'>

type AcceptanceProps = {
  projectId: string
  target: Omit<AcceptanceTarget, 'sourceVersionId'>
  stepTitle: string
  disabled: boolean
  pending: boolean
  retryVersionId?: string
  submissionError?: { sourceVersionId: string; message: string }
  onConfirm: (target: AcceptanceTarget) => void
}

function isEligibleFile(file: FileRecord) {
  return /\.(md|markdown)$/i.test(file.path) && file.size > 0 && file.size <= 32 * 1024
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
}

export function WorkflowStageAcceptance(props: AcceptanceProps) {
  const id = useId()
  const [open, setOpen] = useState(false)
  return <section className="workflow-acceptance" aria-label={`稿件验收：${props.stepTitle}`}>
    <button className="button outline" type="button" aria-expanded={open} aria-controls={id}
      disabled={props.pending} onClick={() => setOpen((previous) => !previous)}>
      用已保存稿件验收「{props.stepTitle}」
    </button>
    <div id={id} hidden={!open}>
      {open && <AcceptanceFiles {...props} />}
    </div>
  </section>
}

function AcceptanceFiles({ projectId, target, stepTitle, disabled, pending, retryVersionId, submissionError, onConfirm }: AcceptanceProps) {
  const id = useId()
  const [page, setPage] = useState<FilePage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selectedId, setSelectedId] = useState('')
  const selected = page.items.find((file) => file.versionId === selectedId)

  useEffect(() => {
    const controller = new AbortController()
    void listFiles(projectId, requestSignal(controller), query.cursor).then((next) => {
      if (controller.signal.aborted) return
      setPage((previous) => {
        const items = new Map((query.cursor ? previous.items : []).map((file) => [file.versionId, file]))
        for (const file of next.items) if (isEligibleFile(file)) items.set(file.versionId, file)
        return { ...next, items: [...items.values()] }
      })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  function load(cursor?: string) {
    setError('')
    setLoading(true)
    setQuery({ cursor })
  }

  return <div className="workflow-acceptance-fields">
    <div className="field">
      <label htmlFor={id}>工作区稿件</label>
      <select id={id} value={selectedId} disabled={pending || !page.items.length}
        aria-describedby={`${id}-requirements`} onChange={(event) => setSelectedId(event.currentTarget.value)}>
        <option value="">请选择稿件</option>
        {page.items.map((file) => <option key={file.versionId} value={file.versionId}>{file.path}</option>)}
      </select>
      <p className="workflow-note" id={`${id}-requirements`}>支持 32 KiB 以内的非空 Markdown 文件。</p>
    </div>
    {loading && <p className="workflow-note" role="status">正在读取工作区文件…</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loading && !error && !page.items.length && <p className="workflow-note">暂无符合条件的稿件。</p>}
    {!loading && (error || page.nextCursor) && <div className="workflow-actions">
      <button className="button outline" type="button" disabled={pending} onClick={() => load(error ? query.cursor : page.nextCursor)}>
        {error ? '重试加载稿件' : '加载更多稿件'}
      </button>
    </div>}
    {selected && <ManuscriptPreview key={selected.versionId} projectId={projectId} file={selected} stepTitle={stepTitle}
      disabled={disabled} pending={pending} retry={retryVersionId === selected.versionId}
      submissionError={submissionError?.sourceVersionId === selected.versionId ? submissionError.message : undefined}
      onConfirm={(sourceVersionId) => onConfirm({ ...target, sourceVersionId })} />}
  </div>
}

function ManuscriptPreview({ projectId, file, stepTitle, disabled, pending, retry, submissionError, onConfirm }: {
  projectId: string
  file: FileRecord
  stepTitle: string
  disabled: boolean
  pending: boolean
  retry: boolean
  submissionError?: string
  onConfirm: (sourceVersionId: string) => void
}) {
  const id = useId()
  const { path, versionId, sha256 } = file
  const [document, setDocument] = useState<Document>()
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void readVersion(projectId, versionId, requestSignal(controller)).then((next) => {
      if (controller.signal.aborted) return
      if (next.versionId !== versionId || next.path !== path || next.sha256 !== sha256) {
        setError('未能核对所选稿件版本，请重新读取。')
      } else {
        setDocument(next)
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(error))
    })
    return () => controller.abort()
  }, [attempt, path, projectId, sha256, versionId])

  if (error) return <>
    <p className="form-error" role="alert">{error}</p>
    <div className="workflow-actions">
      <button className="button outline" type="button" onClick={() => {
        setError('')
        setAttempt((value) => value + 1)
      }}>重试读取稿件</button>
    </div>
  </>
  if (!document) return <p className="workflow-note" role="status">正在读取所选稿件…</p>

  const usable = isEligibleFile(document) && !!document.content.trim() && !document.content.includes('\0')
  return <>
    <p className="workflow-note">稿件 <bdi>{document.path}</bdi></p>
    <p className="workflow-note">版本 <bdi>{document.versionId}</bdi></p>
    <pre className="workspace-text" tabIndex={0} dir="auto" aria-label="待验收的稿件原文">{document.content}</pre>
    {usable ? <>
      <p className="workflow-note" id={id}>确认后，此版本将成为「{stepTitle}」的成果。</p>
      <div className="workflow-actions">
        <button className="button primary" type="button" disabled={disabled && !pending}
          aria-disabled={pending} aria-describedby={id} onClick={() => {
            if (!disabled && !pending) onConfirm(document.versionId)
          }}>{pending ? '正在验收…' : retry ? '重试验收' : '确认验收'}</button>
      </div>
    </> : <p className="form-error" role="alert">此版本不符合验收要求，请选择其他稿件。</p>}
    {submissionError && <p className="form-error" role="alert">{submissionError}</p>}
  </>
}
