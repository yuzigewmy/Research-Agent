import { useEffect, useId, useRef, useState } from 'react'
import { readModelConnection, testModelConnection } from './modelConnectionApi'
import type { ModelConnectionStatus } from './modelConnectionApi'
import { errorMessage } from './projects'
import './model-connection.css'

type ConfigState = { kind: 'loading' } | { kind: 'ready'; status: ModelConnectionStatus } | { kind: 'error'; message: string }
type TestState = { kind: 'testing' | 'success' } | { kind: 'error'; message: string }

export function ModelConnection() {
  const [open, setOpen] = useState(false)
  return <details className="model-connection" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>模型连接</summary>
    {open && <ConnectionDetails />}
  </details>
}

function ConnectionDetails() {
  const testErrorId = useId()
  const [config, setConfig] = useState<ConfigState>({ kind: 'loading' })
  const [reload, setReload] = useState(0)
  const [result, setResult] = useState<TestState>()
  const activeTest = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void readModelConnection(signal).then((status) => {
      if (!controller.signal.aborted) setConfig({ kind: 'ready', status })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setConfig({ kind: 'error', message: signal.aborted ? '读取模型配置超时，请重试。' : errorMessage(cause) })
      }
    })
    return () => controller.abort()
  }, [reload])

  useEffect(() => () => activeTest.current?.abort(), [])

  async function test() {
    if (config.kind !== 'ready' || !config.status.configured || activeTest.current) return
    const controller = new AbortController()
    activeTest.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(35_000)])
    setResult({ kind: 'testing' })
    try {
      await testModelConnection(signal)
      signal.throwIfAborted()
      setResult({ kind: 'success' })
    } catch (cause) {
      if (!controller.signal.aborted) {
        setResult({ kind: 'error', message: signal.aborted ? '测试连接超时，请重试。' : errorMessage(cause) })
      }
    } finally {
      activeTest.current = null
    }
  }

  return <div className="model-connection-body">
    <p className="model-connection-note">
      在服务端设置 <code>RESEARCH_MODEL_BASE_URL</code> 和 <code>RESEARCH_MODEL_NAME</code>。
      按模型服务要求设置 <code>RESEARCH_MODEL_API_KEY</code>，可用 <code>RESEARCH_MODEL_REASONING_EFFORT</code> 指定推理档。修改后请重启服务。
    </p>
    {config.kind === 'loading' && <p className="model-connection-note" role="status">正在读取模型配置…</p>}
    {config.kind === 'error' && <>
      <p className="form-error" role="alert">{config.message}</p>
      <button className="button outline" type="button" onClick={() => {
        setConfig({ kind: 'loading' })
        setReload((value) => value + 1)
      }}>重试读取配置</button>
    </>}
    {config.kind === 'ready' && (config.status.configured ? <>
      <dl className="model-connection-settings">
        <div><dt>模型</dt><dd><bdi>{config.status.model || '未提供'}</bdi></dd></div>
        <div><dt>推理档</dt><dd><bdi>{config.status.reasoningEffort || '默认'}</bdi></dd></div>
      </dl>
      <button className="button outline" type="button" disabled={result?.kind === 'testing'}
        aria-describedby={result?.kind === 'error' ? testErrorId : undefined} onClick={() => void test()}>测试连接</button>
      {result?.kind === 'testing' && <p className="model-connection-note" role="status">正在测试连接…</p>}
      {result?.kind === 'success' && <p role="status">模型连接正常。</p>}
      {result?.kind === 'error' && <p className="form-error" id={testErrorId} role="alert">{result.message}</p>}
    </> : <p className="model-connection-note" role="status">模型未配置。</p>)}
  </div>
}
