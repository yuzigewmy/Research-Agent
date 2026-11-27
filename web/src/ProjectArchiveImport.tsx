import { useEffect, useId, useRef, useState } from 'react'
import {
  errorMessage, importProjectArchive, previewProjectArchiveImport, templates,
} from './projects'
import type { Project, ProjectArchivePreview } from './projects'

const maxArchiveSize = 64 * 1024 * 1024
const sizeFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 })

function formatBytes(bytes: number) {
  if (bytes >= 1_048_576) return `${sizeFormat.format(bytes / 1_048_576)} MiB`
  if (bytes >= 1_024) return `${sizeFormat.format(bytes / 1_024)} KiB`
  return `${bytes} B`
}

export function ProjectArchiveImport({ onImported }: { onImported: (project: Project) => void }) {
  const id = useId()
  const [file, setFile] = useState<File>()
  const [requestId, setRequestId] = useState<string>()
  const [preview, setPreview] = useState<ProjectArchivePreview>()
  const [busy, setBusy] = useState<'preview' | 'import'>()
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const active = useRef<AbortController | null>(null)

  useEffect(() => () => active.current?.abort(), [])

  async function handlePreview(selected?: File) {
    const target = selected ?? file
    if (!target || active.current) return
    if (target.size > maxArchiveSize) {
      setError('项目归档不能超过 64 MiB。')
      return
    }
    const controller = new AbortController()
    active.current = controller
    setBusy('preview')
    setError('')
    setNotice('')
    setPreview(undefined)
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)])
      const data = await previewProjectArchiveImport(target, signal)
      if (!controller.signal.aborted) {
        setPreview(data)
        setRequestId(crypto.randomUUID())
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  async function handleImport() {
    if (!file || !preview || active.current) return
    const reqId = requestId ?? crypto.randomUUID()
    if (!requestId) setRequestId(reqId)
    const controller = new AbortController()
    active.current = controller
    setBusy('import')
    setError('')
    setNotice('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)])
      const result = await importProjectArchive(file, reqId, signal)
      if (controller.signal.aborted) return
      setNotice(
        `已导入新项目「${result.project.displayName || result.project.title}」（资料 ${result.importedMaterials} 份、工作区版本 ${result.importedFiles} 份、文献 ${result.importedCitations} 篇）。`,
      )
      setPreview(undefined)
      setFile(undefined)
      setRequestId(undefined)
      onImported(result.project)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(undefined)
    }
  }

  return (
    <details className="project-archive-import">
      <summary>导入项目归档（ZIP）</summary>
      <div className="project-archive-import-body">
        <div className="field">
          <label htmlFor={`${id}-file`}>选择单项目归档 ZIP（上限 64 MiB）</label>
          <input
            id={`${id}-file`}
            type="file"
            accept=".zip,application/zip"
            disabled={busy !== undefined}
            onChange={(event) => {
              const next = event.currentTarget.files?.[0]
              setFile(next)
              setPreview(undefined)
              setRequestId(undefined)
              setError('')
              setNotice('')
              if (next) void handlePreview(next)
            }}
          />
          <p className="project-management-help">
            导入前会自动校验归档清单与每份对象的 SHA-256 摘要，并创建相互隔离的新项目。
          </p>
        </div>
        <div className="project-management-actions">
          <button
            className="button outline"
            type="button"
            disabled={!file || busy !== undefined}
            onClick={() => void handlePreview()}
          >
            {busy === 'preview' ? '正在校验归档…' : '预览归档清单'}
          </button>
          {preview && (
            <button
              className="button primary"
              type="button"
              disabled={busy !== undefined}
              onClick={() => void handleImport()}
            >
              {busy === 'import' ? '正在导入新项目…' : '确认导入为新项目'}
            </button>
          )}
        </div>
        {error && <p className="form-error" role="alert">{error}</p>}
        {notice && <p className="project-management-help" role="status">{notice}</p>}
        {preview && (
          <div className="project-archive-preview" role="region" aria-label="归档导入预览">
            <dl className="project-archive-meta">
              <div><dt>项目题目</dt><dd dir="auto">{preview.title}</dd></div>
              {preview.displayName && <div><dt>显示名</dt><dd dir="auto">{preview.displayName}</dd></div>}
              <div><dt>项目模板</dt><dd>{templates[preview.templateId]}</dd></div>
              <div><dt>对象总大小</dt><dd>{formatBytes(preview.totalBytes)}</dd></div>
            </dl>
            <div className="project-archive-lists">
              <div>
                <strong>项目资料（{preview.materials.length} 份）</strong>
                {preview.materials.length === 0 ? <p className="project-management-help">无资料</p> : (
                  <ul>
                    {preview.materials.map((item, idx) => (
                      <li key={`${item.name}-${item.sha256}-${idx}`}>
                        <bdi>{item.name}</bdi>（{formatBytes(item.size)}，SHA-256 <code>{item.sha256.slice(0, 8)}</code>）
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              <div>
                <strong>工作区文件版本（{preview.workspaceFiles.length} 份）</strong>
                {preview.workspaceFiles.length === 0 ? <p className="project-management-help">无工作区文件</p> : (
                  <ul>
                    {preview.workspaceFiles.map((item, idx) => (
                      <li key={`${item.path}-${item.sha256}-${idx}`}>
                        <code>{item.path}</code>（{formatBytes(item.size)}
                        {item.latest ? '，当前版本' : '，历史版本'}）
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              <div>
                <strong>已核验文献（{preview.citations.length} 篇）</strong>
                {preview.citations.length === 0 ? <p className="project-management-help">无已核验文献</p> : (
                  <ul>
                    {preview.citations.map((item) => (
                      <li key={item.doi}>
                        <code>{item.doi}</code>{item.title ? ` · ${item.title}` : ''}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              <div>
                <strong>旧工作流成果清单（{preview.artifacts.length} 项）</strong>
                {preview.artifacts.length === 0 ? <p className="project-management-help">无旧成果</p> : (
                  <ul>
                    {preview.artifacts.map((item, idx) => (
                      <li key={`${item.runId}-${item.stepKey}-${idx}`}>
                        <bdi>{item.name}</bdi>（{formatBytes(item.size)}，步骤 <code>{item.stepKey}</code>）
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </div>
        )}
      </div>
    </details>
  )
}
