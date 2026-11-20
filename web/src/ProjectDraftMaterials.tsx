import { useEffect, useId, useRef, useState } from 'react'
import { materialSizeLimit } from './materials'
import type { Material, MaterialPage } from './materials'
import { draftMaterialDownloadPath, listDraftMaterials } from './projectDrafts'
import { errorMessage } from './projects'
import './materials.css'

const materialDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })

type Props = {
  draftId: string
  canUpload: boolean
  uploading: boolean
  onUpload: (file: File) => Promise<Material | undefined>
}

function mergeMaterials(current: Material[], incoming: Material[]) {
  const byId = new Map(current.map((item) => [item.materialId, item]))
  for (const item of incoming) byId.set(item.materialId, item)
  return [...byId.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt))
}

export function ProjectDraftMaterials({ draftId, canUpload, uploading, onUpload }: Props) {
  const id = useId()
  const input = useRef<HTMLInputElement>(null)
  const [page, setPage] = useState<MaterialPage>({ items: [] })
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState('')
  const [uploadError, setUploadError] = useState('')
  const [retryFile, setRetryFile] = useState<File>()
  const [uploaded, setUploaded] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    void listDraftMaterials(draftId, signal, query.cursor).then((next) => {
      if (!controller.signal.aborted) {
        setPage((current) => ({ ...next, items: mergeMaterials(current.items, next.items) }))
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setListError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [draftId, query])

  function load(cursor?: string) {
    setLoading(true)
    setListError('')
    setQuery({ cursor })
  }

  async function upload(file: File) {
    if (!canUpload) return
    setUploadError('')
    setUploaded('')
    setRetryFile(undefined)
    if (file.size > materialSizeLimit) {
      setUploadError('单个文件不能超过 64 MiB。')
      return
    }
    try {
      const item = await onUpload(file)
      if (!item) return
      setPage((current) => ({ ...current, items: mergeMaterials(current.items, [item]) }))
      setUploaded(item.name)
    } catch (error) {
      setUploadError(errorMessage(error))
      setRetryFile(file)
    }
  }

  return <section className="workspace-preview" aria-labelledby={id}>
    <div className="material-heading">
      <h4 id={id}>草稿资料</h4>
      <input ref={input} type="file" hidden onChange={(event) => {
        const file = event.currentTarget.files?.[0]
        event.currentTarget.value = ''
        if (file) void upload(file)
      }} />
      <button className="button outline" type="button" disabled={!canUpload}
        aria-describedby={`${id}-hint`} onClick={() => input.current?.click()}>
        {uploading ? '正在上传…' : '上传草稿资料'}
      </button>
    </div>
    <p className="material-note" id={`${id}-hint`}>
      每份最多 64 MiB。整理草稿时，按文件名取最新版，最多读取最近 4 份文本资料开头的各 8 KiB。创建项目后，原件会进入项目资料库。
    </p>
    {uploaded && <p className="material-note" role="status">已保存资料 <bdi>{uploaded}</bdi></p>}
    {uploadError && <div className="workspace-preview">
      <p className="form-error" role="alert">{uploadError}</p>
      {retryFile && <button className="button outline" type="button" disabled={!canUpload}
        onClick={() => void upload(retryFile)}>重试上传</button>}
    </div>}
    {page.items.length > 0 && <ul className="material-list" role="list" aria-label="草稿资料列表">
      {page.items.map((item) => <li className="material-row" key={item.materialId}>
        <div className="material-info">
          <strong dir="auto">{item.name}</strong>
          <div className="material-meta material-note">
            <span>{item.kind === 'text' ? '文本' : '文件'}</span>
            <span>SHA-256 <code title={item.sha256}>{item.sha256.slice(0, 12)}</code></span>
            <time dateTime={item.createdAt}>{materialDate.format(new Date(item.createdAt))}</time>
          </div>
        </div>
        <a className="button outline" href={draftMaterialDownloadPath(draftId, item.materialId)}
          aria-label={`下载原件：${item.name}`} download>下载原件</a>
      </li>)}
    </ul>}
    {loading && <p className="material-note" role="status">正在读取草稿资料…</p>}
    {!loading && !listError && !page.items.length && <p className="material-note">暂无草稿资料。</p>}
    {listError && <p className="form-error" role="alert">{listError}</p>}
    {!loading && (listError || page.nextCursor) && <div className="material-actions">
      <button className="button outline" type="button" onClick={() => load(listError ? query.cursor : page.nextCursor)}>
        {listError ? '重试读取资料' : '加载更多资料'}
      </button>
    </div>}
  </section>
}
