import type React from 'react'
import { useEffect, useMemo, useReducer, useRef } from 'react'
import {
  classifyEdge,
  formatCount,
  graphDepths,
  heatColor,
  heatmapURL,
  heatWidth,
  layoutGraph,
  parseHeatFrame,
  parseTraceFrame,
  requestTotal,
  terminalNodeIds,
  traceURL,
  type HeatEdge,
  type TraceEvent,
} from './api'
import { useI18n } from './i18n'

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

// Terminal-node palette. A template-less endpoint is an outcome, not a step, so it
// is colored by intent: 'done' reads as a calm completion (a desaturated teal-green
// that is clearly positive without competing with the live OK_COLOR on edges),
// 'exit' as a muted neutral drop-off (slate) so a user leaving doesn't look like an
// error. Both are quieter than an active request node so endpoints settle the eye.
const DONE_COLOR = '#2ea88a' // completion ring + check
const DONE_FILL = '#11201d' // faint green-tinted disc
const EXIT_COLOR = '#6e7681' // drop-off ring (neutral slate, never red)
const EXIT_FILL = '#15191f' // faint neutral disc

// Edge-emphasis multipliers keep the forward funnel dominant. Back/loop edges and
// flows into terminals are drawn thinner and more transparent than forward edges so
// the main left-to-right path stays the loudest thing on the canvas even when every
// edge is busy. Widths are still derived from heatWidth; these only scale + fade.
const FORWARD_OPACITY = 0.95
const BACK_OPACITY = 0.4
const TERMINAL_OPACITY = 0.5
const BACK_WIDTH_SCALE = 0.55 // back/loop edges render at ~half their volume width
const TERMINAL_WIDTH_SCALE = 0.6 // terminal inflow likewise recedes
const IDLE_OPACITY = 0.45 // zero-traffic edges: a faint skeleton of the graph
const ENTRY_HALO_MAX_W = 6 // cap the entry-volume node halo so the start node stays calm

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
  const { t } = useI18n()
  // Terminal endpoints (done/exit). A dot whose destination is one of these is a
  // user finishing/leaving rather than making a call, so we tint it as a completion
  // (the trace wire has no 'terminal' flag — we infer it from the destination id).
  const terminals = useMemo(() => terminalNodeIds(graph.nodes), [graph.nodes])
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
      // A successful dot landing on a terminal is a completion/drop-off: tint it
      // calm-teal (done) or muted-slate (exit) so finishing reads differently from
      // an in-flight request; a failed dot stays red regardless of destination.
      const color = !ev.ok
        ? ERR_COLOR
        : terminals.has(ev.to)
          ? terminalRole(ev.to) === 'dropoff'
            ? EXIT_COLOR
            : DONE_COLOR
          : OK_COLOR
      dotsRef.current.push({
        id: seqRef.current++,
        fromId,
        toId: ev.to,
        color,
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
        <span style={{ color: TEXT, fontWeight: 600 }}>{t('graph.events.title')}</span>
        <span style={{ color: MUTED }}> {t('graph.events.sub')}</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 14, alignItems: 'center' }}>
          <Legend color={OK_COLOR} label={t('graph.legend.ok')} />
          <Legend color={ERR_COLOR} label={t('graph.legend.error')} />
        </span>
      </figcaption>
      <svg
        viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
        width="100%"
        role="img"
        aria-label={t('graph.aria.events')}
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
                    {c.errors > 0 && <tspan fill={ERR_COLOR}> · {c.errors} {t('graph.err')}</tspan>}
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
  const { t } = useI18n()
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

  // Terminal nodes (done/exit: no apiTemplateId) and per-node funnel depth are
  // pure functions of the graph, so memoize them. They drive edge classification
  // and which inflow counts as a request vs a completion/drop-off.
  const terminals = useMemo(() => terminalNodeIds(graph.nodes), [graph.nodes])
  const depths = useMemo(() => graphDepths(graph.nodes, graph.edges, start), [graph.nodes, graph.edges, start])

  function ingest(edges: HeatEdge[]) {
    const heat = heatRef.current
    for (const e of edges) {
      heat.set(edgeKey(e.from, e.to), e)
    }
    // The "N requests" headline counts only real request edges (those into
    // non-terminal nodes); completions/drop-offs into done/exit render as flow but
    // never inflate the request total. Counts are cumulative, so summing the
    // current edges gives the running total.
    totalRef.current = requestTotal(Array.from(heat.values()), terminals)
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

  // The busiest *request* edge sets the width scale so widths stay legible whether
  // the peak is 12 or 12 million requests (ln keeps a wide range readable in one
  // frame). Terminal inflow is excluded from the scale: a completion edge can carry
  // every user that finished, and letting it set the ceiling would crush the actual
  // funnel edges to hairlines. Terminal edges are then drawn against this same scale
  // but capped, so they still read as flow without dominating.
  let maxRequests = 0
  for (const e of heat.values()) {
    if (terminals.has(e.to)) continue
    if (e.requests > maxRequests) maxRequests = e.requests
  }
  // Fall back to the overall peak when *every* lit edge is terminal (e.g. a
  // health-check-style graph that is all endpoint), so widths still differentiate.
  if (maxRequests === 0) {
    for (const e of heat.values()) if (e.requests > maxRequests) maxRequests = e.requests
  }

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

  // Terminal inflow: how many users ended at each endpoint, summed across all edges
  // into it (a completion/drop-off can be reached from several places). The node
  // shows this as its outcome count rather than as "requests". Errors are summed too
  // so a terminal that somehow logged failures still surfaces them.
  const terminalInflow = new Map<string, { requests: number; errors: number }>()
  for (const e of heat.values()) {
    if (!terminals.has(e.to) || !positions[e.to]) continue
    const cur = terminalInflow.get(e.to) ?? { requests: 0, errors: 0 }
    cur.requests += e.requests
    cur.errors += e.errors
    terminalInflow.set(e.to, cur)
  }

  return (
    <figure style={figure}>
      <figcaption style={caption}>
        <span style={{ color: TEXT, fontWeight: 600 }}>{t('graph.flow.title')}</span>
        <span style={{ color: MUTED }}> {t('graph.flow.sub')}</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 14, alignItems: 'center' }}>
          <span style={{ color: MUTED, fontSize: 12 }}>
            <span style={{ color: TEXT, fontWeight: 600 }}>{formatCount(totalRef.current)}</span>{' '}
            {t('graph.flow.requests')}
          </span>
          <Legend color={OK_COLOR} label={t('graph.legend.healthy')} />
          <Legend color={ERR_COLOR} label={t('graph.legend.errors')} />
        </span>
      </figcaption>
      <svg
        viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
        width="100%"
        role="img"
        aria-label={t('graph.aria.flow')}
        style={canvas}
      >
        <defs>
          <ArrowMarker />
        </defs>

        {/* Edges, weighted by volume and tinted by error ratio, but sorted into
            classes so the forward funnel dominates: forward edges are boldest and
            labeled; back/loop edges and flows into terminals recede (thinner, more
            faded, soft arrowhead) so high-volume runs read as a funnel, not a
            tangle. Idle edges keep a faint skeleton so the graph's shape shows. */}
        <g opacity={pulse}>
          {graph.edges.map((e, i) => {
            const a = positions[e.from]
            const b = positions[e.to]
            if (!a || !b) return null
            const h = heat.get(edgeKey(e.from, e.to))
            const hot = h !== undefined && h.requests > 0
            const kind = classifyEdge(e.from, e.to, terminals, depths)
            const role = kind === 'terminal' ? terminalRole(e.to) : null
            // Base width from volume, then class-scaled so back/terminal edges sit
            // below forward edges of the same volume.
            const baseW = hot ? heatWidth(h.requests, maxRequests) : 1.5
            const w =
              kind === 'back'
                ? Math.max(1.2, baseW * BACK_WIDTH_SCALE)
                : kind === 'terminal'
                  ? Math.max(1.2, baseW * TERMINAL_WIDTH_SCALE)
                  : baseW
            // Terminal inflow is a neutral outcome unless it actually errored: a user
            // simply finishing tints calm-green, leaving tints muted-slate, and only
            // real errors pull it toward red. Forward/back edges keep the error tint.
            const color = !hot
              ? EDGE
              : kind === 'terminal'
                ? h.errors > 0
                  ? heatColor(h.errors, h.requests)
                  : role === 'dropoff'
                    ? EXIT_COLOR
                    : DONE_COLOR
                : heatColor(h.errors, h.requests)
            const edgeOpacity = !hot
              ? IDLE_OPACITY
              : kind === 'back'
                ? BACK_OPACITY
                : kind === 'terminal'
                  ? TERMINAL_OPACITY
                  : FORWARD_OPACITY
            // Forward edges get the bright arrowhead; everything de-emphasized gets
            // the soft one so the head matches the line's prominence.
            const marker = kind === 'forward' && hot ? 'url(#lg-arrow)' : 'url(#lg-arrow-soft)'
            // Stop the line further from the rim for thick strokes: a round-capped
            // wide edge would otherwise bulge into the node. Half the stroke width is
            // the cap's reach, so add it to the base gap (plus the constant arrowhead).
            const { x1, y1, x2, y2 } = trimToRim(a, b, NODE_R + 4 + w / 2)
            const mid = { x: (x1 + x2) / 2, y: (y1 + y2) / 2 }
            // Offset the label perpendicular to the edge so it clears the stroke
            // (which can be up to HEAT_MAX_W thick) and adjacent labels collide
            // less. The offset grows with stroke width.
            const off = labelOffset(x1, y1, x2, y2, w)
            // Only forward edges carry a count label: back/loop edges are skipped to
            // cut clutter, and terminal flow is summed under its endpoint node (as a
            // completion/drop-off) rather than labeled twice on the edge. Never label
            // zero traffic.
            const showLabel = hot && kind === 'forward'
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
                  markerEnd={marker}
                  opacity={edgeOpacity}
                />
                {showLabel && (
                  <text
                    x={mid.x + off.x}
                    y={mid.y + off.y}
                    textAnchor="middle"
                    fontSize={11}
                    fontFamily="ui-monospace, monospace"
                    fill={TEXT}
                    style={labelHalo}
                  >
                    {formatCount(h!.requests)}
                    {h!.errors > 0 && (
                      <tspan fill={ERR_COLOR}> · {formatCount(h!.errors)} {t('graph.err')}</tspan>
                    )}
                  </text>
                )}
              </g>
            )
          })}
        </g>

        {/* Nodes. Request nodes keep the live disc with an entry halo; terminal
            endpoints (done/exit) render distinctly as a completion ✓ or a muted
            drop-off so they read as outcomes, not broken/idle nodes. */}
        <g opacity={pulse}>
          {graph.nodes.map((n) => {
            const p = positions[n.id]
            if (!p) return null
            if (terminals.has(n.id)) {
              return (
                <TerminalNode
                  key={n.id}
                  id={n.id}
                  x={p.x}
                  y={p.y}
                  inflow={terminalInflow.get(n.id)}
                  t={t}
                />
              )
            }
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
                    // The entry halo signals first-request volume, but it is a halo,
                    // not a primary edge: cap it so a start node that receives every
                    // user (entry >= the busiest forward edge) doesn't bloom into a
                    // ring that overpowers the funnel.
                    strokeWidth={Math.min(ENTRY_HALO_MAX_W, Math.max(2, heatWidth(entry.requests, maxRequests) - 2))}
                    opacity={0.8}
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
                    {formatCount(entry.requests)} {t('graph.in')}
                    {entry.errors > 0 && (
                      <tspan fill={ERR_COLOR}> · {formatCount(entry.errors)} {t('graph.err')}</tspan>
                    )}
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

// ArrowMarker defines the two arrowheads the edges reference. The critical detail
// is markerUnits="userSpaceOnUse": without it a marker scales with the edge's stroke
// width, so a HEAT_MAX_W (14) edge would render a ~14×-size triangle that swallows
// the node. Pinning the units to user space makes every arrowhead a constant ~10 SVG
// units regardless of edge thickness. refX is set near the tip (x≈9 of a 10-wide
// head) so, combined with the rim gap trimToRim leaves, the point lands just outside
// the destination circle. 'lg-arrow' is the bright forward head; 'lg-arrow-soft' is
// the muted head for de-emphasized (back/terminal/idle) edges so the head's weight
// matches its line.
const ARROW_SIZE = 10 // arrowhead extent in SVG user units (constant at any stroke width)

function ArrowMarker() {
  const common = {
    viewBox: '0 0 10 10',
    refX: 9,
    refY: 5,
    markerWidth: ARROW_SIZE,
    markerHeight: ARROW_SIZE,
    markerUnits: 'userSpaceOnUse' as const,
    orient: 'auto-start-reverse' as const,
  }
  return (
    <>
      <marker id="lg-arrow" {...common}>
        <path d="M0,0 L10,5 L0,10 z" fill={MUTED} />
      </marker>
      <marker id="lg-arrow-soft" {...common}>
        <path d="M0,0 L10,5 L0,10 z" fill={EDGE} />
      </marker>
    </>
  )
}

// TerminalNode draws a journey endpoint (a template-less node: done/exit). It reads
// as an outcome, not a step: 'done' is a calm completion (teal disc + ✓ + "completed
// N"); 'exit' is a muted drop-off (neutral disc + dashed ring + "left N"). The
// inflow count is the number of users that ended here — a completion/drop-off, never
// summed into the run's request total.
function TerminalNode({
  id,
  x,
  y,
  inflow,
  t,
}: {
  id: string
  x: number
  y: number
  inflow?: { requests: number; errors: number }
  t: (key: string, vars?: Record<string, string | number>) => string
}) {
  const role = terminalRole(id)
  const accent = role === 'dropoff' ? EXIT_COLOR : DONE_COLOR
  const fill = role === 'dropoff' ? EXIT_FILL : DONE_FILL
  const count = inflow?.requests ?? 0
  const errors = inflow?.errors ?? 0
  const lit = count > 0
  // The outcome line: "completed N" / "left N" (localized), in the accent color so
  // it visually ties to the node and reads as a result rather than a request count.
  const label =
    role === 'dropoff'
      ? `${t('graph.left')} ${formatCount(count)}`
      : `${t('graph.completed')} ${formatCount(count)}`
  return (
    <g>
      {/* A soft outcome halo when users have arrived, sized gently (not by volume)
          so endpoints stay calm even when most traffic ends here. */}
      {lit && (
        <circle cx={x} cy={y} r={NODE_R + 6} fill="none" stroke={accent} strokeWidth={2.5} opacity={0.5} />
      )}
      <circle
        cx={x}
        cy={y}
        r={NODE_R}
        fill={fill}
        stroke={accent}
        strokeWidth={lit ? 2.25 : 1.5}
        strokeDasharray={role === 'dropoff' ? '4 4' : undefined}
        opacity={lit ? 1 : 0.75}
      />
      {/* A ✓ marks completion; the drop-off leans on its dashed ring + muted tone. */}
      {role === 'completion' && (
        <text
          x={x}
          y={y - 6}
          textAnchor="middle"
          fontSize={15}
          fontWeight={700}
          fill={accent}
          aria-hidden="true"
        >
          ✓
        </text>
      )}
      <text
        x={x}
        y={role === 'completion' ? y + 13 : y + 4}
        textAnchor="middle"
        fontSize={role === 'completion' ? 11 : 13}
        fontFamily="ui-monospace, monospace"
        fill={lit ? TEXT : MUTED}
      >
        {id}
      </text>
      {lit && (
        <text x={x} y={y + NODE_R + 16} textAnchor="middle" fontSize={11} fill={accent} style={labelHalo}>
          {label}
          {errors > 0 && (
            <tspan fill={ERR_COLOR}> · {formatCount(errors)} {t('graph.err')}</tspan>
          )}
        </text>
      )}
    </g>
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

// terminalRole classifies a terminal node as a 'completion' or a 'dropoff' for
// styling and copy. 'exit' is the drop-off (a user left); 'done' — and any other
// template-less endpoint — reads as a completion (a user finished), so an unnamed
// terminal defaults to the positive outcome rather than looking like a leak.
type TerminalRole = 'completion' | 'dropoff'
function terminalRole(id: string): TerminalRole {
  return id === 'exit' ? 'dropoff' : 'completion'
}

// labelOffset returns a small perpendicular nudge for an edge's count label so it
// sits just off the stroke instead of on top of it. The line's unit normal is
// rotated 90° from its direction; the offset distance grows with the stroke width
// (a thick HEAT_MAX_W edge needs more clearance) within a sensible range. It biases
// to whichever side points "up" so labels don't dive under the edge.
function labelOffset(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  width: number,
): { x: number; y: number } {
  const dx = x2 - x1
  const dy = y2 - y1
  const len = Math.hypot(dx, dy) || 1
  // Perpendicular unit vector; flip so it always has a negative y (points up).
  let nx = -dy / len
  let ny = dx / len
  if (ny > 0) {
    nx = -nx
    ny = -ny
  }
  const dist = 8 + width / 2
  return { x: nx * dist, y: ny * dist }
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
