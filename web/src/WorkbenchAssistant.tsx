import { useId, useState } from 'react'
import { t, useLocale } from './i18n'
import type { WorkbenchLanguage } from './i18n'

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
  language?: WorkbenchLanguage
}) {
  const id = useId()
  const contextLocale = useLocale()
  const locale = language ?? contextLocale
  const [open, setOpen] = useState(() => readFlag(assistantOpenKey))
  const [muted, setMuted] = useState(() => readFlag(assistantMutedKey))
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
      <strong id={`${id}-heading`}>{t(locale, 'assistant.title')}</strong>
      <div className="workbench-assistant-controls">
        <button className="button outline workbench-assistant-btn" type="button"
          aria-pressed={muted} onClick={toggleMuted}>
          {t(locale, muted ? 'assistant.muted' : 'assistant.mute')}
        </button>
        <button className="button outline workbench-assistant-btn" type="button"
          aria-expanded={open} aria-controls={`${id}-body`} onClick={toggleOpen}>
          {t(locale, open ? 'assistant.collapse' : 'assistant.open')}
        </button>
      </div>
    </div>
    <div id={`${id}-body`} className="workbench-assistant-body" hidden={!open}>
      {notificationDenied && (
        <p className="workbench-assistant-fallback" role="status">
          {t(locale, 'assistant.desktopDenied')}
        </p>
      )}
      {!notificationSupported && (
        <p className="workspace-note" role="status">
          {t(locale, 'assistant.desktopUnsupported')}
        </p>
      )}
      {muted && (
        <p className="workspace-note" role="status">
          {t(locale, 'assistant.mutedNotice')}
        </p>
      )}
      <div className="workbench-assistant-links" role="navigation" aria-label={t(locale, 'assistant.quickNav')}>
        {projectId !== null ? <>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#conversation-title')}>
            {t(locale, 'assistant.navConversation')}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('.workflow-panel')}>
            {t(locale, 'assistant.navWorkflow')}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#project-workspace')}>
            {t(locale, 'assistant.navWorkspace')}
          </button>
          <button className="button outline workbench-assistant-btn" type="button"
            onClick={() => scrollToSelector('#project-diagrams', true)}>
            {t(locale, 'assistant.navDiagrams')}
          </button>
        </> : (
          <p className="workspace-note">
            {t(locale, 'assistant.emptyProjectHint')}
          </p>
        )}
        <button className="button outline workbench-assistant-btn" type="button"
          onClick={() => scrollToSelector('.notifications', true)}>
          {t(locale, 'assistant.navNotifications')}
        </button>
        <button className="button outline workbench-assistant-btn" type="button"
          onClick={() => scrollToSelector('.workspace-components', true)}>
          {t(locale, 'assistant.navComponents')}
        </button>
      </div>
    </div>
  </aside>
}
