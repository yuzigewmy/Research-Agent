import { useEffect, useId, useRef, useState } from 'react'
import { connectionModelLabels, readModelConnection, testModelConnection } from './modelConnectionApi'
import type { ConnectionModelId, ModelConnectionStatus } from './modelConnectionApi'
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
  const id = useId()
  const testErrorId = `${id}-error`
  const [config, setConfig] = useState<ConfigState>({ kind: 'loading' })
  const [reload, setReload] = useState(0)
  const [result, setResult] = useState<TestState>()
  const [modelId, setModelId] = useState<ConnectionModelId>('default')
  const activeTest = useRef<AbortController | null>(null)
  const options = config.kind === 'ready' ? config.status.modelOptions ?? [] : []
  const selected = options.find((option) => option.id === modelId) ?? options[0]
  const modelInfo = selected ?? (config.kind === 'ready' ? config.status : undefined)
  const vision = selected?.id === 'vision'

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
      await testModelConnection(selected?.id, signal)
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
      {options.length > 1 && <div className="field model-connection-role">
        <label htmlFor={`${id}-role`}>模型用途</label>
        <select id={`${id}-role`} value={selected?.id} disabled={result?.kind === 'testing'} onChange={(event) => {
          const option = options.find(({ id }) => id === event.currentTarget.value)
          if (option) {
            setModelId(option.id)
            setResult(undefined)
          }
        }}>
          {options.map(({ id }) => <option key={id} value={id}>{connectionModelLabels[id]}</option>)}
        </select>
      </div>}
      <dl className="model-connection-settings">
        <div><dt>模型</dt><dd><bdi>{modelInfo?.model || '未提供'}</bdi></dd></div>
        <div><dt>推理档</dt><dd><bdi>{modelInfo?.reasoningEffort || '默认'}</bdi></dd></div>
      </dl>
      <p className="model-connection-note">{vision
        ? '测试会发送内置方块图，核对模型读取的数量。'
        : '测试会发送一条固定消息，验证所选模型的文本流式回复。'}</p>
      <button className="button outline" type="button" disabled={result?.kind === 'testing'}
        aria-describedby={result?.kind === 'error' ? testErrorId : undefined} onClick={() => void test()}>测试连接</button>
      {result?.kind === 'testing' && <p className="model-connection-note" role="status">正在测试连接…</p>}
      {result?.kind === 'success' && <p role="status">{vision ? '测试通过，模型已读出图中方块的数量。' : '模型连接正常。'}</p>}
      {result?.kind === 'error' && <p className="form-error" id={testErrorId} role="alert">{result.message}</p>}
    </> : <p className="model-connection-note" role="status">模型未配置。</p>)}
  </div>
}
