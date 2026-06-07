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
  traceEnabled: boolean // visualize live traffic (per-request for small runs, flow map for large)
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
  // Closed-run pool size. The server synthesizes the pool (u0..uN-1) from this when
  // `users` is empty, so a large closed run is a small body instead of one object
  // per user; the open model ignores it.
  userCount?: number
  seed: number
  workers?: string[]
  aggregateWorkers?: boolean
  workload?: WorkloadSpec
  segments?: Segment[]
  trace?: boolean // opt the run into visualization (per-step events for small runs, per-edge aggregates at any scale)
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

// MAX_TRACE_USERS is the run size at or below which the backend additionally emits
// per-request trace events. Above it, tracing is still honored but only as per-edge
// aggregates, so the UI uses this cap to pick the render mode (events vs heatmap),
// not whether to enable visualization.
export const MAX_TRACE_USERS = 200

// traceable reports whether a run is small enough that the backend will additionally
// stream per-request trace events — i.e. whether the live view should animate
// individual requests ('events') or fall back to the aggregate heatmap. It mirrors
// the server's traceSmallEnough: closed runs are capped on the user count, open runs
// on the back-pressure max-concurrency (the open model ignores the user count).
export function traceable(form: ExperimentForm): boolean {
  if (form.workloadKind === 'open') {
    return form.maxConcurrency > 0 && form.maxConcurrency <= MAX_TRACE_USERS
  }
  return form.users > 0 && form.users <= MAX_TRACE_USERS
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
  // Neither model ships one object per virtual user. The open model generates its
  // own sessions from the arrival rate and reads only a single template user; the
  // closed model now sends an empty pool plus `userCount` and lets the server
  // synthesize u0..uN-1 at run time. Materializing one object per user would be
  // megabytes — over the server's request size limit ("request body too large") —
  // at large counts (~270k+), which was the closed-run bug this fixes.
  const users = form.workloadKind === 'open' ? [{ id: 'u0' }] : []

  // Size the safety cap to the configured load so the guard protects the target
  // (host allowlist + a ceiling) without silently throttling what the operator
  // asked for — a hardcoded 1000 rps would cap a 12k arrival rate. Both fields
  // must be > 0 (the guard rejects 0); an "uncapped" (0) max-concurrency maps to a
  // generous ceiling derived from the arrival rate.
  // Math.ceil every term: the form fields can be fractional, and the server decodes
  // these into ints (a non-integer would be rejected with a 400).
  const rateCap =
    form.workloadKind === 'open'
      ? {
          maxRps: Math.max(1000, Math.ceil(form.arrivalRate * 1.5)),
          maxConcurrency:
            form.maxConcurrency > 0
              ? Math.max(Math.ceil(form.maxConcurrency), 200)
              : Math.max(2000, Math.ceil(form.arrivalRate * 2)),
        }
      : { maxRps: Math.max(1000, Math.ceil(form.users)), maxConcurrency: Math.max(200, Math.ceil(form.users)) }

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
      rateCap,
      envClass: 'dev',
    },
    graph,
    templates,
    start: form.start,
    maxSteps: form.maxSteps,
    users,
    seed: 1,
  }
  // Closed runs send the pool size as a count and let the server synthesize
  // u0..uN-1; the open model generates its own sessions, so the count is
  // meaningless there and is left off to keep the open spec clean.
  if (form.workloadKind !== 'open') spec.userCount = form.users
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
  // Opt into visualization whenever requested; the backend now honors it at any
  // scale (small runs additionally get per-request events, all opted-in runs get
  // per-edge aggregates). The render mode is chosen client-side via traceable().
  if (form.traceEnabled) spec.trace = true
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

// HeatEdge is one edge's cumulative traffic in the aggregate heatmap stream: total
// `requests` and `errors` (int64 counts) seen on the edge `from` -> `to`. `from` is
// "" for the entry into a node (a user starting there), matching the trace contract.
export interface HeatEdge {
  from: string
  to: string
  requests: number
  errors: number
}

// HeatFrame is one SSE frame of the per-edge heatmap stream: every edge that has
// seen traffic so far, with cumulative counts. The final frame sets done === true,
// then the server closes. Unlike the trace stream this scales to any run size
// because the payload is bounded by the edge count, not the request count.
export interface HeatFrame {
  edges: HeatEdge[]
  done?: boolean
}

// parseHeatFrame parses a single heatmap SSE "data:" line, mirroring parseTraceFrame:
// it returns null for comments, blank lines, and malformed payloads.
export function parseHeatFrame(line: string): HeatFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as HeatFrame
  } catch {
    return null
  }
}

// --- Latency heatmap stream (the canonical load-test heatmap) -------------------
// A LatencyFrame is a 2-D histogram: rows are latency bands (LOW -> HIGH), columns
// are time buckets since the run started, and each cell holds the request count in
// that band × bucket. It streams over SSE while the run is active and the final
// frame sets done === true, then the server closes — same lifecycle as the per-edge
// heatmap, but the payload encodes density over time rather than over the graph.

// LatencyRow describes one latency band on the Y axis. hiMs === 0 marks the
// unbounded top bucket (everything at or above loMs, e.g. "5s+").
export interface LatencyRow {
  loMs: number
  hiMs: number
  label: string
}

export interface LatencyFrame {
  binWidthMs: number // wall-clock width of one time column (ms)
  rows: LatencyRow[] // latency bands, ordered LOW -> HIGH
  cells: number[][] // cells[rowIndex][colIndex] = request count
  maxCount: number // the densest cell's count, for color scaling
  done?: boolean
}

// parseLatencyFrame parses a single latency-heatmap SSE "data:" line, mirroring
// parseHeatFrame exactly: it returns null for comments, blank lines, and malformed
// payloads, keeping the stream open on a bad frame.
export function parseLatencyFrame(line: string): LatencyFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as LatencyFrame
  } catch {
    return null
  }
}

// LAT_CELL_EMPTY / LAT_CELL_HOT are the endpoints of the latency-heatmap density
// ramp: a near-blank tint of the accent for low/zero density, the strong saturated
// accent at the peak. Kept as "#rrggbb" so latencyCellColor can reuse lerpColor.
export const LAT_CELL_EMPTY = '#eef2ff' // indigo-50: almost blank
export const LAT_CELL_HOT = '#4338ca' // indigo-700: dense

// latencyCellColor maps a cell's request count onto a sequential color ramp from a
// very-light tint (low/zero density) to a strong, saturated accent (max density).
// A zero count is nearly blank so empty cells recede; the ramp is interpolated in
// sRGB via lerpColor, so it stays dependency-free. The fraction uses a sqrt so the
// low end of a wide count range still separates visibly (a few requests already
// read as more than nothing).
export function latencyCellColor(count: number, maxCount: number): string {
  if (count <= 0 || maxCount <= 0) return LAT_CELL_EMPTY
  const frac = Math.sqrt(clamp01(count / maxCount))
  return lerpColor(LAT_CELL_EMPTY, LAT_CELL_HOT, frac)
}

// --- Heatmap visual encoding (pure, unit-tested) -------------------------------
// These map an edge's aggregate counts onto the stroke width and color the SVG
// draws. They live here (next to layoutGraph) so they can be tested without the
// React component, matching the project's "pure helpers in api.ts" convention.

const clamp01 = (n: number) => (n < 0 ? 0 : n > 1 ? 1 : n)

// HEAT_MIN_W / HEAT_MAX_W bound the edge stroke width (SVG units); the busiest
// edge gets HEAT_MAX_W, an idle/zero edge HEAT_MIN_W.
export const HEAT_MIN_W = 1.5
export const HEAT_MAX_W = 14

// heatWidth maps a request count onto a stroke width using a logarithmic scale so
// the busiest edge is the thickest and a 12-request edge and a 12-million-request
// edge stay legible in the same frame: width = MIN + (MAX-MIN)·ln(n+1)/ln(max+1).
// It returns the floor when there is no traffic (n or max <= 0).
export function heatWidth(requests: number, maxRequests: number): number {
  if (requests <= 0 || maxRequests <= 0) return HEAT_MIN_W
  const frac = Math.log(requests + 1) / Math.log(maxRequests + 1)
  return HEAT_MIN_W + (HEAT_MAX_W - HEAT_MIN_W) * clamp01(frac)
}

// --- Terminal nodes & edge classification (pure, unit-tested) -------------------
// The flow map reads as a forward funnel: requests enter on the left and fan
// toward an outcome on the right. To keep that funnel legible at high volume the
// view sorts edges into classes and treats the graph's endpoints specially. These
// helpers encode that grammar without React so they can be tested in isolation.

// terminalNodeIds is the set of node ids that are journey endpoints: a node with
// no apiTemplateId fires no request, so reaching it means the user *finished*
// (done) or *left* (exit) rather than made another call. The backend now emits a
// "terminal" traversal into these, so the flow stream carries inflow edges to
// them; the view styles those as completion/drop-off, not as requests.
export function terminalNodeIds(nodes: { id: string; apiTemplateId?: string }[]): Set<string> {
  const term = new Set<string>()
  for (const n of nodes) {
    if (!n.apiTemplateId) term.add(n.id)
  }
  return term
}

// EdgeKind sorts an edge by its role in the funnel so the view can weight it:
//   'forward'  — advances the journey (drawn boldest; this is the main funnel).
//   'back'     — returns to an earlier, already-visited node (a loop, e.g.
//                category -> browse); de-emphasized so it doesn't fight forward.
//   'terminal' — flows into a template-less endpoint (done/exit); rendered as a
//                completion/drop-off, faded so endpoints read as outcomes.
export type EdgeKind = 'forward' | 'back' | 'terminal'

// classifyEdge labels one edge given the terminal set and each node's BFS depth
// (its funnel column, as produced by layoutGraph). Terminal wins first (an edge
// into done/exit is an outcome regardless of direction). Otherwise an edge whose
// destination sits at an equal-or-shallower depth than its source is a back/loop
// edge; everything else advances the funnel and is 'forward'. Missing depths
// (unreachable nodes) default to forward so they still draw at full strength.
export function classifyEdge(
  from: string,
  to: string,
  terminals: Set<string>,
  depth: Map<string, number>,
): EdgeKind {
  if (terminals.has(to)) return 'terminal'
  const df = depth.get(from)
  const dt = depth.get(to)
  if (df !== undefined && dt !== undefined && dt <= df) return 'back'
  return 'forward'
}

// requestTotal sums the request counts that represent real API calls — every edge
// *except* those flowing into a terminal node. Completions and drop-offs into
// done/exit are journey outcomes, not requests, so counting them would inflate the
// "N requests" headline; they still render as completion flow, just outside this
// total. Entry edges (from === "") into a non-terminal node are real first
// requests and are included.
export function requestTotal(
  edges: { from: string; to: string; requests: number }[],
  terminals: Set<string>,
): number {
  let total = 0
  for (const e of edges) {
    if (terminals.has(e.to)) continue
    total += e.requests
  }
  return total
}

// HEAT_OK / HEAT_ERR are the endpoints of the error-ratio color ramp (the same
// GitHub-dark green/red used elsewhere in the live view).
export const HEAT_OK = '#3fb950'
export const HEAT_ERR = '#f85149'

// heatColor tints an edge from healthy-green to error-red by its error ratio
// (errors/requests). With no requests it is fully green (nothing has failed). The
// result is an "rgb(r, g, b)" string interpolated in sRGB — good enough for a
// status tint and dependency-free.
export function heatColor(errors: number, requests: number): string {
  const ratio = requests > 0 ? clamp01(errors / requests) : 0
  return lerpColor(HEAT_OK, HEAT_ERR, ratio)
}

// lerpColor linearly interpolates between two "#rrggbb" colors; t is clamped to
// [0,1]. Kept tiny to avoid pulling in a color dependency.
export function lerpColor(a: string, b: string, t: number): string {
  const ca = hexToRgb(a)
  const cb = hexToRgb(b)
  const k = clamp01(t)
  const r = Math.round(ca.r + (cb.r - ca.r) * k)
  const g = Math.round(ca.g + (cb.g - ca.g) * k)
  const bl = Math.round(ca.b + (cb.b - ca.b) * k)
  return `rgb(${r}, ${g}, ${bl})`
}

function hexToRgb(hex: string): { r: number; g: number; b: number } {
  const n = parseInt(hex.slice(1), 16)
  return { r: (n >> 16) & 0xff, g: (n >> 8) & 0xff, b: n & 0xff }
}

// formatCount renders large cumulative counts compactly (1234 -> "1.2k",
// 5_000_000 -> "5M") so per-edge labels stay short at any scale.
export function formatCount(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return trimZero(n / 1000) + 'k'
  if (n < 1_000_000_000) return trimZero(n / 1_000_000) + 'M'
  return trimZero(n / 1_000_000_000) + 'B'
}

// trimZero formats to one decimal but drops a trailing ".0" so "5.0k" reads "5k".
function trimZero(n: number): string {
  return n.toFixed(1).replace(/\.0$/, '')
}

// Layout spacing, in the SVG's own (unitless) coordinate space. The SVG scales to
// fit via its viewBox, so these are relative, not pixels.
const COL_GAP = 200 // horizontal distance between layers (columns)
const ROW_GAP = 110 // vertical distance between nodes in the same column

// graphDepths runs the BFS that underlies the layout: from `start`, each reachable
// node gets its shortest-path depth (its funnel column); unreachable nodes (and the
// case where `start` is missing) are simply absent from the map. Only edges between
// declared nodes are followed, in input order, so the result is deterministic. It
// is exported (and reused by layoutGraph) so the flow view can classify edges as
// forward vs back/loop from the same depths the layout draws.
export function graphDepths(
  nodes: { id: string }[],
  edges: { from: string; to: string }[],
  start: string,
): Map<string, number> {
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
  return depth
}

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

  // Shortest-path depth from start (the column); shared with the flow view.
  const depth = graphDepths(nodes, edges, start)

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

// ImportResult is what POST /api/import returns on success: a ready-to-edit
// scenario the caller can drop straight into the Scenario card's fields.
export interface ImportResult {
  graph: unknown
  templates: unknown
  start: string
  maxSteps: number
}

// importScenario converts a raw OpenAPI or HAR document into a scenario via the
// backend. `format` is 'auto' (let the server sniff it), 'openapi', or 'har'. The
// body is the raw spec text (JSON/YAML/HAR), posted as-is. On a non-2xx it throws
// the server's own error text so the UI can show a meaningful message (the backend
// returns 400 {error} on a bad spec and 501 when the importer is unavailable);
// otherwise it returns the parsed scenario. It deliberately throws rather than
// returning a sentinel so the caller's catch surfaces the message inline.
export async function importScenario(
  spec: string,
  format: 'auto' | 'openapi' | 'har',
): Promise<ImportResult> {
  const res = await fetch(`${API}/import?format=${format}`, { method: 'POST', body: spec })
  if (!res.ok) {
    const text = (await res.text()).trim()
    let message = text
    // The server reports failures as { "error": "..." }; unwrap it when present so
    // the user sees the reason, not the raw JSON envelope. Fall back to the body
    // text, then to the status code, so there is always something to show.
    try {
      const parsed = JSON.parse(text) as { error?: unknown }
      if (parsed && typeof parsed.error === 'string' && parsed.error.trim()) message = parsed.error
    } catch {
      /* not JSON: keep the raw text */
    }
    throw new Error(message || `import failed: ${res.status}`)
  }
  return (await res.json()) as ImportResult
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

// heatmapURL is the per-edge aggregate SSE stream for a run (opt-in via spec.trace),
// used by the heatmap view for runs too large to animate request-by-request.
export function heatmapURL(runId: string): string {
  return `${API}/runs/${runId}/heatmap`
}

// latencyHeatmapURL is the latency-over-time SSE stream for a run (opt-in via
// spec.trace): a 2-D histogram of request counts by latency band × time bucket,
// used by the canonical load-test latency heatmap.
export function latencyHeatmapURL(runId: string): string {
  return `${API}/runs/${runId}/latency-heatmap`
}

// reportHTMLURL is the server-rendered, standalone HTML report for a run.
export function reportHTMLURL(runId: string): string {
  return `${API}/runs/${runId}/report.html`
}

// compareURL is the server-rendered HTML diff of two runs (regression view).
export function compareURL(a: string, b: string): string {
  return `${API}/runs/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`
}

// SharedReportError carries a stable code (and the HTTP status) so the viewer can
// render a localized message instead of a hard-coded English string. The message
// is kept as a readable English fallback for non-UI callers / logs.
export class SharedReportError extends Error {
  code: 'expired' | 'notFound' | 'unavailable'
  status: number
  constructor(code: 'expired' | 'notFound' | 'unavailable', status: number, message: string) {
    super(message)
    this.name = 'SharedReportError'
    this.code = code
    this.status = status
  }
}

export async function getSharedReport(token: string): Promise<Report> {
  const res = await fetch(`${API}/reports/shared/${token}`)
  if (res.status === 410) throw new SharedReportError('expired', 410, 'This shared report has expired.')
  if (res.status === 404)
    throw new SharedReportError('notFound', 404, 'This shared report was not found.')
  if (!res.ok)
    throw new SharedReportError('unavailable', res.status, `Shared report unavailable (${res.status}).`)
  return (await res.json()) as Report
}

// shareTokenFromQuery extracts a read-only viewer token from a query string,
// e.g. "?share=abc" -> "abc". Returns null when absent or blank.
export function shareTokenFromQuery(search: string): string | null {
  const t = new URLSearchParams(search).get('share')
  return t && t.trim() ? t.trim() : null
}
