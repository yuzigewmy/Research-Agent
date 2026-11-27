import { useEffect, useId, useState } from 'react'
import { doiUrl, listCitations } from './literature'
import type { Citation } from './literature'
import { errorMessage } from './projects'

export function PDFEvidenceCitation({ citation }: { citation: Citation }) {
  const publication = [citation.journal, citation.year].filter(Boolean).join(' · ')
  return <div className="manuscript-pdf-evidence-citation literature-details">
    <p className="workspace-note">关联的项目文献</p>
    {citation.title?.trim() && <strong dir="auto">{citation.title}</strong>}
    {publication && <p className="workspace-note" dir="auto">{publication}</p>}
    <a className="literature-link" href={doiUrl(citation.doi)} target="_blank" rel="noopener noreferrer"
      aria-label={`打开 DOI ${citation.doi}（新窗口）`}><bdi>{citation.doi}</bdi></a>
  </div>
}

type PickerProps = {
  id: string
  projectId: string
  sourceName: string
  sourcePage: number
  currentDoi?: string
  blockReason?: string
  disabled: boolean
  saving: boolean
  onSave: (doi: string) => Promise<boolean>
}

export function PDFEvidenceCitationPicker({ id, projectId, sourceName, sourcePage, currentDoi, blockReason,
  disabled, saving, onSave }: PickerProps) {
  const fieldId = useId()
  const [citations, setCitations] = useState<Citation[]>([])
  const [query, setQuery] = useState<{ cursor?: string }>({})
  const [nextCursor, setNextCursor] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [selectedDoi, setSelectedDoi] = useState('')
  const [saveError, setSaveError] = useState('')
  const [saved, setSaved] = useState(false)
  const selected = citations.find(({ doi }) => doi === selectedDoi)

  useEffect(() => {
    const controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
    void listCitations(projectId, signal, query.cursor).then(({ items, nextCursor }) => {
      if (controller.signal.aborted) return
      setCitations((previous) => {
        const byDoi = new Map(previous.map((citation) => [citation.doi, citation]))
        for (const citation of items) byDoi.set(citation.doi, citation)
        return [...byDoi.values()]
      })
      setNextCursor(nextCursor)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setLoadError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [projectId, query])

  function loadPage(cursor?: string) {
    setLoadError('')
    setLoading(true)
    setQuery({ cursor })
  }

  async function confirm() {
    if (!selected || selected.doi === currentDoi || blockReason || disabled) return
    setSaveError('')
    setSaved(false)
    try {
      if (await onSave(selected.doi)) {
        setSelectedDoi('')
        setSaved(true)
      }
    } catch (error) {
      setSaveError(errorMessage(error))
    }
  }

  return <fieldset id={id} className="literature-results manuscript-pdf-evidence-citation-picker">
    <legend>关联项目已核验文献</legend>
    <p id={`${fieldId}-note`} className="workspace-note">核验范围为 DOI 登记元数据。请自行判断文献与原 PDF 证据的对应关系。</p>
    <div className="field">
      <label htmlFor={fieldId}>项目已核验文献</label>
      <select id={fieldId} className="manuscript-pdf-evidence-citation-select" value={selectedDoi}
        disabled={disabled || !!blockReason || !citations.length} aria-describedby={`${fieldId}-note`}
        onChange={(event) => {
          setSelectedDoi(event.currentTarget.value)
          setSaveError('')
          setSaved(false)
        }}>
        <option value="">选择文献</option>
        {citations.map(({ doi, title }) => <option key={doi} value={doi} disabled={doi === currentDoi}>
          {[title?.trim(), doi, doi === currentDoi ? '已关联' : ''].filter(Boolean).join(' · ')}
        </option>)}
      </select>
    </div>
    {loading && <p className="workspace-note" role="status">正在加载已核验文献…</p>}
    {loadError && <p className="form-error" role="alert">读取已核验文献失败。{loadError}</p>}
    {!loading && !loadError && !citations.length && <p className="workspace-note">项目文献库暂无已核验文献。</p>}
    {(loadError || nextCursor) && <button type="button" className="button outline manuscript-pdf-evidence-citation-more"
      disabled={disabled || !!blockReason || loading}
      onClick={() => loadPage(loadError ? query.cursor : nextCursor)}>{loadError ? '重试加载文献' : '加载更多文献'}</button>}
    {selected && <div className="manuscript-pdf-evidence-citation-selection" id={`${fieldId}-selection`}>
      <p className="workspace-note">原 PDF 证据：<bdi>{sourceName}</bdi>，第 {sourcePage} 页</p>
      <p className="workspace-note">所选 DOI：<bdi>{selected.doi}</bdi></p>
    </div>}
    {blockReason && <p id={`${fieldId}-blocked`} className="workspace-note">{blockReason}</p>}
    <button type="button" className="button primary manuscript-pdf-evidence-citation-confirm"
      aria-describedby={blockReason ? `${fieldId}-blocked` : selected ? `${fieldId}-selection` : undefined}
      disabled={disabled || !!blockReason || !selected || selected.doi === currentDoi}
      onClick={() => void confirm()}>{saving ? '正在保存…' : currentDoi ? '确认更换关联文献' : '确认关联文献'}</button>
    {saveError && <p className="form-error manuscript-pdf-evidence-citation-error" role="alert">保存文献关联失败。{saveError}</p>}
    {saved && <p className="workspace-note" role="status">已保存文献关联。</p>}
  </fieldset>
}
