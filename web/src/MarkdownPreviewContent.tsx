import { renderToString } from 'katex'
import Markdown from 'react-markdown'
import remarkMath from 'remark-math'
import 'katex/dist/katex.min.css'

function Formula({ source, displayMode }: { source: string; displayMode: boolean }) {
  try {
    const html = renderToString(source, { displayMode, trust: false, maxSize: 20, maxExpand: 500 })
    return <span dangerouslySetInnerHTML={{ __html: html }} />
  } catch {
    return <code className="katex-error" title="公式无法解析">{source}</code>
  }
}

export default function MarkdownPreviewContent({ content }: { content: string }) {
  return <div className="markdown-preview-content" dir="auto">
    <Markdown
      skipHtml
      remarkPlugins={[remarkMath]}
      components={{
        code: ({ className, children }) => {
          if (typeof children === 'string') {
            const classes = className?.split(/\s+/) ?? []
            if (classes.includes('math-inline') || classes.includes('math-display')) {
              return <Formula source={children} displayMode={classes.includes('math-display')} />
            }
          }
          return <code className={className}>{children}</code>
        },
        pre: ({ node, children }) => {
          const code = node?.children[0]
          const classes = code?.type === 'element' && code.tagName === 'code' ? code.properties.className : undefined
          return Array.isArray(classes) && classes.includes('math-display') ? <div>{children}</div> : <pre>{children}</pre>
        },
        a: ({ href, children }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
        img: ({ alt }) => <span>{alt ? `［图片：${alt}］` : '［图片］'}</span>,
      }}
    >{content}</Markdown>
  </div>
}
