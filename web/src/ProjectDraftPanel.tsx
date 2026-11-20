import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import {
  createProjectDraft, finalizeProjectDraft, getProjectDraft, listProjectDrafts, planProjectDraft, saveProjectDraft,
} from './projectDrafts'
import type { ProjectDraft, ProjectDraftFields } from './projectDrafts'
import { errorMessage, isTemplateId, ProjectApiError, templates } from './projects'
import type { Project } from './projects'

const encoder = new TextEncoder()
const draftDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
const textFields = [
  ['goal', '研究目标'], ['deliverables', '预期交付物'], ['clarification', '补充说明'],
] as const

type Operation = 'create' | 'read' | 'save' | 'plan' | 'finalize'
type DraftEdit = { draft: ProjectDraft; fields: ProjectDraftFields }
type Props = { onCreated: (project: Project) => void }

function requestSignal(controller: AbortController, timeout = 10_000) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)])
}

function draftFields({ title, templateId, goal, deliverables, clarification }: ProjectDraftFields): ProjectDraftFields {
  return { title: title.trim(), templateId, goal: goal.trim(), deliverables: deliverables.trim(), clarification: clarification.trim() }
}

function mergeDrafts(current: ProjectDraft[], incoming: ProjectDraft[]) {
  const byId = new Map(current.map((draft) => [draft.draftId, draft]))
  for (const draft of incoming) {
    const previous = byId.get(draft.draftId)
    if (!previous || previous.revision <= draft.revision) byId.set(draft.draftId, draft)
  }
  return [...byId.values()].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || b.draftId.localeCompare(a.draftId))
}

export function ProjectDraftPanel({ onCreated }: Props) {
  const [opened, setOpened] = useState(false)
  return <details className="workspace-panel" onToggle={(event) => {
    if (event.currentTarget.open) setOpened(true)
  }}>
    <summary className="turn-role">用自然语言建项</summary>
    {opened && <DraftWorkspace onCreated={onCreated} />}
  </details>
}

function DraftWorkspace({ onCreated }: Props) {
  const id = useId()
  const editorHeading = useRef<HTMLHeadingElement>(null)
  const mutation = useRef<AbortController | null>(null)
  const attempt = useRef<{ key: string; requestId: string } | undefined>(undefined)
  const [drafts, setDrafts] = useState<ProjectDraft[]>([])
  const [cursor, setCursor] = useState<string>()
  const [page, setPage] = useState<{ cursor?: string }>({})
  const [listing, setListing] = useState(true)
  const [listError, setListError] = useState('')
  const [modelConfigured, setConfigured] = useState<boolean>()
  const [prompt, setPrompt] = useState('')
  const [edit, setEdit] = useState<DraftEdit>()
  const [pending, setPending] = useState<Operation>()
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [pollError, setPollError] = useState<{ draftId: string; message: string }>()

  const draft = edit?.draft
  const fields = edit && draftFields(edit.fields)
  const dirty = !!draft && JSON.stringify(draftFields(draft)) !== JSON.stringify(fields)
  const planningId = draft?.status === 'PLANNING' ? draft.draftId : undefined
  const invalidTitle = !!fields && (Array.from(fields.title).length > 200 || fields.title.includes('\0'))
  const invalidText = (text: string) => encoder.encode(text).length > 4 * 1024 || text.includes('\0')
  const invalidFields = invalidTitle || (!!fields && textFields.some(([key]) => invalidText(fields[key])))
  const editable = draft?.status === 'DRAFT' && !pending
  const trimmedPrompt = prompt.trim()
  const invalidPrompt = encoder.encode(trimmedPrompt).length > 8 * 1024 || trimmedPrompt.includes('\0')
  const visibleDrafts = drafts.filter((item) => item.status !== 'CREATED')

  useEffect(() => {
    const controller = new AbortController()
    void listProjectDrafts(requestSignal(controller), page.cursor).then((next) => {
      if (controller.signal.aborted) return
      setDrafts((current) => mergeDrafts(current, next.items))
      setCursor(next.nextCursor)
      setConfigured(next.modelConfigured)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setListError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setListing(false)
    })
    return () => controller.abort()
  }, [page])

  useEffect(() => {
    if (!planningId) return
    const controller = new AbortController()
    let timer: number | undefined
    async function poll(draftId: string) {
      try {
        const next = await getProjectDraft(draftId, requestSignal(controller))
        if (controller.signal.aborted) return
        setDrafts((current) => mergeDrafts(current, [next]))
        setEdit((current) => current?.draft.draftId === draftId && current.draft.revision <= next.revision
          ? { draft: next, fields: draftFields(next) } : current)
        setPollError(undefined)
        if (next.status !== 'PLANNING') return
      } catch (cause) {
        if (controller.signal.aborted) return
        setPollError({ draftId, message: errorMessage(cause) })
      }
      timer = window.setTimeout(() => void poll(draftId), 2_000)
    }
    timer = window.setTimeout(() => void poll(planningId), 2_000)
    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [planningId])

  useEffect(() => () => mutation.current?.abort(), [])
  useEffect(() => { if (draft?.draftId) editorHeading.current?.focus() }, [draft?.draftId])

  function loadPage(nextCursor?: string) {
    setListing(true)
    setListError('')
    setPage({ cursor: nextCursor })
  }

  async function run(kind: Operation, draftId?: string) {
    if (mutation.current) return
    if (kind === 'create' && (dirty || !trimmedPrompt || invalidPrompt)) return
    if (kind === 'read' && !draftId) return
    if (kind === 'save' || kind === 'plan' || kind === 'finalize') {
      if (!editable || !fields || conflict) return
      if (kind === 'save' && (!dirty || invalidFields)) return
      if (kind !== 'save' && dirty) return
      if (kind === 'plan' && modelConfigured !== true) return
      if (kind === 'finalize' && (!fields.title || !fields.goal)) return
    }
    const controller = new AbortController()
    mutation.current = controller
    setPending(kind)
    setError('')
    try {
      const signal = requestSignal(controller, kind === 'plan' ? 105_000 : undefined)
      function requestId(values: unknown[]) {
        const key = JSON.stringify([kind, ...values])
        if (attempt.current?.key !== key) attempt.current = { key, requestId: crypto.randomUUID() }
        return attempt.current.requestId
      }
      let next: ProjectDraft
      if (kind === 'read' && draftId) {
        next = await getProjectDraft(draftId, signal)
      } else if (kind === 'create') {
        next = await createProjectDraft({ requestId: requestId([trimmedPrompt]), prompt: trimmedPrompt }, signal)
      } else if (draft && fields) {
        const expectedRevision = draft.revision
        const input = { requestId: requestId([draft.draftId, expectedRevision, kind === 'save' ? fields : null]), expectedRevision }
        if (kind === 'finalize') {
          const project = await finalizeProjectDraft(draft.draftId, input, signal)
          if (controller.signal.aborted) return
          attempt.current = undefined
          setDrafts((current) => mergeDrafts(current, [{ ...draft, status: 'CREATED', revision: expectedRevision + 1 }]))
          setEdit(undefined)
          onCreated(project)
          return
        }
        if (kind === 'save') next = await saveProjectDraft(draft.draftId, { ...input, ...fields }, signal)
        else next = await planProjectDraft(draft.draftId, input, signal)
      } else return
      if (controller.signal.aborted) return
      if (kind !== 'read') attempt.current = undefined
      if (kind === 'create') setPrompt('')
      setDrafts((current) => mergeDrafts(current, [next]))
      setEdit({ draft: next, fields: draftFields(next) })
      setConflict(false)
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(errorMessage(cause))
      if (cause instanceof ProjectApiError) {
        if (cause.code === 'MODEL_UNAVAILABLE') setConfigured(false)
        if (cause.status === 409 && kind !== 'read') {
          attempt.current = undefined
          if (kind !== 'create') setConflict(true)
        }
      }
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    void run('create')
  }

  return <div className="workspace-preview">
    <form className="field" onSubmit={create}>
      <label htmlFor={`${id}-prompt`}>研究描述</label>
      <textarea className="workspace-text" id={`${id}-prompt`} rows={3} value={prompt} readOnly={!!pending}
        aria-invalid={invalidPrompt || undefined} aria-describedby={`${id}-prompt-hint`}
        onChange={(event) => setPrompt(event.currentTarget.value)} />
      <p id={`${id}-prompt-hint`} className={invalidPrompt ? 'form-error' : 'workspace-note'}>
        {invalidPrompt ? '研究描述须在 8 KiB 以内且不含空字符。' : '先保存研究描述。之后可选择用模型整理草稿。'}
      </p>
      <div className="workspace-actions"><button className="button outline" type="submit"
        disabled={!!pending || dirty || !trimmedPrompt || invalidPrompt}>{pending === 'create' ? '正在创建草稿…' : '创建草稿'}</button></div>
    </form>
    <h3>已保存草稿</h3>
    {visibleDrafts.length > 0 && <ul className="workspace-list" role="list" aria-label="已保存草稿">
      {visibleDrafts.map((item) => <li key={item.draftId}>
        <button className="workspace-row" type="button" disabled={!!pending || dirty}
          aria-current={item.draftId === draft?.draftId ? 'true' : undefined}
          onClick={() => void run('read', item.draftId)}>
          <bdi>{item.title || item.prompt.slice(0, 60)}</bdi>
          <time className="workspace-note" dateTime={item.updatedAt}>{draftDate.format(new Date(item.updatedAt))}</time>
        </button>
      </li>)}
    </ul>}
    {listing && <p className="workspace-note" role="status">正在读取草稿列表…</p>}
    {pending === 'read' && <p className="workspace-note" role="status">正在读取草稿…</p>}
    {listError && <p className="form-error" role="alert">{listError}</p>}
    {!listing && !listError && !visibleDrafts.length && <p className="workspace-note">暂无未创建项目的草稿。</p>}
    {!listing && (listError || cursor || modelConfigured === false) && <div className="workspace-actions">
      {listError ? <button className="button outline" type="button" onClick={() => loadPage(page.cursor)}>重试读取草稿列表</button>
        : cursor && <button className="button outline" type="button" onClick={() => loadPage(cursor)}>加载更多草稿</button>}
      {modelConfigured === false && <button className="button outline" type="button" onClick={() => loadPage()}>刷新模型状态</button>}
    </div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {edit && draft && <section className="workspace-preview" aria-labelledby={`${id}-editor`}>
      <h3 id={`${id}-editor`} ref={editorHeading} tabIndex={-1}>项目草稿</h3>
      {draft.status === 'CREATED' && draft.projectId ? (
        <a className="button outline" href={`?${new URLSearchParams({ project: draft.projectId })}`}>打开已创建项目</a>
      ) : <>
        {planningId || pending === 'plan' ? <p className="workspace-note" role="status">正在整理建项草稿…</p> : null}
        {pollError?.draftId === planningId && pollError && <p className="form-error" role="alert">{pollError.message}</p>}
        {draft.message && <p className="workspace-note" role="status" dir="auto">{draft.message}</p>}
        {draft.question && <p className="workspace-note">待补充的问题 <bdi>{draft.question}</bdi></p>}
        <details><summary>研究描述原文</summary><p className="turn-content" dir="auto">{draft.prompt}</p></details>
        <div className="field">
          <label htmlFor={`${id}-title`}>项目题名</label>
          <input id={`${id}-title`} value={edit.fields.title} readOnly={!editable} aria-invalid={invalidTitle || undefined}
            onChange={(event) => setEdit({ ...edit, fields: { ...edit.fields, title: event.currentTarget.value } })} />
          {invalidTitle && <p className="form-error">项目题名须在 200 个字符以内且不含空字符。</p>}
        </div>
        <div className="field">
          <label htmlFor={`${id}-template`}>项目模板</label>
          <select id={`${id}-template`} value={edit.fields.templateId} disabled={!editable} onChange={(event) => {
            const templateId = event.currentTarget.value
            if (isTemplateId(templateId)) setEdit({ ...edit, fields: { ...edit.fields, templateId } })
          }}>
            {Object.entries(templates).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
          </select>
        </div>
        {textFields.map(([key, label]) => {
          const invalid = invalidText(fields?.[key] ?? '')
          return <div className="field" key={key}>
            <label htmlFor={`${id}-${key}`}>{label}</label>
            <textarea className="workspace-text" id={`${id}-${key}`} rows={2} value={edit.fields[key]} readOnly={!editable}
              aria-invalid={invalid || undefined} onChange={(event) => setEdit({ ...edit, fields: { ...edit.fields, [key]: event.currentTarget.value } })} />
            {invalid && <p className="form-error">{label}须在 4 KiB 以内且不含空字符。</p>}
          </div>
        })}
        {modelConfigured === false && <p className="workspace-note">可手工填写并创建项目。配置研究模型后可整理草稿。</p>}
        <p className="workspace-note">{conflict ? '草稿已有更新，请加载最新版本。' : dirty ? '请先保存修改，再整理草稿或创建项目。'
          : '填写题名和研究目标后即可创建。整理草稿会将研究描述及已保存的表单发送给研究模型。'}</p>
        <div className="workspace-actions">
          {conflict ? <button className="button outline" type="button" disabled={!!pending} onClick={() => void run('read', draft.draftId)}>
            {dirty ? '放弃修改并读取最新草稿' : '读取最新草稿'}
          </button> : <>
            <button className="button outline" type="button" disabled={!editable || !dirty || invalidFields}
              onClick={() => void run('save')}>{pending === 'save' ? '正在保存…' : '保存草稿'}</button>
            {dirty && <button className="button outline" type="button" disabled={!editable}
              onClick={() => setEdit({ draft, fields: draftFields(draft) })}>放弃未保存修改</button>}
            <button className="button outline" type="button" disabled={!editable || dirty || modelConfigured !== true}
              onClick={() => void run('plan')}>整理建项草稿</button>
            <button className="button primary" type="button" disabled={!editable || dirty || !fields?.title || !fields.goal}
              onClick={() => void run('finalize')}>{pending === 'finalize' ? '正在创建…' : '创建项目'}</button>
          </>}
        </div>
      </>}
    </section>}
  </div>
}
