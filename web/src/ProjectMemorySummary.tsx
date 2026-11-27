import { useEffect, useId, useState } from 'react'
import { t, useLocale } from './i18n'
import { errorMessage, getProjectMemorySummary, templates } from './projects'
import type { ProjectMemorySummary as MemorySummaryData } from './projects'

type MemoryState =
  | { kind: 'idle' }
  | { kind: 'loading'; data?: MemorySummaryData }
  | { kind: 'ready'; data: MemorySummaryData }
  | { kind: 'error'; message: string; data?: MemorySummaryData }

const workflowStatusLabels: Record<string, string> = {
  READY: '就绪',
  QUEUED: '排队中',
  RUNNING: '运行中',
  WAITING_CONFIRMATION: '等待确认',
  SUCCEEDED: '已完成',
  FAILED: '失败',
  CANCELLED: '已取消',
  INTERRUPTED: '已中断',
}

export function ProjectMemorySummary({ projectId }: { projectId: string }) {
  const locale = useLocale()
  const headingId = useId()
  const [open, setOpen] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<MemoryState>({ kind: 'idle' })

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    setState((prev) => ({ kind: 'loading', data: 'data' in prev ? prev.data : undefined }))
    void getProjectMemorySummary(projectId, signal).then((data) => {
      if (!controller.signal.aborted) setState({ kind: 'ready', data })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setState((prev) => ({
          kind: 'error',
          message: errorMessage(cause),
          data: 'data' in prev ? prev.data : undefined,
        }))
      }
    })
    return () => controller.abort()
  }, [projectId, open, attempt])

  const data = 'data' in state ? state.data : undefined

  return (
    <details
      className="project-memory-summary"
      aria-labelledby={headingId}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary id={headingId}>{t(locale, 'panel.memorySummary')}</summary>
      <div className="project-memory-body">
        <div className="project-management-actions">
          <button
            className="button outline"
            type="button"
            disabled={state.kind === 'loading'}
            onClick={() => setAttempt((value) => value + 1)}
          >
            {state.kind === 'loading' ? '正在汇总…' : '刷新研究记忆'}
          </button>
        </div>
        {state.kind === 'error' && <p className="form-error" role="alert">{state.message}</p>}
        {data && (
          <>
            <dl className="project-memory-stats">
              <div>
                <dt>项目模板</dt>
                <dd>{templates[data.project.templateId]}</dd>
              </div>
              <div>
                <dt>已核验文献</dt>
                <dd>{data.verifiedCitationCount} 篇</dd>
              </div>
              <div>
                <dt>已完成配对计算报告</dt>
                <dd>{data.completedComparisonCount} 份</dd>
              </div>
              <div>
                <dt>最近工作流</dt>
                <dd>
                  {data.latestWorkflow
                    ? `${workflowStatusLabels[data.latestWorkflow.status] ?? data.latestWorkflow.status}（${data.latestWorkflow.succeededSteps}/${data.latestWorkflow.totalSteps} 步）`
                    : '暂无运行记录'}
                </dd>
              </div>
            </dl>
            {(data.project.goal || data.project.deliverables) && (
              <div className="project-memory-section">
                <h5>研究目标与交付</h5>
                {data.project.goal && <p className="project-management-help">目标：{data.project.goal}</p>}
                {data.project.deliverables && <p className="project-management-help">交付物：{data.project.deliverables}</p>}
              </div>
            )}
            <div className="project-memory-section">
              <h5>研究简报（research/brief.md）</h5>
              {data.researchBrief ? (
                <>
                  <p className="project-management-help">
                    版本 <code>{data.researchBrief.versionId.slice(0, 8)}</code> · SHA-256{' '}
                    <code title={data.researchBrief.sha256}>{data.researchBrief.sha256.slice(0, 8)}</code>
                    {data.researchBrief.truncated ? '（已截取前 800 字）' : ''}
                  </p>
                  <pre className="project-memory-brief" dir="auto">{data.researchBrief.excerpt}</pre>
                </>
              ) : (
                <p className="project-management-help">尚未在工作区保存 <code>research/brief.md</code> 研究简报。</p>
              )}
            </div>
            <div className="project-memory-section">
              <h5>跨阶段证据来源链（{data.evidenceChain.length} 条）</h5>
              {data.evidenceChain.length === 0 ? (
                <p className="project-management-help">
                  暂无稿件配对指标或 PDF/DOI 文献关联记录。可在工作区稿件中选中文字绑定证据。
                </p>
              ) : (
                <ul className="project-memory-chain" role="list">
                  {data.evidenceChain.map((item) => (
                    <li key={`${item.kind}-${item.linkId}`} className="project-memory-chain-item">
                      <div className="project-memory-chain-head">
                        <span className="project-memory-badge">
                          {item.kind === 'comparison' ? '配对指标证据' : 'PDF/文献证据'}
                        </span>
                        <code>{item.manuscriptPath}</code>
                        <span className="project-management-help">
                          稿件版本 <code>{item.manuscriptVersionId.slice(0, 8)}</code>
                        </span>
                        {item.manuscriptOutdated && (
                          <span className="project-memory-warn">稿件已换版</span>
                        )}
                        {item.sourceChanged && (
                          <span className="project-memory-warn">来源已换版，请复核</span>
                        )}
                      </div>
                      <p className="project-memory-chain-source">{item.sourceSummary}</p>
                      {item.citationTitle && (
                        <p className="project-management-help">关联文献：{item.citationTitle}</p>
                      )}
                      <p className="project-management-help">稿件节选：<q dir="auto">{item.selectedText}</q></p>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </div>
    </details>
  )
}
