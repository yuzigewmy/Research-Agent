import { useEffect, useId, useRef, useState } from 'react'
import { materialDownloadPath } from './materials'
import { PdfPreview } from './PdfPreview'
import { errorMessage, ProjectApiError, request } from './projects'
import { isPdfQuotes, manuscriptSelectionByteLimit } from './turns'
import type { ManuscriptSelection, TurnReference } from './turns'

export type PDFEvidenceReference = TurnReference & { page: number; quote: string }

type CreateLink = Pick<ManuscriptSelection, 'startByte' | 'endByte'>
  & Pick<PDFEvidenceReference, 'materialId' | 'sha256' | 'page' | 'quote'>
type Link = CreateLink & Pick<PDFEvidenceReference, 'name'> & { linkId: string; text: string; sourceChanged: boolean }
type LinkPage = { items: Link[] }
type Props = { projectId: string; versionId: string } & (
  { readOnly: true } | {
    readOnly?: false
    references: readonly PDFEvidenceReference[]
    selection?: ManuscriptSelection
    blockReason?: string
  }
)

function isPathId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value !== '.' && value !== '..'
}

export function isPDFEvidenceReference(reference: Partial<TurnReference> | null): reference is PDFEvidenceReference {
  if (!reference || !isPathId(reference.materialId) || typeof reference.name !== 'string' || !reference.name.length) return false
  const { materialId, sha256, page, quote } = reference
  return isPdfQuotes({ [materialId]: { sha256, page, text: quote } }, [materialId])
}

function isLink(value: unknown): value is Link {
  const link = value as Partial<Link> | null
  return !!link && isPathId(link.linkId)
    && typeof link.startByte === 'number' && Number.isSafeInteger(link.startByte) && link.startByte >= 0
    && typeof link.endByte === 'number' && Number.isSafeInteger(link.endByte) && link.endByte > link.startByte
    && typeof link.text === 'string' && link.text.length > 0 && typeof link.sourceChanged === 'boolean'
    && isPDFEvidenceReference(link)
}

function isLinkPage(value: unknown): value is LinkPage {
  const items = (value as Partial<LinkPage> | null)?.items
  return Array.isArray(items) && items.every(isLink)
}

function requestSignal(controller: AbortController) {
  return AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)])
}

function PDFExcerpt({ page, quote }: Pick<PDFEvidenceReference, 'page' | 'quote'>) {
  return <>
    <p className="workspace-note manuscript-pdf-evidence-page">PDF 页码（浏览器选区）：第 {page} 页</p>
    <p className="workspace-note">PDF 节选（浏览器选区）</p>
    <pre className="workspace-text manuscript-pdf-evidence-quote" tabIndex={0}
      aria-label="PDF 节选（浏览器选区）" dir="auto">{quote}</pre>
  </>
}

function EvidenceList({ items, projectId }: { items: Link[]; projectId: string }) {
  const [openLinkId, setOpenLinkId] = useState<string>()
  const previewId = useId()
  return <ul className="manuscript-evidence-list manuscript-pdf-evidence-list" role="list" aria-label="本稿件版本的 PDF 证据">
    {items.map(({ linkId, materialId, name, sha256, page, quote, text, sourceChanged }) => (
      <li key={linkId} data-link-id={linkId}>
        <p><strong><bdi>{name}</bdi></strong></p>
        <PDFExcerpt page={page} quote={quote} />
        <p className="workspace-note">稿件选文</p>
        <pre className="workspace-text" tabIndex={0} aria-label="关联的稿件选文" dir="auto">{text}</pre>
        <p className="workspace-note">原 PDF 的 SHA-256 <code>{sha256}</code></p>
        {sourceChanged && <p className="workspace-note manuscript-pdf-evidence-source-changed">
          同名 PDF 已有新版。此关联仍指向下方的原 PDF。
        </p>}
        <div className="workspace-actions">
          <button className="button outline" type="button" aria-expanded={openLinkId === linkId}
            aria-controls={openLinkId === linkId ? previewId : undefined}
            onClick={() => setOpenLinkId(openLinkId === linkId ? undefined : linkId)}>
            {openLinkId === linkId ? '收起原文' : `查看原文第 ${page} 页`}
          </button>
          <a className="manuscript-pdf-evidence-download" href={materialDownloadPath(projectId, materialId)}
            aria-label={`下载原 PDF：${name}`} download>下载原 PDF</a>
        </div>
        {openLinkId === linkId && <div id={previewId} className="manuscript-pdf-evidence-preview">
          <PdfPreview url={materialDownloadPath(projectId, materialId)} initialPage={page} />
        </div>}
      </li>
    ))}
  </ul>
}

function EvidenceControls({ references, selection, blockReason, loading, saving, onLink }: {
  references: readonly PDFEvidenceReference[]
  selection?: ManuscriptSelection
  blockReason?: string
  loading: boolean
  saving: boolean
  onLink: (input: CreateLink) => void
}) {
  const id = useId()
  const [selectedReference, setSelectedReference] = useState<PDFEvidenceReference>()
  const reference = selectedReference && references.includes(selectedReference) ? selectedReference : undefined
  const hint = blockReason ?? (!selection
    ? `请在已保存稿件中选取非空白文字（最多 ${manuscriptSelectionByteLimit / 1024} KiB）。`
    : undefined)

  return <div className="manuscript-evidence-controls">
    <p className="workspace-note" id={`${id}-instructions`}>
      在「项目资料」中预览 PDF，选中页内文字后点击「引用所选文字」。选中已保存稿件中的文字，再选择已有的 PDF 引用并关联。关联后，即使清空对话引用，本稿件版本也会保留该证据。
    </p>
    <div className="field">
      <label htmlFor={`${id}-reference`}>已有 PDF 引用</label>
      <select id={`${id}-reference`} className="manuscript-pdf-evidence-reference" value={reference?.materialId ?? ''}
        disabled={saving || !references.length} aria-describedby={`${id}-instructions`}
        onChange={(event) => {
          const materialId = event.currentTarget.value
          setSelectedReference(references.find((item) => item.materialId === materialId))
        }}>
        <option value="">{references.length ? '选择 PDF 引用' : '暂无 PDF 引用'}</option>
        {references.map(({ materialId, name, page }) => <option key={materialId} value={materialId}>
          {name} · 第 {page} 页 · {materialId.slice(-8)}
        </option>)}
      </select>
    </div>
    {reference && <PDFExcerpt page={reference.page} quote={reference.quote} />}
    {selection && !blockReason && <pre className="workspace-text" tabIndex={0}
      aria-label="当前稿件选区" dir="auto">{selection.text}</pre>}
    {hint && <p className="workspace-note" id={`${id}-hint`}>{hint}</p>}
    <div className="workspace-actions">
      <button className="button primary manuscript-pdf-evidence-link" type="button"
        aria-describedby={hint ? `${id}-hint` : undefined}
        disabled={loading || saving || !!blockReason || !selection || !reference}
        onClick={() => {
          if (!selection || !reference || blockReason) return
          const { startByte, endByte } = selection
          const { materialId, sha256, page, quote } = reference
          onLink({ startByte, endByte, materialId, sha256, page, quote })
        }}>{saving ? '正在关联…' : '关联 PDF 证据'}</button>
    </div>
  </div>
}

export function ManuscriptPDFEvidence(props: Props) {
  const { projectId, versionId } = props
  if (!isPathId(projectId) || !isPathId(versionId)) return <p className="form-error" role="alert">稿件版本链接无效。</p>
  const route = `/api/v1/projects/${encodeURIComponent(projectId)}/files/versions/${encodeURIComponent(versionId)}/pdf-links`
  return <VersionEvidence key={route} route={route} {...props} />
}

function VersionEvidence(props: Props & { route: string }) {
  const { projectId, route, readOnly } = props
  const id = useId()
  const [links, setLinks] = useState<Link[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saved, setSaved] = useState(false)
  const mutation = useRef<AbortController>(null)

  useEffect(() => {
    const controller = new AbortController()
    void request(route, isLinkPage, { cache: 'no-store', signal: requestSignal(controller) }).then(({ items }) => {
      if (controller.signal.aborted) return
      setLinks(items)
      setLoading(false)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setLoadError(errorMessage(cause))
      setLoading(false)
    })
    return () => controller.abort()
  }, [route, refresh])

  useEffect(() => () => mutation.current?.abort(), [])

  async function saveLink(input: CreateLink) {
    if (readOnly || props.blockReason || loading || mutation.current) return
    const controller = new AbortController()
    mutation.current = controller
    setSaving(true)
    setSaved(false)
    setSaveError('')
    try {
      const record = await request(route, (value): value is Link => isLink(value)
        && value.materialId === input.materialId && value.sha256 === input.sha256
        && value.page === input.page && value.quote === input.quote
        && value.startByte === input.startByte && value.endByte === input.endByte, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
        signal: requestSignal(controller),
      })
      if (controller.signal.aborted) return
      setLinks((current) => current.some(({ linkId }) => linkId === record.linkId)
        ? current.map((item) => item.linkId === record.linkId ? record : item) : [record, ...current])
      setSaved(true)
    } catch (cause) {
      if (controller.signal.aborted) return
      setSaveError(cause instanceof ProjectApiError && cause.status === 409 && cause.code === 'MANUSCRIPT_VERSION_CHANGED'
        ? '稿件已有新版本。请点击「重新加载」，再选择要关联的文字。' : errorMessage(cause))
    } finally {
      mutation.current = null
      if (!controller.signal.aborted) setSaving(false)
    }
  }

  return <section className="manuscript-evidence manuscript-pdf-evidence" aria-labelledby={id} data-read-only={!!readOnly}>
    <h5 id={id}>{readOnly ? '稿件 PDF 证据（只读）' : '稿件 PDF 证据'}</h5>
    {!readOnly && <EvidenceControls references={props.references} selection={props.selection} blockReason={props.blockReason}
      loading={loading} saving={saving} onLink={(input) => void saveLink(input)} />}
    {saveError && <p className="form-error manuscript-pdf-evidence-save-error" role="alert">{saveError}</p>}
    {saved && <p className="workspace-note" role="status">已关联 PDF 证据。</p>}
    <div className="workspace-actions">
      <button className="button outline manuscript-pdf-evidence-refresh" type="button" disabled={loading || saving}
        onClick={() => {
          setLoadError('')
          setLoading(true)
          setRefresh((value) => value + 1)
        }}>{loadError ? '重试读取 PDF 证据' : '刷新 PDF 证据'}</button>
    </div>
    {loading && <p className="workspace-note" role="status">正在读取 PDF 证据…</p>}
    {loadError && <p className="form-error" role="alert">读取 PDF 证据失败。{loadError}</p>}
    {links.length > 0 && <EvidenceList items={links} projectId={projectId} />}
    {!loading && !loadError && !links.length && <p className="workspace-note">此稿件版本尚未关联 PDF 证据。</p>}
  </section>
}
