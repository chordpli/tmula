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
  traceEnabled: boolean // stream per-step events for the live graph (small runs only)
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
  trace?: boolean // opt the run into per-step trace frames (honored only for small runs)
}

// parseSegments reads the persona-mix JSON. A blank value means no personas
// (homogeneous run); anything else must be a JSON array of objects each with a
// string `name` and numeric `weight`, or it throws, so a malformed mix is caught
// here rather than rejected confusingly by the server — same contract as the
// graph/templates fields.
export function parseSegments(json: string): Segment[] {
  if (!json.trim()) return []
  const parsed = JSON.parse(json)
  if (!Array.isArray(parsed)) throw new Error('segments must be a JSON array')
  parsed.forEach((seg, i) => {
    if (typeof seg !== 'object' || seg === null) {
      throw new Error(`segment ${i} must be an object with a name and weight`)
    }
    const { name, weight } = seg as { name?: unknown; weight?: unknown }
    if (typeof name !== 'string') throw new Error(`segment ${i} name must be a string`)
    if (typeof weight !== 'number') throw new Error(`segment ${i} weight must be a number`)
  })
  return parsed as Segment[]
}

// runDisabled reports whether the Run button should be disabled for a given run
// status — i.e. while a run is in flight. 'pending' is included alongside
// 'starting' and 'running' because the SSE stream can emit it before 'running';
// omitting it briefly re-enables the button mid-run.
export function runDisabled(status: string): boolean {
  return status === 'starting' || status === 'pending' || status === 'running'
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

// MAX_TRACE_USERS is the run size above which the backend ignores tracing, so the
// UI gates the toggle (and the spec field) to the same cap.
export const MAX_TRACE_USERS = 200

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
  // Opt into tracing only for small runs; the backend ignores it above the cap,
  // so attaching it there would be misleading.
  if (form.traceEnabled && form.users <= MAX_TRACE_USERS) spec.trace = true
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

// TraceEvent is one step a virtual user took: a request from `from` to `to`. The
// entry request has from === "". `status` is 0 on a transport error, and `ok` is
// true when the request completed with status < 400.
export interface TraceEvent {
  seq: number
  userId: string
  from: string
  to: string
  status: number
  latencyMs: number
  ok: boolean
}

// TraceFrame is one SSE frame of the live-trace stream: zero or more events in
// ascending seq order. The final frame sets done === true, then the server closes.
export interface TraceFrame {
  events: TraceEvent[]
  done?: boolean
}

// parseTraceFrame parses a single trace SSE "data:" line, mirroring parseSSEData:
// it returns null for comments, blank lines, and malformed payloads.
export function parseTraceFrame(line: string): TraceFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as TraceFrame
  } catch {
    return null
  }
}

// Layout spacing, in the SVG's own (unitless) coordinate space. The SVG scales to
// fit via its viewBox, so these are relative, not pixels.
const COL_GAP = 200 // horizontal distance between layers (columns)
const ROW_GAP = 110 // vertical distance between nodes in the same column

// layoutGraph computes a deterministic layered (DAG) layout: BFS depth from
// `start` is the column (x); nodes sharing a depth are spread vertically (y) and
// centered around a common midline so unbalanced columns still look tidy. Nodes
// unreachable from `start` are parked together in a single trailing column. The
// result is a plain id -> {x,y} map the SVG renders from; it is pure and stable
// for a given input, so it is unit-tested.
export function layoutGraph(
  nodes: { id: string }[],
  edges: { from: string; to: string }[],
  start: string,
): Record<string, { x: number; y: number }> {
  const ids = nodes.map((n) => n.id)
  const known = new Set(ids)

  // Adjacency: only edges between declared nodes, in input order (determinism).
  const adj = new Map<string, string[]>()
  for (const id of ids) adj.set(id, [])
  for (const e of edges) {
    if (known.has(e.from) && known.has(e.to)) adj.get(e.from)!.push(e.to)
  }

  // BFS from start assigns each reachable node its shortest depth (the column).
  const depth = new Map<string, number>()
  if (known.has(start)) {
    const queue = [start]
    depth.set(start, 0)
    for (let i = 0; i < queue.length; i++) {
      const cur = queue[i]
      const d = depth.get(cur)!
      for (const next of adj.get(cur)!) {
        if (!depth.has(next)) {
          depth.set(next, d + 1)
          queue.push(next)
        }
      }
    }
  }

  // Bucket reachable nodes by depth (discovery order within a column); collect the
  // rest (unreachable, incl. when start is missing) for one trailing column.
  const columns: string[][] = []
  const unreachable: string[] = []
  for (const id of ids) {
    const d = depth.get(id)
    if (d === undefined) {
      unreachable.push(id)
      continue
    }
    while (columns.length <= d) columns.push([])
    columns[d].push(id)
  }
  if (unreachable.length > 0) columns.push(unreachable)

  // Centre each column vertically around y = 0 so columns of differing heights
  // stay visually balanced regardless of how many nodes they hold.
  const pos: Record<string, { x: number; y: number }> = {}
  columns.forEach((col, c) => {
    const offset = ((col.length - 1) * ROW_GAP) / 2
    col.forEach((id, r) => {
      pos[id] = { x: c * COL_GAP, y: r * ROW_GAP - offset }
    })
  })
  return pos
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

// traceURL is the per-step live-trace SSE stream for a run (opt-in via spec.trace).
export function traceURL(runId: string): string {
  return `${API}/runs/${runId}/trace`
}

// reportHTMLURL is the server-rendered, standalone HTML report for a run.
export function reportHTMLURL(runId: string): string {
  return `${API}/runs/${runId}/report.html`
}

// compareURL is the server-rendered HTML diff of two runs (regression view).
export function compareURL(a: string, b: string): string {
  return `${API}/runs/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`
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
