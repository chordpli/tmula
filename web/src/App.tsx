import type React from 'react'
import { useEffect, useRef, useState } from 'react'
import {
  addBaseUrlHostToAllowlist,
  allowlistMatchesHost,
  buildRunSpec,
  compareURL,
  createExperiment,
  getReport,
  hostFromBaseUrl,
  importScenario,
  killRun,
  MAX_TRACE_USERS,
  parseAllowlist,
  reportHTMLURL,
  runDisabled,
  shareTokenFromQuery,
  startRun,
  streamURL,
  traceable,
  type ExperimentForm,
  type OutcomeSummary,
  type Report,
  type Stats,
} from './api'
import GraphEditor from './GraphEditor'
import { parseEditableGraph } from './graphEditorModel'
import HelpTip from './HelpTip'
import { LANGS, useI18n } from './i18n'
import LatencyHeatmap from './LatencyHeatmap'
import LiveGraph from './LiveGraph'
import { presets, type Preset } from './presets'
import ReportView, { OutcomeView, StatsView } from './ReportView'
import { doctorForm, type DoctorIssue } from './scenarioDoctor'
import Viewer from './Viewer'

// stringify renders a preset's graph/templates the same way the Scenario card's
// textareas hold them — pretty-printed JSON — so applying a preset (or an import)
// fills the fields with text the operator can read and tweak.
function stringify(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

// The default scenario is the branching shop preset, kept in presets.ts as the
// single source of truth so "Start from a template" and the initial form can never
// drift apart. It is a small branching shop journey: a shopper browses, may search
// or jump to a category, lands on a product, and a fraction add to cart and check
// out (the cart -> checkout edge is a dependency). The exit edges drain the rest so
// traffic spreads realistically across the graph the instant a run starts.
const shopPreset = presets.find((p) => p.id === 'shop')!
const defaultGraph = stringify(shopPreset.graph)
const defaultTemplates = stringify(shopPreset.templates)

// Defaults are tuned so a non-developer sees traffic the instant they click Run:
// an open (organic) model that ramps real arrivals over 30s, tracing on, against a
// local target with a friendly allowlist and the branching shop scenario above.
const initialForm: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost, 127.0.0.1',
  users: 20,
  maxSteps: shopPreset.maxSteps,
  deviationPct: 0,
  start: shopPreset.start,
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
  const { t } = useI18n()
  const [form, setForm] = useState<ExperimentForm>(initialForm)
  const [runId, setRunId] = useState<string>('')
  const [runMode, setRunMode] = useState<string>('')
  const [status, setStatus] = useState<string>('')
  const [stats, setStats] = useState<Stats | null>(null)
  // outcome is the journey-outcome headline (completion/drop-off rates) the live
  // graph streams up while a traced run flows; it outlives the stream so the
  // report can show it after the run completes.
  const [outcome, setOutcome] = useState<OutcomeSummary | null>(null)
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState<string>('')
  // loadedPresetKey is the nameKey of the template just applied from a chip, kept
  // (instead of a rendered string) so the "Loaded template" confirmation re-renders
  // in the active language when the operator switches EN/한국어.
  const [loadedPresetKey, setLoadedPresetKey] = useState<string>('')
  // history is the ids of completed runs, in order, so a finished run can be
  // compared against the one before it.
  const [history, setHistory] = useState<string[]>([])
  const esRef = useRef<EventSource | null>(null)
  const doneRef = useRef(false)

  useEffect(() => () => esRef.current?.close(), [])

  function set<K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  function setBaseUrl(value: string) {
    set('baseUrl', value)
  }

  function syncBaseHostToAllowlist() {
    setForm((f) => ({
      ...f,
      allowlist: addBaseUrlHostToAllowlist(f.baseUrl, f.allowlist),
    }))
  }

  function addBaseHostToAllowlist() {
    setForm((f) => ({
      ...f,
      allowlist: addBaseUrlHostToAllowlist(f.baseUrl, f.allowlist),
    }))
  }

  // applyScenario fills the scenario fields from a loaded template or import,
  // pretty-printing the graph/templates and carrying over start/maxSteps (and an
  // optional baseUrl). It is the one place both presets and imports converge so
  // the fields are written consistently.
  function applyScenario(s: {
    graph: unknown
    templates: unknown
    start: string
    maxSteps: number
    baseUrl?: string
  }) {
    setForm((f) => ({
      ...f,
      graphJSON: stringify(s.graph),
      templatesJSON: stringify(s.templates),
      start: s.start,
      maxSteps: s.maxSteps,
      ...(s.baseUrl
        ? { baseUrl: s.baseUrl, allowlist: addBaseUrlHostToAllowlist(s.baseUrl, f.allowlist) }
        : {}),
    }))
  }

  function applyPreset(p: Preset) {
    applyScenario(p)
    setLoadedPresetKey(p.nameKey)
  }

  async function run() {
    setError('')
    setReport(null)
    setStats(null)
    setOutcome(null)
    setStatus('starting')
    try {
      const blocking = doctorForm(form).find((i) => i.severity === 'error')
      if (blocking) {
        setStatus('')
        setError(t(blocking.messageKey, blocking.vars))
        return
      }
      const host = hostFromBaseUrl(form.baseUrl)
      if (host && !allowlistMatchesHost(parseAllowlist(form.allowlist), host)) {
        setStatus('')
        setError(t('run.allowlistBlocked', { host }))
        return
      }
      const spec = buildRunSpec(form)
      const workerCount = spec.workers?.length ?? 0
      setRunMode(
        workerCount > 0
          ? t('mode.distributed', { count: workerCount, plural: workerCount === 1 ? '' : 's' })
          : t('mode.local'),
      )
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
        setError(t('run.connLost'))
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
  const baseHost = hostFromBaseUrl(form.baseUrl)
  const allowlistCoversBase = !baseHost || allowlistMatchesHost(parseAllowlist(form.allowlist), baseHost)
  const doctorIssues = doctorForm(form)
  const sizeUnit = openModel ? t('unit.maxConcurrency') : t('unit.users')
  const liveCopy =
    liveMode === 'events'
      ? t('live.events', { max: MAX_TRACE_USERS, unit: sizeUnit })
      : t('live.flow', { max: MAX_TRACE_USERS, unit: sizeUnit })

  return (
    <main className="app">
      <header className="masthead">
        <span className="brand">
          <span className="brand__mark" aria-hidden="true">
            <BrandGlyph />
          </span>
          <span>
            <h1 className="brand__name">tmula</h1>
            <p className="brand__tag">{t('brand.tagline')}</p>
          </span>
        </span>
        <span className="masthead__spacer" />
        {status && (
          <span className="masthead__status">
            <StatusPill status={status} />
          </span>
        )}
        <LangToggle />
      </header>

      <div className="stack">
        {/* ---- Target ---- */}
        <section className="card" aria-labelledby="card-target">
          <div className="card__head">
            <span className="card__step" aria-hidden="true">1</span>
            <h2 className="card__title" id="card-target">{t('card.target')}</h2>
          </div>
          <p className="card__hint">{t('card.target.hint')}</p>
          <div className="stack" style={{ gap: 16 }}>
            <Field label={t('field.baseUrl')} help={t('help.baseUrl')}>
              <input
                className="input"
                value={form.baseUrl}
                onChange={(e) => setBaseUrl(e.target.value)}
                onBlur={syncBaseHostToAllowlist}
                placeholder="http://localhost:9000"
              />
            </Field>
            <Field
              label={t('field.allowlist')}
              help={t('help.allowlist')}
              tip={<HelpTip label={t('field.allowlist')} text={t('help.allowlist.tip')} />}
            >
              <input
                className="input"
                value={form.allowlist}
                onChange={(e) => set('allowlist', e.target.value)}
                placeholder="localhost, 127.0.0.1"
              />
              {!allowlistCoversBase && baseHost && (
                <div className="field-warning" role="status">
                  <span>{t('allowlist.missingHost', { host: baseHost })}</span>
                  <button className="field-warning__button" type="button" onClick={addBaseHostToAllowlist}>
                    {t('allowlist.addHost')}
                  </button>
                </div>
              )}
            </Field>
            <Field label={t('field.workers')} help={t('help.workers')}>
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
                label={t('check.aggregate')}
                sub={t('check.aggregate.sub')}
              />
            )}
          </div>
        </section>

        {/* ---- Load model ---- */}
        <section className="card" aria-labelledby="card-load">
          <div className="card__head">
            <span className="card__step" aria-hidden="true">2</span>
            <h2 className="card__title" id="card-load">{t('card.load')}</h2>
          </div>
          <p className="card__hint">
            {t('card.load.hintLead')} <strong>{t('card.load.hintOpen')}</strong>{' '}
            {t('card.load.hintOpenRest')} <strong>{t('card.load.hintClosed')}</strong>{' '}
            {t('card.load.hintClosedRest')}
          </p>
          <div className="stack" style={{ gap: 16 }}>
            <Field label={t('field.workload')} help={t('help.workload')}>
              <select
                className="select"
                value={form.workloadKind}
                onChange={(e) => set('workloadKind', e.target.value as 'closed' | 'open')}
              >
                <option value="open">{t('workload.open')}</option>
                <option value="closed">{t('workload.closed')}</option>
              </select>
            </Field>

            {openModel && (
              <>
                <hr className="divider" />
                <div className="field-row field-row--2">
                  <Field
                    label={t('field.arrivalRate')}
                    help={t('help.arrivalRate')}
                    tip={<HelpTip label={t('field.arrivalRate')} text={t('help.arrivalRate.tip')} />}
                  >
                    <div className="input-suffix">
                      <input
                        className="input"
                        type="number"
                        min={1}
                        value={form.arrivalRate}
                        onChange={(e) => set('arrivalRate', Math.max(1, Number(e.target.value) || 1))}
                      />
                      <span className="input-suffix__unit">{t('unit.perSec')}</span>
                    </div>
                  </Field>
                  <Field label={t('field.duration')} help={t('help.duration')}>
                    <div className="input-suffix">
                      <input
                        className="input"
                        type="number"
                        min={1}
                        value={form.durationSeconds}
                        onChange={(e) => set('durationSeconds', Math.max(1, Number(e.target.value) || 1))}
                      />
                      <span className="input-suffix__unit">{t('unit.sec')}</span>
                    </div>
                  </Field>
                  <Field
                    label={t('field.maxConcurrency')}
                    help={t('help.maxConcurrency')}
                    tip={
                      <HelpTip label={t('field.maxConcurrency')} text={t('help.maxConcurrency.tip')} />
                    }
                  >
                    <input
                      className="input"
                      type="number"
                      min={0}
                      value={form.maxConcurrency}
                      onChange={(e) => set('maxConcurrency', Math.max(0, Number(e.target.value) || 0))}
                    />
                  </Field>
                  <Field
                    label={t('field.thinkTime')}
                    help={t('help.thinkTime')}
                    tip={<HelpTip label={t('field.thinkTime')} text={t('help.thinkTime.tip')} />}
                  >
                    <div className="range-pair">
                      <input
                        className="input"
                        type="number"
                        min={0}
                        aria-label={t('aria.thinkMin')}
                        value={form.thinkMinMs}
                        onChange={(e) => set('thinkMinMs', Math.max(0, Number(e.target.value) || 0))}
                      />
                      <span className="range-pair__dash" aria-hidden="true">–</span>
                      <input
                        className="input"
                        type="number"
                        min={0}
                        aria-label={t('aria.thinkMax')}
                        value={form.thinkMaxMs}
                        onChange={(e) => set('thinkMaxMs', Math.max(0, Number(e.target.value) || 0))}
                      />
                    </div>
                  </Field>
                </div>
                <Field
                  label={
                    <>
                      {t('field.personas')}
                      <span className="field__badge">{t('badge.advanced')}</span>
                    </>
                  }
                  help={t('help.personas')}
                  tip={<HelpTip label={t('field.personas')} text={t('help.personas.tip')} />}
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
            <h2 className="card__title" id="card-scenario">{t('card.scenario')}</h2>
          </div>
          <p className="card__hint">{t('card.scenario.hint')}</p>
          <div className="stack" style={{ gap: 16 }}>
            {/* Presets (Feature A): one-click starting points. */}
            <PresetRow onPick={applyPreset} loadedKey={loadedPresetKey} />
            <ScenarioDoctorPanel issues={doctorIssues} />
            <GraphEditor
              graphJSON={form.graphJSON}
              templatesJSON={form.templatesJSON}
              start={form.start}
              onGraphJSONChange={(json) => set('graphJSON', json)}
              onTemplatesJSONChange={(json) => set('templatesJSON', json)}
              onStartChange={(next) => set('start', next)}
            />

            <div className="field-row">
              <Field label={t('field.start')} help={t('help.start')}>
                <StartNodeControl
                  graphJSON={form.graphJSON}
                  value={form.start}
                  onChange={(next) => set('start', next)}
                />
              </Field>
              <Field label={t('field.maxSteps')} help={t('help.maxSteps')}>
                <input
                  className="input"
                  type="number"
                  min={1}
                  value={form.maxSteps}
                  onChange={(e) => set('maxSteps', Math.max(1, Number(e.target.value) || 1))}
                />
              </Field>
              <Field
                label={t('field.deviation')}
                help={t('help.deviation')}
                tip={<HelpTip label={t('field.deviation')} text={t('help.deviation.tip')} />}
              >
                <div className="input-suffix">
                  <input
                    className="input"
                    type="number"
                    min={0}
                    max={100}
                    value={form.deviationPct}
                    onChange={(e) =>
                      set('deviationPct', Math.min(100, Math.max(0, Number(e.target.value) || 0)))
                    }
                  />
                  <span className="input-suffix__unit">{t('unit.percent')}</span>
                </div>
              </Field>
              {!openModel && (
                <Field label={t('field.users')} help={t('help.users')}>
                  <input
                    className="input"
                    type="number"
                    min={1}
                    value={form.users}
                    onChange={(e) => set('users', Math.max(1, Number(e.target.value) || 1))}
                  />
                </Field>
              )}
            </div>

            <Check
              checked={traceOn}
              onChange={(v) => set('traceEnabled', v)}
              label={t('check.trace')}
              sub={traceOn ? t('check.trace.subWith', { mode: liveCopy }) : t('check.trace.sub')}
            />

            <hr className="divider" />

            <details className="advanced">
              <summary className="advanced__summary">
                {t('advanced.json')}
                <span className="field__badge">{t('badge.jsonAdvanced')}</span>
              </summary>
              <div className="stack advanced__body" style={{ gap: 16 }}>
                <Field
                  label={t('field.graph')}
                  help={t('help.graph')}
                  tip={<HelpTip label={t('field.graph')} text={t('help.graph.tip')} />}
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
                  label={t('field.templates')}
                  help={t('help.templates')}
                  tip={<HelpTip label={t('field.templates')} text={t('help.templates.tip')} />}
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
            </details>

            <hr className="divider" />

            {/* Import (Feature B): build a scenario from a spec or recording. */}
            <ImportPanel onImported={applyScenario} />
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
              {runDisabled(status) ? t('run.running') : t('run.button')}
            </button>
            {runId && isRunning && (
              <button
                className="btn btn--danger btn--lg"
                onClick={() => killRun(runId).catch((e) => setError(String(e)))}
              >
                <StopIcon />
                {t('run.kill')}
              </button>
            )}
            <span className="actionbar__note">
              {openModel
                ? renderNote(t('run.noteOpen', { rate: form.arrivalRate, duration: form.durationSeconds }))
                : renderNote(t('run.noteClosed', { users: form.users, steps: form.maxSteps }))}
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
              <h2 className="runhead__title">{t('run.title')}</h2>
              <span className="runhead__id">{runId || '—'}</span>
              <StatusPill status={status} />
              {runMode && <span className="runhead__mode">· {runMode}</span>}
            </div>

            {traceOn && parsedGraph && runId && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">{t('viz.flow.title')}</h3>
                  <span className="viz__sub">{t('viz.flow.sub')}</span>
                </div>
                <LiveGraph
                  key={runId}
                  graph={parsedGraph}
                  start={form.start}
                  runId={runId}
                  active={isRunning}
                  mode={liveMode}
                  onOutcome={setOutcome}
                />
              </div>
            )}

            {traceOn && runId && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">{t('viz.latency.title')}</h3>
                  <span className="viz__sub">{t('viz.latency.sub')}</span>
                </div>
                <LatencyHeatmap runId={runId} active={isRunning} />
              </div>
            )}

            {stats && (
              <div className="viz">
                <div className="viz__head">
                  <h3 className="viz__title">{t('viz.metrics.title')}</h3>
                </div>
                <StatsView stats={stats} />
                {/* The journey-outcome headline accumulates from the live trace/flow
                    stream; it only shows once at least one journey has started. */}
                {outcome && outcome.started > 0 && <OutcomeView outcome={outcome} />}
              </div>
            )}
          </section>
        )}

        {/* ---- Report ---- */}
        {report && (
          <section className="card">
            <div className="reportlinks">
              <a className="reportlink" href={reportHTMLURL(report.run.id)} target="_blank" rel="noreferrer">
                {t('report.viewHtml')}
                <ExternalIcon />
              </a>
              {prevRunId && (
                <a className="reportlink" href={compareURL(prevRunId, report.run.id)} target="_blank" rel="noreferrer">
                  {t('report.compare')}
                  <ExternalIcon />
                </a>
              )}
            </div>
            <ReportView report={report} outcome={outcome} />
          </section>
        )}
      </div>
    </main>
  )
}

// PresetRow renders the "Start from a template" chips above the scenario fields and
// the brief "Loaded template" confirmation. Picking a chip fills the scenario via
// the parent's applyPreset. Each chip carries the preset's one-line description as
// its title so hovering explains what it loads. `loadedKey` is the nameKey of the
// last-applied preset (or empty); the note is rendered from it here so it follows
// the active language rather than freezing at the language it was clicked in.
function PresetRow({ onPick, loadedKey }: { onPick: (p: Preset) => void; loadedKey: string }) {
  const { t } = useI18n()
  return (
    <div className="presets">
      <div className="presets__head">
        <span className="presets__label">{t('presets.label')}</span>
        <span className="presets__hint">{t('presets.hint')}</span>
      </div>
      <div className="presets__row">
        {presets.map((p) => (
          <button
            key={p.id}
            type="button"
            className="chip"
            onClick={() => onPick(p)}
            title={t(p.descKey)}
          >
            <span className="chip__name">{t(p.nameKey)}</span>
            <span className="chip__desc">{t(p.descKey)}</span>
          </button>
        ))}
      </div>
      {loadedKey && (
        <p className="presets__note" role="status">
          <CheckMini />
          {t('presets.loaded', { name: t(loadedKey) })}
        </p>
      )}
    </div>
  )
}

function ScenarioDoctorPanel({ issues }: { issues: DoctorIssue[] }) {
  const { t } = useI18n()
  const errors = issues.filter((i) => i.severity === 'error').length
  const warnings = issues.filter((i) => i.severity === 'warning').length
  const level = errors > 0 ? 'error' : warnings > 0 ? 'warning' : 'ok'
  const visible = issues.slice(0, 8)
  const hidden = Math.max(0, issues.length - visible.length)
  // A clean bill of health should be quiet: one slim line instead of a panel, so
  // the doctor only claims space when it actually has something to say.
  if (issues.length === 0) {
    return (
      <p className="doctor-slim" role="status">
        <CheckMini />
        <span className="doctor-slim__title">{t('doctor.title')}</span>
        {t('doctor.clean')}
      </p>
    )
  }
  return (
    <div className={`doctor doctor--${level}`}>
      <div className="doctor__head">
        <span className="doctor__title">{t('doctor.title')}</span>
        <span className="doctor__summary">
          {issues.length === 0 ? t('doctor.clean') : t('doctor.summary', { errors, warnings })}
        </span>
      </div>
      {visible.length > 0 && (
        <ul className="doctor__list">
          {visible.map((item, i) => (
            <li key={`${item.code}-${i}`} className={`doctor__item doctor__item--${item.severity}`}>
              <span className="doctor__severity">{t(`doctor.severity.${item.severity}`)}</span>
              <span>{t(item.messageKey, item.vars)}</span>
            </li>
          ))}
          {hidden > 0 && <li className="doctor__more">{t('doctor.more', { count: hidden })}</li>}
        </ul>
      )}
    </div>
  )
}

// ImportPanel is the OpenAPI / HAR importer (Feature B). It accepts either an
// uploaded file (read as text client-side) or pasted text, plus a format selector
// (Auto / OpenAPI / HAR). On Import it calls the backend and, on success, fills the
// scenario fields via the parent's onImported. It is deliberately forgiving: every
// failure path catches the error and shows it inline, so nothing reaches the
// console and the operator always sees a readable message.
function ImportPanel({
  onImported,
}: {
  onImported: (s: { graph: unknown; templates: unknown; start: string; maxSteps: number }) => void
}) {
  const { t } = useI18n()
  const [text, setText] = useState('')
  const [fileName, setFileName] = useState('')
  const [format, setFormat] = useState<'auto' | 'openapi' | 'har' | 'accesslog'>('auto')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [err, setErr] = useState('')

  async function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    setErr('')
    setNote('')
    const file = e.target.files?.[0]
    if (!file) return
    setFileName(file.name)
    // Pick the format from the extension so the strongest signal (the filename)
    // isn't lost: a .har upload must parse as HAR, not be sniffed as OpenAPI.
    // .json stays on 'auto' since it can be either.
    const lower = file.name.toLowerCase()
    if (lower.endsWith('.har')) setFormat('har')
    else if (lower.endsWith('.log') || lower.endsWith('.jsonl')) setFormat('accesslog')
    else if (lower.endsWith('.yaml') || lower.endsWith('.yml')) setFormat('openapi')
    try {
      // Read the file client-side so the textarea reflects exactly what will be
      // imported; the operator can still edit it before importing.
      setText(await file.text())
    } catch {
      setErr(t('import.emptyError'))
    }
  }

  async function doImport() {
    const spec = text.trim()
    setErr('')
    setNote('')
    if (!spec) {
      setErr(t('import.emptyError'))
      return
    }
    setBusy(true)
    try {
      const result = await importScenario(spec, format)
      onImported(result)
      setNote(t('import.success'))
    } catch (e) {
      // Surface the server's message (400 {error}); map the unavailable importer
      // (501) onto a friendlier line. Never rethrow — keep it inline.
      const msg = e instanceof Error ? e.message : String(e)
      setErr(/501/.test(msg) ? t('import.unavailable') : msg || t('import.unavailable'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="import">
      <div className="import__head">
        <span className="import__title">{t('import.title')}</span>
        <span className="import__hint">{t('import.hint')}</span>
      </div>

      <div className="import__grid">
        <label className="field import__file">
          <span className="field__label">{t('import.file')}</span>
          <input
            className="filepick"
            type="file"
            accept=".json,.yaml,.yml,.har,.log,.jsonl"
            onChange={onFile}
          />
          <span className="field__help">{fileName || t('import.fileHint')}</span>
        </label>

        <Field label={t('import.format')}>
          <select
            className="select"
            value={format}
            onChange={(e) => setFormat(e.target.value as 'auto' | 'openapi' | 'har' | 'accesslog')}
          >
            <option value="auto">{t('import.format.auto')}</option>
            <option value="openapi">{t('import.format.openapi')}</option>
            <option value="har">{t('import.format.har')}</option>
            <option value="accesslog">{t('import.format.accesslog')}</option>
          </select>
        </Field>
      </div>

      <Field label={t('import.paste')}>
        <textarea
          className="textarea"
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            // Editing the pasted text means it is no longer tied to the picked file.
            if (fileName) setFileName('')
          }}
          rows={5}
          placeholder={t('import.pastePlaceholder')}
          spellCheck={false}
        />
      </Field>

      <div className="import__actions">
        <button type="button" className="btn btn--ghost" onClick={doImport} disabled={busy}>
          <ImportIcon />
          {busy ? t('import.importing') : t('import.button')}
        </button>
        {note && (
          <span className="import__ok" role="status">
            <CheckMini />
            {note}
          </span>
        )}
      </div>

      {err && (
        <div className="import__err" role="alert">
          <AlertIcon />
          <span>{err}</span>
        </div>
      )}
    </div>
  )
}

// LangToggle is the header language switch (EN / 한국어): a small segmented control
// bound to the i18n context. The active language is marked with aria-pressed so the
// current choice is announced.
function LangToggle() {
  const { lang, setLang, t } = useI18n()
  return (
    <div className="langtoggle" role="group" aria-label={t('lang.label')}>
      {LANGS.map((l) => (
        <button
          key={l.code}
          type="button"
          className={`langtoggle__btn${lang === l.code ? ' langtoggle__btn--on' : ''}`}
          aria-pressed={lang === l.code}
          onClick={() => setLang(l.code)}
        >
          {l.label}
        </button>
      ))}
    </div>
  )
}

// renderNote turns an interpolated note string into React, bolding the {strong}
// segments the source marks with **…** — so a translated note like
// "약 초당 **12**명씩 **30초** 동안" keeps the numbers emphasized in any language.
// We use **…** in the dictionary instead of literal <strong> so the i18n strings
// stay plain text (no embedded markup to mistranslate).
function renderNote(text: string): React.ReactNode {
  const parts = text.split(/\*\*(.+?)\*\*/g)
  return parts.map((part, i) =>
    i % 2 === 1 ? <strong key={i}>{part}</strong> : <span key={i}>{part}</span>,
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

// Field is a labeled form control with optional helper text and an optional inline
// help affordance (a HelpTip badge rendered next to the label). The label is a real
// <label> wrapping the control so the association is automatic and accessible; when
// a tip is present the label and badge share a row.
function Field({
  label,
  help,
  tip,
  children,
}: {
  label: React.ReactNode
  help?: string
  tip?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <label className="field">
      <span className="field__labelrow">
        <span className="field__label">{label}</span>
        {tip}
      </span>
      {children}
      {help && <span className="field__help">{help}</span>}
    </label>
  )
}

// StartNodeControl picks the start node from the graph's actual nodes, so a typo
// can never point a run at a node that does not exist. When the graph JSON is
// invalid (no node list to offer) it degrades to the old free-text input.
function StartNodeControl({
  graphJSON,
  value,
  onChange,
}: {
  graphJSON: string
  value: string
  onChange: (next: string) => void
}) {
  const graph = parseEditableGraph(graphJSON)
  const nodeIDs = graph?.nodes.map((n) => n.id).filter(Boolean) ?? []
  if (nodeIDs.length === 0) {
    return <input className="input" value={value} onChange={(e) => onChange(e.target.value)} />
  }
  return (
    <select className="select" value={value} onChange={(e) => onChange(e.target.value)}>
      {value && !nodeIDs.includes(value) && <option value={value}>{value}</option>}
      {nodeIDs.map((id) => (
        <option key={id} value={id}>
          {id}
        </option>
      ))}
    </select>
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

function ImportIcon() {
  // A downward tray — "bring a spec in".
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M12 3v10m0 0l-4-4m4 4l4-4M5 17v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function CheckMini() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M5 12.5l4 4 10-10"
        stroke="currentColor"
        strokeWidth="2.4"
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
