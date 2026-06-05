// API helpers for the tmula control plane. Pure functions live here (and are
// unit-tested) so the React component stays thin.

export interface ExperimentForm {
  baseUrl: string
  allowlist: string // comma-separated
  users: number
  maxSteps: number
  start: string
  graphJSON: string
  templatesJSON: string
}

export interface RunSpec {
  experiment: unknown
  targetEnv: unknown
  graph: unknown
  templates: unknown
  start: string
  maxSteps: number
  users: { id: string }[]
  seed: number
}

export interface Stats {
  total: number
  errors: number
  timeouts: number
  errorRate: number
  statusCounts: Record<string, number>
  p50: number
  p95: number
  p99: number
  max: number
}

export interface Finding {
  runId: string
  category: string
  severity: string
  evidenceRef?: string
  description: string
}

export interface Report {
  run: { id: string; status: string; killReason?: string }
  stats: Stats
  findings: Finding[]
}

// buildRunSpec turns the form into the RunSpec the API expects. It throws on
// invalid JSON so the caller can surface a clear error.
export function buildRunSpec(form: ExperimentForm): RunSpec {
  const graph = JSON.parse(form.graphJSON)
  const templates = JSON.parse(form.templatesJSON)
  const allowlist = form.allowlist
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  const users = Array.from({ length: form.users }, (_, i) => ({ id: `u${i}` }))

  return {
    experiment: {
      name: 'ui-run',
      targetEnvId: 'env',
      scenarioGraphId: 'graph',
      params: { virtualUserCount: form.users, deviationRate: 0, authStrategy: 'pool' },
    },
    targetEnv: {
      baseUrl: form.baseUrl,
      allowlist,
      rateCap: { maxRps: 1000, maxConcurrency: 200 },
      envClass: 'dev',
    },
    graph,
    templates,
    start: form.start,
    maxSteps: form.maxSteps,
    users,
    seed: 1,
  }
}

export interface StreamFrame {
  status?: string
  reason?: string
  stats?: Stats
}

// parseSSEData parses a single SSE "data:" line, returning null for anything
// else (comments, blank lines, malformed payloads).
export function parseSSEData(line: string): StreamFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as StreamFrame
  } catch {
    return null
  }
}

const API = '/api'

export async function createExperiment(spec: RunSpec): Promise<string> {
  const res = await fetch(`${API}/experiments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(spec),
  })
  if (!res.ok) throw new Error(`create failed: ${res.status} ${await res.text()}`)
  return (await res.json()).id as string
}

export async function startRun(experimentId: string): Promise<string> {
  const res = await fetch(`${API}/experiments/${experimentId}/run`, { method: 'POST' })
  if (!res.ok) throw new Error(`run failed: ${res.status} ${await res.text()}`)
  return (await res.json()).runId as string
}

export async function getReport(runId: string): Promise<Report> {
  const res = await fetch(`${API}/runs/${runId}/report`)
  if (!res.ok) throw new Error(`report failed: ${res.status}`)
  return (await res.json()) as Report
}

export async function killRun(runId: string): Promise<void> {
  const res = await fetch(`${API}/runs/${runId}/kill`, { method: 'POST' })
  if (!res.ok) throw new Error(`kill failed: ${res.status}`)
}

export function streamURL(runId: string): string {
  return `${API}/runs/${runId}/stream`
}

export async function getSharedReport(token: string): Promise<Report> {
  const res = await fetch(`${API}/reports/shared/${token}`)
  if (res.status === 410) throw new Error('This shared report has expired.')
  if (res.status === 404) throw new Error('This shared report was not found.')
  if (!res.ok) throw new Error(`Shared report unavailable (${res.status}).`)
  return (await res.json()) as Report
}

// shareTokenFromQuery extracts a read-only viewer token from a query string,
// e.g. "?share=abc" -> "abc". Returns null when absent or blank.
export function shareTokenFromQuery(search: string): string | null {
  const t = new URLSearchParams(search).get('share')
  return t && t.trim() ? t.trim() : null
}
