import { layoutGraph } from './api'
import type { EditableEdge, EditableGraph } from './graphEditorModel'

const PREVIEW_NODE_HALF_W = 48
const PREVIEW_NODE_HALF_H = 22
const PREVIEW_PORT_SPACING = 22
const PREVIEW_ROUTE_MARGIN = 46

export type PreviewEdgeKind = 'primary' | 'secondary' | 'dependency' | 'completion' | 'back' | 'exit'
export type PreviewEdgeMode = 'journey' | 'all'

export interface PreviewRoute {
  edge: EditableEdge
  index: number
  kind: PreviewEdgeKind
  showLabel: boolean
  d: string
  label: {
    x: number
    y: number
    width: number
    value: string
  }
  bounds: {
    minX: number
    maxX: number
    minY: number
    maxY: number
  }
}

export interface PreviewGeometry {
  positions: Record<string, { x: number; y: number }>
  routes: PreviewRoute[]
  viewBox: {
    minX: number
    minY: number
    width: number
    height: number
  }
}

export function previewGeometryForMode(
  geometry: PreviewGeometry,
  graph: EditableGraph,
  mode: PreviewEdgeMode,
  start: string,
): PreviewGeometry {
  const routes = mode === 'all' ? geometry.routes : geometry.routes.filter((route) => isJourneyRoute(route.kind))
  const hiddenExitNodeIDs = new Set(routes.filter((route) => route.kind === 'exit').map((route) => route.edge.to))
  const visibleNodeIDs = new Set<string>([start])
  if (mode === 'all') {
    for (const node of graph.nodes) {
      if (!hiddenExitNodeIDs.has(node.id)) visibleNodeIDs.add(node.id)
    }
  } else {
    for (const route of routes) {
      visibleNodeIDs.add(route.edge.from)
      visibleNodeIDs.add(route.edge.to)
    }
  }

  return {
    ...geometry,
    routes,
    viewBox: previewViewBox(
      graph.nodes
        .filter((node) => visibleNodeIDs.has(node.id))
        .map((node) => geometry.positions[node.id])
        .filter((point): point is { x: number; y: number } => Boolean(point)),
      routes,
    ),
  }
}

export function buildPreviewGeometry(graph: EditableGraph, start: string): PreviewGeometry | null {
  const positions = layoutPreviewGraph(graph, start)
  const nodePoints = Object.values(positions)
  if (nodePoints.length === 0) return null

  const validEdges = graph.edges
    .map((edge, index) => ({ edge, index, from: positions[edge.from], to: positions[edge.to] }))
    .filter((item): item is { edge: EditableEdge; index: number; from: { x: number; y: number }; to: { x: number; y: number } } =>
      Boolean(item.from && item.to),
    )

  const outgoing = groupEdgeIndexes(validEdges, 'from')
  const incoming = groupEdgeIndexes(validEdges, 'to')
  const outgoingMax = maxOutgoingWeights(validEdges)
  const terminalIDs = new Set(graph.nodes.filter((node) => !node.apiTemplateId).map((node) => node.id))
  const mainPoints = graph.nodes
    .filter((node) => !isExitNode(node.id))
    .map((node) => positions[node.id])
    .filter((point): point is { x: number; y: number } => Boolean(point))
  const railPoints = mainPoints.length > 0 ? mainPoints : nodePoints
  const mainTop = Math.min(...railPoints.map((p) => p.y - PREVIEW_NODE_HALF_H))

  const noteLanes = new Map<string, number>()
  const rawRoutes = validEdges.map(({ edge, index, from, to }) => {
    const outList = outgoing.get(edge.from) ?? [index]
    const inList = incoming.get(edge.to) ?? [index]
    const startOffset = portOffset(outList.indexOf(index), outList.length)
    const endOffset = portOffset(inList.indexOf(index), inList.length)
    const dx = to.x - from.x
    const kind = classifyPreviewEdge(edge, dx, terminalIDs, outgoingMax)
    const showLabel = shouldShowLabel(edge, kind, outgoingMax)

    if (kind === 'exit') {
      const routeTop = from.y < mainTop + PREVIEW_NODE_HALF_H * 2
      const lane = nextNoteLane(noteLanes, edge.from)
      return exitRoute(edge, index, from, lane, routeTop, kind, showLabel)
    }
    if (dx === 0) {
      const routeTop = from.y < mainTop + PREVIEW_NODE_HALF_H * 2
      const lane = nextNoteLane(noteLanes, edge.from)
      return sameColumnRoute(edge, index, from, lane, routeTop, kind, showLabel)
    }
    if (dx < 0) {
      const routeTop = from.y < mainTop + PREVIEW_NODE_HALF_H * 2
      const lane = nextNoteLane(noteLanes, edge.from)
      return backRoute(edge, index, from, lane, routeTop, kind, showLabel)
    }
    return forwardRoute(edge, index, from, to, startOffset, endOffset, kind, showLabel)
  })
  const routes = separateLabels(rawRoutes).sort((a, b) => routeRank(a.kind) - routeRank(b.kind) || a.index - b.index)

  const routeBounds = routes.flatMap((r) => [r.bounds])
  const minX = Math.min(
    ...nodePoints.map((p) => p.x - PREVIEW_NODE_HALF_W),
    ...routeBounds.map((b) => b.minX),
  )
  const maxX = Math.max(
    ...nodePoints.map((p) => p.x + PREVIEW_NODE_HALF_W),
    ...routeBounds.map((b) => b.maxX),
  )
  const minY = Math.min(
    ...nodePoints.map((p) => p.y - PREVIEW_NODE_HALF_H),
    ...routeBounds.map((b) => b.minY),
  )
  const maxY = Math.max(
    ...nodePoints.map((p) => p.y + PREVIEW_NODE_HALF_H),
    ...routeBounds.map((b) => b.maxY),
  )

  return {
    positions,
    routes,
    viewBox: paddedViewBox(minX, maxX, minY, maxY),
  }
}

function nextNoteLane(lanes: Map<string, number>, source: string): number {
  const lane = lanes.get(source) ?? 0
  lanes.set(source, lane + 1)
  return lane
}

function isJourneyRoute(kind: PreviewEdgeKind): boolean {
  return kind === 'primary' || kind === 'dependency' || kind === 'completion'
}

function previewViewBox(nodePoints: { x: number; y: number }[], routes: PreviewRoute[]): PreviewGeometry['viewBox'] {
  const routeBounds = routes.flatMap((route) => [route.bounds])
  if (nodePoints.length === 0 && routeBounds.length === 0) {
    return paddedViewBox(-PREVIEW_NODE_HALF_W, PREVIEW_NODE_HALF_W, -PREVIEW_NODE_HALF_H, PREVIEW_NODE_HALF_H)
  }
  const minX = Math.min(
    ...nodePoints.map((p) => p.x - PREVIEW_NODE_HALF_W),
    ...routeBounds.map((b) => b.minX),
  )
  const maxX = Math.max(
    ...nodePoints.map((p) => p.x + PREVIEW_NODE_HALF_W),
    ...routeBounds.map((b) => b.maxX),
  )
  const minY = Math.min(
    ...nodePoints.map((p) => p.y - PREVIEW_NODE_HALF_H),
    ...routeBounds.map((b) => b.minY),
  )
  const maxY = Math.max(
    ...nodePoints.map((p) => p.y + PREVIEW_NODE_HALF_H),
    ...routeBounds.map((b) => b.maxY),
  )
  return paddedViewBox(minX, maxX, minY, maxY)
}

function paddedViewBox(minX: number, maxX: number, minY: number, maxY: number): PreviewGeometry['viewBox'] {
  return {
    minX: minX - PREVIEW_ROUTE_MARGIN,
    minY: minY - PREVIEW_ROUTE_MARGIN,
    width: maxX - minX + PREVIEW_ROUTE_MARGIN * 2,
    height: maxY - minY + PREVIEW_ROUTE_MARGIN * 2,
  }
}

function layoutPreviewGraph(graph: EditableGraph, start: string): Record<string, { x: number; y: number }> {
  const positions = { ...layoutGraph(graph.nodes, graph.edges, start) }
  const nonExitPoints = graph.nodes
    .filter((node) => !isExitNode(node.id))
    .map((node) => positions[node.id])
    .filter((point): point is { x: number; y: number } => Boolean(point))
  if (nonExitPoints.length === 0) return positions

  const mainBottom = Math.max(...nonExitPoints.map((p) => p.y + PREVIEW_NODE_HALF_H))
  const exitNodes = graph.nodes.filter((node) => !node.apiTemplateId && isExitNode(node.id))
  exitNodes.forEach((node, i) => {
    const incomingSources = graph.edges
      .filter((edge) => edge.to === node.id)
      .map((edge) => positions[edge.from])
      .filter((point): point is { x: number; y: number } => Boolean(point))
    const sourceRight = incomingSources.length > 0 ? Math.max(...incomingSources.map((p) => p.x)) : Math.max(...nonExitPoints.map((p) => p.x))
    positions[node.id] = {
      x: sourceRight + 128,
      y: mainBottom + 170 + i * 96,
    }
  })
  return positions
}

function groupEdgeIndexes(
  edges: { edge: EditableEdge; index: number }[],
  side: 'from' | 'to',
): Map<string, number[]> {
  const groups = new Map<string, number[]>()
  for (const { edge, index } of edges) {
    const key = edge[side]
    groups.set(key, [...(groups.get(key) ?? []), index])
  }
  return groups
}

function maxOutgoingWeights(edges: { edge: EditableEdge }[]): Map<string, number> {
  const max = new Map<string, number>()
  for (const { edge } of edges) {
    max.set(edge.from, Math.max(max.get(edge.from) ?? -Infinity, edge.weight))
  }
  return max
}

function classifyPreviewEdge(
  edge: EditableEdge,
  dx: number,
  terminalIDs: Set<string>,
  outgoingMax: Map<string, number>,
): PreviewEdgeKind {
  if (terminalIDs.has(edge.to)) return isExitNode(edge.to) ? 'exit' : 'completion'
  if (edge.dependency) return 'dependency'
  if (dx <= 0) return 'back'
  const strongestFromSource = edge.weight === outgoingMax.get(edge.from)
  if (edge.weight >= 0.25 || strongestFromSource) return 'primary'
  return 'secondary'
}

function shouldShowLabel(
  edge: EditableEdge,
  kind: PreviewEdgeKind,
  outgoingMax: Map<string, number>,
): boolean {
  if (kind === 'exit' || kind === 'back') return true
  if (kind === 'secondary' || kind === 'completion') return false
  if (kind === 'dependency') return true
  return edge.weight >= 0.25 || edge.weight === outgoingMax.get(edge.from)
}

function isExitNode(id: string): boolean {
  return /^(exit|drop|abandon|cancel|leave)$/i.test(id.trim())
}

function routeRank(kind: PreviewEdgeKind): number {
  switch (kind) {
    case 'exit':
      return 0
    case 'completion':
      return 1
    case 'back':
      return 2
    case 'secondary':
      return 3
    case 'primary':
      return 4
    case 'dependency':
      return 5
  }
}

function portOffset(order: number, total: number): number {
  const safeOrder = Math.max(0, order)
  return (safeOrder - (total - 1) / 2) * PREVIEW_PORT_SPACING
}

function forwardRoute(
  edge: EditableEdge,
  index: number,
  from: { x: number; y: number },
  to: { x: number; y: number },
  startOffset: number,
  endOffset: number,
  kind: PreviewEdgeKind,
  showLabel: boolean,
): PreviewRoute {
  const start = { x: from.x + PREVIEW_NODE_HALF_W, y: from.y + startOffset }
  const end = { x: to.x - PREVIEW_NODE_HALF_W, y: to.y + endOffset }
  const curve = Math.max(28, Math.min(62, Math.abs(end.x - start.x) * 0.3))
  const c1 = { x: start.x + curve, y: start.y }
  const c2 = { x: end.x - curve, y: end.y }
  const labelPoint = cubicPoint(start, c1, c2, end, 0.5)
  const label = edgeLabel(edge.weight, labelPoint.x, labelPoint.y - 10)
  return route(edge, index, kind, showLabel, `M ${fmt(start.x)} ${fmt(start.y)} C ${fmt(c1.x)} ${fmt(c1.y)}, ${fmt(c2.x)} ${fmt(c2.y)}, ${fmt(end.x)} ${fmt(end.y)}`, [
    start,
    c1,
    c2,
    end,
  ], label)
}

function sameColumnRoute(
  edge: EditableEdge,
  index: number,
  from: { x: number; y: number },
  lane: number,
  routeTop: boolean,
  kind: PreviewEdgeKind,
  showLabel: boolean,
): PreviewRoute {
  return noteRoute(edge, index, from, lane, routeTop, kind, showLabel, `to ${edge.to} ${edge.weight}`)
}

function backRoute(
  edge: EditableEdge,
  index: number,
  from: { x: number; y: number },
  lane: number,
  routeTop: boolean,
  kind: PreviewEdgeKind,
  showLabel: boolean,
): PreviewRoute {
  return noteRoute(edge, index, from, lane, routeTop, kind, showLabel, `to ${edge.to} ${edge.weight}`)
}

function exitRoute(
  edge: EditableEdge,
  index: number,
  from: { x: number; y: number },
  lane: number,
  routeTop: boolean,
  kind: PreviewEdgeKind,
  showLabel: boolean,
): PreviewRoute {
  return noteRoute(edge, index, from, lane, routeTop, kind, showLabel, `exit ${edge.weight}`)
}

function noteRoute(
  edge: EditableEdge,
  index: number,
  from: { x: number; y: number },
  lane: number,
  routeTop: boolean,
  kind: PreviewEdgeKind,
  showLabel: boolean,
  value: string,
): PreviewRoute {
  const direction = routeTop ? -1 : 1
  const x = from.x
  const y = from.y + direction * (PREVIEW_NODE_HALF_H + 28 + lane * 18)
  const label = noteLabel(value, x, y)
  // A short connector stub from the chip's near edge to the node boundary, so the
  // floating note label reads as attached to its source node instead of orphaned.
  const chipEdgeY = y - direction * 11
  const nodeEdgeY = from.y + direction * PREVIEW_NODE_HALF_H
  const points = [
    { x, y: chipEdgeY },
    { x, y: nodeEdgeY },
    { x, y },
  ]
  return route(edge, index, kind, showLabel, `M ${fmt(x)} ${fmt(chipEdgeY)} L ${fmt(x)} ${fmt(nodeEdgeY)}`, points, label)
}

function separateLabels(routes: PreviewRoute[]): PreviewRoute[] {
  const placed: { minX: number; maxX: number; minY: number; maxY: number }[] = []
  const offsets = [0, -20, 20, -40, 40, -60, 60, -80, 80]
  return routes.map((route) => {
    if (!route.showLabel) return route
    const label =
      offsets
        .map((offset) => ({ ...route.label, y: route.label.y + offset }))
        .find((candidate) => !placed.some((rect) => overlaps(labelRect(candidate), rect))) ?? route.label
    const next = label === route.label ? route : withLabel(route, label)
    placed.push(labelRect(next.label))
    return next
  })
}

function edgeLabel(weight: number, x: number, y: number): PreviewRoute['label'] {
  const value = String(weight)
  return { value, x, y, width: Math.max(34, value.length * 8 + 16) }
}

function noteLabel(value: string, x: number, y: number): PreviewRoute['label'] {
  return { value, x, y, width: Math.max(54, value.length * 7 + 14) }
}

function withLabel(route: PreviewRoute, label: PreviewRoute['label']): PreviewRoute {
  const rect = labelRect(label)
  return {
    ...route,
    label,
    bounds: {
      minX: Math.min(route.bounds.minX, rect.minX),
      maxX: Math.max(route.bounds.maxX, rect.maxX),
      minY: Math.min(route.bounds.minY, rect.minY),
      maxY: Math.max(route.bounds.maxY, rect.maxY),
    },
  }
}

function labelRect(label: PreviewRoute['label']): { minX: number; maxX: number; minY: number; maxY: number } {
  const xPad = 6
  const yPad = 5
  return {
    minX: label.x - label.width / 2 - xPad,
    maxX: label.x + label.width / 2 + xPad,
    minY: label.y - 12 - yPad,
    maxY: label.y + 8 + yPad,
  }
}

function overlaps(
  a: { minX: number; maxX: number; minY: number; maxY: number },
  b: { minX: number; maxX: number; minY: number; maxY: number },
): boolean {
  return a.minX < b.maxX && b.minX < a.maxX && a.minY < b.maxY && b.minY < a.maxY
}

function route(
  edge: EditableEdge,
  index: number,
  kind: PreviewEdgeKind,
  showLabel: boolean,
  d: string,
  points: { x: number; y: number }[],
  label: PreviewRoute['label'],
): PreviewRoute {
  const labelBounds = labelRect(label)
  const xs = points.map((p) => p.x)
  const ys = points.map((p) => p.y)
  if (showLabel) {
    xs.push(labelBounds.minX, labelBounds.maxX)
    ys.push(labelBounds.minY, labelBounds.maxY)
  }
  return {
    edge,
    index,
    kind,
    showLabel,
    d,
    label,
    bounds: {
      minX: Math.min(...xs),
      maxX: Math.max(...xs),
      minY: Math.min(...ys),
      maxY: Math.max(...ys),
    },
  }
}

function cubicPoint(
  start: { x: number; y: number },
  c1: { x: number; y: number },
  c2: { x: number; y: number },
  end: { x: number; y: number },
  t: number,
): { x: number; y: number } {
  const mt = 1 - t
  return {
    x: mt ** 3 * start.x + 3 * mt ** 2 * t * c1.x + 3 * mt * t ** 2 * c2.x + t ** 3 * end.x,
    y: mt ** 3 * start.y + 3 * mt ** 2 * t * c1.y + 3 * mt * t ** 2 * c2.y + t ** 3 * end.y,
  }
}

function fmt(n: number): string {
  return Number(n.toFixed(2)).toString()
}
