import { useId, useRef, useState } from 'react'
import { isWordQuoteText, wordQuoteByteLimit } from './turns'

interface WordPreviewProps {
  text: string
  sections?: string[]
  name: string
  quoteBlocked?: boolean
  onQuote?: (text: string) => void
}

function WordText({ text, name, onQuote, quoteBlocked }: Omit<WordPreviewProps, 'sections'>) {
  const preview = useRef<HTMLPreElement>(null)
  const hintId = useId()
  const [message, setMessage] = useState('')
  const limit = `${wordQuoteByteLimit / 1024} KiB`

  function quoteSelection() {
    if (!onQuote || quoteBlocked) return
    const selection = window.getSelection()
    const range = selection?.rangeCount === 1 ? selection.getRangeAt(0) : undefined
    if (!range || range.collapsed || !preview.current?.contains(range.commonAncestorContainer)) {
      setMessage('请在当前 Word 预览正文内选中文字。')
      return
    }
    const selected = range.toString()
    if (!isWordQuoteText(selected)) {
      setMessage(`请选择非空白文字，且选区不能超过 ${limit}。`)
      return
    }
    onQuote(selected)
    setMessage('已将 Word 选区加入对话。')
  }

  return <>
    <pre ref={preview} className="material-text" data-word-preview-text tabIndex={0}
      aria-label={`文本预览：${name}`} dir="auto">{text}</pre>
    {onQuote && <div className="material-actions">
      <button className="button outline" type="button" data-word-quote-add disabled={quoteBlocked}
        aria-describedby={hintId} onMouseDown={(event) => event.preventDefault()}
        onClick={quoteSelection}>引用 Word 所选文字</button>
      <span className="material-note" id={hintId} role="status">
        {quoteBlocked ? '已选满四份资料。' : message || `选中当前 Word 预览中的文字后可引用（最多 ${limit}）。`}
      </span>
    </div>}
  </>
}

export function WordPreview({ sections, text, ...props }: WordPreviewProps) {
  const [index, setIndex] = useState(0)
  return <div className="material-word-sections">
    {sections && <>
      <p className="material-note">按显式分页标记分段。实际页码由排版引擎决定。</p>
      <div className="material-actions">
        <button className="button outline" type="button" disabled={index === 0}
          onClick={() => setIndex(index - 1)}>上一段</button>
        <span className="material-note" role="status">第 {index + 1} 段，共 {sections.length} 段</span>
        <button className="button outline" type="button" disabled={index === sections.length - 1}
          onClick={() => setIndex(index + 1)}>下一段</button>
      </div>
    </>}
    <WordText key={index} {...props} text={sections?.[index] ?? text} />
  </div>
}
