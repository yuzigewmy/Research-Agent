import { lazy, Suspense } from 'react'
import type { ComponentProps } from 'react'

const Viewer = lazy(() => import('./PdfMaterialPreview').catch(() => ({
  default: () => <p className="form-error" role="alert">无法加载 PDF 查看器，请刷新页面或下载原件。</p>,
})))

export function PdfPreview(props: ComponentProps<typeof Viewer>) {
  return <Suspense fallback={<p className="material-note" role="status">正在加载 PDF 查看器…</p>}>
    <Viewer key={props.url} {...props} />
  </Suspense>
}
