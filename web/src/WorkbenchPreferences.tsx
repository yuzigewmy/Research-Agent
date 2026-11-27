import { useId, useLayoutEffect, useState } from 'react'
import { t } from './i18n'
import type { WorkbenchLanguage } from './i18n'

const themeKey = 'research-agent.appearance.theme'
const sidebarKey = 'research-agent.appearance.sidebar'
const languageKey = 'research-agent.appearance.language'

export type { WorkbenchLanguage }

type Theme = 'system' | 'light' | 'dark'
const themeOptions: readonly Theme[] = ['system', 'light', 'dark']

const languageLabels: Record<WorkbenchLanguage, string> = {
  'zh-CN': '简体中文',
  en: 'English',
}

function storedPreference(key: string) {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function rememberPreference(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // The current page still uses the selected preference.
  }
}

export function useWorkbenchPreferences() {
  const [theme, setTheme] = useState<Theme>(() => {
    const saved = storedPreference(themeKey)
    return saved === 'light' || saved === 'dark' ? saved : 'system'
  })
  const [sidebarHidden, setSidebarHidden] = useState(() => storedPreference(sidebarKey) === 'hidden')
  const [language, setLanguage] = useState<WorkbenchLanguage>(() => {
    const saved = storedPreference(languageKey)
    return saved === 'en' ? 'en' : 'zh-CN'
  })

  useLayoutEffect(() => {
    const root = document.documentElement
    const system = window.matchMedia('(prefers-color-scheme: dark)')
    function applyTheme() {
      root.dataset.workbenchTheme = theme === 'system' ? (system.matches ? 'dark' : 'light') : theme
    }
    applyTheme()
    if (theme === 'system') system.addEventListener('change', applyTheme)
    return () => {
      system.removeEventListener('change', applyTheme)
      delete root.dataset.workbenchTheme
    }
  }, [theme])

  useLayoutEffect(() => {
    document.documentElement.lang = language
  }, [language])

  function chooseTheme(next: Theme) {
    setTheme(next)
    rememberPreference(themeKey, next)
  }

  function toggleSidebar() {
    const next = !sidebarHidden
    setSidebarHidden(next)
    rememberPreference(sidebarKey, next ? 'hidden' : 'visible')
  }

  function chooseLanguage(next: WorkbenchLanguage) {
    setLanguage(next)
    rememberPreference(languageKey, next)
  }

  return { theme, chooseTheme, sidebarHidden, toggleSidebar, language, chooseLanguage }
}

export function WorkbenchPreferences({ theme, chooseTheme, sidebarHidden, toggleSidebar, language, chooseLanguage }: ReturnType<typeof useWorkbenchPreferences>) {
  const themeId = useId()
  const langId = useId()

  return <div className="workbench-preferences">
    <div className="workbench-theme field">
      <label htmlFor={themeId}>{t(language, 'prefs.theme')}</label>
      <select id={themeId} value={theme} onChange={event => chooseTheme(event.currentTarget.value as Theme)}>
        {themeOptions.map((value) => <option key={value} value={value}>{t(language, `prefs.theme.${value}`)}</option>)}
      </select>
    </div>
    <div className="workbench-theme field">
      <label htmlFor={langId}>{t(language, 'prefs.language')}</label>
      <select id={langId} value={language} onChange={event => chooseLanguage(event.currentTarget.value as WorkbenchLanguage)}>
        {Object.entries(languageLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
      </select>
    </div>
    <button className="button outline" type="button" aria-expanded={!sidebarHidden} aria-controls="project-index" onClick={toggleSidebar}>
      {t(language, sidebarHidden ? 'prefs.showProjects' : 'prefs.hideProjects')}
    </button>
  </div>
}
