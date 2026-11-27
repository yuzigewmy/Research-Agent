import { useState } from 'react'
import { workspaceHTMLPath } from './workspace'
import type { FileRecord } from './workspace'

export function WorkspaceHTMLPreview({ projectId, file }: { projectId: string; file: FileRecord }) {
  const [open, setOpen] = useState(false)
  const title = `HTML 预览 ${file.path}`

  return <details className="workspace-html-preview" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>预览已保存的 HTML</summary>
    {open && <div className="workspace-preview">
      <p className="workspace-note turn-content">版本 <bdi>{file.versionId}</bdi> · SHA-256 <code>{file.sha256}</code></p>
      <div className="workspace-actions">
        <a className="button outline" download href={workspaceHTMLPath(projectId, file.versionId, 'download')}>下载此版本原文</a>
      </div>
      <iframe title={title} src={workspaceHTMLPath(projectId, file.versionId, 'preview')}
        sandbox="" referrerPolicy="no-referrer" className="engineering-preview-frame" />
    </div>}
  </details>
}
