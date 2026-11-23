import { zipSync } from 'fflate'
import { ProjectApiError } from './projects'

const documentExtensions = new Set(['.md', '.markdown', '.txt'])
const sourceExtensions = new Set([
  '.c', '.h', '.cc', '.cpp', '.cxx', '.hpp', '.cs', '.go', '.java', '.kt', '.kts', '.rs', '.swift',
  '.js', '.mjs', '.cjs', '.jsx', '.ts', '.tsx', '.py', '.pyw', '.rb', '.php', '.lua', '.r', '.jl',
  '.scala', '.sql', '.sh', '.ps1', '.html', '.css', '.vue', '.svelte',
])
const ignoredDirectories = new Set([
  '.git', '.svn', '.venv', 'node_modules', 'vendor', '__macosx', '__pycache__', 'dist', 'build', 'target',
])
const encoder = new TextEncoder()

export async function packSoftwareFolder(files: File[], signal: AbortSignal) {
  const root = files[0].webkitRelativePath.split('/')[0]
  const entries: { path: string; file: File }[] = []
  const names = new Set<string>()
  let size = 0
  let sourceSize = 0
  for (const file of files) {
    const [directory, ...segments] = file.webkitRelativePath.split('/')
    if (!root || directory !== root || !segments.length) throw new ProjectApiError('请选择一个包含材料的目录。', 422)
    if (segments.slice(0, -1).some((segment) => ignoredDirectories.has(segment.toLowerCase()))) continue
    const extension = file.name.slice(file.name.lastIndexOf('.')).toLowerCase()
    const isSource = sourceExtensions.has(extension)
    if (!isSource && !documentExtensions.has(extension)) continue

    const path = segments.join('/')
    const identity = path.toLowerCase()
    if (encoder.encode(path).length > 512 || /[\\:\p{Cc}]/u.test(path) || names.has(identity) ||
        segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
      throw new ProjectApiError('目录中包含无效或重复的材料路径。', 422)
    }
    names.add(identity)
    entries.push({ path, file })
    size += file.size
    if (isSource) sourceSize += file.size
    if (entries.length > 1000 || size > 16 * 1024 * 1024) {
      throw new ProjectApiError('单个目录最多导入 1000 个文件，合计 16 MiB。', 413)
    }
    if (sourceSize > 64 * 1024) throw new ProjectApiError('目录内的源码合计不能超过 64 KiB。', 413)
  }
  if (!entries.length) throw new ProjectApiError('目录中没有可导入的源码或 Markdown、TXT 说明。', 422)

  entries.sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0)
  const contents: Record<string, Uint8Array> = Object.create(null)
  for (const { path, file } of entries) {
    signal.throwIfAborted()
    contents[path] = new Uint8Array(await file.arrayBuffer())
  }
  signal.throwIfAborted()
  const zip = zipSync(contents, { level: 0, mtime: new Date(1980, 0, 1) })
  return new File([zip], `${root}.zip`, { type: 'application/zip' })
}
