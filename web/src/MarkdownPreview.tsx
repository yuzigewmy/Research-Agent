import { lazy, Suspense, useState } from 'react'
import type { Document } from './workspace'
import './markdown-preview.css'

const PreviewContent = lazy(() => import('./MarkdownPreviewContent').catch(() => ({
  default: () => <p className="form-error" role="alert">无法加载 Markdown 预览。可下载当前版本的原文。</p>,
})))

function downloadSource({ content, path }: Document) {
  const url = URL.createObjectURL(new Blob([content], { type: 'text/markdown;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = path.split('/').at(-1) ?? '稿件.md'
  link.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
}

export function MarkdownPreview({ file }: { file: Document }) {
  const [open, setOpen] = useState(false)
  return <details className="markdown-preview" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>预览已保存的 Markdown</summary>
    {open && <div className="markdown-preview-body">
      <p className="workspace-note">版本 <bdi>{file.versionId}</bdi> · SHA-256 <code>{file.sha256}</code></p>
      <div className="workspace-actions">
        <button className="button outline" type="button" onClick={() => downloadSource(file)}>下载此版本原文</button>
      </div>
      <Suspense fallback={<p className="workspace-note" role="status">正在加载 Markdown 预览…</p>}>
        <PreviewContent content={file.content} />
      </Suspense>
    </div>}
  </details>
}
