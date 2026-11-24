import './workspace-backup.css'

export function WorkspaceBackup() {
  return <details className="workspace-backup">
    <summary>工作区备份</summary>
    <p>下载包含项目、答卷、对话和资料各版本的 ZIP。备份包括整个工作区，请妥善保存。</p>
    <form action="/api/v1/workspace-backup" method="post" target="_blank" rel="noopener">
      <button className="button outline" type="submit">下载工作区备份</button>
    </form>
    <p>恢复时使用启动程序的 <code>--restore</code> 选项，将备份恢复到一个新的资料库目录。</p>
  </details>
}
