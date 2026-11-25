import { useEffect, useRef, useState } from 'react'
import { errorMessage, request } from './projects'
import './workspace-components.css'

const componentIds = ['pdf.preview', 'docx.export', 'pdf.software.export'] as const
type ComponentId = (typeof componentIds)[number]

const componentLabels = {
  'pdf.preview': { name: 'PDF 查看资源', size: '组件资源' },
  'docx.export': { name: 'Word 导出', size: '样例文件' },
  'pdf.software.export': { name: '鉴别材料 PDF', size: '字体文件' },
} satisfies Record<ComponentId, { name: string; size: string }>

const statusLabels = { ready: '可用', unconfigured: '未配置', unavailable: '不可用' } as const

type ComponentCheck = {
  id: ComponentId
  status: keyof typeof statusLabels
  version?: string
  bytes?: number
  message: string
}

type CheckResponse = { components: ComponentCheck[] }

function isCheckResponse(value: unknown): value is CheckResponse {
  const response = value as Partial<CheckResponse> | null
  const components = response?.components
  return Array.isArray(components) && components.length === componentIds.length
    && components.every((component, index) => component != null && component.id === componentIds[index]
      && typeof component.status === 'string' && Object.hasOwn(statusLabels, component.status)
      && typeof component.message === 'string'
      && (component.version === undefined || typeof component.version === 'string')
      && (component.bytes === undefined || (Number.isSafeInteger(component.bytes) && component.bytes >= 0)))
}

const sizeFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 })
const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

function formatSize(bytes: number) {
  const unit = bytes < 1024 ? 0 : Math.min(units.length - 1, Math.floor(Math.log2(bytes) / 10))
  return `${sizeFormat.format(bytes / 1024 ** unit)} ${units[unit]}`
}

export function WorkspaceComponents() {
  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<CheckResponse>()
  const [error, setError] = useState<string>()
  const activeCheck = useRef<AbortController | null>(null)

  useEffect(() => () => activeCheck.current?.abort(), [])

  async function check() {
    if (activeCheck.current) return
    const controller = new AbortController()
    activeCheck.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setChecking(true)
    setError(undefined)
    try {
      const next = await request('/api/v1/workspace-components/check', isCheckResponse, { method: 'POST', signal })
      signal.throwIfAborted()
      setResult(next)
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(signal.aborted ? '检查超过 15 秒，请重新检查。' : errorMessage(cause))
      }
    } finally {
      if (!controller.signal.aborted) {
        activeCheck.current = null
        setChecking(false)
      }
    }
  }

  return <details className="workspace-components" onToggle={(event) => {
    if (event.currentTarget.open || !activeCheck.current) return
    activeCheck.current.abort()
    activeCheck.current = null
    setChecking(false)
  }}>
    <summary>组件检查</summary>
    <div className="workspace-components-body">
      <p className="workspace-components-note">点击「检查组件」，使用固定样例检查本机组件。</p>
      <div className="workspace-components-actions">
        <button className="button outline" type="button" aria-disabled={checking || undefined} onClick={() => void check()}>
          {result || error ? '重新检查组件' : '检查组件'}
        </button>
        <p className="workspace-components-note" role="status">
          {checking && '正在使用固定样例检查本机组件，最多等待 15 秒。'}
          {!checking && result && !error && '检查完成。'}
        </p>
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      {result && <div className="workspace-components-results" aria-busy={checking}>
        {(checking || error) && <p className="workspace-components-note">下方是上次完成的检查结果。</p>}
        <ul className="workspace-components-list" role="list">
          {result.components.map(({ id, status, version, bytes, message }) => <li key={id}>
            <div className="workspace-components-row-heading">
              <strong>{componentLabels[id].name}</strong>
              <span className="workspace-components-status" data-status={status}>{statusLabels[status]}</span>
            </div>
            <p dir="auto">{message}</p>
            {(version?.trim() || bytes !== undefined) && <dl className="workspace-components-meta">
              {version?.trim() && <div><dt>版本</dt><dd><bdi>{version}</bdi></dd></div>}
              {bytes !== undefined && <div><dt>{componentLabels[id].size}</dt><dd>{formatSize(bytes)}</dd></div>}
            </dl>}
          </li>)}
        </ul>
      </div>}
    </div>
  </details>
}
