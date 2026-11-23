import { useId } from 'react'
import { WorkflowMaterialLabel, WorkflowMaterialPicker } from './WorkflowCourseOptions'
import { isPatentDescription } from './workflow'
import type { PatentOptions, WorkflowRun } from './workflow'

type PatentDraft = { description: string; materialIds: string[] }

export function initialPatentDraft(): PatentDraft {
  return { description: '', materialIds: [] }
}

export function preparePatent(draft: PatentDraft) {
  const description = draft.description.trim()
  const descriptionError = isPatentDescription(description)
    ? undefined : '技术说明最多 4 KiB（UTF-8），且不能含有空字符。'
  const options: PatentOptions = {
    description: description || undefined,
    materialIds: draft.materialIds.length ? draft.materialIds : undefined,
  }
  return { options, descriptionError }
}

export function WorkflowPatentOptions({ projectId, draft, descriptionError, disabled, onChange }: {
  projectId: string
  draft: PatentDraft
  descriptionError?: string
  disabled: boolean
  onChange: (draft: PatentDraft) => void
}) {
  const id = useId()
  return <div className="create-form">
    <div className="field workflow-topic-requirements">
      <label htmlFor={id}>技术说明</label>
      <textarea id={id} rows={3} value={draft.description} readOnly={disabled}
        aria-invalid={!!descriptionError || undefined} aria-describedby={`${id}-help`}
        onChange={(event) => onChange({ ...draft, description: event.currentTarget.value })} />
      <p id={`${id}-help`} className={descriptionError ? 'form-error' : 'workflow-note'}>
        {descriptionError ?? '选填，可填写要解决的技术问题、方案要点和实施方式。最多 4 KiB（UTF-8）。'}
      </p>
    </div>
    <WorkflowMaterialPicker projectId={projectId} selected={draft.materialIds} disabled={disabled}
      onChange={(materialIds) => onChange({ ...draft, materialIds })} />
  </div>
}

export function WorkflowPatentSummary({ run }: { run: WorkflowRun }) {
  if (!run.patent) return null
  const { description, materials, materialIds } = run.patent
  const count = materials?.length ?? materialIds?.length ?? 0
  return <div className="workflow-topic-selected">
    <p className="workflow-note">技术方案草案确认{run.checkpoints ? '开启' : '关闭'}</p>
    {count === 0 && <p className="workflow-note">本轮未选用项目资料。</p>}
    {(count > 0 || description) && <details className="workflow-artifact-history">
      <summary>查看固定技术说明与资料 · {count} 份资料</summary>
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
