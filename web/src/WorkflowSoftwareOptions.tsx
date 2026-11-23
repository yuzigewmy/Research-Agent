import { useId } from 'react'
import { WorkflowMaterialLabel, WorkflowMaterialPicker } from './WorkflowCourseOptions'
import { defaultSoftwareVersion, isSoftwareDescription, isSoftwareVersion, workflowMaterialLimit } from './workflow'
import type { SoftwareOptions, WorkflowRun } from './workflow'

type SoftwareDraft = { version: string; description: string; materialIds: string[] }
type SoftwareErrors = Partial<Record<'version' | 'description', string>>

export function initialSoftwareDraft(): SoftwareDraft {
  return { version: defaultSoftwareVersion, description: '', materialIds: [] }
}

export function prepareSoftware(draft: SoftwareDraft) {
  const version = draft.version.trim() || defaultSoftwareVersion
  const description = draft.description.trim()
  const errors: SoftwareErrors = {}
  if (!isSoftwareVersion(version)) errors.version = '版本号最多 64 个 UTF-8 字节，且不能包含控制字符。'
  if (!isSoftwareDescription(description)) errors.description = '软件说明最多 4 KiB（UTF-8），且不能含有空字符。'
  const options: SoftwareOptions = {
    version,
    description: description || undefined,
    materialIds: draft.materialIds.length ? draft.materialIds : undefined,
  }
  return { options, errors, valid: !errors.version && !errors.description }
}

export function WorkflowSoftwareOptions({ projectId, draft, errors, disabled, onChange }: {
  projectId: string
  draft: SoftwareDraft
  errors: SoftwareErrors
  disabled: boolean
  onChange: (draft: SoftwareDraft) => void
}) {
  const id = useId()
  return <div className="create-form">
    <div className="field">
      <label htmlFor={`${id}-version`}>软件版本号</label>
      <input id={`${id}-version`} type="text" value={draft.version} readOnly={disabled} autoComplete="off"
        aria-invalid={!!errors.version || undefined} aria-describedby={`${id}-version-help`}
        onChange={(event) => onChange({ ...draft, version: event.currentTarget.value })} />
      <p id={`${id}-version-help`} className={errors.version ? 'form-error' : 'workflow-note'}>
        {errors.version || `留空使用 ${defaultSoftwareVersion}，最多 64 个 UTF-8 字节。`}
      </p>
    </div>
    <div className="field workflow-topic-requirements">
      <label htmlFor={`${id}-description`}>软件说明</label>
      <textarea id={`${id}-description`} rows={3} value={draft.description} readOnly={disabled}
        aria-invalid={!!errors.description || undefined} aria-describedby={`${id}-description-help`}
        onChange={(event) => onChange({ ...draft, description: event.currentTarget.value })} />
      <p id={`${id}-description-help`} className={errors.description ? 'form-error' : 'workflow-note'}>
        {errors.description || '选填，可填写主要功能和操作流程，最多 4 KiB（UTF-8）。'}
      </p>
    </div>
    <WorkflowMaterialPicker projectId={projectId} selected={draft.materialIds} disabled={disabled}
      help={`选填，最多 ${workflowMaterialLimit} 份 UTF-8 文本（含源码）或 Word。源码文本合计最多 64 KiB。`}
      onChange={(materialIds) => onChange({ ...draft, materialIds })} />
  </div>
}

export function WorkflowSoftwareSummary({ run }: { run: WorkflowRun }) {
  const settings = run.software
  if (!settings) return null
  const { version, description, materials, materialIds } = settings
  const count = materials?.length ?? materialIds?.length ?? 0
  return <div className="workflow-topic-selected">
    <p className="workflow-note">本轮软件版本 <bdi>{version}</bdi> · 手册草稿确认{run.checkpoints ? '开启' : '关闭'}</p>
    {!count && <p className="workflow-note">未选用项目资料。</p>}
    {(count > 0 || description) && <details className="workflow-artifact-history">
      <summary>查看软件说明与资料 · {count} 份资料</summary>
      <div className="workflow-run-history-content">
        {description && <p className="workflow-note workflow-step-message" dir="auto">{description}</p>}
        {materials?.length ? <ul className="material-list" role="list" aria-label="固定资料版本">
          {materials.map((material) => <li className="material-row" key={material.materialId}>
            <WorkflowMaterialLabel {...material} />
          </li>)}
        </ul> : !!materialIds?.length && <p className="workflow-note">资料版本编号 <bdi>{materialIds.join('、')}</bdi></p>}
      </div>
    </details>}
  </div>
}
