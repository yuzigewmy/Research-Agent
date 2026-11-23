import { useEffect, useId, useState } from 'react'
import { archiveFormatLabel, listMaterials } from './materials'
import type { Material, MaterialPage } from './materials'
import { errorMessage } from './projects'
import type { CourseTemplateId } from './projects'
import {
  courseCharacterMax, courseCharacterMin, courseDisciplineLabels,
  courseMethodLabels, courseReportTypeLabels, courseRequirementsLimit, workflowMaterialLimit,
} from './workflow'
import type { CourseOptions, CourseProfile, WorkflowRun } from './workflow'

type CourseDraft = Pick<CourseProfile, 'discipline'> & Required<Pick<CourseProfile, 'method' | 'reportType'>> & {
  targetCharacters: string
  requirements: string
  materialIds: string[]
}

type CourseErrors = Partial<Record<'targetCharacters' | 'requirements', string>>

export function initialCourseDraft(templateId: CourseTemplateId | null): CourseDraft {
  return {
    targetCharacters: templateId === 'course_report' ? '10000' : '8000',
    discipline: 'auto', method: 'auto', reportType: 'auto', requirements: '', materialIds: [],
  }
}

export function prepareCourse(templateId: CourseTemplateId, draft: CourseDraft) {
  const errors: CourseErrors = {}
  const target = draft.targetCharacters.trim()
  const targetCharacters = Number(target)
  const requirements = draft.requirements.trim()
  if (!/^\d+$/.test(target) || targetCharacters < courseCharacterMin || targetCharacters > courseCharacterMax) {
    errors.targetCharacters = '请输入 1000 至 30000 之间的整数。'
  }
  if (new TextEncoder().encode(requirements).length > courseRequirementsLimit || requirements.includes('\0')) {
    errors.requirements = '补充要求须为 4 KiB 以内且不含空字符。'
  }
  const options: CourseOptions = {
    targetCharacters,
    discipline: draft.discipline,
    ...(templateId === 'course_paper' ? { method: draft.method } : { reportType: draft.reportType }),
    requirements: requirements || undefined,
    materialIds: draft.materialIds.length ? draft.materialIds : undefined,
  }
  return { options, errors, valid: Object.keys(errors).length === 0 }
}

function CourseSelect<T extends string>({ id, label, labels, value, disabled, onChange }: {
  id: string
  label: string
  labels: Readonly<Record<T, string>>
  value: T
  disabled: boolean
  onChange: (next: T) => void
}) {
  return <div className="field">
    <label htmlFor={id}>{label}</label>
    <select id={id} value={value} disabled={disabled} onChange={(event) => {
      const next = event.currentTarget.value
      if (Object.hasOwn(labels, next)) onChange(next as T)
    }}>
      {Object.entries(labels).map(([key, text]) => <option key={key} value={key}>{String(text)}</option>)}
    </select>
  </div>
}

const materialDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' })

export function WorkflowMaterialLabel({ name, sha256, format, createdAt }: Pick<Material, 'name' | 'sha256' | 'format'> & {
  createdAt?: string
}) {
  return <span className="material-info">
    <bdi>{name}</bdi>
    <span className="material-meta material-note">
      <span>{format === 'archive' ? archiveFormatLabel(name) : format === 'word' ? 'Word' : 'UTF-8 文本'}</span>
      {createdAt && <time dateTime={createdAt}>{materialDate.format(new Date(createdAt))}</time>}
      <span>SHA-256 <code title={sha256}>{sha256.slice(0, 12)}</code></span>
    </span>
  </span>
}

export function WorkflowMaterialPicker({ projectId, selected, disabled, onChange, help, allowArchives = false }: {
  projectId: string
  selected: string[]
  disabled: boolean
  onChange: (materialIds: string[]) => void
  help?: string
  allowArchives?: boolean
}) {
  const id = useId()
  const [page, setPage] = useState<MaterialPage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!query) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void listMaterials(projectId, signal, query.cursor).then((next) => {
      if (controller.signal.aborted) return
      setPage((previous) => {
        // Keep previously loaded versions available when refreshing after an upload.
        const ordered = query.cursor ? [...previous.items, ...next.items] : [...next.items, ...previous.items]
        return { nextCursor: next.nextCursor, items: [...new Map(ordered.map((item) => [item.materialId, item])).values()] }
      })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  function load(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  const eligible = page.items.filter(({ format, kind }) => {
    if (format === 'archive') return allowArchives
    return format === 'word' || (format === undefined && kind === 'text')
  })

  return <details className="workflow-artifact-history" onToggle={(event) => {
    if (event.currentTarget.open && !query && !loading) load()
  }}>
    <summary>选择项目资料 · 已选 {selected.length}/{workflowMaterialLimit}</summary>
    <div className="workflow-run-history-content">
      <p className="workflow-note" id={id}>
        {help ?? `选填，最多 ${workflowMaterialLimit} 份 UTF-8 文本或 Word。同名文件可按 SHA-256 摘要区分版本。`}
      </p>
      {eligible.length > 0 && <ul className="workspace-list" role="list" aria-label="可选资料版本" aria-describedby={id}>
        {eligible.map((material) => {
          const checked = selected.includes(material.materialId)
          return <li key={material.materialId}>
            <label className="workflow-checkpoint workspace-row">
              <input type="checkbox" checked={checked} disabled={disabled || (!checked && selected.length >= workflowMaterialLimit)}
                onChange={(event) => onChange(event.currentTarget.checked ? [...selected, material.materialId]
                  : selected.filter((materialId) => materialId !== material.materialId))} />
              <WorkflowMaterialLabel {...material} />
            </label>
          </li>
        })}
      </ul>}
      {loading && <p className="workflow-note" role="status">正在读取资料版本…</p>}
      {error && <p className="form-error" role="alert">读取资料失败。{error}</p>}
      {query && !loading && !error && eligible.length === 0 && <p className="workflow-note">
        {page.nextCursor ? '已读取的资料中没有可选版本。' : '暂无可选资料。可在「项目资料」上传文件后刷新。'}
      </p>}
      <div className="workflow-actions">
        <button className="button outline" type="button" disabled={loading} onClick={() => load()}>
          刷新资料
        </button>
        {(page.nextCursor || error) && <button className="button outline" type="button" disabled={loading}
          onClick={() => load(error ? query?.cursor : page.nextCursor)}>{error ? '重试读取' : '加载更多资料'}</button>}
      </div>
    </div>
  </details>
}

export function WorkflowCourseOptions({ projectId, templateId, draft, errors, disabled, onChange }: {
  projectId: string
  templateId: CourseTemplateId
  draft: CourseDraft
  errors: CourseErrors
  disabled: boolean
  onChange: (draft: CourseDraft) => void
}) {
  const id = useId()
  return <div className="create-form">
    <div className="field">
      <label htmlFor={`${id}-characters`}>目标正文字数</label>
      <input id={`${id}-characters`} type="text" inputMode="numeric" value={draft.targetCharacters}
        readOnly={disabled} aria-invalid={!!errors.targetCharacters || undefined} aria-describedby={`${id}-characters-help`}
        onChange={(event) => onChange({ ...draft, targetCharacters: event.currentTarget.value })} />
      <p id={`${id}-characters-help`} className={errors.targetCharacters ? 'form-error' : 'workflow-note'}>
        {errors.targetCharacters || '中文稿件，目标 1000 至 30000 字。'}
      </p>
    </div>
    <div className="workflow-actions">
      <CourseSelect id={`${id}-discipline`} label="学科方向" labels={courseDisciplineLabels} value={draft.discipline}
        disabled={disabled} onChange={(discipline) => onChange({ ...draft, discipline })} />
      {templateId === 'course_paper'
        ? <CourseSelect id={`${id}-method`} label="研究方法" labels={courseMethodLabels} value={draft.method}
            disabled={disabled} onChange={(method) => onChange({ ...draft, method })} />
        : <CourseSelect id={`${id}-report-type`} label="报告类型" labels={courseReportTypeLabels} value={draft.reportType}
            disabled={disabled} onChange={(reportType) => onChange({ ...draft, reportType })} />}
    </div>
    <div className="field workflow-topic-requirements">
      <label htmlFor={`${id}-requirements`}>补充要求</label>
      <textarea id={`${id}-requirements`} value={draft.requirements} rows={3} readOnly={disabled}
        aria-invalid={!!errors.requirements || undefined} aria-describedby={`${id}-requirements-help`}
        onChange={(event) => onChange({ ...draft, requirements: event.currentTarget.value })} />
      <p id={`${id}-requirements-help`} className={errors.requirements ? 'form-error' : 'workflow-note'}>
        {errors.requirements || '选填，最多 4 KiB（UTF-8）。可填写课程要求、报告结构或评分要点。'}
      </p>
    </div>
    <WorkflowMaterialPicker projectId={projectId} selected={draft.materialIds} disabled={disabled}
      onChange={(materialIds) => onChange({ ...draft, materialIds })} />
  </div>
}

export function WorkflowCourseSummary({ run }: { run: WorkflowRun }) {
  const settings = run.course
  if (!settings) return null
  const materialCount = settings.materials?.length ?? settings.materialIds?.length ?? 0
  return <div className="workflow-topic-selected">
    <p className="workflow-note">本轮固定设置 · 中文 · 目标正文 {settings.targetCharacters} 字 · 规划确认{run.checkpoints ? '开启' : '关闭'}</p>
    <p className="workflow-note">
      学科方向 {courseDisciplineLabels[settings.discipline]}
      {settings.method && ` · 研究方法 ${courseMethodLabels[settings.method]}`}
      {settings.reportType && ` · 报告类型 ${courseReportTypeLabels[settings.reportType]}`}
    </p>
    {!materialCount && <p className="workflow-note">未选用项目资料。</p>}
    {(materialCount > 0 || settings.requirements) && <details className="workflow-artifact-history">
      <summary>查看补充要求与资料 · {materialCount} 份资料</summary>
      <div className="workflow-run-history-content">
        {settings.requirements && <p className="workflow-note workflow-step-message" dir="auto">{settings.requirements}</p>}
        {!!settings.materials?.length && <ul className="material-list" role="list" aria-label="固定资料版本">
          {settings.materials.map((material) => <li className="material-row" key={material.materialId}>
            <WorkflowMaterialLabel {...material} />
          </li>)}
        </ul>}
        {!settings.materials?.length && !!settings.materialIds?.length && (
          <p className="workflow-note">资料版本编号 <bdi>{settings.materialIds.join('、')}</bdi></p>
        )}
      </div>
    </details>}
  </div>
}
