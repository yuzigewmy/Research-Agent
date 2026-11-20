import { useState } from 'react'
import { TaskPanel } from './TaskPanel'
import type { TaskPanelSync } from './TaskPanel'

export function ConversationTasks({ projectId, turnId, text, sync }: {
  projectId: string
  turnId: string
  text: string
  sync?: TaskPanelSync
}) {
  const [opened, setOpened] = useState(false)

  return <details onToggle={(event) => {
    if (event.currentTarget.open) setOpened(true)
  }}>
    <summary className="turn-role">从此对话提议任务</summary>
    {opened && <TaskPanel projectId={projectId} sourceTurnId={turnId} initialInstruction={text} sync={sync} />}
  </details>
}
