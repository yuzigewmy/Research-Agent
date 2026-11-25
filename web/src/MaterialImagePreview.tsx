import { useState } from 'react'

type ImageSize = { width: number; height: number }

function ImageAttempt({ url, name, onRetry }: { url: string; name: string; onRetry: () => void }) {
  const [image, setImage] = useState<ImageSize | 'error'>()
  const [zoom, setZoom] = useState('fit')
  const fit = zoom === 'fit'

  if (image === 'error') return <div className="request-error">
    <p role="alert">无法加载原图。可重试或下载原件查看。</p>
    <button className="button outline" type="button" onClick={onRetry}>重试加载原图</button>
  </div>

  return <div className="material-image-preview">
    {image ? <div className="material-image-preview-toolbar">
      <label className="material-image-preview-zoom">缩放
        <select value={zoom} onChange={(event) => setZoom(event.currentTarget.value)}>
          <option value="fit">适应宽度</option>
          <option value="100">100%</option>
          <option value="150">150%</option>
          <option value="200">200%</option>
        </select>
      </label>
      <span className="material-note">原图尺寸 {image.width} × {image.height} 像素</span>
    </div> : <p className="material-note" role="status">正在加载原图…</p>}
    <div className="material-image-preview-viewport" hidden={!image} tabIndex={0}
      role="group" aria-label="原图滚动区">
      <img className={fit ? 'material-image-preview-img material-image-preview-fit' : 'material-image-preview-img'}
        src={url} alt={name} decoding="async"
        width={image && (fit ? image.width : Math.round(image.width * Number(zoom) / 100))}
        onLoad={(event) => {
          const { naturalWidth: width, naturalHeight: height } = event.currentTarget
          setImage(width > 0 && height > 0 ? { width, height } : 'error')
        }}
        onError={() => setImage('error')} />
    </div>
  </div>
}

export function MaterialImagePreview({ url, name }: { url: string; name: string }) {
  const [attempt, setAttempt] = useState(0)
  return <ImageAttempt key={`${url}:${attempt}`} url={url} name={name}
    onRetry={() => setAttempt((value) => value + 1)} />
}
