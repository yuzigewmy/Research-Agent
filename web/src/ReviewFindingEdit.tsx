import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { isFileRecord } from './workspace'
import type { Document } from './workspace'

const editLabels = {
  QUEUED: '修订已排队', RUNNING: '正在生成修订', SUCCEEDED: '修订已生成', FAILED: '生成修订失败', INTERRUPTED: '生成已中断',
} as const

export type FindingEdit = {
  editId: string
  versionId: string
  status: keyof typeof editLabels
  original: string
  replacement?: string
  reason?: string
  message?: string
  appliedVersionId?: string
}

export type ReviewFinding = {
  number: number
  line: number
  quote: string
  severity: 'major' | 'minor'
  finding: string
  suggestion: string
  edit?: FindingEdit
}

export type RevisionActions = {
  versionId: string
  disabled: boolean
  onApplying: (pending: boolean) => void
  onApplied: (document: Document, reviewId: string, findingNumber: number) => void
}

function isId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isFindingEdit(value: unknown): value is FindingEdit {
  const edit = value as Partial<FindingEdit> | null
  return !!edit && isId(edit.editId) && isId(edit.versionId) && typeof edit.original === 'string'
    && typeof edit.status === 'string' && Object.hasOwn(editLabels, edit.status)
    && [edit.replacement, edit.reason, edit.message].every((text) => text === undefined || typeof text === 'string')
    && (edit.appliedVersionId === undefined || isId(edit.appliedVersionId))
}

export function isReviewFinding(value: unknown): value is ReviewFinding {
  const finding = value as Partial<ReviewFinding> | null
  return !!finding && typeof finding.number === 'number' && Number.isSafeInteger(finding.number) && finding.number > 0
    && typeof finding.line === 'number' && Number.isSafeInteger(finding.line) && finding.line > 0
    && (finding.severity === 'major' || finding.severity === 'minor')
    && typeof finding.quote === 'string' && typeof finding.finding === 'string' && typeof finding.suggestion === 'string'
    && (finding.edit === undefined || isFindingEdit(finding.edit))
}

function isDocument(value: unknown): value is Document {
  return isFileRecord(value) && 'content' in value && typeof value.content === 'string'
}

function signalFor(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function editPath(projectId: string, versionId: string, editId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/edits/${encodeURIComponent(editId)}`
}

function isActive(edit?: FindingEdit) {
  return edit?.status === 'QUEUED' || edit?.status === 'RUNNING'
}

function mergeEdit(current: FindingEdit | undefined, latest: FindingEdit) {
  if (current?.editId !== latest.editId) return latest
  if (current.appliedVersionId || (!isActive(current) && isActive(latest))
    || (current.status === 'RUNNING' && latest.status === 'QUEUED')) return current
  return latest
}

function severityLabel(finding: ReviewFinding) {
  return finding.severity === 'major' ? '主要问题' : '次要问题'
}

export function ReviewFindings({ projectId, reviewId, route, findings, revisionActions, applying, modelConfigured, initialNumber }: {
  projectId: string
  reviewId: string
  route: string
  findings: ReviewFinding[]
  revisionActions?: RevisionActions
  applying: boolean
  modelConfigured: boolean | undefined
  initialNumber?: number
}) {
  const id = useId()
  const [selected, setSelected] = useState(initialNumber)

  return <section className="workspace-history" aria-labelledby={id}>
    <strong id={id}>评审意见</strong>
    <ul className="workspace-list" role="list">
      {findings.map((finding) => <li key={finding.number}>
        <button className="workspace-row" type="button" aria-expanded={selected === finding.number}
          aria-controls={`${id}-${finding.number}`} disabled={applying}
          onClick={() => setSelected(selected === finding.number ? undefined : finding.number)}>
          <span>意见 {finding.number} · 第 {finding.line} 行</span>
          <span className="workspace-note">{severityLabel(finding)}</span>
        </button>
      </li>)}
    </ul>
    {findings.map((finding) => <div key={finding.number} id={`${id}-${finding.number}`} hidden={selected !== finding.number}>
      <ReviewFindingEdit projectId={projectId} reviewId={reviewId} route={`${route}/findings/${finding.number}/edits`}
        finding={finding} expanded={selected === finding.number} revisionActions={revisionActions} modelConfigured={modelConfigured} />
    </div>)}
  </section>
}

export function ReviewFindingEdit({ projectId, reviewId, route, finding, expanded, revisionActions, modelConfigured }: {
  projectId: string
  reviewId: string
  route: string
  finding: ReviewFinding
  expanded: boolean
  revisionActions?: RevisionActions
  modelConfigured: boolean | undefined
}) {
  const [localEdit, setEdit] = useState<FindingEdit>()
  const edit = localEdit && finding.edit?.editId === localEdit.editId
    ? mergeEdit(localEdit, finding.edit) : localEdit ?? finding.edit
  const [pending, setPending] = useState<'generate' | 'apply'>()
  const [confirmedId, setConfirmedId] = useState<string>()
  const [conflictVersion, setConflictVersion] = useState<string>()
  const [actionError, setActionError] = useState('')
  const [readError, setReadError] = useState('')
  const mutation = useRef<{ controller: AbortController; release?: () => void } | null>(null)
  const baseVersion = revisionActions?.versionId
  const active = isActive(edit)
  const failed = edit?.status === 'FAILED' || edit?.status === 'INTERRUPTED'
  const stale = !!edit && edit.versionId !== baseVersion
  const hasConflict = conflictVersion !== undefined && conflictVersion === baseVersion
  const blocked = !revisionActions || !isId(baseVersion) || revisionActions.disabled || !!pending || hasConflict
  const canGenerate = !blocked && !!modelConfigured && !active && !edit?.appliedVersionId
    && (!edit || failed || stale || edit.replacement === undefined)
  const canApply = !blocked && !stale && edit?.status === 'SUCCEEDED'
    && edit.replacement !== undefined && !edit.appliedVersionId
  const editId = edit?.editId
  const editVersion = edit?.versionId

  useEffect(() => {
    if (!expanded || !active || !editId || !editVersion || readError) return
    const controller = new AbortController()
    let timer: number | undefined
    async function refresh(currentId: string, version: string) {
      try {
        const next = await request(editPath(projectId, version, currentId),
          (value): value is FindingEdit => isFindingEdit(value) && value.editId === currentId && value.versionId === version,
          { signal: signalFor(controller) })
        if (controller.signal.aborted) return
        setEdit(next)
        if (isActive(next)) timer = window.setTimeout(() => void refresh(currentId, version), 2_000)
      } catch (error) {
        if (!controller.signal.aborted) setReadError(errorMessage(error))
      }
    }
    timer = window.setTimeout(() => void refresh(editId, editVersion), 2_000)
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [active, editId, editVersion, expanded, projectId, readError])

  useEffect(() => () => {
    mutation.current?.controller.abort()
    mutation.current?.release?.()
  }, [])

  function showError(error: unknown, version: string) {
    if (error instanceof ProjectApiError && error.status === 409 && error.code === 'REVISION_CONFLICT') {
      setConflictVersion(version)
      setConfirmedId(undefined)
      setActionError('稿件已有更新。请重新加载稿件后再生成修订。')
    } else setActionError(errorMessage(error))
  }

  async function generate() {
    if (!canGenerate || !baseVersion || mutation.current) return
    const controller = new AbortController()
    mutation.current = { controller }
    setPending('generate')
    setActionError('')
    setConfirmedId(undefined)
    try {
      // The same inputs replay the same request. A failed attempt can be replaced explicitly.
      const previous = edit?.versionId === baseVersion ? edit.editId : 'new'
      const requestId = `finding-${reviewId}-${finding.number}-${baseVersion}-${previous}`
      const next = await request(route, (value): value is FindingEdit => isFindingEdit(value) && value.versionId === baseVersion, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ requestId, baseVersionId: baseVersion }), signal: signalFor(controller),
      })
      if (controller.signal.aborted) return
      setReadError('')
      setEdit(next)
    } catch (error) {
      if (!controller.signal.aborted) showError(error, baseVersion)
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  async function apply() {
    if (!canApply || !edit || confirmedId !== edit.editId || !revisionActions || mutation.current) return
    const controller = new AbortController()
    let released = false
    const release = () => {
      if (released) return
      released = true
      revisionActions.onApplying(false)
    }
    mutation.current = { controller, release }
    setPending('apply')
    setActionError('')
    revisionActions.onApplying(true)
    try {
      const document = await request(`${editPath(projectId, edit.versionId, edit.editId)}/apply`, isDocument, {
        method: 'POST', signal: signalFor(controller),
      })
      if (controller.signal.aborted) return
      setEdit({ ...edit, appliedVersionId: document.versionId })
      setConfirmedId(undefined)
      revisionActions.onApplied(document, reviewId, finding.number)
    } catch (error) {
      if (!controller.signal.aborted) showError(error, edit.versionId)
    } finally {
      mutation.current = null
      release()
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  if (!expanded) return null
  const needsConfirmation = confirmedId === editId && editId !== undefined
  return <div className="workspace-preview">
    <p className="workspace-note">稿件摘录</p>
    <pre className="workspace-text" tabIndex={0} dir="auto" aria-label="稿件摘录">{finding.quote}</pre>
    <p dir="auto">{finding.finding}</p>
    <p>修改建议 <bdi>{finding.suggestion}</bdi></p>
    {revisionActions?.disabled && pending !== 'apply' && <p className="workspace-note">请在编辑器就绪且稿件已保存时生成或应用修订。</p>}
    {edit && <>
      <p className="workspace-note" role="status">{edit.appliedVersionId ? '修订已应用' : editLabels[edit.status]}</p>
      {edit.message && <p className="workspace-note" dir="auto">{edit.message}</p>}
      {edit.status === 'SUCCEEDED' && <>
        <p className="workspace-note">修订原文</p>
        <pre className="workspace-text" tabIndex={0} dir="auto" aria-label="修订原文">{edit.original}</pre>
        <p className="workspace-note">建议替换文本</p>
        {edit.replacement === undefined ? <p className="workspace-note">未收到替换文本。</p>
          : edit.replacement === '' ? <p className="workspace-note">此建议将删除原文选区。</p>
            : <pre className="workspace-text" tabIndex={0} dir="auto" aria-label="建议替换文本">{edit.replacement}</pre>}
        {edit.reason && <p>修改理由 <bdi>{edit.reason}</bdi></p>}
      </>}
      {stale && revisionActions && !edit.appliedVersionId && <p className="workspace-note">此修订基于另一稿件版本。可基于当前稿件重新生成。</p>}
    </>}
    {readError && <p className="form-error" role="alert">{readError}</p>}
    {actionError && <p className="form-error" role="alert">{actionError}</p>}
    {revisionActions && !active && !edit?.appliedVersionId && (!edit || failed || stale || edit.replacement === undefined) && <>
      <p className="workspace-note">生成修订会将所引原文、邻近文字和评审意见发送给模型。</p>
      <div className="workspace-actions">
        <button className="button outline" type="button" disabled={!canGenerate} onClick={() => void generate()}>
          {pending === 'generate' ? '正在提交…' : edit ? '重新生成定向修订' : '生成定向修订'}
        </button>
      </div>
    </>}
    {readError && <div className="workspace-actions">
      <button className="button outline" type="button" onClick={() => setReadError('')}>重新查询修订</button>
    </div>}
    {revisionActions && edit?.status === 'SUCCEEDED' && !edit.appliedVersionId && !stale && edit.replacement !== undefined && (
      needsConfirmation ? <div className="workspace-preview" role="group" aria-label="确认应用修订">
        <p>确认用建议文本替换原文，并保存为新版本？</p>
        <div className="workspace-actions">
          <button className="button primary" type="button" disabled={!canApply} onClick={() => void apply()}>
            {pending === 'apply' ? '正在应用…' : '确认应用'}
          </button>
          <button className="button outline" type="button" disabled={!!pending} onClick={() => setConfirmedId(undefined)}>取消</button>
        </div>
      </div> : <div className="workspace-actions">
        <button className="button primary" type="button" disabled={!canApply} onClick={() => setConfirmedId(edit.editId)}>应用到稿件</button>
      </div>
    )}
  </div>
}
