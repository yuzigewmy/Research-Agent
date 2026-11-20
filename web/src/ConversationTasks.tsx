import { useState } from 'react'
import { TaskPanel } from './TaskPanel'
import type { TaskPanelSync } from './TaskPanel'

export function ConversationTasks({ projectId, turnId, text, proposedTaskId, sync }: {
  projectId: string
  turnId: string
  text: string
  proposedTaskId?: string
  sync?: TaskPanelSync
}) {
  const [open, setOpen] = useState(!!proposedTaskId)
  const [opened, setOpened] = useState(!!proposedTaskId)

  return <details open={open} onToggle={(event) => {
    if (event.target !== event.currentTarget) return
    setOpen(event.currentTarget.open)
    if (event.currentTarget.open) setOpened(true)
  }}>
    <summary className="turn-role">{proposedTaskId ? '助手建议的研究任务' : '从此对话提议任务'}</summary>
    {opened && <TaskPanel projectId={projectId} sourceTurnId={turnId} initialInstruction={text}
      proposedTaskId={proposedTaskId} sync={sync} />}
  </details>
}
