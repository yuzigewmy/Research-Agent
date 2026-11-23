import { useEffect, useId, useState } from 'react'
import { errorMessage } from './projects'
import { WorkflowPatentFigureLinks } from './WorkflowPatentFigureLinks'
import { WorkflowSoftwarePdfDownload } from './WorkflowSoftwarePdfDownload'
import { isSoftwarePdfStep, listWorkflowArtifacts, workflowArtifactPath } from './workflow'
import type { WorkflowArtifactPage } from './workflow'

const artifactDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' })

type ArtifactSource = {
  projectId: string
  runId: string
  stepKey: string
  stepTitle: string
  softwarePdfEnabled?: boolean
  patentFigures?: boolean
  patentWordConfigured?: boolean
}

export function WorkflowArtifactHistory({ artifactId, count, ...source }: ArtifactSource & { artifactId?: string; count?: number }) {
  const [open, setOpen] = useState(false)
  return <details className="workflow-artifact-history" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>成果历史{count !== undefined && `（${count}）`}</summary>
    {open && <ArtifactList key={`${source.runId}:${artifactId ?? ''}:${count ?? ''}`} {...source} />}
  </details>
}

function ArtifactList({ projectId, runId, stepKey, stepTitle, softwarePdfEnabled, patentFigures, patentWordConfigured }: ArtifactSource) {
  const id = useId()
  const [page, setPage] = useState<WorkflowArtifactPage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void listWorkflowArtifacts(projectId, runId, stepKey, signal, query.cursor).then((next) => {
      if (!controller.signal.aborted) setPage((current) => ({
        items: query.cursor ? [...current.items, ...next.items] : next.items,
        nextCursor: next.nextCursor,
      }))
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, runId, stepKey, query])

  function load(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  return <div className="workflow-artifact-page">
    {page.items.length > 0 && <ul className="workflow-artifact-list" aria-label={`${stepTitle}成果版本`}>
      {page.items.map(({ artifactId, createdAt, current, source }, index) => {
        const date = artifactDate.format(new Date(createdAt))
        const label = current ? '当前成果' : '历史成果'
        const versionLabelId = `${id}-version-${index}`
        return <li key={artifactId}>
          <span className="workflow-note" id={versionLabelId}>{label} · <time dateTime={createdAt}>{date}</time></span>
          <a className="workflow-download" href={workflowArtifactPath(projectId, runId, stepKey, artifactId)}
            aria-label={`下载${label}版本 ${stepTitle} ${date}`} download>下载此版本</a>
          {softwarePdfEnabled && isSoftwarePdfStep(stepKey) && (
            <WorkflowSoftwarePdfDownload projectId={projectId} runId={runId} stepKey={stepKey} stepTitle={stepTitle}
              artifactId={artifactId} versionLabel={`${label} ${date}`} />
          )}
          {patentFigures && <WorkflowPatentFigureLinks projectId={projectId} runId={runId} artifactId={artifactId}
            wordEnabled={patentWordConfigured === true} versionLabelId={versionLabelId} />}
          {source && <span className="workflow-note">工作区来源 <bdi>{source.path}</bdi> · 版本 <bdi>{source.versionId}</bdi></span>}
        </li>
      })}
    </ul>}
    {loading && <p className="workflow-note" role="status">正在加载成果历史…</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loading && !error && !page.items.length && <p className="workflow-note">暂无可下载的成果。</p>}
    {!loading && (error || page.nextCursor) && <button className="button outline" type="button"
      onClick={() => load(error ? query.cursor : page.nextCursor)}>
      {error ? '重试加载成果历史' : '加载更多成果'}
    </button>}
  </div>
}
