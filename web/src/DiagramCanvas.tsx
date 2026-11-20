import { useId, useRef, useState } from 'react'
import type { PointerEvent } from 'react'
import { clampPoint, diagramHeight, diagramWidth, edgeEndpoints, edgeKey, labelLines, nodeHeight, nodeWidth } from './diagram'
import type { Diagram, Point } from './diagram'

type Drag = { pointerId: number; nodeId: string; origin: Point; offset: Point }

interface Props {
  diagram: Diagram
  zoom?: number
  selectedId?: string
  onSelect?: (id: string) => void
  onMove?: (id: string, position: Point) => void
}

function canvasPoint(svg: SVGSVGElement, clientX: number, clientY: number) {
  const matrix = svg.getScreenCTM()
  if (!matrix) return undefined
  return new DOMPoint(clientX, clientY).matrixTransform(matrix.inverse())
}

export function DiagramCanvas({ diagram, zoom = 100, selectedId, onSelect, onMove }: Props) {
  const arrowId = useId()
  const svg = useRef<SVGSVGElement>(null)
  const drag = useRef<Drag | null>(null)
  const [preview, setPreview] = useState<{ nodeId: string; position: Point }>()
  const nodes = diagram.nodes.map((node) => ({ ...node,
    ...(preview?.nodeId === node.id && onMove ? preview.position : clampPoint(node)),
  }))
  const byId = new Map(nodes.map((node) => [node.id, node]))

  function begin(event: PointerEvent<SVGGElement>, nodeId: string, position: Point) {
    const canvas = svg.current
    if (!onMove || !canvas || drag.current || event.button !== 0) return
    const point = canvasPoint(canvas, event.clientX, event.clientY)
    if (!point) return
    canvas.setPointerCapture(event.pointerId)
    drag.current = {
      pointerId: event.pointerId,
      nodeId,
      origin: position,
      offset: { x: point.x - position.x, y: point.y - position.y },
    }
    onSelect?.(nodeId)
    event.preventDefault()
  }

  function dragPosition(event: PointerEvent<SVGSVGElement>, current: Drag) {
    const point = canvasPoint(event.currentTarget, event.clientX, event.clientY)
    return point && clampPoint({ x: point.x - current.offset.x, y: point.y - current.offset.y })
  }

  function move(event: PointerEvent<SVGSVGElement>) {
    const current = drag.current
    if (!current || current.pointerId !== event.pointerId) return
    const position = dragPosition(event, current)
    if (position) setPreview({ nodeId: current.nodeId, position })
  }

  function finish(event: PointerEvent<SVGSVGElement>, commit: boolean) {
    const current = drag.current
    if (!current || current.pointerId !== event.pointerId) return
    drag.current = null
    setPreview(undefined)
    if (commit) {
      const position = dragPosition(event, current)
      if (position && (position.x !== current.origin.x || position.y !== current.origin.y)) {
        onMove?.(current.nodeId, position)
      }
    }
  }

  return <div className="diagram-viewport" role="region" tabIndex={0} aria-label="流程图画布滚动区">
    <svg ref={svg} className="diagram-canvas" role="img" aria-label="流程图画布"
      viewBox={`0 0 ${diagramWidth} ${diagramHeight}`} width={diagramWidth * zoom / 100} height={diagramHeight * zoom / 100}
      onPointerMove={move} onPointerUp={(event) => finish(event, true)}
      onPointerCancel={(event) => finish(event, false)} onLostPointerCapture={(event) => finish(event, false)}>
      <defs>
        <marker id={arrowId} refX={10} refY={4} markerWidth={10} markerHeight={8}
          markerUnits="userSpaceOnUse" orient="auto">
          <path d="M0 0 L10 4 L0 8 Z" className="diagram-arrow" />
        </marker>
      </defs>
      {diagram.edges.map((edge) => {
        const from = byId.get(edge.from)
        const to = byId.get(edge.to)
        const endpoints = from && to && edgeEndpoints(from, to)
        return endpoints && <line key={edgeKey(edge)} className="diagram-line" {...endpoints} markerEnd={`url(#${arrowId})`} />
      })}
      {nodes.map((node) => {
        const lines = labelLines(node.label)
        return <g key={node.id} data-node-id={node.id} className="diagram-node" data-selected={node.id === selectedId || undefined}
          data-editable={!!onMove || undefined} transform={`translate(${node.x} ${node.y})`}
          onPointerDown={(event) => begin(event, node.id, node)}>
          <rect width={nodeWidth} height={nodeHeight} rx={6} />
          <text textAnchor="middle" xmlSpace="preserve">
            {lines.map((line, index) => <tspan key={index} x={nodeWidth / 2}
              y={nodeHeight / 2 + 5 + (index - (lines.length - 1) / 2) * 18}>{line}</tspan>)}
          </text>
        </g>
      })}
    </svg>
  </div>
}
