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

// TemplateSummary is the slice of an API template the visual editor can edit
// inline: the request method and path. Everything else in the template object
// (payloadTemplate, extract, …) is preserved untouched by updateTemplateInJSON.
export interface TemplateSummary {
  method: string
  path: string
}

export function templateSummaryFromJSON(json: string, id: string): TemplateSummary | null {
  try {
    const parsed = JSON.parse(json) as Record<string, unknown>
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    const tpl = parsed[id]
    if (!tpl || typeof tpl !== 'object' || Array.isArray(tpl)) return null
    const t = tpl as Record<string, unknown>
    return {
      method: typeof t.method === 'string' ? t.method : '',
      path: typeof t.path === 'string' ? t.path : '',
    }
  } catch {
    return null
  }
}

// updateTemplateInJSON patches method/path on one template, creating the template
// object when it does not exist yet, and re-serializes the whole document the same
// way the textarea holds it. On unparseable JSON the original text is returned so
// an inline edit can never destroy what the operator typed.
export function updateTemplateInJSON(json: string, id: string, patch: Partial<TemplateSummary>): string {
  if (!id) return json
  try {
    const parsed = (json.trim() ? JSON.parse(json) : {}) as Record<string, unknown>
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return json
    const prev = parsed[id]
    const base = prev && typeof prev === 'object' && !Array.isArray(prev) ? (prev as Record<string, unknown>) : {}
    parsed[id] = { ...base, ...patch }
    return JSON.stringify(parsed, null, 2)
  } catch {
    return json
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

export function addEdge(g: EditableGraph, from: string, to: string, weight = 1): EditableGraph {
  if (!from || !to) return g
  if (g.edges.some((e) => e.from === from && e.to === to)) return g
  return { ...g, edges: [...g.edges, { from, to, weight: Math.max(0, weight) }] }
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
