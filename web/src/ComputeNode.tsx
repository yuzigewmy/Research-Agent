import { useEffect, useRef, useState } from 'react'
import { t, useLocale } from './i18n'
import { errorMessage, request } from './projects'
import './model-connection.css'

const path = '/api/v1/compute-node'
const numberFormat = new Intl.NumberFormat('zh-CN')

type ConfigResponse = { configured: boolean }
type Capability = {
  id: 'paired-mean-v1'
  maxInputBytes: number
  maxRows: number
  timeoutSeconds: number
}
type CheckResponse = { name: string; capabilities: Capability[] }
type ConfigState = { kind: 'loading' } | { kind: 'ready'; configured: boolean } | { kind: 'error'; message: string }

function isConfigResponse(value: unknown): value is ConfigResponse {
  return typeof (value as Partial<ConfigResponse> | null)?.configured === 'boolean'
}

function isLimit(value: unknown): boolean {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isCapability(value: unknown): value is Capability {
  const capability = value as Partial<Capability> | null
  return capability?.id === 'paired-mean-v1' && isLimit(capability.maxInputBytes)
    && isLimit(capability.maxRows) && isLimit(capability.timeoutSeconds)
}

function isCheckResponse(value: unknown): value is CheckResponse {
  const response = value as Partial<CheckResponse> | null
  return typeof response?.name === 'string' && response.name.trim().length > 0
    && Array.isArray(response.capabilities) && response.capabilities.every(isCapability)
}

export function ComputeNode() {
  const locale = useLocale()
  const [open, setOpen] = useState(false)
  const [config, setConfig] = useState<ConfigState>({ kind: 'loading' })
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<CheckResponse>()
  const [checkError, setCheckError] = useState<string>()
  const activeCheck = useRef<AbortController | null>(null)
  const configured = config.kind === 'ready' && config.configured

  function loadConfig() {
    setConfig({ kind: 'loading' })
    setLoadAttempt((attempt) => attempt + 1)
  }

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void request(path, isConfigResponse, { signal, cache: 'no-store' }).then(({ configured }) => {
      signal.throwIfAborted()
      setConfig({ kind: 'ready', configured })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setConfig({ kind: 'error', message: signal.aborted ? '读取配置超时，请重试。' : errorMessage(cause) })
      }
    })
    return () => controller.abort()
  }, [open, loadAttempt])

  useEffect(() => () => activeCheck.current?.abort(), [])

  async function check() {
    if (!configured || activeCheck.current) return
    const controller = new AbortController()
    activeCheck.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    setChecking(true)
    setCheckError(undefined)
    try {
      const next = await request(`${path}/test`, isCheckResponse, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', signal,
      })
      signal.throwIfAborted()
      setResult(next)
    } catch (cause) {
      if (!controller.signal.aborted) {
        setCheckError(signal.aborted ? '检查超时，请重试。' : errorMessage(cause))
      }
    } finally {
      activeCheck.current = null
      if (!controller.signal.aborted) setChecking(false)
    }
  }

  return <details className="model-connection" id="compute-node" onToggle={(event) => {
    if (event.target !== event.currentTarget) return
    const expanded = event.currentTarget.open
    if (expanded) loadConfig()
    setOpen(expanded)
  }}>
    <summary>{t(locale, 'section.computeNode')}</summary>
    <div className="model-connection-body">
      <p className="model-connection-note">检查只读取节点状态和能力。</p>
      {config.kind === 'loading' && <p className="model-connection-note" role="status">正在读取计算节点配置…</p>}
      {config.kind === 'error' && <>
        <p className="form-error" role="alert">读取计算节点配置失败。{config.message}</p>
        <button className="button outline" type="button" data-compute-node-retry-config
          onClick={loadConfig}>重试读取配置</button>
      </>}
      {config.kind === 'ready' && (config.configured ? <>
        <p className="model-connection-note">{t(locale, 'section.computeNodeConfigured')}</p>
        <button className="button outline" type="button" data-compute-node-check disabled={checking}
          onClick={() => void check()}>{t(locale, 'section.computeNodeCheck')}</button>
      </> : <p className="model-connection-note" role="status" data-compute-node-unconfigured>
        计算节点未配置。请在服务端设置 <code>RESEARCH_COMPUTE_NODE_URL</code> 和 <code>RESEARCH_COMPUTE_NODE_TOKEN</code>，然后重启服务。
      </p>)}
      {checking && <p className="model-connection-note" role="status">正在检查节点…</p>}
      {!checking && configured && result && !checkError && <p role="status">节点检查通过。</p>}
      {checkError && <p className="form-error" role="alert" data-compute-node-error>检查节点失败。{checkError}</p>}
      {result && <div data-compute-node-result>
        {(checking || checkError || !configured) &&
          <p className="model-connection-note">以下为上次成功检查的结果。</p>}
        <dl className="model-connection-settings">
          <div><dt>节点名称</dt><dd><bdi>{result.name}</bdi></dd></div>
        </dl>
        {result.capabilities.length === 0 && <p className="model-connection-note">节点未报告可用能力。</p>}
        {result.capabilities.map((capability) => <div key={capability.id} data-compute-node-capability={capability.id}>
          <strong>配对指标对比</strong>
          <dl className="model-connection-settings">
            <div><dt>输入大小上限</dt><dd>{numberFormat.format(capability.maxInputBytes)} 字节</dd></div>
            <div><dt>行数上限</dt><dd>{numberFormat.format(capability.maxRows)} 行</dd></div>
            <div><dt>运行时限</dt><dd>{numberFormat.format(capability.timeoutSeconds)} 秒</dd></div>
          </dl>
        </div>)}
      </div>}
    </div>
  </details>
}
