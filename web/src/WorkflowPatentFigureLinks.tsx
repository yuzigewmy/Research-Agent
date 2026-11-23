import { workflowPatentFigurePath } from './workflow'

export function WorkflowPatentFigureLinks({ projectId, runId, artifactId }: {
  projectId: string
  runId: string
  artifactId: string
}) {
  return <>
    <a className="workflow-download" href={workflowPatentFigurePath(projectId, runId, artifactId, 'system')} download>
      下载系统框图 SVG
    </a>
    <a className="workflow-download" href={workflowPatentFigurePath(projectId, runId, artifactId, 'process')} download>
      下载操作流程图 SVG
    </a>
  </>
}
