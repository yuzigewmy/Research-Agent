import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ProjectTemplatePicker } from './ProjectTemplatePicker'
import { createProject, errorMessage } from './projects'
import type { CreateProject, Project, TemplateId } from './projects'
import type { ProjectTemplate } from './projectTemplates'

const titleLimit = 200

function newRequestId() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('')
}

export function CreateProjectForm({ templateItems, onCreated }: {
  templateItems: ProjectTemplate[]
  onCreated: (project: Project) => void
}) {
  const [title, setTitle] = useState('')
  const [preferredTemplate, setTemplate] = useState<TemplateId>('freeform')
  const template = (templateItems.find((item) => item.id === preferredTemplate) ?? templateItems[0])?.id
  const titleLabel = template === 'software_registration' ? '软件全称'
    : template === 'patent_disclosure' ? '技术方案名称' : '项目名称'
  const [pending, setPending] = useState(false)
  const [titleError, setTitleError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const titleInput = useRef<HTMLInputElement>(null)
  const attempt = useRef<CreateProject | null>(null)
  const saving = useRef(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (saving.current || !template) return

    const trimmed = title.trim()
    const length = Array.from(trimmed).length
    if (length === 0 || length > titleLimit) {
      setTitleError(length === 0 ? `请输入${titleLabel}。` : `${titleLabel}不能超过 ${titleLimit} 个字符。`)
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
        <label htmlFor="project-title">{titleLabel}</label>
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
      <ProjectTemplatePicker id="project-template" items={templateItems} value={template} disabled={pending}
        onChange={(next) => {
          setTemplate(next)
          setTitleError('')
          setSubmitError('')
        }} />
      {submitError && <p className="form-error" role="alert">{submitError}</p>}
      <div className="form-actions">
        <button type="submit" className="button primary" disabled={!template} aria-disabled={pending || !template}>
          {pending ? '正在创建…' : '创建项目'}
        </button>
      </div>
    </form>
  )
}
