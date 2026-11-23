import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError, request } from './projects'
import { isFileRecord } from './workspace'
import type { Document } from './workspace'

export interface SelectionEditRange {
  startByte: number
  endByte: number
  text: string
}

interface SelectionEditProps {
  projectId: string
  versionId: string
  selection?: SelectionEditRange | null
  disabled: boolean
  onApplying: (pending: boolean) => void
  onApplied: (document: Document) => void
}

const statusLabels = {
  PROPOSED: '待确认',
  QUEUED: '建议已排队',
  RUNNING: '正在生成建议…',
  CANCELLING: '正在取消',
  CANCELLED: '建议生成已取消',
  SUCCEEDED: '建议已生成',
  FAILED: '生成建议失败',
  INTERRUPTED: '生成已中断',
} as const
const selectionLimit = 8 * 1024
export const selectionEditInstructionLimit = 2 * 1024
const encoder = new TextEncoder()

export type SelectionEditRecord = {
  editId: string
  versionId: string
  status: keyof typeof statusLabels
  original: string
  replacement?: string
  reason?: string
  message?: string
  appliedVersionId?: string
}

type CreateEdit = Pick<SelectionEditRange, 'startByte' | 'endByte'> & {
  requestId: string
  instruction: string
}

function validId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

function isEditRecord(value: unknown): value is SelectionEditRecord {
  const edit = value as Partial<SelectionEditRecord> | null
  return !!edit && validId(edit.editId) && validId(edit.versionId) && typeof edit.original === 'string'
    && typeof edit.status === 'string' && Object.hasOwn(statusLabels, edit.status)
    && (edit.replacement === undefined || typeof edit.replacement === 'string')
    && (edit.reason === undefined || typeof edit.reason === 'string')
    && (edit.message === undefined || typeof edit.message === 'string')
    && (edit.appliedVersionId === undefined || validId(edit.appliedVersionId))
}

function isDocument(value: unknown): value is Document {
  return isFileRecord(value) && 'content' in value && typeof value.content === 'string'
}

function active(edit?: SelectionEditRecord) {
  return edit?.status === 'QUEUED' || edit?.status === 'RUNNING' || edit?.status === 'CANCELLING'
}

function readSavedEdit(key: string) {
  try {
    const editId = sessionStorage.getItem(key)
    return validId(editId) ? editId : undefined
  } catch {
    return undefined
  }
}

function saveEdit(key: string, editId?: string) {
  try {
    if (editId) sessionStorage.setItem(key, editId)
    else sessionStorage.removeItem(key)
  } catch {
    // Browsing restrictions can disable storage. The live suggestion still works.
  }
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function editsPath(projectId: string, versionId: string) {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/edits`
}

function editPath(projectId: string, versionId: string, editId: string) {
  return `${editsPath(projectId, versionId)}/${encodeURIComponent(editId)}`
}

export function getSelectionEdit(projectId: string, versionId: string, editId: string, signal: AbortSignal) {
  return request(editPath(projectId, versionId, editId), (value): value is SelectionEditRecord =>
    isEditRecord(value) && value.editId === editId && value.versionId === versionId, { signal, cache: 'no-store' })
}

export function applySelectionEdit(projectId: string, versionId: string, editId: string, signal: AbortSignal, expectedPath?: string) {
  return request(`${editPath(projectId, versionId, editId)}/apply`, (value): value is Document =>
    isDocument(value) && (expectedPath === undefined || value.path === expectedPath), { method: 'POST', signal })
}

export function isSelectionEditVersionConflict(error: unknown) {
  return error instanceof ProjectApiError && error.status === 409
    && (error.code === 'VERSION' || error.code === 'VERSION_CONFLICT' || error.code === 'REVISION_CONFLICT')
}

export function SelectionEditPreview({ edit }: { edit: SelectionEditRecord }) {
  return <>
    <p>选区原文</p>
    <pre className="workspace-text" tabIndex={0} aria-label="选区原文" dir="auto">{edit.original}</pre>
    {edit.status === 'SUCCEEDED' && <>
      <p>建议替换文本</p>
      {edit.replacement === undefined ? <p className="form-error" role="alert">未收到替换文本，请重新生成建议。</p>
        : edit.replacement === '' ? <p className="workspace-note">此建议会删除整个选区。</p>
        : <pre className="workspace-text" tabIndex={0} aria-label="建议替换文本" dir="auto">{edit.replacement}</pre>}
      {edit.reason && <p className="workspace-note">修改理由：<bdi>{edit.reason}</bdi></p>}
    </>}
  </>
}

export function SelectionEdit(props: SelectionEditProps) {
  const { projectId, versionId } = props
  if (!validId(projectId) || !validId(versionId)) return null
  return <VersionSelectionEdit key={editsPath(projectId, versionId)} {...props} />
}

function VersionSelectionEdit({ projectId, versionId, selection, disabled, onApplying, onApplied }: SelectionEditProps) {
  const route = editsPath(projectId, versionId)
  const id = useId()
  const storageKey = `research.selection-edit:${route}`
  const [editId, setEditId] = useState(() => readSavedEdit(storageKey))
  const [edit, setEdit] = useState<SelectionEditRecord>()
  const [instruction, setInstruction] = useState('')
  const [pending, setPending] = useState<'create' | 'apply'>()
  const [loadError, setLoadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [conflict, setConflict] = useState(false)
  const mutation = useRef<AbortController | null>(null)
  const attempt = useRef<CreateEdit | undefined>(undefined)
  const canRead = !!editId && (!edit || active(edit)) && !loadError
  const selectedBytes = selection ? selection.endByte - selection.startByte : 0
  const hasSelection = !!selection?.text && Number.isSafeInteger(selection.startByte)
    && Number.isSafeInteger(selection.endByte) && selection.startByte >= 0 && selectedBytes > 0
  const cleanedInstruction = instruction.trim()
  const instructionTooLong = encoder.encode(cleanedInstruction).length > selectionEditInstructionLimit
  const generationBlocked = disabled || !!pending || (!!editId && !edit) || active(edit) || conflict
  const canGenerate = !generationBlocked && hasSelection && selectedBytes <= selectionLimit
    && !!cleanedInstruction && !instructionTooLong
  const canApply = !disabled && !pending && !conflict && edit?.status === 'SUCCEEDED'
    && edit.replacement !== undefined && !edit.appliedVersionId

  useEffect(() => {
    if (!editId || !canRead) return
    const currentId = editId
    const controller = new AbortController()
    let timer: number | undefined

    async function poll() {
      try {
        const next = await getSelectionEdit(projectId, versionId, currentId, requestSignal(controller))
        if (controller.signal.aborted) return
        setEdit(next)
        if (active(next)) timer = window.setTimeout(() => void poll(), 2_000)
      } catch (error) {
        if (!controller.signal.aborted) setLoadError(errorMessage(error))
      }
    }

    void poll()
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [canRead, editId, projectId, versionId])

  useEffect(() => () => mutation.current?.abort(), [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canGenerate || !selection || mutation.current) return
    const controller = new AbortController()
    mutation.current = controller
    setPending('create')
    setActionError('')
    try {
      const { startByte, endByte } = selection
      const previous = attempt.current
      const input = previous?.startByte === startByte && previous.endByte === endByte
        && previous.instruction === cleanedInstruction ? previous
        : { startByte, endByte, instruction: cleanedInstruction, requestId: crypto.randomUUID() }
      attempt.current = input
      const next = await request(route, (value): value is SelectionEditRecord =>
        isEditRecord(value) && value.versionId === versionId, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input), signal: requestSignal(controller),
      })
      if (controller.signal.aborted) return
      attempt.current = undefined
      setLoadError('')
      setEdit(next)
      setEditId(next.editId)
      saveEdit(storageKey, next.editId)
    } catch (error) {
      if (!controller.signal.aborted) setActionError(errorMessage(error))
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  async function apply() {
    if (!canApply || !edit || mutation.current) return
    const controller = new AbortController()
    mutation.current = controller
    setPending('apply')
    onApplying(true)
    setActionError('')
    try {
      const next = await applySelectionEdit(projectId, versionId, edit.editId, requestSignal(controller))
      if (controller.signal.aborted) return
      setEdit({ ...edit, appliedVersionId: next.versionId })
      onApplied(next)
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof ProjectApiError && error.status === 409 && error.code === 'REVISION_CONFLICT') {
        setConflict(true)
      } else setActionError(errorMessage(error))
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) {
        onApplying(false)
        setPending(undefined)
      }
    }
  }

  function dismissUnavailable() {
    saveEdit(storageKey)
    setEdit(undefined)
    setEditId(undefined)
    setLoadError('')
  }

  return <section className="workspace-history" aria-labelledby={`${id}-heading`}>
    <strong id={`${id}-heading`}>选区改稿</strong>
    <form className="field" onSubmit={(event) => void submit(event)}>
      <label htmlFor={`${id}-instruction`}>修改要求</label>
      <input id={`${id}-instruction`} value={instruction} required readOnly={!!pending}
        aria-describedby={`${id}-disclosure`} aria-invalid={instructionTooLong || undefined}
        onChange={(event) => setInstruction(event.currentTarget.value)} />
      <p className="workspace-note" id={`${id}-disclosure`}>生成建议会将原文选区、邻近文本和修改要求发送给模型。</p>
      {disabled ? <p className="workspace-note">保存稿件修改后可生成或应用建议。</p>
        : !hasSelection ? <p className="workspace-note">请在已保存的稿件中选中要修改的文字。</p>
        : selectedBytes > selectionLimit ? <p className="form-error">选区超过 8 KiB，请缩小选区。</p>
        : <p className="workspace-note">已选中 {selectedBytes} 字节。</p>}
      {instructionTooLong && <p className="form-error" role="alert">修改要求不能超过 2 KiB。</p>}
      <div className="workspace-actions">
        <button className="button outline" type="submit" disabled={!canGenerate}>
          {pending === 'create' ? '正在提交…' : '生成修改建议'}
        </button>
      </div>
    </form>
    {editId && !edit && !loadError && <p className="workspace-note" role="status">正在加载修改建议…</p>}
    {loadError && <div className="workspace-preview">
      <p className="form-error" role="alert">{loadError}</p>
      <div className="workspace-actions">
        <button className="button outline" type="button" onClick={() => setLoadError('')}>重新查询建议</button>
        <button className="button outline" type="button" onClick={dismissUnavailable}>关闭这条建议</button>
      </div>
    </div>}
    {edit && <div className="workspace-preview">
      <p className="workspace-note" role="status">{edit.appliedVersionId ? '建议已应用' : statusLabels[edit.status]}</p>
      {edit.message && <p className="workspace-note" dir="auto">{edit.message}</p>}
      <SelectionEditPreview edit={edit} />
      {edit.status === 'SUCCEEDED' && !edit.appliedVersionId && (
        <button className="button primary" type="button" disabled={!canApply}
          onClick={() => void apply()}>{pending === 'apply' ? '正在应用…' : '应用到稿件'}</button>
      )}
    </div>}
    {conflict && <p className="form-error" role="alert">稿件已有更新，建议仍保留。请重新加载稿件，选中要修改的文字后再生成建议。</p>}
    {actionError && <p className="form-error" role="alert">{actionError}</p>}
  </section>
}
