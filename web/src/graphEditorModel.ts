export interface EditableNode {
  id: string
  apiTemplateId?: string
}

export interface EditableEdge {
  from: string
  to: string
  weight: number
  dependency?: boolean
}

export interface EditableGraph {
  id?: string
  nodes: EditableNode[]
  edges: EditableEdge[]
}

export function parseEditableGraph(json: string): EditableGraph | null {
  try {
    const parsed = JSON.parse(json) as Partial<EditableGraph>
    if (!parsed || typeof parsed !== 'object') return null
    if (!Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) return null
    return {
      id: typeof parsed.id === 'string' ? parsed.id : undefined,
      nodes: parsed.nodes.map((n) => ({
        id: String((n as EditableNode).id ?? ''),
        apiTemplateId:
          typeof (n as EditableNode).apiTemplateId === 'string'
            ? (n as EditableNode).apiTemplateId
            : undefined,
      })),
      edges: parsed.edges.map((e) =>
        compactEdge({
          from: String((e as EditableEdge).from ?? ''),
          to: String((e as EditableEdge).to ?? ''),
          weight:
            typeof (e as EditableEdge).weight === 'number' && Number.isFinite((e as EditableEdge).weight)
              ? (e as EditableEdge).weight
              : 0,
          dependency: Boolean((e as EditableEdge).dependency),
        }),
      ),
    }
  } catch {
    return null
  }
}

export function stringifyEditableGraph(g: EditableGraph): string {
  return JSON.stringify(g, null, 2)
}

export function templateIDsFromJSON(json: string): string[] {
  try {
    const parsed = JSON.parse(json) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return []
    return Object.keys(parsed).sort()
  } catch {
    return []
  }
}

export function addNode(g: EditableGraph, id: string, apiTemplateId = ''): EditableGraph {
  const nextID = id.trim()
  if (!nextID || g.nodes.some((n) => n.id === nextID)) return g
  return {
    ...g,
    nodes: [...g.nodes, compactNode({ id: nextID, apiTemplateId })],
  }
}

export function updateNode(g: EditableGraph, index: number, patch: Partial<EditableNode>): EditableGraph {
  const prev = g.nodes[index]
  if (!prev) return g
  const next = compactNode({ ...prev, ...patch, id: patch.id?.trim() ?? prev.id })
  const oldID = prev.id
  const newID = next.id
  const nodes = g.nodes.map((n, i) => (i === index ? next : n))
  const edges =
    newID && newID !== oldID
      ? g.edges.map((e) => ({
          ...e,
          from: e.from === oldID ? newID : e.from,
          to: e.to === oldID ? newID : e.to,
        }))
      : g.edges
  return { ...g, nodes, edges }
}

export function removeNode(g: EditableGraph, index: number): EditableGraph {
  const removed = g.nodes[index]
  if (!removed) return g
  return {
    ...g,
    nodes: g.nodes.filter((_, i) => i !== index),
    edges: g.edges.filter((e) => e.from !== removed.id && e.to !== removed.id),
  }
}

export function addEdge(g: EditableGraph, from: string, to: string): EditableGraph {
  if (!from || !to) return g
  if (g.edges.some((e) => e.from === from && e.to === to)) return g
  return { ...g, edges: [...g.edges, { from, to, weight: 1 }] }
}

export function updateEdge(g: EditableGraph, index: number, patch: Partial<EditableEdge>): EditableGraph {
  if (!g.edges[index]) return g
  return {
    ...g,
    edges: g.edges.map((e, i) =>
      i === index
        ? compactEdge({
            ...e,
            ...patch,
            weight: patch.weight === undefined ? e.weight : Math.max(0, patch.weight),
          })
        : e,
    ),
  }
}

export function removeEdge(g: EditableGraph, index: number): EditableGraph {
  return { ...g, edges: g.edges.filter((_, i) => i !== index) }
}

function compactNode(n: EditableNode): EditableNode {
  const id = n.id.trim()
  const apiTemplateId = n.apiTemplateId?.trim()
  return apiTemplateId ? { id, apiTemplateId } : { id }
}

function compactEdge(e: EditableEdge): EditableEdge {
  return e.dependency ? { ...e, dependency: true } : { from: e.from, to: e.to, weight: e.weight }
}
