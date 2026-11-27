import { useEffect, useId, useRef, useState } from 'react'
import { connectionModelLabels, readModelConnection, testModelConnection } from './modelConnectionApi'
import type { ConnectionModelId, ModelCapabilityCatalog, ModelConnectionStatus } from './modelConnectionApi'
import { errorMessage } from './projects'
import './model-connection.css'

type ConfigState = { kind: 'loading' } | { kind: 'ready'; status: ModelConnectionStatus } | { kind: 'error'; message: string }
type TestState = { kind: 'testing' | 'success' } | { kind: 'error'; message: string }

export function ModelConnection() {
  const [open, setOpen] = useState(false)
  return <details className="model-connection" onToggle={(event) => {
    if (event.target === event.currentTarget) setOpen(event.currentTarget.open)
  }}>
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
  const generation = selected?.id === 'image'

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
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(generation ? 125_000 : 35_000)])
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
        {generation
          ? <div><dt>图片质量</dt><dd><bdi>{selected?.imageQuality}</bdi></dd></div>
          : <div><dt>推理档</dt><dd><bdi>{modelInfo?.reasoningEffort || '默认'}</bdi></dd></div>}
      </dl>
      <p className="model-connection-note">{generation
        ? '测试会生成一张 1024 × 1024 的 PNG 并检查文件。'
        : vision ? '测试会发送内置方块图，核对模型读取的数量。'
          : '测试会发送一条固定消息，验证所选模型的文本流式回复。'}</p>
      <button className="button outline" type="button" disabled={result?.kind === 'testing'}
        aria-describedby={result?.kind === 'error' ? testErrorId : undefined} onClick={() => void test()}>测试连接</button>
      {result?.kind === 'testing' && <p className="model-connection-note" role="status">正在测试连接…</p>}
      {result?.kind === 'success' && <p role="status">{generation ? '测试通过，已收到合格的 PNG。'
        : vision ? '测试通过，模型已读出图中方块的数量。' : '模型连接正常。'}</p>}
      {result?.kind === 'error' && <p className="form-error" id={testErrorId} role="alert">{result.message}</p>}
    </> : <p className="model-connection-note" role="status">模型未配置。</p>)}
    {config.kind === 'ready' && config.status.catalog && <CapabilityCatalog catalog={config.status.catalog} />}
  </div>
}

const catalogStatusLabels = {
  configured: '已配置',
  inherited: '继承全局默认',
  unconfigured: '未配置',
}

const capabilityLabels = {
  text: '文本处理',
  vision: '图像识别',
  image: '图片生成',
}

const limitLabels: Readonly<Record<string, string>> = {
  conversationTime: '对话回复时限',
  conversationInput: '对话输入上限',
  conversationOutput: '对话输出上限',
  workflowStepTime: '工作流单步时限',
  reviewTime: '审稿时限',
  reviewSource: '审稿原稿大小上限',
  automaticReviewTime: '多轮审稿时限',
  automaticReviewRounds: '多轮审稿轮数上限',
  editTime: '改稿时限',
  editSelection: '改稿选区大小上限',
  visionTime: '视觉识别时限',
  visionImageBytes: '识别图片文件大小上限',
  visionImageEdge: '识别图片单边长度上限',
  visionImagePixels: '识别图片总像素上限',
  imageTime: '图片生成时限',
  imageBytes: '生成图片文件大小上限',
}

const limitUnits = { seconds: '秒', bytes: '字节', count: '次', pixels: '像素' }
const catalogNumber = new Intl.NumberFormat('zh-CN')

function CapabilityCatalog({ catalog }: { catalog: ModelCapabilityCatalog }) {
  return <details className="model-capability-catalog">
    <summary>模型能力目录</summary>
    <div className="model-catalog-body">
      <p className="model-connection-note">目录显示服务端配置与技术上限。连通性以「测试连接」结果为准。</p>
      <h2>模型角色</h2>
      <ul className="model-catalog-roles">
        {catalog.roles.map((role) => <li key={role.id} data-model-role={role.id} data-model-status={role.status}>
          <p className="model-catalog-role-heading">
            <strong>{connectionModelLabels[role.id]}</strong>
            <span>{catalogStatusLabels[role.status]}</span>
          </p>
          <dl className="model-catalog-settings">
            <div><dt>用途</dt><dd>{capabilityLabels[role.capability]}</dd></div>
            {role.model && <div><dt>模型</dt><dd><bdi>{role.model}</bdi></dd></div>}
            {role.reasoningEffort && <div><dt>推理档</dt><dd><bdi>{role.reasoningEffort}</bdi></dd></div>}
            {role.imageQuality && <div><dt>图片质量</dt><dd><bdi>{role.imageQuality}</bdi></dd></div>}
            <div><dt>连接测试时限</dt><dd>{catalogNumber.format(role.testSeconds)} 秒</dd></div>
          </dl>
        </li>)}
      </ul>
      <h2>模板默认模型</h2>
      <dl className="model-catalog-facts">
        {catalog.templateDefaults.map(({ templateId, label, modelId }) => {
          const role = catalog.roles.find(({ id }) => id === modelId)
          return <div key={templateId} data-template-id={templateId}>
            <dt>{label}</dt>
            <dd>{connectionModelLabels[modelId]} · {role?.model ? <bdi>{role.model}</bdi> : '未配置'}</dd>
          </div>
        })}
      </dl>
      <h2>技术上限</h2>
      <dl className="model-catalog-facts">
        {catalog.limits.map(({ id, value, unit }) => <div key={id} data-limit-id={id}>
          <dt>{Object.hasOwn(limitLabels, id) ? limitLabels[id] : id}</dt>
          <dd>{catalogNumber.format(value)} {unit === 'count' && id === 'automaticReviewRounds' ? '轮' : limitUnits[unit]}</dd>
        </div>)}
      </dl>
    </div>
  </details>
}
