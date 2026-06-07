import type React from 'react'
import { useEffect, useMemo, useReducer, useRef } from 'react'
import {
  formatCount,
  heatColor,
  heatmapURL,
  heatWidth,
  layoutGraph,
  parseHeatFrame,
  parseTraceFrame,
  traceURL,
  type HeatEdge,
  type TraceEvent,
} from './api'

// LiveGraph visualizes a run's traffic over its scenario graph. It has two modes:
//
//   'events' — the Phase-1 view for small runs: every request is a dot that
//              travels from one node to the next (a pulse at the entry node),
//              green when it succeeded and red when it failed.
//   'flow'   — the Phase-2 view that works at any scale (millions of requests):
//              instead of per-request dots it draws each edge weighted by its
//              cumulative request volume and tinted by its error ratio, so a
//              glance shows where traffic concentrates and where errors are. This
//              is an edge-weighted flow diagram, not a latency heatmap (the
//              canonical load-test heatmap lives in LatencyHeatmap.tsx).
//
// The graph is laid out by layoutGraph (a pure, tested helper). In 'events' mode
// all motion runs from a single requestAnimationFrame loop so it stays smooth and
// the dot count can be capped; 'flow' mode does no per-dot animation (it is an
// aggregate) beyond a subtle opacity pulse when a fresh frame arrives.

interface GraphNode {
  id: string
  apiTemplateId?: string
}

interface GraphEdge {
  from: string
  to: string
  weight?: number
  dependency?: boolean
}

interface LiveGraphProps {
  graph: { nodes: GraphNode[]; edges: GraphEdge[] }
  start: string
  runId: string
  active: boolean
  mode: 'events' | 'flow'
}

// Visual + motion tuning. Colors track the GitHub-dark palette so green/red
// traffic reads clearly against the dark canvas.
const NODE_R = 26 // node circle radius (SVG units; viewBox scales to fit)
const DOT_R = 6 // travelling request dot radius
const PAD = 60 // padding around the graph's bounding box
const TRAVEL_MS = 600 // how long a dot takes to cross an edge / a pulse to fade
const MAX_DOTS = 300 // cap concurrent dots so the RAF loop stays cheap
const PULSE_MS = 450 // heatmap: how long the opacity pulse on a fresh frame lasts
const OK_COLOR = '#3fb950'
const ERR_COLOR = '#f85149'
const BG = '#0d1117'
const EDGE = '#30363d'
const NODE_FILL = '#161b22'
const NODE_STROKE = '#3d444d'
const TEXT = '#c9d1d9'
const MUTED = '#8b949e'

// A Dot is one in-flight request animation. For an entry request (from === "")
// fromId is null and the dot pulses in place at the destination.
interface Dot {
  id: number
  fromId: string | null
  toId: string
  color: string
  start: number // performance.now() when the dot was spawned
}

interface NodeCount {
  total: number
  errors: number
}

export default function LiveGraph({ graph, start, runId, active, mode }: LiveGraphProps) {
  // Layout is pure and depends only on the graph + start, so memoize it.
  const positions = useMemo(
    () => layoutGraph(graph.nodes, graph.edges, start),
    [graph.nodes, graph.edges, start],
  )

  // The viewBox is derived from the laid-out node extents so the SVG scales to
  // fit any graph shape responsively.
  const view = useMemo(() => boundingBox(positions, PAD), [positions])

  return mode === 'flow' ? (
    <FlowView graph={graph} start={start} runId={runId} active={active} positions={positions} view={view} />
  ) : (
    <EventsView graph={graph} start={start} runId={runId} active={active} positions={positions} view={view} />
  )
}

// Positions/view are computed once by the parent and shared by both modes.
interface ModeProps {
  graph: { nodes: GraphNode[]; edges: GraphEdge[] }
  start: string
  runId: string
  active: boolean
  positions: Record<string, { x: number; y: number }>
  view: { x: number; y: number; w: number; h: number }
}

// ---------------------------------------------------------------------------
// Events mode (Phase 1): per-request travelling dots.
// ---------------------------------------------------------------------------

function EventsView({ graph, start, runId, active, positions, view }: ModeProps) {
  // Live, mutable state lives in refs so the rAF loop and SSE handler can update
  // it without forcing a React render per event. A single forced render per
  // animation frame (via tick) reads these refs to paint.
  const dotsRef = useRef<Dot[]>([])
  const countsRef = useRef<Map<string, NodeCount>>(new Map())
  const seqRef = useRef(0) // monotonic dot id
  const rafRef = useRef<number | null>(null)
  const esRef = useRef<EventSource | null>(null)
  const [, tick] = useReducer((n: number) => n + 1, 0)

  // The animation loop: prune expired dots each frame and request a render while
  // any dot is alive. It is independent of the data source so it can coast to a
  // stop after the last event without leaking a timer.
  const ensureLoop = useRef<() => void>(() => {})
  ensureLoop.current = () => {
    if (rafRef.current !== null) return
    const step = () => {
      const now = performance.now()
      const before = dotsRef.current.length
      dotsRef.current = dotsRef.current.filter((d) => now - d.start < TRAVEL_MS)
      if (dotsRef.current.length > 0) {
        rafRef.current = requestAnimationFrame(step)
      } else {
        rafRef.current = null
      }
      // Render this frame whenever something is (or just was) moving.
      if (dotsRef.current.length > 0 || before > 0) tick()
    }
    rafRef.current = requestAnimationFrame(step)
  }

  function ingest(events: TraceEvent[]) {
    const counts = countsRef.current
    for (const ev of events) {
      // Per-node live counters: attribute the request to its destination node.
      const c = counts.get(ev.to) ?? { total: 0, errors: 0 }
      c.total += 1
      if (!ev.ok) c.errors += 1
      counts.set(ev.to, c)

      // Only animate edges we can place; skip events that name unknown nodes.
      if (!positions[ev.to]) continue
      const fromId = ev.from && positions[ev.from] ? ev.from : null
      dotsRef.current.push({
        id: seqRef.current++,
        fromId,
        toId: ev.to,
        color: ev.ok ? OK_COLOR : ERR_COLOR,
        start: performance.now(),
      })
    }
    // Cap concurrent dots: drop the oldest beyond the limit so motion stays cheap.
    if (dotsRef.current.length > MAX_DOTS) {
      dotsRef.current = dotsRef.current.slice(dotsRef.current.length - MAX_DOTS)
    }
    ensureLoop.current()
  }

  // Open the trace stream while active; tear it down on unmount, when active goes
  // false, on the done frame, or when the run id changes.
  useEffect(() => {
    if (!active) return
    // Reset live state for a fresh run.
    dotsRef.current = []
    countsRef.current = new Map()
    tick()

    const es = new EventSource(traceURL(runId))
    esRef.current = es
    es.onmessage = (e: MessageEvent) => {
      // EventSource strips the "data:" prefix; re-add it so the tested pure
      // parser (which mirrors parseSSEData) handles the line uniformly.
      const line = typeof e.data === 'string' && e.data.startsWith('data:') ? e.data : `data: ${e.data}`
      const frame = parseTraceFrame(line)
      if (!frame) return // malformed frame: ignore, keep the stream open
      if (frame.events?.length) ingest(frame.events)
      if (frame.done) es.close()
    }
    es.onerror = () => {
      // The server also closes the stream on completion; nothing to recover, so
      // just close our side. Any in-flight dots finish animating on their own.
      es.close()
    }
    return () => {
      es.close()
      esRef.current = null
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [active, runId])

  const now = performance.now()
  const counts = countsRef.current

  return (
    <figure style={figure}>
      <figcaption style={caption}>
        <span style={{ color: TEXT, fontWeight: 600 }}>Live traffic</span>
        <span style={{ color: MUTED }}> — each dot is one request</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 14, alignItems: 'center' }}>
          <Legend color={OK_COLOR} label="ok" />
          <Legend color={ERR_COLOR} label="error" />
        </span>
      </figcaption>
      <svg
        viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
        width="100%"
        role="img"
        aria-label="Live request traffic over the scenario graph"
        style={canvas}
      >
        <defs>
          <ArrowMarker />
        </defs>

        {/* Edges first so nodes and dots paint on top. */}
        <g>
          {graph.edges.map((e, i) => {
            const a = positions[e.from]
            const b = positions[e.to]
            if (!a || !b) return null
            // Stop the line at the node rim (not the center) so the arrowhead sits
            // just outside the destination circle.
            const { x1, y1, x2, y2 } = trimToRim(a, b, NODE_R + 2)
            return (
              <line
                key={`e${i}`}
                x1={x1}
                y1={y1}
                x2={x2}
                y2={y2}
                stroke={EDGE}
                strokeWidth={1.5}
                // Dependency edges are dashed to read as "must happen before".
                strokeDasharray={e.dependency ? '6 5' : undefined}
                markerEnd="url(#lg-arrow)"
              />
            )
          })}
        </g>

        {/* Nodes: a labeled circle with live request/error counters beneath. */}
        <g>
          {graph.nodes.map((n) => {
            const p = positions[n.id]
            if (!p) return null
            const c = counts.get(n.id)
            const isStart = n.id === start
            return (
              <g key={n.id}>
                <circle
                  cx={p.x}
                  cy={p.y}
                  r={NODE_R}
                  fill={NODE_FILL}
                  stroke={isStart ? OK_COLOR : NODE_STROKE}
                  strokeWidth={isStart ? 2.5 : 1.5}
                />
                <text
                  x={p.x}
                  y={p.y + 4}
                  textAnchor="middle"
                  fontSize={13}
                  fontFamily="ui-monospace, monospace"
                  fill={TEXT}
                >
                  {n.id}
                </text>
                {c && (
                  <text x={p.x} y={p.y + NODE_R + 16} textAnchor="middle" fontSize={11} fill={MUTED}>
                    {c.total}
                    {c.errors > 0 && <tspan fill={ERR_COLOR}> · {c.errors} err</tspan>}
                  </text>
                )}
              </g>
            )
          })}
        </g>

        {/* Traffic dots, painted last. Position + opacity are interpolated from
            each dot's age; entry-request dots pulse in place at the node. */}
        <g>
          {dotsRef.current.map((d) => {
            const t = clamp01((now - d.start) / TRAVEL_MS)
            const dest = positions[d.toId]
            if (!dest) return null
            if (d.fromId === null) {
              // Entry pulse: a ring expanding out of the destination node, fading.
              return (
                <circle
                  key={d.id}
                  cx={dest.x}
                  cy={dest.y}
                  r={NODE_R + t * 16}
                  fill="none"
                  stroke={d.color}
                  strokeWidth={2}
                  opacity={1 - t}
                />
              )
            }
            const src = positions[d.fromId]
            if (!src) return null
            const e = easeInOut(t)
            return (
              <circle
                key={d.id}
                cx={src.x + (dest.x - src.x) * e}
                cy={src.y + (dest.y - src.y) * e}
                r={DOT_R}
                fill={d.color}
                // Hold full opacity through the travel, then fade in the last 25%.
                opacity={t < 0.75 ? 1 : 1 - (t - 0.75) / 0.25}
              />
            )
          })}
        </g>
      </svg>
    </figure>
  )
}

// ---------------------------------------------------------------------------
// Flow mode (Phase 2): per-edge aggregate flow, scales to any run size.
// ---------------------------------------------------------------------------

// edgeKey identifies an edge by endpoints so stream aggregates can be matched to
// the declared graph edges. Entry edges (from === "") key on their destination.
function edgeKey(from: string, to: string): string {
  return `${from} ${to}`
}

function FlowView({ graph, start, runId, active, positions, view }: ModeProps) {
  // The latest per-edge aggregates, keyed by endpoints. Held in a ref so the SSE
  // handler can replace it without a render per frame; tick() repaints on update.
  const heatRef = useRef<Map<string, HeatEdge>>(new Map())
  const totalRef = useRef(0) // cumulative request count across all edges
  const pulseRef = useRef(0) // performance.now() of the last frame, drives the pulse
  const rafRef = useRef<number | null>(null)
  const [, tick] = useReducer((n: number) => n + 1, 0)

  // Drive a short opacity pulse after each frame so the graph visibly "breathes"
  // on update without animating individual requests. The loop coasts to a stop
  // once the pulse has elapsed, so it never spins when traffic is idle.
  const ensurePulse = useRef<() => void>(() => {})
  ensurePulse.current = () => {
    if (rafRef.current !== null) return
    const step = () => {
      const elapsed = performance.now() - pulseRef.current
      if (elapsed < PULSE_MS) {
        rafRef.current = requestAnimationFrame(step)
      } else {
        rafRef.current = null
      }
      tick()
    }
    rafRef.current = requestAnimationFrame(step)
  }

  function ingest(edges: HeatEdge[]) {
    const heat = heatRef.current
    let total = 0
    for (const e of edges) {
      heat.set(edgeKey(e.from, e.to), e)
    }
    // Counts are cumulative, so the total is just the sum of the current edges.
    for (const e of heat.values()) total += e.requests
    totalRef.current = total
    pulseRef.current = performance.now()
    ensurePulse.current()
  }

  // Open the heatmap stream while active; tear it down on unmount, when active
  // goes false, on the done frame, or when the run id changes.
  useEffect(() => {
    if (!active) return
    heatRef.current = new Map()
    totalRef.current = 0
    tick()

    const es = new EventSource(heatmapURL(runId))
    es.onmessage = (e: MessageEvent) => {
      const line = typeof e.data === 'string' && e.data.startsWith('data:') ? e.data : `data: ${e.data}`
      const frame = parseHeatFrame(line)
      if (!frame) return // malformed frame: ignore, keep the stream open
      if (frame.edges?.length) ingest(frame.edges)
      if (frame.done) es.close()
    }
    es.onerror = () => {
      es.close()
    }
    return () => {
      es.close()
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [active, runId])

  const heat = heatRef.current

  // The busiest edge sets the scale so widths stay legible whether the peak is 12
  // or 12 million requests (ln keeps a wide range readable in one frame).
  let maxRequests = 0
  for (const e of heat.values()) if (e.requests > maxRequests) maxRequests = e.requests

  // A short fade-in right after each frame, applied as a global opacity nudge so
  // the whole graph pulses on update. 1 when idle (no in-flight pulse).
  const sincePulse = performance.now() - pulseRef.current
  const pulse = sincePulse < PULSE_MS ? 0.82 + 0.18 * (sincePulse / PULSE_MS) : 1

  // Entry edges (from === "") have no source node to draw a line from; surface
  // them as a halo on the destination node instead.
  const entryHeat = new Map<string, HeatEdge>()
  for (const e of heat.values()) {
    if (e.from === '' && positions[e.to]) entryHeat.set(e.to, e)
  }

  return (
    <figure style={figure}>
      <figcaption style={caption}>
        <span style={{ color: TEXT, fontWeight: 600 }}>Traffic flow</span>
        <span style={{ color: MUTED }}> — edge thickness is request volume</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 14, alignItems: 'center' }}>
          <span style={{ color: MUTED, fontSize: 12 }}>
            <span style={{ color: TEXT, fontWeight: 600 }}>{formatCount(totalRef.current)}</span> requests
          </span>
          <Legend color={OK_COLOR} label="healthy" />
          <Legend color={ERR_COLOR} label="errors" />
        </span>
      </figcaption>
      <svg
        viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
        width="100%"
        role="img"
        aria-label="Aggregate request traffic flow over the scenario graph"
        style={canvas}
      >
        <defs>
          <ArrowMarker />
        </defs>

        {/* Edges, weighted by volume and tinted by error ratio. Idle edges keep
            the faint base stroke so the graph's shape stays visible. */}
        <g opacity={pulse}>
          {graph.edges.map((e, i) => {
            const a = positions[e.from]
            const b = positions[e.to]
            if (!a || !b) return null
            const h = heat.get(edgeKey(e.from, e.to))
            const hot = h !== undefined && h.requests > 0
            const w = hot ? heatWidth(h.requests, maxRequests) : 1.5
            const color = hot ? heatColor(h.errors, h.requests) : EDGE
            const { x1, y1, x2, y2 } = trimToRim(a, b, NODE_R + 2)
            const mid = { x: (x1 + x2) / 2, y: (y1 + y2) / 2 }
            return (
              <g key={`e${i}`}>
                <line
                  x1={x1}
                  y1={y1}
                  x2={x2}
                  y2={y2}
                  stroke={color}
                  strokeWidth={w}
                  strokeLinecap="round"
                  strokeDasharray={e.dependency ? '6 5' : undefined}
                  markerEnd="url(#lg-arrow)"
                  opacity={hot ? 0.95 : 0.6}
                />
                {hot && (
                  <text
                    x={mid.x}
                    y={mid.y - 6}
                    textAnchor="middle"
                    fontSize={11}
                    fontFamily="ui-monospace, monospace"
                    fill={TEXT}
                    style={labelHalo}
                  >
                    {formatCount(h.requests)}
                    {h.errors > 0 && <tspan fill={ERR_COLOR}> · {formatCount(h.errors)} err</tspan>}
                  </text>
                )}
              </g>
            )
          })}
        </g>

        {/* Nodes, with a colored halo for entry traffic (the "" -> node edge). */}
        <g opacity={pulse}>
          {graph.nodes.map((n) => {
            const p = positions[n.id]
            if (!p) return null
            const entry = entryHeat.get(n.id)
            const isStart = n.id === start
            return (
              <g key={n.id}>
                {entry && entry.requests > 0 && (
                  <circle
                    cx={p.x}
                    cy={p.y}
                    r={NODE_R + 6}
                    fill="none"
                    stroke={heatColor(entry.errors, entry.requests)}
                    strokeWidth={Math.max(2, heatWidth(entry.requests, maxRequests) - 2)}
                    opacity={0.85}
                  />
                )}
                <circle
                  cx={p.x}
                  cy={p.y}
                  r={NODE_R}
                  fill={NODE_FILL}
                  stroke={isStart ? OK_COLOR : NODE_STROKE}
                  strokeWidth={isStart ? 2.5 : 1.5}
                />
                <text
                  x={p.x}
                  y={p.y + 4}
                  textAnchor="middle"
                  fontSize={13}
                  fontFamily="ui-monospace, monospace"
                  fill={TEXT}
                >
                  {n.id}
                </text>
                {entry && entry.requests > 0 && (
                  <text x={p.x} y={p.y + NODE_R + 16} textAnchor="middle" fontSize={11} fill={MUTED}>
                    {formatCount(entry.requests)} in
                    {entry.errors > 0 && <tspan fill={ERR_COLOR}> · {formatCount(entry.errors)} err</tspan>}
                  </text>
                )}
              </g>
            )
          })}
        </g>
      </svg>
    </figure>
  )
}

// ---------------------------------------------------------------------------
// Shared presentation.
// ---------------------------------------------------------------------------

function ArrowMarker() {
  return (
    <marker
      id="lg-arrow"
      viewBox="0 0 10 10"
      refX="9"
      refY="5"
      markerWidth="7"
      markerHeight="7"
      orient="auto-start-reverse"
    >
      <path d="M0,0 L10,5 L0,10 z" fill={EDGE} />
    </marker>
  )
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, color: MUTED, fontSize: 12 }}>
      <span style={{ width: 9, height: 9, borderRadius: '50%', background: color, display: 'inline-block' }} />
      {label}
    </span>
  )
}

// boundingBox returns a padded viewBox covering all node centers (plus the node
// radius and counter labels), so the SVG scales to fit whatever the layout produced.
function boundingBox(
  positions: Record<string, { x: number; y: number }>,
  pad: number,
): { x: number; y: number; w: number; h: number } {
  const pts = Object.values(positions)
  if (pts.length === 0) return { x: 0, y: 0, w: 1, h: 1 }
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const p of pts) {
    minX = Math.min(minX, p.x)
    minY = Math.min(minY, p.y)
    maxX = Math.max(maxX, p.x)
    maxY = Math.max(maxY, p.y)
  }
  // Extra bottom room for the counter label under each node.
  return {
    x: minX - pad,
    y: minY - pad,
    w: maxX - minX + pad * 2,
    h: maxY - minY + pad * 2 + 18,
  }
}

// trimToRim shortens the segment a->b so it starts/ends `r` units from each
// endpoint, leaving a gap at the node rim for a clean arrowhead.
function trimToRim(
  a: { x: number; y: number },
  b: { x: number; y: number },
  r: number,
): { x1: number; y1: number; x2: number; y2: number } {
  const dx = b.x - a.x
  const dy = b.y - a.y
  const len = Math.hypot(dx, dy) || 1
  const ux = dx / len
  const uy = dy / len
  return { x1: a.x + ux * r, y1: a.y + uy * r, x2: b.x - ux * r, y2: b.y - uy * r }
}

const clamp01 = (n: number) => (n < 0 ? 0 : n > 1 ? 1 : n)
// Smooth start/stop so dots accelerate out of a node and ease into the next.
const easeInOut = (t: number) => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2)

const figure: React.CSSProperties = {
  margin: 0,
  border: '1px solid #30363d',
  borderRadius: 10,
  padding: 10,
  background: BG,
}
const caption: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 6,
  fontSize: 13,
  marginBottom: 8,
  padding: '0 2px',
}
const canvas: React.CSSProperties = {
  display: 'block',
  background: BG,
  borderRadius: 8,
  maxHeight: 460,
}
// A subtle dark backdrop behind edge labels so the count stays readable where it
// crosses a thick, brightly-colored stroke.
const labelHalo: React.CSSProperties = {
  paintOrder: 'stroke',
  stroke: BG,
  strokeWidth: 3,
  strokeLinejoin: 'round',
}
