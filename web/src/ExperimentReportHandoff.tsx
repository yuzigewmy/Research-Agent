import { useEffect, useRef, useState } from 'react'
import { errorMessage } from './projects'
import { saveComparisonReport } from './tasks'

export function ExperimentReportHandoff({ projectId, taskId, onSaved }: {
  projectId: string
  taskId: string
  onSaved: (path: string) => void
}) {
  const active = useRef<AbortController | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => () => active.current?.abort(), [])

  async function save() {
    if (active.current || saved) return
    const controller = new AbortController()
    active.current = controller
    setSaving(true)
    setError('')
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
      const file = await saveComparisonReport(projectId, taskId, signal)
      if (controller.signal.aborted) return
      onSaved(file.path)
      setSaved(true)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      active.current = null
      if (!controller.signal.aborted) setSaving(false)
    }
  }

  return <>
    <button className="button outline" type="button" aria-disabled={saving || saved} onClick={() => void save()}>
      {saved ? '报告已保存' : saving ? '正在保存报告…' : '保存报告到工作区'}
    </button>
    {saved && <a className="button outline" href="#project-workspace">前往文本工作区</a>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </>
}
