import {
  allowlistMatchesHost,
  hostFromBaseUrl,
  parseAllowlist,
  parseSegments,
  type ExperimentForm,
} from './api'

export type DoctorSeverity = 'error' | 'warning'

export interface DoctorIssue {
  severity: DoctorSeverity
  code: string
  messageKey: string
  vars?: Record<string, string | number>
}

interface GraphNode {
  id?: unknown
  apiTemplateId?: unknown
}

interface GraphEdge {
  from?: unknown
  to?: unknown
  weight?: unknown
  dependency?: unknown
}

interface ScenarioGraphShape {
  nodes?: unknown
  edges?: unknown
}

interface TemplateShape {
  method?: unknown
  path?: unknown
  extract?: unknown
}

type TemplateMap = Record<string, TemplateShape>

export function doctorForm(form: ExperimentForm): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  const baseHost = hostFromBaseUrl(form.baseUrl)
  if (baseHost && !allowlistMatchesHost(parseAllowlist(form.allowlist), baseHost)) {
    issues.push(issue('error', 'allowlist-missing-host', 'doctor.allowlistMissingHost', { host: baseHost }))
  }

  const graphResult = parseJSON<ScenarioGraphShape>(form.graphJSON)
  if (!graphResult.ok) {
    issues.push(issue('error', 'graph-json', 'doctor.graphJson', { error: graphResult.error }))
  }
  const templatesResult = parseJSON<TemplateMap>(form.templatesJSON)
  if (!templatesResult.ok) {
    issues.push(issue('error', 'templates-json', 'doctor.templatesJson', { error: templatesResult.error }))
  }

  if (form.workloadKind === 'open') {
    try {
      const segments = parseSegments(form.segmentsJSON)
      if (graphResult.ok) {
        const nodeIDs = nodeIDSet(graphResult.value)
        for (const seg of segments) {
          if (seg.start && !nodeIDs.has(seg.start)) {
            issues.push(issue('error', 'segment-start', 'doctor.segmentStartMissing', { name: seg.name, node: seg.start }))
          }
        }
      }
    } catch (e) {
      issues.push(issue('error', 'segments-json', 'doctor.segmentsJson', { error: messageOf(e) }))
    }
  } else if (form.segmentsJSON.trim()) {
    issues.push(issue('warning', 'segments-closed', 'doctor.segmentsClosed'))
  }

  if (graphResult.ok) {
    issues.push(...doctorGraph(graphResult.value, form.start, templatesResult.ok ? templatesResult.value : null))
  }
  if (templatesResult.ok && graphResult.ok) {
    issues.push(...doctorTemplates(templatesResult.value, graphResult.value))
  }
  return issues
}

function doctorGraph(g: ScenarioGraphShape, start: string, templates: TemplateMap | null): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  if (!Array.isArray(g.nodes) || g.nodes.length === 0) {
    return [issue('error', 'graph-empty', 'doctor.graphEmpty')]
  }
  const nodes = g.nodes as GraphNode[]
  const edges = Array.isArray(g.edges) ? (g.edges as GraphEdge[]) : []
  const known = new Set<string>()
  const seen = new Set<string>()
  const incoming = new Map<string, number>()
  const outgoingWeight = new Map<string, number>()

  for (const n of nodes) {
    if (typeof n.id !== 'string' || !n.id.trim()) {
      issues.push(issue('error', 'node-id', 'doctor.nodeIDMissing'))
      continue
    }
    const id = n.id
    if (seen.has(id)) {
      issues.push(issue('error', 'duplicate-node', 'doctor.duplicateNode', { node: id }))
    }
    seen.add(id)
    known.add(id)
    incoming.set(id, 0)
    if (typeof n.apiTemplateId === 'string' && n.apiTemplateId && templates && !templates[n.apiTemplateId]) {
      issues.push(issue('error', 'node-template-missing', 'doctor.nodeTemplateMissing', { node: id, template: n.apiTemplateId }))
    }
  }

  if (!known.has(start)) {
    issues.push(issue('error', 'start-missing', 'doctor.startMissing', { node: start }))
  } else {
    const startNode = nodes.find((n) => n.id === start)
    if (startNode && !startNode.apiTemplateId) {
      issues.push(issue('warning', 'start-terminal', 'doctor.startTerminal', { node: start }))
    }
  }

  for (const e of edges) {
    const from = typeof e.from === 'string' ? e.from : ''
    const to = typeof e.to === 'string' ? e.to : ''
    if (!known.has(from) || !known.has(to)) {
      issues.push(issue('error', 'edge-unknown-node', 'doctor.edgeUnknownNode', { from: from || '?', to: to || '?' }))
      continue
    }
    const weight = typeof e.weight === 'number' ? e.weight : 0
    if (!(weight >= 0) || !Number.isFinite(weight)) {
      issues.push(issue('error', 'edge-weight', 'doctor.edgeWeightInvalid', { from, to, weight: String(e.weight) }))
    }
    incoming.set(to, (incoming.get(to) ?? 0) + 1)
    outgoingWeight.set(from, (outgoingWeight.get(from) ?? 0) + Math.max(0, weight))
  }

  for (const [node, count] of incoming) {
    if (node !== start && count === 0) {
      issues.push(issue('warning', 'node-no-incoming', 'doctor.nodeNoIncoming', { node }))
    }
  }
  for (const [node, sum] of outgoingWeight) {
    if (sum > 1.000000001) {
      issues.push(issue('warning', 'outgoing-weight-high', 'doctor.outgoingWeightHigh', { node, weight: round(sum) }))
    }
  }
  return issues
}

function doctorTemplates(templates: TemplateMap, g: ScenarioGraphShape): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  const used = new Set<string>()
  const nodes = Array.isArray(g.nodes) ? (g.nodes as GraphNode[]) : []
  for (const n of nodes) {
    if (typeof n.apiTemplateId === 'string' && n.apiTemplateId) used.add(n.apiTemplateId)
  }
  for (const [id, tmpl] of Object.entries(templates)) {
    if (!tmpl || typeof tmpl !== 'object') {
      issues.push(issue('error', 'template-shape', 'doctor.templateShape', { template: id }))
      continue
    }
    if (typeof tmpl.method !== 'string' || !tmpl.method.trim()) {
      issues.push(issue('error', 'template-method', 'doctor.templateMethodMissing', { template: id }))
    }
    if (typeof tmpl.path !== 'string' || !tmpl.path.trim()) {
      issues.push(issue('error', 'template-path', 'doctor.templatePathMissing', { template: id }))
    }
    if (tmpl.extract !== undefined) {
      if (!isRecord(tmpl.extract)) {
        issues.push(issue('error', 'template-extract-shape', 'doctor.templateExtractShape', { template: id }))
      } else {
        for (const [name, path] of Object.entries(tmpl.extract)) {
          if (!name.trim() || typeof path !== 'string' || !path.trim()) {
            issues.push(issue('error', 'template-extract-entry', 'doctor.templateExtractEntry', { template: id }))
            break
          }
        }
      }
    }
    if (!used.has(id)) {
      issues.push(issue('warning', 'template-unused', 'doctor.templateUnused', { template: id }))
    }
  }
  return issues
}

function nodeIDSet(g: ScenarioGraphShape): Set<string> {
  const ids = new Set<string>()
  if (!Array.isArray(g.nodes)) return ids
  for (const n of g.nodes as GraphNode[]) {
    if (typeof n.id === 'string') ids.add(n.id)
  }
  return ids
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function parseJSON<T>(text: string): { ok: true; value: T } | { ok: false; error: string } {
  try {
    return { ok: true, value: JSON.parse(text) as T }
  } catch (e) {
    return { ok: false, error: messageOf(e) }
  }
}

function issue(
  severity: DoctorSeverity,
  code: string,
  messageKey: string,
  vars?: Record<string, string | number>,
): DoctorIssue {
  return { severity, code, messageKey, vars }
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function round(n: number): number {
  return Math.round(n * 1000) / 1000
}
