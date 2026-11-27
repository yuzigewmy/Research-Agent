import { useId, useLayoutEffect, useState } from 'react'

const themeKey = 'research-agent.appearance.theme'
const sidebarKey = 'research-agent.appearance.sidebar'

const themes = { system: '跟随系统', light: '浅色', dark: '深色' } as const
type Theme = keyof typeof themes

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

  function chooseTheme(next: Theme) {
    setTheme(next)
    rememberPreference(themeKey, next)
  }

  function toggleSidebar() {
    const next = !sidebarHidden
    setSidebarHidden(next)
    rememberPreference(sidebarKey, next ? 'hidden' : 'visible')
  }

  return { theme, chooseTheme, sidebarHidden, toggleSidebar }
}

export function WorkbenchPreferences({ theme, chooseTheme, sidebarHidden, toggleSidebar }: ReturnType<typeof useWorkbenchPreferences>) {
  const themeId = useId()
  return <div className="workbench-preferences">
    <div className="workbench-theme field">
      <label htmlFor={themeId}>主题</label>
      <select id={themeId} value={theme} onChange={event => chooseTheme(event.currentTarget.value as Theme)}>
        {Object.entries(themes).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
      </select>
    </div>
    <button className="button outline" type="button" aria-expanded={!sidebarHidden} aria-controls="project-index" onClick={toggleSidebar}>
      {sidebarHidden ? '展开项目列表' : '收起项目列表'}
    </button>
  </div>
}
