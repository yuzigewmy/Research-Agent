import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, ProjectApiError } from './projects'
import {
  changeQuestionnaireStatus, getQuestionnaire, normalizeQuestionnaire, questionnaireCsvPath,
  questionnaireLink, questionnaireProblem, questionnaireQuestionLimit, saveQuestionnaire, saveQuestionnaireResponses,
} from './questionnaires'
import type { Question, QuestionnaireDefinition, QuestionnaireSnapshot } from './questionnaires'
import './questionnaire.css'

const statusLabels = { DRAFT: '草稿', PUBLISHED: '已发布 · 正在回收', CLOSED: '已关闭回收' }
type Operation = 'read' | 'save' | 'publish' | 'close' | 'export'

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function QuestionEditor({ question, index, disabled, onChange, onRemove }: {
  question: Question
  index: number
  disabled: boolean
  onChange: (question: Question) => void
  onRemove: () => void
}) {
  const id = useId()
  return <fieldset className="questionnaire-question field" disabled={disabled}>
    <legend>第 {index + 1} 题</legend>
    <label htmlFor={`${id}-label`}>题干</label>
    <input id={`${id}-label`} value={question.label} dir="auto" required
      onChange={(event) => onChange({ ...question, label: event.currentTarget.value })} />
    <label htmlFor={`${id}-type`}>题型</label>
    <select id={`${id}-type`} value={question.type} onChange={(event) => {
      const type = event.currentTarget.value === 'single' ? 'single' : 'text'
      onChange({ ...question, type, options: type === 'single' ? ['', ''] : undefined })
    }}>
      <option value="single">单选题</option>
      <option value="text">文本题</option>
    </select>
    {question.type === 'single' && <>
      <label htmlFor={`${id}-options`}>单选选项</label>
      <textarea id={`${id}-options`} rows={3} dir="auto" value={question.options?.join('\n') ?? ''} aria-describedby={`${id}-help`}
        onChange={(event) => onChange({ ...question, options: event.currentTarget.value.split('\n') })} />
      <p id={`${id}-help`} className="questionnaire-note">每行填写一个选项，共 2 至 12 项。每项最多 120 个 UTF-8 字节。</p>
    </>}
    <div className="questionnaire-actions">
      <label className="questionnaire-choice">
        <input type="checkbox" checked={question.required}
          onChange={(event) => onChange({ ...question, required: event.currentTarget.checked })} />
        必答
      </label>
      <button className="button outline" type="button" aria-label={`删除第 ${index + 1} 题`} onClick={onRemove}>删除题目</button>
    </div>
  </fieldset>
}

export function QuestionnairePanel({ projectId, archived = false }: { projectId: string; archived?: boolean }) {
  const id = useId()
  const [snapshot, setSnapshot] = useState<QuestionnaireSnapshot>()
  const [draft, setDraft] = useState<QuestionnaireDefinition>()
  const [pending, setPending] = useState<Operation>()
  const [error, setError] = useState('')
  const [feedback, setFeedback] = useState('')
  const [needsRefresh, setNeedsRefresh] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const active = useRef<AbortController | null>(null)
  const busy = pending !== undefined
  const definition = draft ?? snapshot
  const published = snapshot?.status === 'PUBLISHED'
  const editable = snapshot?.status === 'DRAFT' && !archived
  const publicUrl = published && snapshot.publicId && !archived
    ? new URL(questionnaireLink(snapshot.publicId), window.location.origin).href : undefined

  useEffect(() => {
    const controller = new AbortController()
    void getQuestionnaire(projectId, requestSignal(controller)).then((next) => {
      if (!controller.signal.aborted) setSnapshot(next)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    })
    return () => controller.abort()
  }, [projectId])

  useEffect(() => () => active.current?.abort(), [])

  function update(next: QuestionnaireDefinition) {
    setDraft({ title: next.title, intro: next.intro, questions: next.questions })
    if (!needsRefresh) setError('')
    setFeedback('')
  }

  function addQuestion() {
    if (!definition || definition.questions.length >= questionnaireQuestionLimit) return
    const ids = new Set(definition.questions.map(({ id: questionId }) => questionId))
    let number = 1
    while (ids.has(`q${number}`)) number++
    update({ ...definition, questions: [...definition.questions, {
      id: `q${number}`, label: '', type: 'single', required: true, options: ['', ''],
    }] })
  }

  async function perform(operation: Operation, nextDefinition?: QuestionnaireDefinition) {
    if (active.current || (operation !== 'read' && !snapshot)) return
    const controller = new AbortController()
    active.current = controller
    setPending(operation)
    setError('')
    setFeedback('')
    try {
      const signal = requestSignal(controller)
      if (operation === 'read') {
        const next = await getQuestionnaire(projectId, signal)
        if (controller.signal.aborted) return
        setSnapshot(next)
        setNeedsRefresh(false)
        if (next.status !== 'DRAFT') setDraft(undefined)
        setConfirmClose(false)
        if (draft && next.status === 'DRAFT') setFeedback('已刷新问卷版本。本地草稿已保留，请核对后保存。')
      } else if (operation === 'export') {
        const material = await saveQuestionnaireResponses(projectId, signal)
        if (!controller.signal.aborted) setFeedback(`已保存 ${material.name}。请在「问卷分析」点击「刷新文件列表」。`)
      } else if (snapshot) {
        const next = operation === 'save' && nextDefinition
          ? await saveQuestionnaire(projectId, snapshot.revision, nextDefinition, signal)
          : operation === 'publish' || operation === 'close'
            ? await changeQuestionnaireStatus(projectId, operation, snapshot.revision, signal) : undefined
        if (controller.signal.aborted || !next) return
        setSnapshot(next)
        setDraft(undefined)
        setConfirmClose(false)
        setFeedback(operation === 'save' ? '问卷草稿已保存。' : operation === 'publish' ? '问卷已发布，可以分享填写链接。' : '已停止回收答卷。')
      }
    } catch (cause) {
      if (controller.signal.aborted) return
      if (cause instanceof ProjectApiError && cause.status === 409) setNeedsRefresh(true)
      setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setPending(undefined)
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!definition || !editable || busy || needsRefresh) return
    const normalized = normalizeQuestionnaire(definition)
    const problem = questionnaireProblem(normalized)
    if (problem) setError(problem)
    else void perform('save', normalized)
  }

  return <section className="questionnaire-panel" aria-labelledby={`${id}-heading`}>
    <header className="questionnaire-heading">
      <h4 id={`${id}-heading`}>互动问卷</h4>
      {snapshot && <p className="questionnaire-note" role="status">{statusLabels[snapshot.status]}</p>}
    </header>
    {!snapshot && !error && <p className="questionnaire-note" role="status">正在读取问卷…</p>}
    {editable && definition && <form className="create-form" noValidate onSubmit={submit} aria-busy={busy}>
      <div className="field">
        <label htmlFor={`${id}-title`}>问卷标题</label>
        <input id={`${id}-title`} dir="auto" value={definition.title} required readOnly={busy}
          onChange={(event) => update({ ...definition, title: event.currentTarget.value })} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-intro`}>卷首语</label>
        <textarea id={`${id}-intro`} rows={3} dir="auto" value={definition.intro} readOnly={busy}
          onChange={(event) => update({ ...definition, intro: event.currentTarget.value })} />
      </div>
      {definition.questions.map((question, index) => <QuestionEditor key={question.id} question={question} index={index}
        disabled={busy} onChange={(next) => update({ ...definition,
          questions: definition.questions.map((item) => item.id === next.id ? next : item),
        })} onRemove={() => update({ ...definition, questions: definition.questions.filter((item) => item.id !== question.id) })} />)}
      <div className="questionnaire-actions">
        <button className="button outline" type="button" disabled={busy || definition.questions.length >= questionnaireQuestionLimit}
          onClick={addQuestion}>添加题目</button>
        <button className="button primary" type="submit" disabled={busy || needsRefresh}>
          {pending === 'save' ? '正在保存…' : '保存问卷'}
        </button>
        <button className="button outline" type="button" disabled={busy || needsRefresh || !!draft || !snapshot.revision}
          onClick={() => void perform('publish')}>{pending === 'publish' ? '正在发布…' : '发布问卷'}</button>
      </div>
      <p className="questionnaire-note">发布前请保存问卷。发布后题目会固定，填写者可通过链接提交答卷。</p>
    </form>}
    {snapshot && !editable && <>
      <p><strong dir="auto">{snapshot.title}</strong></p>
      {snapshot.intro && <p className="questionnaire-intro" dir="auto">{snapshot.intro}</p>}
      {snapshot.questions.length > 0 && <ol className="questionnaire-content-list" aria-label="问卷题目">
        {snapshot.questions.map((question) => <li key={question.id}>
          <bdi>{question.label}</bdi> <span className="questionnaire-note">{question.type === 'single' ? '单选' : '文本'}{question.required && ' · 必答'}</span>
          {question.options?.length ? <ul className="questionnaire-content-list">{question.options.map((option) => <li key={option} dir="auto">{option}</li>)}</ul> : null}
        </li>)}
      </ol>}
    </>}
    {publicUrl && <p className="questionnaire-note">
      填写链接 <a className="questionnaire-link" href={publicUrl} target="_blank" rel="noopener noreferrer">{publicUrl}</a>
    </p>}
    {snapshot && snapshot.status !== 'DRAFT' && <>
      <p className="questionnaire-note" role="status">已回收 {snapshot.responseCount} 份答卷</p>
      <div className="questionnaire-actions">
        {published && <button className="button outline" type="button" disabled={busy} onClick={() => void perform('read')}>
          刷新回收数量
        </button>}
        {published && !archived && <button className="button outline" type="button" disabled={busy || needsRefresh}
          aria-expanded={confirmClose} aria-controls={confirmClose ? `${id}-close` : undefined}
          onClick={() => setConfirmClose((current) => !current)}>关闭回收</button>}
        <a className="button outline" href={questionnaireCsvPath(projectId)} download>下载答卷 CSV</a>
        <button className="button outline" type="button" disabled={busy} onClick={() => void perform('export')}>
          {pending === 'export' ? '正在保存…' : '保存为项目资料'}
        </button>
      </div>
      {confirmClose && published && <div className="field" id={`${id}-close`} role="group" aria-labelledby={`${id}-close-help`}>
        <p className="questionnaire-note" id={`${id}-close-help`}>关闭后停止接收新的答卷，已回收的答卷可继续下载。</p>
        <div className="questionnaire-actions">
          <button className="button outline" type="button" disabled={busy || needsRefresh} onClick={() => void perform('close')}>
            {pending === 'close' ? '正在关闭…' : '确认关闭回收'}
          </button>
          <button className="button outline" type="button" disabled={busy} onClick={() => setConfirmClose(false)}>取消</button>
        </div>
      </div>}
    </>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {(needsRefresh || (!snapshot && !!error)) && <div className="questionnaire-actions">
      <button className="button outline" type="button" disabled={busy} onClick={() => void perform('read')}>刷新问卷</button>
    </div>}
    {feedback && <p className="questionnaire-note" role="status">{feedback}</p>}
  </section>
}
