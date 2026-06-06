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
  workers: string // comma-separated gRPC worker addresses; blank = run locally
  aggregateWorkers: boolean // distributed: workers summarize their shard instead of streaming
  // Workload: 'closed' = fixed `users`; 'open' = arrival-rate sessions over time.
  workloadKind: 'closed' | 'open'
  arrivalRate: number // open: users arriving per second
  durationSeconds: number // open: how long to keep users arriving
  maxConcurrency: number // open: back-pressure cap (0 = uncapped)
  thinkMinMs: number // pause between a user's steps (uniform [min,max])
  thinkMaxMs: number
  segmentsJSON: string // open: persona mix as a JSON array (blank/[] = homogeneous)
}

// Segment is one persona in an open run: a weighted share of arrivals with its
// own entry node and pacing overrides.
export interface Segment {
  name: string
  weight: number
  start?: string
  maxSteps?: number
  thinkTime?: { minMs: number; maxMs: number }
}

export interface WorkloadSpec {
  kind: 'open'
  arrival: { shape: 'constant'; startRate: number; peakRate: number }
  durationSeconds: number
  maxConcurrency: number
  thinkTime: { minMs: number; maxMs: number }
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
  workers?: string[]
  aggregateWorkers?: boolean
  workload?: WorkloadSpec
  segments?: Segment[]
}

// parseSegments reads the persona-mix JSON. A blank value means no personas
// (homogeneous run); anything else must be a JSON array or it throws, so the
// caller surfaces a clear error — same contract as the graph/templates fields.
export function parseSegments(json: string): Segment[] {
  if (!json.trim()) return []
  const parsed = JSON.parse(json)
  if (!Array.isArray(parsed)) throw new Error('segments must be a JSON array')
  return parsed as Segment[]
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
  run: { id: string; status: string; killReason?: string; mode?: string }
  stats: Stats
  findings: Finding[]
  workers?: number
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
  const workers = form.workers
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  const users = Array.from({ length: form.users }, (_, i) => ({ id: `u${i}` }))

  const spec: RunSpec = {
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
  // Only attach workers when the operator named at least one address; an empty
  // list would otherwise signal a distributed run with no workers. Worker-side
  // aggregation only makes sense for a distributed run, so gate it on workers.
  if (workers.length > 0) {
    spec.workers = workers
    if (form.aggregateWorkers) spec.aggregateWorkers = true
  }
  // Attach the open workload model when selected; otherwise the run uses the
  // default closed (fixed-user) model.
  if (form.workloadKind === 'open') {
    spec.workload = {
      kind: 'open',
      arrival: { shape: 'constant', startRate: form.arrivalRate, peakRate: form.arrivalRate },
      durationSeconds: form.durationSeconds,
      maxConcurrency: form.maxConcurrency,
      thinkTime: { minMs: form.thinkMinMs, maxMs: form.thinkMaxMs },
    }
    // Personas apply only to the open model; attach them only when provided.
    const segments = parseSegments(form.segmentsJSON)
    if (segments.length > 0) spec.segments = segments
  }
  return spec
}

export interface CapacityPlan {
  arrivalPerSec: number
  peakConcurrency: number
  workersNeeded: number
}

// getCapacity asks the server to size a target population via Little's Law.
export async function getCapacity(
  totalUsers: number,
  windowSeconds: number,
  avgSessionSeconds: number,
  perWorkerCap = 2000,
): Promise<CapacityPlan> {
  const q = new URLSearchParams({
    totalUsers: String(totalUsers),
    windowSeconds: String(windowSeconds),
    avgSessionSeconds: String(avgSessionSeconds),
    perWorkerCap: String(perWorkerCap),
  })
  const res = await fetch(`${API}/capacity?${q}`)
  if (!res.ok) throw new Error(`capacity failed: ${res.status}`)
  return (await res.json()) as CapacityPlan
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
