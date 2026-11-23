import { useEffect, useId, useRef, useState } from 'react'
import { errorMessage } from './projects'
import { applySelectionEdit, getSelectionEdit, isSelectionEditVersionConflict, SelectionEditPreview } from './SelectionEdit'
import type { SelectionEditRecord } from './SelectionEdit'
import type { TaskInputVersion, TaskStatus } from './tasks'
import { readVersion } from './workspace'

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

export function TaskSelectionEdit({ projectId, taskId, source, status, revision, refreshVersion }: {
  projectId: string
  taskId: string
  source: TaskInputVersion
  status: TaskStatus
  revision: number
  refreshVersion: number
}) {
  const { path, versionId, sha256 } = source
  const fileLabelId = useId()
  const [edit, setEdit] = useState<SelectionEditRecord>()
  const [saved, setSaved] = useState<Pick<TaskInputVersion, 'versionId' | 'sha256'>>()
  const [readError, setReadError] = useState('')
  const [versionError, setVersionError] = useState('')
  const [applyError, setApplyError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [applying, setApplying] = useState(false)
  const [reload, setReload] = useState(0)
  const submission = useRef<AbortController | null>(null)
  const preview = useRef<HTMLDivElement>(null)
  const appliedVersionId = saved?.versionId ?? edit?.appliedVersionId
  const applicable = status === 'SUCCEEDED' && edit?.status === 'SUCCEEDED'
    && !edit.unchanged && edit.replacement !== undefined && !appliedVersionId
  const canApply = applicable && !conflict && !applying && !readError

  useEffect(() => {
    const controller = new AbortController()
    void getSelectionEdit(projectId, versionId, taskId, requestSignal(controller)).then((next) => {
      if (controller.signal.aborted) return
      setEdit((previous) => previous?.appliedVersionId && !next.appliedVersionId
        ? { ...next, appliedVersionId: previous.appliedVersionId } : next)
      setReadError('')
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setReadError(errorMessage(error))
    })
    return () => controller.abort()
  }, [projectId, versionId, taskId, status, revision, refreshVersion, reload])

  useEffect(() => {
    if (!appliedVersionId || appliedVersionId === saved?.versionId) return
    const controller = new AbortController()
    void readVersion(projectId, appliedVersionId, requestSignal(controller)).then((doc) => {
      if (controller.signal.aborted) return
      if (doc.versionId !== appliedVersionId || doc.path !== path) {
        setVersionError('无法核对已应用的稿件版本。')
        return
      }
      setSaved({ versionId: doc.versionId, sha256: doc.sha256 })
      setVersionError('')
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setVersionError(errorMessage(error))
    })
    return () => controller.abort()
  }, [appliedVersionId, path, projectId, saved?.versionId, refreshVersion, reload])

  useEffect(() => () => submission.current?.abort(), [])

  async function apply() {
    if (!canApply || submission.current) return
    const controller = new AbortController()
    const control = document.activeElement
    submission.current = controller
    setApplying(true)
    setApplyError('')
    try {
      const doc = await applySelectionEdit(projectId, versionId, taskId, requestSignal(controller), path)
      if (controller.signal.aborted) return
      setSaved({ versionId: doc.versionId, sha256: doc.sha256 })
      if (document.activeElement === control) preview.current?.focus()
    } catch (error) {
      if (controller.signal.aborted) return
      if (isSelectionEditVersionConflict(error)) {
        setConflict(true)
        setApplyError('稿件已有新版本，原修改建议仍保留。请重新加载最新稿件，重新选择要修改的文字。')
      } else setApplyError(errorMessage(error))
    } finally {
      submission.current = null
      if (!controller.signal.aborted) setApplying(false)
    }
  }

  return <div className="workspace-preview" role="group" aria-labelledby={fileLabelId} ref={preview} tabIndex={-1}>
    <p className="task-note">
      <span id={fileLabelId}>固定选区来自 <bdi>{path}</bdi></span> · 版本 <bdi>{versionId}</bdi> · SHA-256 <code title={sha256}>{sha256.slice(0, 12)}</code>
    </p>
    {!edit && !readError && <p className="task-note" role="status">正在读取稿件选区…</p>}
    {edit && <SelectionEditPreview edit={edit} />}
    {(readError || versionError) && <div className="task-input-body">
      {readError && <p className="form-error" role="alert">{readError}</p>}
      {versionError && <p className="form-error" role="alert">{versionError}</p>}
      <div className="task-actions">
        <button className="button outline" type="button" aria-describedby={fileLabelId} disabled={applying} onClick={() => {
          setReadError('')
          setVersionError('')
          setReload((value) => value + 1)
        }}>重新查询建议</button>
      </div>
    </div>}
    {applicable && (
      <div className="task-actions">
        <button className="button primary" type="button" aria-describedby={fileLabelId}
          disabled={conflict || !!readError} aria-disabled={applying}
          onClick={() => void apply()}>{applying ? '正在应用…' : '应用到稿件'}</button>
      </div>
    )}
    {applyError && !appliedVersionId && <p className="form-error" role="alert">{applyError}</p>}
    {appliedVersionId && <div className="task-input-body">
      <p className="task-note" role="status">
        建议已应用。新版本 <bdi>{appliedVersionId}</bdi>
        {saved && <> · SHA-256 <code title={saved.sha256}>{saved.sha256.slice(0, 12)}</code></>}
      </p>
      <p className="task-note"><a href="#project-workspace">前往文本工作区</a>，重新加载稿件以查看已保存的版本。</p>
    </div>}
  </div>
}
