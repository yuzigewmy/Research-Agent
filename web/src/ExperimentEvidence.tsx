import { useEffect, useState } from 'react'
import { errorMessage } from './projects'
import { comparisonRowStatus, getComparisonEvidence } from './tasks'
import type { ComparisonEvidence, TaskInputVersion } from './tasks'

type Props = { projectId: string; taskId: string; input: TaskInputVersion }

export function ExperimentEvidence(props: Props) {
  const [open, setOpen] = useState(false)
  return <details className="task-input-disclosure" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>查看计算证据</summary>
    {open && <EvidencePage {...props} />}
  </details>
}

function EvidencePage({ projectId, taskId, input }: Props) {
  const [cursors, setCursors] = useState([''])
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ComparisonEvidence | string>()
  const cursor = cursors.at(-1) ?? ''
  const { versionId, path, sha256 } = input

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void getComparisonEvidence(projectId, taskId, { versionId, path, sha256 }, cursor, signal).then(page => {
      signal.throwIfAborted()
      setState(page)
    }).catch(cause => {
      if (!controller.signal.aborted) setState(errorMessage(cause))
    })
    return () => controller.abort()
  }, [projectId, taskId, versionId, path, sha256, cursor, attempt])

  if (typeof state === 'string') return <div className="task-input-body">
    <p className="form-error" role="alert">{state}</p>
    <div className="task-actions"><button className="button outline" type="button" onClick={() => {
      setState(undefined)
      setAttempt(value => value + 1)
    }}>重试读取证据</button></div>
  </div>

  if (!state) return <p className="task-note" role="status">正在读取计算证据…</p>
  const { pairedRows, skippedRows, items, nextCursor } = state
  return <div className="task-input-body">
    <p className="task-note"><bdi>{path}</bdi> · 版本 <bdi>{versionId}</bdi> · SHA-256 <code>{sha256}</code></p>
    <p className="task-note" role="status">第 {cursors.length} 页 · {pairedRows} 行参与计算 · {skippedRows} 行因缺失跳过</p>
    <table className="task-evidence" aria-label="配对计算原始行">
      <thead><tr><th scope="col">原行号</th><th scope="col">基线</th><th scope="col">方案</th><th scope="col">处理结果</th></tr></thead>
      <tbody>{items.map(row => <tr key={row.line}>
        <th scope="row">{row.line}</th><td>{row.baseline ?? '—'}</td><td>{row.variant ?? '—'}</td><td>{comparisonRowStatus(row)}</td>
      </tr>)}</tbody>
    </table>
    {(cursors.length > 1 || nextCursor) && <div className="task-actions">
      <button className="button outline" type="button" disabled={cursors.length <= 1} onClick={() => {
        setState(undefined)
        setCursors(previous => previous.slice(0, -1))
      }}>上一页</button>
      <button className="button outline" type="button" disabled={!nextCursor} onClick={() => {
        if (!nextCursor) return
        setState(undefined)
        setCursors(previous => [...previous, nextCursor])
      }}>下一页</button>
    </div>}
  </div>
}
