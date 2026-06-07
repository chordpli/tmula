import type React from 'react'
import { useEffect, useRef, useState } from 'react'
import {
  buildRunSpec,
  compareURL,
  createExperiment,
  getReport,
  killRun,
  MAX_TRACE_USERS,
  reportHTMLURL,
  runDisabled,
  shareTokenFromQuery,
  startRun,
  streamURL,
  traceable,
  type ExperimentForm,
  type Report,
  type Stats,
} from './api'
import LatencyHeatmap from './LatencyHeatmap'
import LiveGraph from './LiveGraph'
import ReportView, { StatsView } from './ReportView'
import Viewer from './Viewer'

// The default scenario is a small branching shop journey: a shopper browses, may
// search or jump to a category, lands on a product, and a fraction add to cart and
// check out (the cart -> checkout edge is a dependency). The exit edges drain the
// rest so traffic spreads realistically across the graph the instant a run starts.
const defaultGraph = JSON.stringify(
  {
    id: 'shop',
    nodes: [
      { id: 'browse', apiTemplateId: 't_browse' },
      { id: 'search', apiTemplateId: 't_search' },
      { id: 'category', apiTemplateId: 't_category' },
      { id: 'product', apiTemplateId: 't_product' },
      { id: 'cart', apiTemplateId: 't_cart' },
      { id: 'checkout', apiTemplateId: 't_checkout' },
      { id: 'done' },
      { id: 'exit' },
    ],
    edges: [
      { from: 'browse', to: 'search', weight: 0.4 },
      { from: 'browse', to: 'category', weight: 0.4 },
      { from: 'browse', to: 'exit', weight: 0.2 },
      { from: 'search', to: 'product', weight: 0.65 },
      { from: 'search', to: 'category', weight: 0.15 },
      { from: 'search', to: 'exit', weight: 0.2 },
      { from: 'category', to: 'product', weight: 0.7 },
      { from: 'category', to: 'browse', weight: 0.15 },
      { from: 'category', to: 'exit', weight: 0.15 },
      { from: 'product', to: 'cart', weight: 0.45 },
      { from: 'product', to: 'browse', weight: 0.25 },
      { from: 'product', to: 'exit', weight: 0.3 },
      { from: 'cart', to: 'checkout', weight: 0.6, dependency: true },
      { from: 'cart', to: 'exit', weight: 0.4 },
      { from: 'checkout', to: 'done', weight: 1.0 },
    ],
  },
  null,
  2,
)

const defaultTemplates = JSON.stringify(
  {
    t_browse: { method: 'GET', path: '/browse' },
    t_search: { method: 'GET', path: '/search' },
    t_category: { method: 'GET', path: '/category' },
    t_product: { method: 'GET', path: '/product' },
    t_cart: { method: 'POST', path: '/cart', payloadTemplate: '{"productId":"p7","qty":1}' },
    t_checkout: { method: 'POST', path: '/checkout', payloadTemplate: '{"total":42}' },
  },
  null,
  2,
)

// Defaults are tuned so a non-developer sees traffic the instant they click Run:
// an open (organic) model that ramps real arrivals over 30s, tracing on, against a
// local target with a friendly allowlist and the branching shop scenario above.
const initialForm: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost, 127.0.0.1',
  users: 20,
  maxSteps: 12,
  start: 'browse',
  graphJSON: defaultGraph,
  templatesJSON: defaultTemplates,
  workers: '',
  aggregateWorkers: false,
  workloadKind: 'open',
  arrivalRate: 12,
  durationSeconds: 30,
  maxConcurrency: 80,
  thinkMinMs: 300,
  thinkMaxMs: 900,
  segmentsJSON: '',
  traceEnabled: true,
}

// segmentsPlaceholder shows the persona-mix shape without prefilling it, so an
// open run stays homogeneous until the operator opts in.
const segmentsPlaceholder = `[
  { "name": "browser", "weight": 0.7, "start": "browse" },
  { "name": "buyer", "weight": 0.3, "start": "cart", "thinkTime": { "minMs": 200, "maxMs": 800 } }
]`

// App routes to the read-only viewer when a ?share=<token> link is opened,
// otherwise it shows the operator console.
export default function App() {
  const token = shareTokenFromQuery(window.location.search)
  return token ? <Viewer token={token} /> : <Operator />
}

function Operator() {
  const [form, setForm] = useState<ExperimentForm>(initialForm)
  const [runId, setRunId] = useState<string>('')
  const [runMode, setRunMode] = useState<string>('')
  const [status, setStatus] = useState<string>('')
  const [stats, setStats] = useState<Stats | null>(null)
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState<string>('')
  // history is the ids of completed runs, in order, so a finished run can be
  // compared against the one before it.
  const [history, setHistory] = useState<string[]>([])
  const esRef = useRef<EventSource | null>(null)
  const doneRef = useRef(false)

  useEffect(() => () => esRef.current?.close(), [])

  function set<K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  async function run() {
    setError('')
    setReport(null)
    setStats(null)
    setStatus('starting')
    try {
      const spec = buildRunSpec(form)
      const workerCount = spec.workers?.length ?? 0
      setRunMode(workerCount > 0 ? `distributed (${workerCount} worker${workerCount === 1 ? '' : 's'})` : 'local')
      const expId = await createExperiment(spec)
      const id = await startRun(expId)
      setRunId(id)
      listen(id)
    } catch (e) {
      setStatus('')
      setError(String(e instanceof Error ? e.message : e))
    }
  }

  function listen(id: string) {
    esRef.current?.close()
    doneRef.current = false
    const es = new EventSource(streamURL(id))
    esRef.current = es
    es.onmessage = (ev) => {
      try {
        const frame = JSON.parse(ev.data) as { status?: string; stats?: Stats }
        if (frame.status) setStatus(frame.status)
        if (frame.stats) setStats(frame.stats)
        if (frame.status && frame.status !== 'running' && frame.status !== 'pending') {
          doneRef.current = true
          es.close()
          setHistory((h) => (h.includes(id) ? h : [...h, id]))
          getReport(id).then(setReport).catch((e) => setError(String(e)))
        }
      } catch {
        /* ignore malformed frame */
      }
    }
    es.onerror = () => {
      es.close()
      // The server closes the stream on completion too; only treat it as an
      // error if the run had not already reached a terminal state.
      if (!doneRef.current) {
        setStatus('')
        setError('Connection lost while streaming progress.')
      }
    }
  }

  const prevRunId = report ? previousRunId(history, report.run.id) : undefined

  // Live traffic is now honored at any scale. The run size only picks the render
  // mode: small runs animate each request ('events'); large runs draw an aggregate
  // per-edge flow map that stays cheap no matter how many requests flow.
  const traceOn = form.traceEnabled
  const liveMode: 'events' | 'flow' = traceable(form) ? 'events' : 'flow'
  // Parse the scenario graph for the live view, reusing the same guarded pattern
  // as buildRunSpec: if it does not parse, just skip the visualization.
  const parsedGraph = traceOn ? safeParseGraph(form.graphJSON) : null

  const openModel = form.workloadKind === 'open'
  const hasWorkers = form.workers.trim().length > 0
  const isRunning = status === 'running'
  const sizeUnit = openModel ? 'max concurrency' : 'users'
  const liveCopy =
    liveMode === 'events'
      ? `animating each request (≤${MAX_TRACE_USERS} ${sizeUnit})`
      : `aggregate flow map (>${MAX_TRACE_USERS} ${sizeUnit})`

  return (
    <main className="app">
      <header className="masthead">
        <span className="brand">
          <span className="brand__mark" aria-hidden="true">
            <BrandGlyph />
          </span>
          <span>
            <h1 className="brand__name">tmula</h1>
            <p className="brand__tag">Real-user traffic simulator</p>
          </span>
        </span>
        <span className="masthead__spacer" />
        {status && (
          <span className="masthead__status">
            <StatusPill status={status} />
          </span>
        )}
      </header>

      <div className="stack">
        {/* ---- Target ---- */}
        <section className="card" aria-labelledby="card-target">
          <div className="card__head">
            <span className="card__step" aria-hidden="true">1</span>
            <h2 className="card__title" id="card-target">Target</h2>
          </div>
          <p className="card__hint">
            Where the simulated traffic goes, and the hosts it is allowed to reach. Add worker
            addresses to fan the load out across machines.
          </p>
          <div className="stack" style={{ gap: 16 }}>
            <Field label="Base URL" help="The service under test, e.g. your staging or local server.">
              <input
                className="input"
                value={form.baseUrl}
                onChange={(e) => set('baseUrl', e.target.value)}
                placeholder="http://localhost:9000"
              />
            </Field>
            <Field
              label="Allowlist"
              help="Comma-separated hosts traffic may hit — a guardrail so a run can never escape your target."
            >
              <input
                className="input"
                value={form.allowlist}
                onChange={(e) => set('allowlist', e.target.value)}
                placeholder="localhost, 127.0.0.1"
              />
            </Field>
            <Field
              label="Workers"
              help="Optional. Comma-separated worker addresses to distribute the load. Leave blank to run on this machine."
            >
              <input
                className="input"
                value={form.workers}
                onChange={(e) => set('workers', e.target.value)}
                placeholder="e.g. 127.0.0.1:9101, 127.0.0.1:9102"
              />
            </Field>
            {hasWorkers && (
              <Check
                checked={form.aggregateWorkers}
                onChange={(v) => set('aggregateWorkers', v)}
                label="Aggregate on workers (one summary per shard)"
                sub="Scales to millions of users — each worker summarizes its shard instead of streaming every request. Findings stay run-wide."
              />
            )}
          </div>
        </section>

        {/* ---- Load model ---- */}
        <section className="card" aria-labelledby="card-load">
          <div className="card__head">
            <span className="card__step" aria-hidden="true">2</span>
            <h2 className="card__title" id="card-load">Load model</h2>
          </div>
          <p className="card__hint">
            How users hit your service. <strong>Open</strong> mimics organic traffic — users arrive
            at a rate over time. <strong>Closed</strong> holds a fixed pool that loops.
          </p>
          <div className="stack" style={{ gap: 16 }}>
            <Field label="Workload" help="Open is the most realistic for a public-facing service.">
              <select
                className="select"
                value={form.workloadKind}
                onChange={(e) => set('workloadKind', e.target.value as 'closed' | 'open')}
              >
                <option value="open">Open — users arrive at a rate over time (organic)</option>
                <option value="closed">Closed — a fixed pool of virtual users that loop</option>
              </select>
            </Field>

            {openModel && (
              <>
                <hr className="divider" />
                <div className="field-row field-row--2">
                  <Field label="Arrival rate" help="New users per second.">
                    <div className="input-suffix">
                      <input
                        className="input"
                        type="number"
                        min={1}
                        value={form.arrivalRate}
                        onChange={(e) => set('arrivalRate', Math.max(1, Number(e.target.value) || 1))}
                      />
                      <span className="input-suffix__unit">/ sec</span>
                    </div>
                  </Field>
                  <Field label="Duration" help="How long users keep arriving.">
                    <div className="input-suffix">
                      <input
                        className="input"
                        type="number"
                        min={1}
                        value={form.durationSeconds}
                        onChange={(e) => set('durationSeconds', Math.max(1, Number(e.target.value) || 1))}
                      />
                      <span className="input-suffix__unit">sec</span>
                    </div>
                  </Field>
                  <Field label="Max concurrency" help="Back-pressure cap. 0 = uncapped.">
                    <input
                      className="input"
                      type="number"
                      min={0}
                      value={form.maxConcurrency}
                      onChange={(e) => set('maxConcurrency', Math.max(0, Number(e.target.value) || 0))}
                    />
                  </Field>
                  <Field label="Think time" help="Pause between a user's steps (ms, min–max).">
                    <div className="range-pair">
                      <input
                        className="input"
                        type="number"
                        min={0}
                        aria-label="Think time minimum (ms)"
                        value={form.thinkMinMs}
                        onChange={(e) => set('thinkMinMs', Math.max(0, Number(e.target.value) || 0))}
                      />
                      <span className="range-pair__dash" aria-hidden="true">–</span>
                      <input
                        className="input"
                        type="number"
                        min={0}
                        aria-label="Think time maximum (ms)"
                        value={form.thinkMaxMs}
                        onChange={(e) => set('thinkMaxMs', Math.max(0, Number(e.target.value) || 0))}
                      />
                    </div>
                  </Field>
                </div>
                <Field
                  label={
                    <>
                      Personas
                      <span className="field__badge">advanced</span>
                    </>
                  }
                  help="Optional JSON mix of weighted user types, each with its own entry node and pacing. Leave blank for one uniform population."
                >
                  <textarea
                    className="textarea"
                    value={form.segmentsJSON}
                    onChange={(e) => set('segmentsJSON', e.target.value)}
                    rows={6}
                    placeholder={segmentsPlaceholder}
                    spellCheck={false}
                  />
                </Field>
              </>
            )}
          </div>
        </section>

        {/* ---- Scenario ---- */}
        <section className="card" aria-labelledby="card-scenario">
          <div className="card__head">
            <span className="card__step" aria-hidden="true">3</span>
            <h2 className="card__title" id="card-scenario">Scenario</h2>
          </div>
          <p className="card__hint">
            The journey users take. Each run starts at the start node and walks the graph for up to
            the max steps; the JSON below defines the nodes, edges, and the API each node calls.
          </p>
          <div className="stack" style={{ gap: 16 }}>
            <div className="field-row">
              <Field label="Start node" help="Where every user begins.">
                <input className="input" value={form.start} onChange={(e) => set('start', e.target.value)} />
              </Field>
              <Field label="Max steps" help="Longest path a user may take before stopping.">
                <input
                  className="input"
                  type="number"
                  min={1}
                  value={form.maxSteps}
                  onChange={(e) => set('maxSteps', Math.max(1, Number(e.target.value) || 1))}
                />
              </Field>
              <Field label="Virtual users" help="Closed: the pool size. Open: a nominal upper bound.">
                <input
                  className="input"
                  type="number"
                  min={1}
                  value={form.users}
                  onChange={(e) => set('users', Math.max(1, Number(e.target.value) || 1))}
                />
              </Field>
            </div>

            <Check
              checked={traceOn}
              onChange={(v) => set('traceEnabled', v)}
              label="Show live traffic while the run streams"
              sub={
                traceOn
                  ? `Per-request animation for small runs, an aggregate flow map for large ones · ${liveCopy}`
                  : 'Per-request animation for small runs, an aggregate flow map for large ones'
              }
            />

            <hr className="divider" />

            <Field
              label={
                <>
                  Scenario graph
                  <span className="field__badge">JSON · advanced</span>
                </>
              }
              help="Nodes and weighted edges. A dependency edge must complete before its target runs."
            >
              <textarea
                className="textarea"
                value={form.graphJSON}
                onChange={(e) => set('graphJSON', e.target.value)}
                rows={12}
                spellCheck={false}
              />
            </Field>
            <Field
              label={
                <>
                  API templates
                  <span className="field__badge">JSON · advanced</span>
                </>
              }
              help="The request each node sends: method, path, and an optional payload template."
            >
              <textarea
                className="textarea"
                value={form.templatesJSON}
                onChange={(e) => set('templatesJSON', e.target.value)}
                rows={9}
                spellCheck={false}
              />
            </Field>
          </div>
        </section>

        {/* ---- Run ---- */}
        <section className="card">
          <div className="actionbar">
            <button
              className="btn btn--primary btn--lg"
              onClick={run}
              disabled={runDisabled(status)}
            >
              <PlayIcon />
              {runDisabled(status) ? 'Running…' : 'Run experiment'}
            </button>
            {runId && isRunning && (
              <button
                className="btn btn--danger btn--lg"
                onClick={() => killRun(runId).catch((e) => setError(String(e)))}
              >
                <StopIcon />
                Kill run
              </button>
            )}
            <span className="actionbar__note">
              {openModel ? (
                <>
                  ~<strong>{form.arrivalRate}</strong> users/sec for <strong>{form.durationSeconds}s</strong>
                </>
              ) : (
                <>
                  <strong>{form.users}</strong> virtual users · up to <strong>{form.maxSteps}</strong> steps
                </>
              )}
            </span>
          </div>
        </section>

        {error && (
          <div className="alert" role="alert">
            <span className="alert__icon" aria-hidden="true">
              <AlertIcon />
            </span>
            <span>{error}</span>
          </div>
        )}

        {/* ---- Live run ---- */}
        {status && (
          <section className="card" aria-live="polite">
            <div className="runhead">
              <h2 className="runhead__title">Run</h2>
              <span className="runhead__id">{runId || '—'}</span>
              <StatusPill status={status} />
              {runMode && <span className="runhead__mode">· {runMode}</span>}
            </div>

            {traceOn && parsedGraph && runId && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">Traffic flow</h3>
                  <span className="viz__sub">where requests travel across your scenario</span>
                </div>
                <LiveGraph
                  key={runId}
                  graph={parsedGraph}
                  start={form.start}
                  runId={runId}
                  active={isRunning}
                  mode={liveMode}
                />
              </div>
            )}

            {traceOn && runId && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">Latency heatmap</h3>
                  <span className="viz__sub">request density by latency band over time</span>
                </div>
                <LatencyHeatmap runId={runId} active={isRunning} />
              </div>
            )}

            {stats && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">Live metrics</h3>
                </div>
                <StatsView stats={stats} />
              </div>
            )}
          </section>
        )}

        {/* ---- Report ---- */}
        {report && (
          <section className="card">
            <div className="reportlinks">
              <a className="reportlink" href={reportHTMLURL(report.run.id)} target="_blank" rel="noreferrer">
                View full HTML report
                <ExternalIcon />
              </a>
              {prevRunId && (
                <a className="reportlink" href={compareURL(prevRunId, report.run.id)} target="_blank" rel="noreferrer">
                  Compare with previous run
                  <ExternalIcon />
                </a>
              )}
            </div>
            <ReportView report={report} />
          </section>
        )}
      </div>
    </main>
  )
}

// StatusPill renders the run status as a colored pill: running pulses, terminal
// states read at a glance (completed = green, killed/failed = red).
function StatusPill({ status }: { status: string }) {
  const kind = statusKind(status)
  return (
    <span className={`pill pill--${kind}`}>
      <span className="pill__dot" aria-hidden="true" />
      {status}
    </span>
  )
}

// statusKind maps a run status onto a pill variant.
function statusKind(status: string): 'running' | 'ok' | 'danger' | 'warn' | 'idle' {
  if (status === 'running' || status === 'pending' || status === 'starting') return 'running'
  if (status === 'completed' || status === 'done' || status === 'succeeded') return 'ok'
  if (status === 'killed' || status === 'failed' || status === 'error') return 'danger'
  if (!status) return 'idle'
  return 'warn'
}

// Field is a labeled form control with optional helper text. The label is a real
// <label> wrapping the control so the association is automatic and accessible.
function Field({
  label,
  help,
  children,
}: {
  label: React.ReactNode
  help?: string
  children: React.ReactNode
}) {
  return (
    <label className="field">
      <span className="field__label">{label}</span>
      {children}
      {help && <span className="field__help">{help}</span>}
    </label>
  )
}

// Check is a labeled checkbox card: the whole row is the clickable <label>.
function Check({
  checked,
  onChange,
  label,
  sub,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
  sub?: string
}) {
  return (
    <label className={`check${checked ? ' check--on' : ''}`}>
      <input
        className="check__box"
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="check__body">
        <span className="check__label">{label}</span>
        {sub && <span className="check__sub">{sub}</span>}
      </span>
    </label>
  )
}

// previousRunId returns the completed run before id in history, if any, so a
// finished run can be compared against the one immediately preceding it.
function previousRunId(history: string[], id: string): string | undefined {
  const idx = history.indexOf(id)
  return idx > 0 ? history[idx - 1] : undefined
}

interface ParsedGraph {
  nodes: { id: string; apiTemplateId?: string }[]
  edges: { from: string; to: string; weight?: number; dependency?: boolean }[]
}

// safeParseGraph parses the scenario-graph JSON for the live view, returning null
// on invalid JSON or a missing nodes/edges array — same guarded approach as
// buildRunSpec, but non-throwing so a bad graph simply hides the visualization.
function safeParseGraph(json: string): ParsedGraph | null {
  try {
    const g = JSON.parse(json) as Partial<ParsedGraph>
    if (!Array.isArray(g.nodes) || !Array.isArray(g.edges)) return null
    return { nodes: g.nodes, edges: g.edges }
  } catch {
    return null
  }
}

// --- Inline icons (no asset / dependency). ------------------------------------

function BrandGlyph() {
  // Three rising signal bars — a compact "traffic" mark.
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect x="3" y="13" width="4.5" height="8" rx="1.4" fill="currentColor" opacity="0.7" />
      <rect x="9.75" y="8" width="4.5" height="13" rx="1.4" fill="currentColor" opacity="0.85" />
      <rect x="16.5" y="3" width="4.5" height="18" rx="1.4" fill="currentColor" />
    </svg>
  )
}

function PlayIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M7 5.5v13l11-6.5-11-6.5z" fill="currentColor" />
    </svg>
  )
}

function StopIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor" />
    </svg>
  )
}

function ExternalIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M14 4h6v6M20 4l-9 9M18 13v6a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h6"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function AlertIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2" />
      <path d="M12 7.5v5.5M12 16.2v.3" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  )
}
