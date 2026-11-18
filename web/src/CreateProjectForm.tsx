import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { createProject, errorMessage, isTemplateId, templates } from './projects'
import type { CreateProject, Project, TemplateId } from './projects'

const titleLimit = 200

function newRequestId() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('')
}

export function CreateProjectForm({ onCreated }: { onCreated: (project: Project) => void }) {
  const [title, setTitle] = useState('')
  const [template, setTemplate] = useState<TemplateId>('freeform')
  const [pending, setPending] = useState(false)
  const [titleError, setTitleError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const titleInput = useRef<HTMLInputElement>(null)
  const attempt = useRef<CreateProject | null>(null)
  const saving = useRef(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (saving.current) return

    const trimmed = title.trim()
    const length = Array.from(trimmed).length
    if (length === 0 || length > titleLimit) {
      setTitleError(length === 0 ? '请输入项目名称。' : `项目名称不能超过 ${titleLimit} 个字符。`)
      titleInput.current?.focus()
      return
    }

    setTitleError('')
    setSubmitError('')
    const previous = attempt.current
    // The same payload can be retried if the server stored it before the response was lost.
    const input = previous?.title === trimmed && previous.templateId === template
      ? previous
      : { requestId: newRequestId(), title: trimmed, templateId: template }
    attempt.current = input
    saving.current = true
    setPending(true)
    try {
      const project = await createProject(input)
      attempt.current = null
      setTitle('')
      setTemplate('freeform')
      onCreated(project)
    } catch (error) {
      setSubmitError(errorMessage(error))
    } finally {
      saving.current = false
      setPending(false)
    }
  }

  return (
    <form className="create-form" onSubmit={submit} noValidate aria-busy={pending}>
      <div className="field">
        <label htmlFor="project-title">项目名称</label>
        <input
          id="project-title"
          name="title"
          type="text"
          value={title}
          ref={titleInput}
          required
          readOnly={pending}
          aria-invalid={titleError ? true : undefined}
          aria-describedby={titleError ? 'project-title-error' : undefined}
          onChange={(event) => {
            setTitle(event.currentTarget.value)
            setTitleError('')
            setSubmitError('')
          }}
        />
        {titleError && <p className="form-error" id="project-title-error" role="alert">{titleError}</p>}
      </div>
      <div className="field">
        <label htmlFor="project-template">项目模板</label>
        <select
          id="project-template"
          name="templateId"
          value={template}
          disabled={pending}
          onChange={(event) => {
            if (isTemplateId(event.currentTarget.value)) setTemplate(event.currentTarget.value)
            setSubmitError('')
          }}
        >
          {Object.entries(templates).map(([id, label]) => <option key={id} value={id}>{label}</option>)}
        </select>
      </div>
      {submitError && <p className="form-error" role="alert">{submitError}</p>}
      <div className="form-actions">
        <button type="submit" className="button primary" aria-disabled={pending}>
          {pending ? '正在创建…' : '创建项目'}
        </button>
      </div>
    </form>
  )
}
