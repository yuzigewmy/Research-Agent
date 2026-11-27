import { useId, useRef, useState } from 'react'
import { errorMessage, request } from './projects'
import type { Document } from './workspace'

type CheckStage = 'plan' | 'draft' | 'final'

const checkStages: ReadonlyArray<readonly [CheckStage, string]> = [
  ['plan', '规划'],
  ['draft', '初稿'],
  ['final', '终稿'],
]

const severityLabels: Record<string, string> = {
  error: '错误',
  warning: '警告',
  info: '提示',
}

interface PaperCheckSection {
  name: string
  found: boolean
  required: boolean
  line?: number
}

interface PaperCheckDiagnostic {
  line: number
  severity: string
  rule: string
  message: string
}

interface PaperCheckReport {
  sourceVersionId?: string
  sourcePath?: string
  sourceSha256: string
  stage: CheckStage
  summary: string
  markdown: string
  stats: {
    chineseChars: number
    englishWords: number
    totalLines: number
  }
  sections: PaperCheckSection[]
  citations: {
    matched: string[]
    missing: string[]
  }
  diagnostics: PaperCheckDiagnostic[]
}

function isPaperCheckReport(value: unknown): value is PaperCheckReport {
  const report = value as Partial<PaperCheckReport> | null
  return !!report && typeof report.sourceSha256 === 'string' && typeof report.stage === 'string'
    && typeof report.summary === 'string' && typeof report.markdown === 'string'
    && !!report.stats && typeof report.stats.chineseChars === 'number'
    && Array.isArray(report.sections) && Array.isArray(report.diagnostics)
    && !!report.citations && Array.isArray(report.citations.matched) && Array.isArray(report.citations.missing)
}

export function ManuscriptCheckPanel({ projectId, file, dirty, onJumpToLine }: {
  projectId: string
  file: Document
  dirty: boolean
  onJumpToLine: (line: number) => void
}) {
  const id = useId()
  const [stage, setStage] = useState<CheckStage>('final')
  const [report, setReport] = useState<PaperCheckReport>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const active = useRef<AbortController | null>(null)

  const stale = !!report && report.sourceSha256 !== file.sha256

  async function runCheck() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setBusy(true)
    setError('')
    try {
      const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(file.versionId)}/check`
      const next = await request(route, isPaperCheckReport, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ stage }),
        signal,
      })
      if (!controller.signal.aborted) setReport(next)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <section className="workspace-subpanel workspace-paper-check" aria-labelledby={`${id}-title`}>
    <div className="workspace-heading">
      <h5 id={`${id}-title`}>分阶段稿件检查</h5>
      {stale && <span className="workspace-stale-badge" data-stale="true">已过期</span>}
    </div>
    <div className="workspace-actions">
      <div className="field">
        <label htmlFor={`${id}-stage`}>检查阶段</label>
        <select id={`${id}-stage`} value={stage} disabled={busy}
          onChange={(event) => setStage(event.currentTarget.value as CheckStage)}>
          {checkStages.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </div>
      <button className="button outline" type="button" disabled={busy}
        onClick={() => void runCheck()}>
        {busy ? '正在检查稿件…' : '运行稿件检查'}
      </button>
    </div>
    {dirty && <p className="workspace-note">当前有未保存修改；检查针对已保存版本（SHA-256 <code>{file.sha256.slice(0, 12)}</code>），保存后请重新检查。</p>}
    {stale && <p className="workspace-stale-banner" role="alert" data-stale="true">
      当前检查报告已过期（对应旧版本），请重新检查
    </p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {report && <div className="workspace-check-report">
      <p className="workspace-note" role="status">{report.summary}</p>
      <p className="workspace-note">
        检查来源版本 SHA-256 <code>{report.sourceSha256.slice(0, 12)}</code> ·
        篇幅：中文 {report.stats.chineseChars} 字 / 英文 {report.stats.englishWords} 词 / {report.stats.totalLines} 行
      </p>
      <ul className="workspace-section-chips" role="list" aria-label="章节识别结果">
        {report.sections.map((sec) => (
          <li key={sec.name} data-found={sec.found}>
            {sec.found && sec.line ? (
              <button className="workspace-chip-button" type="button" onClick={() => onJumpToLine(sec.line!)}>
                {sec.name} · 第 {sec.line} 行
              </button>
            ) : (
              <span>{sec.name} · {sec.required ? '缺失' : '可选'}</span>
            )}
          </li>
        ))}
      </ul>
      {(report.citations.matched.length > 0 || report.citations.missing.length > 0) && (
        <p className="workspace-note">
          引用键核对：已匹配 {report.citations.matched.length} 项
          {report.citations.matched.length > 0 && <>（{report.citations.matched.join('、')}）</>}
          ；未登记 {report.citations.missing.length} 项
          {report.citations.missing.length > 0 && <>（{report.citations.missing.join('、')}）</>}
        </p>
      )}
      {report.diagnostics.length === 0 ? (
        <p className="workspace-note">当前阶段未发现结构或引用异常。</p>
      ) : (
        <ul className="workspace-diagnostic-list" role="list" aria-label="稿件检查诊断">
          {report.diagnostics.map((diag, idx) => (
            <li key={`${diag.rule}-${diag.line}-${idx}`} data-severity={diag.severity}>
              <button className="button outline workspace-line-jump" type="button"
                onClick={() => onJumpToLine(diag.line)}>
                第 {diag.line} 行
              </button>
              <span className="workspace-diag-severity" data-severity={diag.severity}>
                {severityLabels[diag.severity] ?? diag.severity}
              </span>
              <span>{diag.message}</span>
            </li>
          ))}
        </ul>
      )}
    </div>}
  </section>
}
