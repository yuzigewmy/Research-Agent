import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from './projects'
import { bibliographyPath, doiUrl, listCitations, saveCitation, searchLiterature } from './literature'
import type { Citation, Work } from './literature'
import './literature.css'

const dateFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' })

function workTitle(work: Work) {
  return work.title?.trim() || work.doi
}

function WorkDetails({ work }: { work: Work }) {
  const publication = [work.journal, work.year, work.language].filter(Boolean).join(' · ')
  return <div className="literature-details">
    {!!work.authors?.length && <p className="literature-note" dir="auto">{work.authors.join('、')}</p>}
    {publication && <p className="literature-note" dir="auto">{publication}</p>}
    <a className="literature-link" href={doiUrl(work.doi)} target="_blank" rel="noopener noreferrer"
      aria-label={`打开 DOI ${work.doi}（新窗口）`}><bdi>{work.doi}</bdi></a>
    {work.abstract && <details>
      <summary>摘要</summary>
      <p dir="auto">{work.abstract}</p>
    </details>}
  </div>
}

type Verification = {
  source: 'search' | 'manual'
  kind: 'loading' | 'success' | 'error'
  message: string
}

function VerificationMessage({ outcome }: { outcome?: Verification }) {
  if (!outcome) return null
  if (outcome.kind === 'error') return <p className="form-error" role="alert">{outcome.message}</p>
  return <p className="literature-note" role="status">{outcome.message}</p>
}

function SearchResults({ items, stored, verifying, outcome, onVerify, id }: {
  items: Work[]
  stored: Set<string>
  verifying: boolean
  outcome?: Verification
  onVerify: (doi: string) => void
  id: string
}) {
  const [selected, setSelected] = useState('')
  return <fieldset className="literature-results">
    <legend>检索结果（{items.length} 条）</legend>
    <button className="button primary" type="button" disabled={!selected || stored.has(selected) || verifying}
      onClick={() => onVerify(selected)}>核验并保存所选文献</button>
    <VerificationMessage outcome={outcome} />
    <ul className="literature-list" role="list">
      {items.map((work) => <li key={work.doi}>
        <label className="literature-choice">
          <input type="radio" name={`${id}-result`} checked={selected === work.doi}
            disabled={stored.has(work.doi) || verifying} onChange={() => setSelected(work.doi)} />
          <strong dir="auto">{workTitle(work)}</strong>
          {stored.has(work.doi) && <span className="literature-note">已保存</span>}
        </label>
        <WorkDetails work={work} />
      </li>)}
    </ul>
  </fieldset>
}

export function LiteraturePanel({ projectId }: { projectId: string }) {
  return <ProjectLiterature key={projectId} projectId={projectId} />
}

function ProjectLiterature({ projectId }: { projectId: string }) {
  const id = useId()
  const [query, setQuery] = useState('')
  const [doi, setDoi] = useState('')
  const [results, setResults] = useState<Work[]>()
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState('')
  const [verification, setVerification] = useState<Verification>()
  const [citations, setCitations] = useState<Citation[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [listQuery, setListQuery] = useState<{ cursor?: string }>({})
  const [loadingList, setLoadingList] = useState(true)
  const [listError, setListError] = useState('')
  const searchRequest = useRef<AbortController | null>(null)
  const verifyRequest = useRef<AbortController | null>(null)
  const stored = new Set(citations.map((citation) => citation.doi))
  const verifying = verification?.kind === 'loading'

  useEffect(() => {
    const controller = new AbortController()
    void listCitations(projectId, controller.signal, listQuery.cursor).then((page) => {
      if (controller.signal.aborted) return
      setCitations((previous) => {
        const byDoi = new Map(previous.map((citation) => [citation.doi, citation]))
        for (const citation of page.items) byDoi.set(citation.doi, citation)
        return [...byDoi.values()]
      })
      setNextCursor(page.nextCursor)
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setListError(errorMessage(error))
    }).finally(() => {
      if (!controller.signal.aborted) setLoadingList(false)
    })
    return () => controller.abort()
  }, [projectId, listQuery])

  useEffect(() => () => {
    searchRequest.current?.abort()
    verifyRequest.current?.abort()
  }, [])

  function loadCitations(cursor?: string) {
    setListError('')
    setLoadingList(true)
    setListQuery({ cursor })
  }

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const text = query.trim()
    if (!text || searchRequest.current || verifyRequest.current) return
    const controller = new AbortController()
    searchRequest.current = controller
    setSearchError('')
    if (verification?.source === 'search') setVerification(undefined)
    setResults(undefined)
    setSearching(true)
    try {
      const page = await searchLiterature(projectId, text, controller.signal)
      if (!controller.signal.aborted) setResults(page.items)
    } catch (error) {
      if (!controller.signal.aborted) setSearchError(errorMessage(error))
    } finally {
      searchRequest.current = null
      if (!controller.signal.aborted) setSearching(false)
    }
  }

  async function verify(input: string, source: Verification['source']) {
    if (!input || verifyRequest.current) return
    const controller = new AbortController()
    verifyRequest.current = controller
    setVerification({ source, kind: 'loading', message: '正在核验 DOI…' })
    try {
      const citation = await saveCitation(projectId, input, controller.signal)
      if (controller.signal.aborted) return
      setCitations((previous) => previous.some((item) => item.doi === citation.doi) ? previous : [citation, ...previous])
      if (source === 'manual') setDoi('')
      setVerification({ source, kind: 'success', message: `${citation.doi} 已存入文献库。` })
    } catch (error) {
      if (!controller.signal.aborted) setVerification({ source, kind: 'error', message: errorMessage(error) })
    } finally {
      verifyRequest.current = null
    }
  }

  return <section className="literature-panel" aria-labelledby={id}>
    <h4 id={id}>项目文献库</h4>
    <p className="literature-note" id={`${id}-verify-note`}>核验仅确认 Crossref 中的 DOI 登记元数据。</p>
    <form className="field" onSubmit={(event) => {
      event.preventDefault()
      void verify(doi.trim(), 'manual')
    }}>
      <label htmlFor={`${id}-doi`}>DOI 或 DOI 链接</label>
      <div className="literature-form-row">
        <input id={`${id}-doi`} type="text" value={doi} onChange={(event) => setDoi(event.target.value)}
          aria-describedby={`${id}-verify-note`} required readOnly={verifying} autoComplete="off" spellCheck={false} />
        <button className="button outline" type="submit" disabled={!doi.trim() || verifying}>按 DOI 核验并保存</button>
      </div>
      <VerificationMessage outcome={verification?.source === 'manual' ? verification : undefined} />
    </form>
    <form className="field" onSubmit={(event) => void search(event)}>
      <label htmlFor={`${id}-query`}>文献检索词</label>
      <div className="literature-form-row">
        <input id={`${id}-query`} type="search" value={query} onChange={(event) => setQuery(event.target.value)}
          aria-describedby={`${id}-search-note`} required readOnly={searching} />
        <button className="button outline" type="submit" disabled={!query.trim() || searching || verifying}>检索 Crossref</button>
      </div>
      <p className="literature-note" id={`${id}-search-note`}>提交检索后，检索词会发送给 Crossref。</p>
    </form>
    {searching && <p className="literature-note" role="status">正在检索…</p>}
    {searchError && <p className="form-error" role="alert">{searchError}</p>}
    {results && (results.length > 0
      ? <SearchResults items={results} stored={stored} verifying={verifying} id={id}
        outcome={verification?.source === 'search' ? verification : undefined} onVerify={(value) => void verify(value, 'search')} />
      : <p className="literature-note" role="status">没有匹配的记录，请更换检索词。</p>)}
    <section className="literature-library" aria-labelledby={`${id}-saved`}>
      <div className="literature-heading">
        <h5 id={`${id}-saved`}>已核验文献</h5>
        <a className="button outline" href={bibliographyPath(projectId)} download>下载 BibTeX</a>
      </div>
      {citations.length > 0 && <ul className="literature-list" role="list">
        {citations.map((citation) => <li key={citation.doi}>
          <strong dir="auto">{workTitle(citation)}</strong>
          <WorkDetails work={citation} />
          <p className="literature-note">核验日期 <time dateTime={citation.verifiedAt}>{dateFormat.format(new Date(citation.verifiedAt))}</time></p>
        </li>)}
      </ul>}
      {loadingList && <p className="literature-note" role="status">正在加载文献…</p>}
      {listError && <>
        <p className="form-error" role="alert">{listError}</p>
        <button className="button outline" type="button" onClick={() => loadCitations(listQuery.cursor)}>重试加载文献</button>
      </>}
      {!loadingList && !listError && !citations.length && <p className="literature-note">尚未保存文献。可检索或直接输入 DOI。</p>}
      {!loadingList && !listError && nextCursor && (
        <button className="button outline" type="button" onClick={() => loadCitations(nextCursor)}>加载更多文献</button>
      )}
    </section>
  </section>
}
