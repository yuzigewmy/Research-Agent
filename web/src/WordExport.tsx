import { useEffect, useRef, useState } from 'react'
import { errorMessage, request } from './projects'

interface WordFile {
  filename: string
  sourceSha256: string
  sha256: string
}

interface WordStatus {
  configured: boolean
  file: WordFile | null
}

function isWordFile(value: unknown): value is WordFile {
  const file = value as Partial<WordFile> | null
  return !!file && typeof file.filename === 'string' && typeof file.sourceSha256 === 'string' && typeof file.sha256 === 'string'
}

function isWordStatus(value: unknown): value is WordStatus {
  const status = value as Partial<WordStatus> | null
  return !!status && typeof status.configured === 'boolean' && (status.file === null || isWordFile(status.file))
}

export function WordExport({ projectId, versionId, sourceSha256, dirty = false }: {
  projectId: string
  versionId: string
  sourceSha256: string
  dirty?: boolean
}) {
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/word`
  const [status, setStatus] = useState<WordStatus>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [revision, setRevision] = useState(0)
  const active = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    request(route, isWordStatus, { signal: controller.signal }).then((value) => {
      if (!controller.signal.aborted) setStatus(value)
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(err))
    })
    return () => {
      controller.abort()
      active.current?.abort()
    }
  }, [route, revision])

  async function generate() {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    setBusy(true)
    setError('')
    try {
      const file = await request(route, isWordFile, { method: 'POST', signal: controller.signal })
      if (!controller.signal.aborted) setStatus({ configured: true, file })
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
      active.current = null
    }
  }

  const file = status?.file?.sourceSha256 === sourceSha256 ? status.file : null
  return <div className="workspace-actions">
    {file && <a className="button outline" href={`${route}/download`} download>下载 Word</a>}
    {!file && status?.configured && <button className="button outline" type="button" disabled={busy || dirty}
      onClick={() => void generate()}>{busy ? '正在生成 Word…' : '导出 Word'}</button>}
    {status?.configured === false && !file && <p className="workspace-note">Word 导出组件未启用。</p>}
    {dirty && status?.configured && <p className="workspace-note">{file ? '可下载上次保存版本的 Word。' : '请保存稿件后导出。'}</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {error && !status && <button className="button outline" type="button" onClick={() => {
      setError('')
      setRevision((current) => current + 1)
    }}>重试读取导出状态</button>}
  </div>
}
