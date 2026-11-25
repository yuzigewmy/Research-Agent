import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, request } from './projects'

const maxRestoreSize = 1024 ** 3

type RestoreReceipt = { directory: string }

function isRestoreReceipt(value: unknown): value is RestoreReceipt {
  const receipt = value as Partial<RestoreReceipt> | null
  return typeof receipt?.directory === 'string' && receipt.directory.length > 0
}

export function WorkspaceRestore() {
  const id = useId()
  const active = useRef<AbortController | null>(null)
  const [file, setFile] = useState<File>()
  const [loading, setLoading] = useState(false)
  const [receipt, setReceipt] = useState<RestoreReceipt>()
  const [error, setError] = useState('')
  useEffect(() => () => active.current?.abort(), [])

  async function restore(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!file || active.current || receipt) return
    if (file.size > maxRestoreSize) {
      setError('浏览器一次可恢复 1 GiB 以内的备份。较大的备份请使用命令行恢复。')
      return
    }
    const controller = new AbortController()
    active.current = controller
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(6 * 60_000)])
    setLoading(true)
    setError('')
    try {
      const result = await request('/api/v1/workspace-restore', isRestoreReceipt, {
        method: 'POST', body: file, headers: { 'Content-Type': 'application/zip' }, signal,
      })
      if (!controller.signal.aborted) setReceipt(result)
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(signal.aborted ? '未能取得恢复结果。请查看资料库目录中的恢复副本。' : errorMessage(cause))
      }
    } finally {
      active.current = null
      if (!controller.signal.aborted) setLoading(false)
    }
  }

  return <form className="workspace-restore field" onSubmit={(event) => void restore(event)}>
    <label htmlFor={id}>选择工作区备份</label>
    <input id={id} type="file" accept=".zip,application/zip" aria-describedby={`${id}-hint`}
      disabled={loading} onChange={(event) => {
        setFile(event.currentTarget.files?.[0])
        setReceipt(undefined)
        setError('')
      }} />
    <p id={`${id}-hint`}>选择工作台导出的 ZIP，压缩包最多 1 GiB，库内数据库文件最多 64 MiB。校验通过后保存为新资料库。</p>
    <button className="button outline" type="submit" disabled={!file || loading || !!receipt}>恢复到新目录</button>
    {loading && <p role="status">正在上传并校验备份…</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {receipt && <div role="status">
      <p>已恢复到 <bdi>{receipt.directory}</bdi></p>
      <p>启动程序时用 <code>--data-dir</code> 指定该目录。也可用 <code>--listen</code> 另选端口，独立查看恢复结果。</p>
    </div>}
  </form>
}
