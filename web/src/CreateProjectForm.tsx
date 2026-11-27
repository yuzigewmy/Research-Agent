import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { t, useLocale } from './i18n'
import { ProjectTemplatePicker } from './ProjectTemplatePicker'
import { createProject, errorMessage, projectStartModeHelp, projectStartModeLabels } from './projects'
import type { CreateProject, Project, ProjectStartMode, TemplateId } from './projects'
import type { ProjectTemplate } from './projectTemplates'

const titleLimit = 200

export interface CreateProjectLaunchOptions {
  startMode?: ProjectStartMode
  contestId?: string
}

function newRequestId() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('')
}

export function CreateProjectForm({ templateItems, onCreated }: {
  templateItems: ProjectTemplate[]
  onCreated: (project: Project, options?: CreateProjectLaunchOptions) => void
}) {
  const locale = useLocale()
  const [title, setTitle] = useState('')
  const [preferredTemplate, setTemplate] = useState<TemplateId>('freeform')
  const [startMode, setStartMode] = useState<ProjectStartMode>('save_only')
  const [preferredContestId, setContestId] = useState('')
  const selectedTemplate = templateItems.find((item) => item.id === preferredTemplate) ?? templateItems[0]
  const template = selectedTemplate?.id
  const isWorkflowTemplate = selectedTemplate?.kind === 'workflow'
  const contests = selectedTemplate?.id === 'math_modeling' ? (selectedTemplate.defaults.contests ?? []) : []
  const contest = contests.find((item) => item.id === preferredContestId)
    ?? contests.find((item) => item.id === selectedTemplate?.defaults.contestId)
    ?? contests[0]
  const effectiveStartMode: ProjectStartMode = isWorkflowTemplate ? startMode : 'save_only'
  const titleLabel = template === 'software_registration' ? t(locale, 'create.softwareName')
    : template === 'patent_disclosure' ? t(locale, 'create.patentName') : t(locale, 'create.projectName')
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
    const payloadStartMode = effectiveStartMode === 'save_only' ? undefined : effectiveStartMode
    // The same payload can be retried if the server stored it before the response was lost.
    const input = previous?.title === trimmed && previous.templateId === template && previous.startMode === payloadStartMode
      ? previous
      : { requestId: newRequestId(), title: trimmed, templateId: template, startMode: payloadStartMode }
    attempt.current = input
    saving.current = true
    setPending(true)
    try {
      const project = await createProject(input)
      const launch: CreateProjectLaunchOptions = {
        startMode: effectiveStartMode,
        contestId: contest?.id,
      }
      attempt.current = null
      setTitle('')
      setTemplate('freeform')
      setStartMode('save_only')
      setContestId('')
      onCreated(project, launch)
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
      {contests.length > 0 && contest && (
        <div className="field">
          <label htmlFor="project-contest">{t(locale, 'create.contestLabel')}</label>
          <select id="project-contest" name="contestId" value={contest.id} disabled={pending}
            aria-describedby="project-contest-help"
            onChange={(event) => setContestId(event.currentTarget.value)}>
            {contests.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}（{item.language === 'en' ? '英文' : '中文'} · {item.ruleYear}）
              </option>
            ))}
          </select>
          <p className="workflow-note" id="project-contest-help">
            默认语言：{contest.language === 'en' ? '英文' : '中文'} · 规则校验：{contest.verifiedAt} · 交付提醒：{contest.reminders.join('；')}
          </p>
        </div>
      )}
      {isWorkflowTemplate && (
        <div className="field">
          <label htmlFor="project-start-mode">{t(locale, 'create.startModeLabel')}</label>
          <select id="project-start-mode" name="startMode" value={startMode} disabled={pending}
            aria-describedby="project-start-mode-help"
            onChange={(event) => {
              const value = event.currentTarget.value
              if (value === 'save_only' || value === 'start_workflow' || value === 'collaborate') setStartMode(value)
            }}>
            {Object.entries(projectStartModeLabels).map(([value, label]) => (
              <option key={value} value={value}>{label}</option>
            ))}
          </select>
          <p className="workflow-note" id="project-start-mode-help">{projectStartModeHelp[startMode]}</p>
        </div>
      )}
      {submitError && <p className="form-error" role="alert">{submitError}</p>}
      <div className="form-actions">
        <button type="submit" className="button primary" disabled={!template} aria-disabled={pending || !template}>
          {pending ? t(locale, 'create.submitting') : t(locale, 'create.submit')}
        </button>
      </div>
    </form>
  )
}
