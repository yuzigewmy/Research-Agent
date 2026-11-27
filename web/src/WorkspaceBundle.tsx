import { useLayoutEffect, useRef, useState } from 'react'
import { errorMessage } from './projects'
import { readWorkspaceBundle, workspaceBundleLimit } from './workspace'
import type { FileRecord } from './workspace'

export type BundleVersion = Pick<FileRecord, 'path' | 'versionId'>

export type BundleSelection = {
  versions: readonly BundleVersion[]
  onAdd: (version: BundleVersion) => void
}

const bundleCountId = 'workspace-bundle-count'

export function WorkspaceBundleAdd({ version, selection, historical = false }: {
  version: BundleVersion
  selection: BundleSelection
  historical?: boolean
}) {
  const selected = selection.versions.some(({ versionId }) => versionId === version.versionId)
  const full = selection.versions.length >= workspaceBundleLimit
  const label = historical ? '将此版本加入 ZIP 清单' : '将已保存版本加入 ZIP 清单'
  return <button className="button outline" type="button" data-workspace-bundle-add={historical ? 'history' : 'current'}
    aria-disabled={selected || full || undefined} aria-describedby={full && !selected ? bundleCountId : undefined}
    onClick={() => { if (!selected && !full) selection.onAdd(version) }}>
    {selected ? '已加入 ZIP 清单' : label}
  </button>
}

export function WorkspaceBundle({ projectId, versions, onRemove }: {
  projectId: string
  versions: readonly BundleVersion[]
  onRemove: (versionId: string) => void
}) {
  const active = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useLayoutEffect(() => () => active.current?.abort(), [])

  async function download() {
    if (active.current || !versions.length) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)])
    setBusy(true)
    setError('')
    try {
      const bundle = await readWorkspaceBundle(projectId, versions.map(({ versionId }) => versionId), signal)
      signal.throwIfAborted()
      const url = URL.createObjectURL(bundle)
      const link = document.createElement('a')
      link.href = url
      link.download = 'workspace-artifacts.zip'
      link.hidden = true
      document.body.append(link)
      try {
        link.click()
      } finally {
        link.remove()
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(signal.aborted ? '下载工作区 ZIP 超时，请重试。' : errorMessage(cause))
      }
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <section id="workspace-bundle" className="workspace-bundle" aria-labelledby="workspace-bundle-title">
    <div className="workspace-heading">
      <h5 id="workspace-bundle-title">工作区 ZIP 清单</h5>
      <p id={bundleCountId} className="workspace-note" role="status">
        已选 {versions.length} / {workspaceBundleLimit} 个版本
        {versions.length >= workspaceBundleLimit && '，请先移除一个版本再添加。'}
      </p>
    </div>
    {versions.length > 0 ? <>
      <ol className="workspace-list" role="list" aria-labelledby="workspace-bundle-title">
        {versions.map(({ path, versionId }) => <li key={versionId} data-workspace-bundle-version={versionId}>
          <div>
            <bdi>{path}</bdi>
            <p className="workspace-note">版本 <bdi>{versionId}</bdi></p>
          </div>
          <button className="button outline" type="button" data-workspace-bundle-remove
            aria-label={`移除 ${path} 的版本 ${versionId}`} onClick={() => onRemove(versionId)}>移除</button>
        </li>)}
      </ol>
      <p className="workspace-note">ZIP 中的 manifest.json 记录每个文件的原路径、版本和 SHA-256。</p>
    </> : <p className="workspace-note">从文件编辑区或版本历史加入已保存的版本。</p>}
    <div className="workspace-actions">
      <button className="button outline" type="button" data-workspace-bundle-download
        disabled={busy || !versions.length} aria-busy={busy} aria-describedby={error ? 'workspace-bundle-error' : undefined}
        onClick={() => void download()}>{busy ? '正在打包…' : error ? '重试下载工作区 ZIP' : '下载工作区 ZIP'}</button>
    </div>
    {error && <p className="form-error" id="workspace-bundle-error" role="alert">{error}</p>}
  </section>
}
