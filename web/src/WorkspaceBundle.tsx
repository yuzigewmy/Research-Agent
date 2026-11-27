import { useLayoutEffect, useRef, useState } from 'react'
import { errorMessage } from './projects'
import {
  readWorkspaceBundle, workspaceBundleItemKey, workspaceBundleLimit, workspaceBundleSizeLimit, workspaceBundleSizeLimitMiB,
} from './workspace'
import type { FileRecord, WorkspaceBundleItem } from './workspace'

export type BundleEntry = WorkspaceBundleItem & { name: string; size: number }

export type BundleSelection = {
  items: readonly BundleEntry[]
  onAdd: (item: BundleEntry) => void
  onRemove: (item: WorkspaceBundleItem) => void
}

const bundleCountId = 'workspace-bundle-count'
const sizeFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 })

function bundleSize(items: readonly BundleEntry[]) {
  return items.reduce((total, item) => total + item.size, 0)
}

export function bundleAddState(items: readonly BundleEntry[], item: BundleEntry) {
  if (items.some(({ kind, id }) => kind === item.kind && id === item.id)) return 'selected'
  if (items.length >= workspaceBundleLimit) return 'count-limit'
  if (bundleSize(items) + item.size > workspaceBundleSizeLimit) return 'size-limit'
  return 'available'
}

const addLabels = {
  current: '将已保存版本加入 ZIP 清单',
  history: '将此版本加入 ZIP 清单',
  material: '将原件加入 ZIP 清单',
  artifact: '将原始成果加入 ZIP 清单',
}

export function WorkspaceBundleAdd({ item, selection, source }: {
  item: BundleEntry
  selection: BundleSelection
  source: keyof typeof addLabels
}) {
  const state = bundleAddState(selection.items, item)
  const limit = state === 'count-limit' || state === 'size-limit'
  let label = addLabels[source]
  if (state === 'selected') label = '已加入 ZIP 清单'
  else if (state === 'count-limit') label = 'ZIP 清单已满'
  else if (state === 'size-limit') label = '超出 ZIP 容量'

  return <button className="button outline" type="button" data-workspace-bundle-add={source}
    aria-label={source === 'material' || source === 'artifact' ? `${label}：${item.name}` : undefined}
    aria-disabled={state !== 'available' || undefined} aria-describedby={limit ? bundleCountId : undefined}
    onClick={() => { if (state === 'available') selection.onAdd(item) }}>
    {label}
  </button>
}

export function WorkspaceVersionBundleAdd({ version, selection, historical = false }: {
  version: Pick<FileRecord, 'path' | 'versionId' | 'size'>
  selection: BundleSelection
  historical?: boolean
}) {
  const { versionId, path, size } = version
  return <WorkspaceBundleAdd item={{ kind: 'version', id: versionId, name: path, size }}
    source={historical ? 'history' : 'current'} selection={selection} />
}

export function WorkspaceArtifactBundleAdd({ artifactId, name, size, selection }: {
  artifactId?: string
  name?: string
  size?: number
  selection: BundleSelection
}) {
  if (artifactId === undefined || name === undefined || size === undefined) return null
  return <WorkspaceBundleAdd item={{ kind: 'artifact', id: artifactId, name, size }} source="artifact" selection={selection} />
}

export function WorkspaceBundle({ projectId, selection: { items, onRemove } }: {
  projectId: string
  selection: BundleSelection
}) {
  const active = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useLayoutEffect(() => () => active.current?.abort(), [])

  async function download() {
    if (active.current || !items.length) return
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)])
    setBusy(true)
    setError('')
    try {
      const bundle = await readWorkspaceBundle(projectId, items, signal)
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

  const totalMiB = bundleSize(items) / (1024 * 1024)
  const sizeLabel = totalMiB > 0 && totalMiB < 0.01 ? '少于 0.01' : sizeFormat.format(totalMiB)

  return <section id="workspace-bundle" className="workspace-bundle" aria-labelledby="workspace-bundle-title">
    <div className="workspace-heading">
      <h5 id="workspace-bundle-title">工作区 ZIP 清单</h5>
      <p id={bundleCountId} className="workspace-note" role="status">
        已选 {items.length} / {workspaceBundleLimit} 项，原件共 {sizeLabel} MiB（上限 {workspaceBundleSizeLimitMiB} MiB）。
        {items.length >= workspaceBundleLimit && '请先移除一项再添加。'}
      </p>
    </div>
    {items.length > 0 ? <>
      <ol className="workspace-list" role="list" aria-labelledby="workspace-bundle-title">
        {items.map((item) => {
          const { kind, id, name } = item
          const isVersion = kind === 'version'
          const isArtifact = kind === 'artifact'
          const originalLabel = isArtifact ? '工作流原始成果' : '项目资料原件'
          return <li key={workspaceBundleItemKey(item)} data-workspace-bundle-version={isVersion ? id : undefined}
            data-workspace-bundle-material={kind === 'material' ? id : undefined}
            data-workspace-bundle-artifact={isArtifact ? id : undefined}>
            <div>
              <bdi>{name}</bdi>
              <p className="workspace-note">{isVersion ? <>文件版本 <bdi>{id}</bdi></> : originalLabel}</p>
            </div>
            <button className="button outline" type="button" data-workspace-bundle-remove
              aria-label={isVersion ? `移除 ${name} 的版本 ${id}` : `移除${isArtifact ? '原始成果' : '原件'} ${name}`}
              onClick={() => onRemove(item)}>移除</button>
          </li>
        })}
      </ol>
      <p className="workspace-note">ZIP 中的 manifest.json 记录每项的来源、原路径或名称以及 SHA-256。</p>
    </> : <p className="workspace-note">从文件编辑区、版本历史、项目资料或工作流加入文件。</p>}
    <p className="workspace-note">工作流成果仅打包原始文件。</p>
    <div className="workspace-actions">
      <button className="button outline" type="button" data-workspace-bundle-download
        disabled={busy || !items.length} aria-busy={busy} aria-describedby={error ? 'workspace-bundle-error' : undefined}
        onClick={() => void download()}>{busy ? '正在打包…' : error ? '重试下载工作区 ZIP' : '下载工作区 ZIP'}</button>
    </div>
    {error && <p className="form-error" id="workspace-bundle-error" role="alert">{error}</p>}
  </section>
}
