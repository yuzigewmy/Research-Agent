import { useState } from 'react'
import { diffLines } from 'diff'
import type { Document } from './workspace'
import './workspace-version-diff.css'

function VersionChanges({ from, to }: { from: string; to: string }) {
  const changes = diffLines(from, to, { stripTrailingCr: true, timeout: 100, maxEditLength: 1_000 })
  if (!changes) return <p className="workspace-note">改动较多，无法在此处逐行对照。可查看两个版本的正文。</p>

  let oldLine = 1
  let newLine = 1
  const groups = changes.flatMap(({ added, removed, count, value }) => {
    const first = added ? newLine : oldLine
    if (!added) oldLine += count
    if (!removed) newLine += count
    if (!added && !removed) return []
    return [{ kind: added ? 'added' : 'removed', first, last: first + count - 1, value }]
  })

  if (!groups.length) return <p className="workspace-note">逐行内容相同。对照将 CRLF 和 LF 视为相同换行。</p>

  return <ol className="workspace-diff-list" aria-label="逐行改动">
    {groups.map(({ kind, first, last, value }) => {
      const range = first === last ? `第 ${first} 行` : `第 ${first} 至 ${last} 行`
      return <li key={`${kind}-${first}`} data-change={kind}>
        <p>{kind === 'added' ? '新增 · 当前版本' : '删除 · 历史版本'} · {range}</p>
        <pre className="workspace-text" tabIndex={0} dir="auto">{value}</pre>
        {!value.endsWith('\n') && <p className="workspace-note">文件末尾无换行。</p>}
      </li>
    })}
  </ol>
}

export function WorkspaceVersionDiff({ historical, current }: { historical: Document; current: Document }) {
  const [open, setOpen] = useState(false)
  return <details className="workspace-diff" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>与当前保存版本对照</summary>
    {open && <div className="workspace-diff-body">
      <dl className="project-properties">
        <div><dt>历史版本</dt><dd><code>{historical.versionId}</code></dd></div>
        <div><dt>当前保存版本</dt><dd><code>{current.versionId}</code></dd></div>
      </dl>
      <VersionChanges from={historical.content} to={current.content} />
    </div>}
  </details>
}
