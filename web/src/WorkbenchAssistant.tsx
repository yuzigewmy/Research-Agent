import { useId, useState } from 'react'
import type { WorkbenchLanguage } from './WorkbenchPreferences'

const assistantOpenKey = 'research-agent.assistant.open'
const assistantMutedKey = 'research-agent.assistant.muted'

function readFlag(key: string): boolean {
  try {
    return localStorage.getItem(key) === 'true'
  } catch {
    return false
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    localStorage.setItem(key, value ? 'true' : 'false')
  } catch {
    // 保留当前会话状态。
  }
}

function scrollToSelector(selector: string, openDetails = false) {
  const target = document.querySelector<HTMLElement>(selector)
  if (!target) return
  if (openDetails && target instanceof HTMLDetailsElement) target.open = true
  target.scrollIntoView({ behavior: 'smooth', block: 'start' })
  if (target instanceof HTMLDetailsElement) {
    target.querySelector<HTMLElement>('summary')?.focus()
  } else {
    target.focus?.()
  }
}

export function WorkbenchAssistant({ projectId, language }: {
  projectId: string | null
  language: WorkbenchLanguage
}) {
  const id = useId()
  const [open, setOpen] = useState(() => readFlag(assistantOpenKey))
  const [muted, setMuted] = useState(() => readFlag(assistantMutedKey))
  const en = language === 'en'
  const notificationSupported = typeof window !== 'undefined' && window.isSecureContext && typeof window.Notification === 'function'
  const notificationDenied = notificationSupported && Notification.permission === 'denied'

  function toggleOpen() {
    const next = !open
    setOpen(next)
    writeFlag(assistantOpenKey, next)
  }

  function toggleMuted() {
    const next = !muted
    setMuted(next)
    writeFlag(assistantMutedKey, next)
  }

  return <aside className="workbench-assistant" aria-labelledby={`${id}-heading`} data-open={open} data-muted={muted}>
    <div className="workbench-assistant-bar">
      <strong id={`${id}-heading`}>{en ? 'In-Page Assistant' : '页内研究助手'}</strong>
      <div className="workbench-assistant-controls">
        <button className="button outline workbench-assistant-btn" type="button"
          aria-pressed={muted} onClick={toggleMuted}>
          {en ? (muted ? 'Muted' : 'Mute') : (muted ? '已静音' : '静音提示')}
        </button>
        <button className="button outline workbench-assistant-btn" type="button"
          aria-expanded={open} aria-controls={`${id}-body`} onClick={toggleOpen}>
          {en ? (open ? 'Collapse' : 'Open') : (open ? '收起助手' : '展开助手')}
        </button>
      </div>
    </div>
    <div id={`${id}-body`} className="workbench-assistant-body" hidden={!open}>
      {notificationDenied && (
        <p className="workbench-assistant-fallback" role="status">
          {en
            ? 'Browser desktop notifications are blocked. In-app notification center remains active.'
            : '浏览器桌面通知已被拒绝，已自动降级为站内通知提醒。'}
        </p>
      )}
      {!notificationSupported && (
        <p className="workspace-note" role="status">
          {en
            ? 'Desktop notifications are unavailable in this context; check the in-app notification center.'
            : '当前环境不支持桌面通知，请使用顶部站内通知查看任务更新。'}
        </p>
      )}
      {muted && (
        <p className="workspace-note" role="status">
          {en ? 'Assistant alerts are muted.' : '页内助手提醒已静音，仍可通过下方快捷入口跳转。'}
        </p>
      )}
      <div className="workbench-assistant-links" role="navigation" aria-label={en ? 'Quick Navigation' : '快捷入口'}>
        {projectId !== null ? <>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#conversation-title')}>
            {en ? 'Conversation' : '跳转对话区'}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('.workflow-panel')}>
            {en ? 'Workflow' : '跳转工作流'}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#project-workspace')}>
            {en ? 'Text Workspace' : '跳转文本工作区'}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#project-diagrams', true)}>
            {en ? 'Diagrams' : '跳转图表工作区'}
          </button>
        </> : (
          <p className="workspace-note">
            {en ? 'Open or create a project to jump directly to conversation, workflow, or manuscript workspace.' : '打开或新建项目后，可在此快速跳转对话区、工作流与文本工作区。'}
          </p>
        )}
        <button className="button outline workbench-assistant-btn" type="button"
          onClick={() => scrollToSelector('.notifications', true)}>
          {en ? 'Notifications' : '查看未读通知'}
        </button>
        <button className="button outline workbench-assistant-btn" type="button"
          onClick={() => scrollToSelector('.workspace-components', true)}>
          {en ? 'Components' : '环境组件检查'}
        </button>
      </div>
    </div>
  </aside>
}
