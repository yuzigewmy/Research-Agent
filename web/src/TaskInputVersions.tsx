import { useEffect, useId, useState } from 'react'
import { errorMessage } from './projects'
import { isMarkdownTaskPath, taskInputLimit } from './tasks'
import type { TaskInputVersion } from './tasks'
import { listFiles, readVersion } from './workspace'
import type { FilePage } from './workspace'

const excerptBytes = 8 * 1024

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
}

function VersionPreview({ projectId, input }: { projectId: string; input: TaskInputVersion }) {
  const { path, sha256, versionId } = input
  const [preview, setPreview] = useState<{ text: string; truncated: boolean }>()
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void readVersion(projectId, versionId, requestSignal(controller)).then((file) => {
      if (controller.signal.aborted) return
      if (file.versionId !== versionId || file.path !== path || file.sha256 !== sha256) {
        setError('无法核对所选文件版本。')
        return
      }
      const bytes = new TextEncoder().encode(file.content)
      const text = new TextDecoder().decode(bytes.subarray(0, excerptBytes), { stream: true })
      setPreview({ text, truncated: bytes.length > excerptBytes })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(error))
    })
    return () => controller.abort()
  }, [attempt, path, projectId, sha256, versionId])

  if (error) return <div className="task-input-body">
    <p className="form-error" role="alert">{error}</p>
    <div className="task-actions"><button className="button outline" type="button" onClick={() => {
      setError('')
      setAttempt((value) => value + 1)
    }}>重试读取版本</button></div>
  </div>
  if (!preview) return <p className="task-note" role="status">正在读取所选版本…</p>
  return <>
    {preview.text
      ? <pre className="workspace-text" dir="auto" tabIndex={0} aria-label={`文件节选：${path}`}>{preview.text}</pre>
      : <p className="task-note">此版本没有可预览的文字。</p>}
    {preview.truncated && <p className="task-note">仅显示文件开头最多 8 KiB。</p>}
  </>
}

export function TaskInputSummary({ projectId, inputs }: { projectId: string; inputs?: TaskInputVersion[] }) {
  const [previewId, setPreviewId] = useState<string>()
  if (!inputs?.length) return null

  return <div className="task-input-body">
    <p className="task-note">任务输入</p>
    <ul className="task-input-list" role="list" aria-label="任务引用的工作区文件">
      {inputs.map((input) => <li key={input.versionId}>
        <p className="task-note"><bdi>{input.path}</bdi> · SHA-256 <code title={input.sha256}>{input.sha256.slice(0, 12)}</code></p>
        <details className="task-input-disclosure" open={previewId === input.versionId} onToggle={(event) => {
          if (event.currentTarget.open) setPreviewId(input.versionId)
          else setPreviewId((current) => current === input.versionId ? undefined : current)
        }}>
          <summary aria-label={`查看所选版本：${input.path}`}>查看所选版本</summary>
          {previewId === input.versionId && <>
            <p className="task-note">版本 <bdi>{input.versionId}</bdi></p>
            <VersionPreview projectId={projectId} input={input} />
          </>}
        </details>
      </li>)}
    </ul>
  </div>
}

type PickerProps = {
  projectId: string
  inputs: TaskInputVersion[]
  disabled: boolean
  markdown?: 'single' | 'multiple'
  onChange: (inputs: TaskInputVersion[]) => void
}

export function TaskInputVersions(props: PickerProps) {
  const [open, setOpen] = useState(false)
  const limit = props.markdown === 'single' ? 1 : taskInputLimit
  return <div className="task-input-body">
    <details className="task-input-disclosure" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>添加工作区文件 <span className="task-note">（已选 {props.inputs.length}/{limit}）</span></summary>
      {open && <FilePicker key={props.projectId} {...props} />}
    </details>
    <p className="task-note">
      {props.markdown === 'single' ? '请选择一份已保存的 Markdown 稿件（.md 或 .markdown）。'
        : props.markdown === 'multiple' ? `请选择 1 至 ${taskInputLimit} 份已保存的 Markdown 文件（.md 或 .markdown）。`
          : `最多选择 ${taskInputLimit} 个文件。确认运行后，每个所选文件开头最多 8 KiB 的文字会发送给研究模型。`}
    </p>
  </div>
}

function FilePicker({ projectId, inputs, disabled, markdown, onChange }: PickerProps) {
  const pickerId = useId()
  const single = markdown === 'single'
  const [page, setPage] = useState<FilePage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    void listFiles(projectId, requestSignal(controller), query.cursor, markdown ? 'markdown' : undefined).then((next) => {
      if (controller.signal.aborted) return
      setPage((previous) => {
        if (!query.cursor) return next
        const paths = new Set(previous.items.map(({ path }) => path))
        return { ...next, items: [...previous.items, ...next.items.filter(({ path }) => !paths.has(path))] }
      })
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [markdown, projectId, query])

  function load(cursor?: string) {
    setLoading(true)
    setError('')
    setQuery({ cursor })
  }

  function select(input: TaskInputVersion, checked: boolean) {
    if (disabled || (markdown && !isMarkdownTaskPath(input.path))) return
    if (single) {
      if (checked) onChange([input])
      return
    }
    const present = inputs.some(({ path }) => path === input.path)
    if (!checked) onChange(inputs.filter(({ path }) => path !== input.path))
    else if (present) onChange(inputs.map((previous) => previous.path === input.path ? input : previous))
    else if (inputs.length < taskInputLimit) onChange([...inputs, input])
  }

  const listedPaths = new Set(page.items.map(({ path }) => path))
  const choices = [...inputs.filter(({ path }) => !listedPaths.has(path)), ...page.items]
    .filter(({ path }) => !markdown || isMarkdownTaskPath(path))
  return <div className="task-input-body">
    <div className="task-actions">
      <button className="button outline" type="button" disabled={loading} onClick={() => load()}>刷新文件列表</button>
    </div>
    {loading && <p className="task-note" role="status">正在读取工作区文件…</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loading && !error && !choices.length && <p className="task-note">
      {markdown ? '工作区暂无已保存的 Markdown 文件。' : '工作区暂无已保存的文件。'}
    </p>}
    {choices.length > 0 && <ul className="task-input-list task-input-choices" role="list" aria-label="可选工作区文件">
      {choices.map(({ path, versionId, sha256 }) => {
        const selected = inputs.find((input) => input.path === path)
        const outdated = !!selected && selected.versionId !== versionId
        const unavailable = disabled || loading || !!error
        const input = { path, versionId, sha256 }
        return <li key={path}>
          <label className="task-input-option">
            <input type={single ? 'radio' : 'checkbox'} name={single ? pickerId : undefined} checked={!!selected}
              disabled={disabled || (!selected && (unavailable || (!single && inputs.length >= taskInputLimit)))}
              onChange={(event) => select(input, event.currentTarget.checked)} />
            <bdi>{path}</bdi>
          </label>
          <p className="task-note">
            {selected ? '已选摘要' : '文件摘要'} <code title={selected?.sha256 ?? sha256}>{(selected?.sha256 ?? sha256).slice(0, 12)}</code>
          </p>
          {outdated && <div className="task-actions">
            <button className="button outline" type="button" disabled={unavailable}
              aria-label={`替换为当前版本：${path}`} onClick={() => select(input, true)}>替换为当前版本</button>
            <p className="task-note">当前版本摘要 <code title={sha256}>{sha256.slice(0, 12)}</code></p>
          </div>}
        </li>
      })}
    </ul>}
    {!loading && page.nextCursor && <div className="task-actions">
      <button className="button outline" type="button" onClick={() => load(page.nextCursor)}>
        {error && query.cursor ? '重试加载更多文件' : '加载更多文件'}
      </button>
    </div>}
  </div>
}
