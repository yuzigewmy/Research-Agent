import { ProjectApiError } from './projects'

export const diagramWidth = 960
export const diagramHeight = 640
export const nodeWidth = 160
export const nodeHeight = 64
export const maxNodeX = diagramWidth - nodeWidth
export const maxNodeY = diagramHeight - nodeHeight
export const maxDiagramNodes = 40
export const maxDiagramEdges = 80

export type Point = { x: number; y: number }
export type DiagramNode = Point & { id: string; label: string }
export type DiagramEdge = { from: string; to: string }
export type Diagram = { version: 1; nodes: DiagramNode[]; edges: DiagramEdge[] }

const encoder = new TextEncoder()
const nodeIdPattern = /^[A-Za-z][A-Za-z0-9_-]{0,31}$/u
const diagramDirectory = 'diagrams/'
const diagramExtension = '.flow.json'

function isDiagramName(name: string) {
  return /\P{White_Space}/u.test(name) && encoder.encode(name).length <= 256 && !/[\\/:\p{Cc}]/u.test(name)
}

export function diagramPath(name: string) {
  const stem = name.trim()
  if (!isDiagramName(stem)) {
    throw new ProjectApiError('请输入流程图名称，不含路径分隔符或冒号，最多 256 字节（UTF-8）。', 400)
  }
  return `${diagramDirectory}${stem}${diagramExtension}`
}

export function isDiagramPath(path: string) {
  return path.startsWith(diagramDirectory) && path.endsWith(diagramExtension)
    && isDiagramName(path.slice(diagramDirectory.length, -diagramExtension.length))
}

export function isNodeLabel(value: unknown): value is string {
  return typeof value === 'string' && /\P{White_Space}/u.test(value) && Array.from(value).length <= 36 && !/\p{Cc}/u.test(value)
}

export function isCoordinate(value: unknown, maximum: number): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= maximum
}

function isNode(value: unknown): value is DiagramNode {
  const node = value as Partial<DiagramNode> | null
  return !!node && typeof node.id === 'string' && nodeIdPattern.test(node.id)
    && isNodeLabel(node.label) && isCoordinate(node.x, maxNodeX) && isCoordinate(node.y, maxNodeY)
}

function isEdge(value: unknown): value is DiagramEdge {
  const edge = value as Partial<DiagramEdge> | null
  return !!edge && typeof edge.from === 'string' && typeof edge.to === 'string' && edge.from !== edge.to
}

export function edgeKey({ from, to }: DiagramEdge) {
  return `${from}:${to}`
}

export function isDiagram(value: unknown): value is Diagram {
  const diagram = value as Partial<Diagram> | null
  if (!diagram || diagram.version !== 1 || !Array.isArray(diagram.nodes) || !Array.isArray(diagram.edges)
    || diagram.nodes.length < 1 || diagram.nodes.length > maxDiagramNodes || diagram.edges.length > maxDiagramEdges
    || !diagram.nodes.every(isNode) || !diagram.edges.every(isEdge)) return false
  const ids = new Set(diagram.nodes.map(({ id }) => id))
  return ids.size === diagram.nodes.length
    && diagram.edges.every(({ from, to }) => ids.has(from) && ids.has(to))
    && new Set(diagram.edges.map(edgeKey)).size === diagram.edges.length
}

export function parseDiagram(content: string): Diagram {
  let value: unknown
  try {
    value = JSON.parse(content)
  } catch {
    throw new ProjectApiError('流程图内容无法读取。', 422)
  }
  if (!isDiagram(value)) throw new ProjectApiError('此版本的流程图内容无效。', 422)
  return {
    version: 1,
    nodes: value.nodes.map(({ id, label, x, y }) => ({ id, label, x, y })),
    edges: value.edges.map(({ from, to }) => ({ from, to })),
  }
}

export function diagramText(diagram: Diagram) {
  return `${JSON.stringify(diagram, null, 2)}\n`
}

export function newNode(nodes: DiagramNode[]): DiagramNode {
  const ids = new Set(nodes.map(({ id }) => id))
  let next = 1
  while (ids.has(`n${next}`)) next++
  const positionAt = (slot: number) => ({ x: 32 + slot % 5 * 192, y: 16 + Math.floor(slot / 5) * 80 })
  const slots = Array.from({ length: maxDiagramNodes }, (_, index) => positionAt(index))
  const position = slots.find(({ x, y }) => nodes.every((node) =>
    Math.abs(node.x - x) >= nodeWidth || Math.abs(node.y - y) >= nodeHeight)) ?? positionAt(nodes.length % maxDiagramNodes)
  return { id: `n${next}`, label: nodes.length ? `节点 ${next}` : '开始', ...position }
}

export function labelLines(label: string): string[] {
  const characters = Array.from(label)
  const lines: string[] = []
  for (let index = 0; index < Math.min(characters.length, 36); index += 12) {
    lines.push(characters.slice(index, index + 12).join(''))
  }
  return lines
}

export function clampPoint({ x, y }: Point): Point {
  return {
    x: Math.min(maxNodeX, Math.max(0, Math.round(x) || 0)),
    y: Math.min(maxNodeY, Math.max(0, Math.round(y) || 0)),
  }
}

export function edgeEndpoints(from: Point, to: Point) {
  const dx = to.x - from.x
  const dy = to.y - from.y
  const fraction = Math.min(nodeWidth / 2 / Math.abs(dx), nodeHeight / 2 / Math.abs(dy))
  if (fraction >= 0.5) return undefined
  return {
    x1: from.x + nodeWidth / 2 + dx * fraction,
    y1: from.y + nodeHeight / 2 + dy * fraction,
    x2: to.x + nodeWidth / 2 - dx * fraction,
    y2: to.y + nodeHeight / 2 - dy * fraction,
  }
}
