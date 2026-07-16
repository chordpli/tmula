import type React from 'react'
import { useEffect, useRef, useState } from 'react'
import {
  addBaseUrlHostToAllowlist,
  allowlistMatchesHost,
  AUTH_FORM_DEFAULTS,
  authFormFromImport,
  authFormFromOAuth2Guide,
  buildRunSpec,
  compareURL,
  createExperiment,
  discoverIssuer,
  findReplaceMePlaceholders,
  formFromRunSpec,
  generateCredentialRows,
  getExperimentSpec,
  getReport,
  hostFromBaseUrl,
  importScenario,
  killRun,
  MAX_TRACE_USERS,
  MAX_WEB_PATTERN_ROWS,
  localizeError,
  mintManagedIdPAdvisory,
  oauth2GuideCanCompileOver,
  openIdConnectDiscoveryUrl,
  parseAllowlist,
  parseCredentials,
  parseLoginCredentials,
  placeholderLabel,
  preflightAuth,
  probeRun,
  reportHTMLURL,
  runDisabled,
  runFailureHintKey,
  runIdFromQuery,
  shareTokenFromQuery,
  startRun,
  streamURL,
  traceable,
  type AuthAdvisory,
  type AuthMode,
  type CredFormat,
  type ExperimentForm,
  type ImportResult,
  type ImportStats,
  type LoginCredFormat,
  type LoginScope,
  type MintAlg,
  type MintEncoding,
  type OAuth2Grant,
  type OAuth2GuideForm,
  type OutcomeSummary,
  type PreflightResult,
  type Report,
  type Stats,
  type StreamFrame,
} from './api'
import {
  ADVANCED_AUTH_ENTRIES,
  advancedFoldOpen,
  entryPatch,
  PRIMARY_AUTH_ENTRIES,
  selectedEntry,
  type AuthEntry,
  type AuthEntryOption,
} from './authEntryModel'
import GraphEditor from './GraphEditor'
import { parseEditableGraph } from './graphEditorModel'
import HelpTip from './HelpTip'
import { LANGS, useI18n } from './i18n'
import { coverageFromStats, type CoverageReport } from './importCoverageModel'
import LatencyHeatmap from './LatencyHeatmap'
import LiveGraph from './LiveGraph'
import { presets, type Preset } from './presets'
import ReportView, { OutcomeView, StatsView } from './ReportView'
import { authDoctorIssues, doctorForm, runBlockers, type DoctorIssue } from './scenarioDoctor'
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
  ...AUTH_FORM_DEFAULTS,
}

// loginFlowPlaceholders show the shape of a login flow (a single POST that captures a
// token) without prefilling it, so the field reads as guidance until the operator
// authors their own. The graph + templates are authored exactly like the scenario.
const loginGraphPlaceholder = `{
  "id": "login",
  "nodes": [{ "id": "login", "apiTemplateId": "t_login" }],
  "edges": []
}`
const loginTemplatesPlaceholder = `{
  "t_login": {
    "method": "POST",
    "path": "/login",
    "payloadTemplate": "{\\"user\\":\\"alice\\",\\"pass\\":\\"secret\\"}",
    "extract": { "access_token": "$.access_token" }
  }
}`

// LOGIN_BODY_SINGLE is the single-identity default login body — the SAME default
// AUTH_FORM_DEFAULTS.loginBodyTemplate ships, so we can detect when the operator has not
// edited it yet. LOGIN_BODY_MULTI is the smart default the multi-user path suggests:
// each virtual user logs in as the NEXT credential-list row, so the body reads the row
// via the {{.username}}/{{.password}} Go-template markers the backend exposes (NOT the
// {username}/{password} markers the single-identity body uses).
const LOGIN_BODY_SINGLE = '{"username": "{username}", "password": "{password}"}'
const LOGIN_BODY_MULTI = '{"username": "{{.username}}", "password": "{{.password}}"}'

// signupStepsPlaceholder shows the bootstrap signup journey shape: a step list with a
// bare method/path and an extract that captures the token. teardownPlaceholder shows
// the matching deprovision step. Both are guidance only until the operator authors
// their own.
const signupStepsPlaceholder = `[
  {
    "id": "signup",
    "method": "POST",
    "path": "/signup",
    "body": "{\\"email\\":\\"test+{{.userIndex}}@example.com\\"}",
    "extract": { "accessToken": "$.token", "id": "$.id" }
  }
]`
const signupTeardownPlaceholder = `[
  { "id": "delete", "method": "DELETE", "path": "/accounts/{{.subject}}" }
]`

// segmentsPlaceholder shows the persona-mix shape without prefilling it, so an
// open run stays homogeneous until the operator opts in.
const segmentsPlaceholder = `[
  { "name": "browser", "weight": 0.7, "start": "browse" },
  { "name": "buyer", "weight": 0.3, "start": "cart", "thinkTime": { "minMs": 200, "maxMs": 800 } }
]`

// App routes to the read-only viewer when a ?share=<token> link is opened,
// otherwise it shows the operator console. A ?run=<run-id> link (the attach
// contract `tmula demo` opens) is handled inside Operator; share wins when both
// are somehow present, since a share link is explicitly read-only.
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
  // runReason is the terminal frame's failure reason (SSE `reason`, mirroring the
  // report's run.killReason): WHY a run died, e.g. "api: prewarm login token …".
  // Kept as the raw backend string — the friendly line above it is derived via
  // runFailureHintKey at render time so it follows the active language.
  const [runReason, setRunReason] = useState<string>('')
  const [stats, setStats] = useState<Stats | null>(null)
  // outcome is the journey-outcome headline (completion/drop-off rates) the live
  // graph streams up while a traced run flows; it outlives the stream so the
  // report can show it after the run completes.
  const [outcome, setOutcome] = useState<OutcomeSummary | null>(null)
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState<string>('')
  // showBlockers turns the live run-blockers alert on after a blocked Run click.
  // ONLY the visibility bit is state — the blockers themselves are re-derived
  // from the current doctor issues on every render, so fixing a condition makes
  // its line disappear and a locale switch re-translates the text.
  const [showBlockers, setShowBlockers] = useState(false)
  // loadedPresetKey is the nameKey of the template just applied from a chip, kept
  // (instead of a rendered string) so the "Loaded template" confirmation re-renders
  // in the active language when the operator switches EN/한국어.
  const [loadedPresetKey, setLoadedPresetKey] = useState<string>('')
  // importCoverage is the coverage report behind the last import (what the
  // access-log learner kept and dropped), rendered beside the graph preview so a
  // partial miniature is visible the moment it appears. Presets and spec imports
  // carry no stats and clear it.
  const [importCoverage, setImportCoverage] = useState<CoverageReport | null>(null)
  // authImported flags that the last import auto-populated the Auth section (a derived
  // login / credential pool / suggested signup). It drives the success banner in the
  // Auth card ("Login auto-detected … just fill the highlighted secret") and is cleared
  // whenever the operator changes the auth mode by hand or runs a plain import with no
  // derived auth. The value is the mode that was auto-filled, so the banner copy can
  // name it; '' means nothing was auto-detected.
  const [authImported, setAuthImported] = useState<'' | AuthMode>('')
  // authAdvisories carries the last import's auth hints (managed-IdP mint footgun,
  // openIdConnect discovery pointer). Unlike authImported it survives a manual mode
  // change: the warning is about the TARGET service, not about what was auto-filled,
  // so it stays relevant until another import replaces it.
  const [authAdvisories, setAuthAdvisories] = useState<AuthAdvisory[]>([])
  // history is the ids of completed runs, in order, so a finished run can be
  // compared against the one before it.
  const [history, setHistory] = useState<string[]>([])
  // attachMissingId is the run id from a ?run=<run-id> link the server did not
  // recognize (404 / unreachable). Kept as the raw id — not a rendered string —
  // so the fallback notice re-renders in the active language, mirroring
  // loadedPresetKey.
  const [attachMissingId, setAttachMissingId] = useState<string>('')
  const esRef = useRef<EventSource | null>(null)
  const doneRef = useRef(false)

  useEffect(() => () => esRef.current?.close(), [])

  // Attach mode (the ?run=<run-id> contract): when the console is opened with a
  // run id — `tmula demo` opens the browser this way — it attaches straight to
  // that run's live view instead of showing only the configuration form. The
  // parameter is read once on mount (the URL stays the single source of truth;
  // back/forward and shared links just re-mount and re-read it). The run's
  // stored spec re-hydrates the form so the flow map draws the run's actual
  // scenario, then the state converges on exactly what the form-submit path
  // produces: the SSE stream drives status/stats and the terminal frame fetches
  // the report. An unknown run (404) or unreachable server falls back to the
  // form with a short notice.
  useEffect(() => {
    const id = runIdFromQuery(window.location.search)
    if (!id) return
    let cancelled = false
    ;(async () => {
      try {
        const rep = await probeRun(id)
        if (cancelled) return
        if (!rep) {
          setAttachMissingId(id)
          return
        }
        // Best-effort form hydration from the run's spec. A missing spec (evicted
        // or restarted server) keeps the defaults; the stream still attaches —
        // the live view then simply has no scenario graph to draw, and LiveGraph
        // ignores traffic on nodes it does not know, so a mismatch never breaks.
        if (rep.run.experimentId) {
          const spec = await getExperimentSpec(rep.run.experimentId)
          if (cancelled) return
          const patch = spec ? formFromRunSpec(spec) : null
          if (patch) setForm((f) => ({ ...f, ...patch }))
        }
        const workerCount = rep.run.workers ?? 0
        setRunMode(
          workerCount > 0
            ? t('mode.distributed', { count: workerCount, plural: workerCount === 1 ? '' : 's' })
            : t('mode.local'),
        )
        setRunId(id)
        setStatus(rep.run.status)
        if (runDisabled(rep.run.status)) {
          // Still in flight: follow the live stream, same as after a form submit.
          listen(id)
        } else {
          // Already terminal: converge straight on the post-run state the stream
          // path would have produced.
          setRunReason(rep.run.killReason ?? '')
          setStats(rep.stats)
          setHistory((h) => (h.includes(id) ? h : [...h, id]))
          setReport(rep)
        }
      } catch {
        if (!cancelled) setAttachMissingId(id)
      }
    })()
    return () => {
      cancelled = true
    }
    // Mount-only by design: the parameter is read once; later runs are driven by
    // the form, not the URL.
  }, [])

  function set<K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) {
    setForm((f) => ({ ...f, [key]: value }))
    // Changing the auth mode by hand dismisses the "auto-detected" banner — once the
    // operator picks a mode themselves, the import's claim no longer describes it.
    if (key === 'authMode') setAuthImported('')
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
  // the fields are written consistently. The optional stats become the import
  // coverage report; a source without stats (presets, spec imports, old servers)
  // clears any previous report, since it no longer describes the loaded scenario.
  function applyScenario(s: {
    graph: unknown
    templates: unknown
    start: string
    maxSteps: number
    baseUrl?: string
    stats?: ImportStats
  }) {
    setImportCoverage(coverageFromStats(s.stats))
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

  // applyImport is the headline P7 path: an import both fills the scenario AND, when the
  // backend derived auth (a login flow / credential pool / suggested signup), POPULATES
  // the Auth section so the only thing left is the secret. It applies the scenario
  // first, then maps the derived auth onto the form via authFormFromImport, and records
  // which mode was auto-filled so the Auth card can show the success banner. An import
  // with no derived auth clears the banner (the scenario still lands) — it does NOT
  // reset the operator's existing auth, so a plain re-import never wipes a configured
  // pool. The non-prod bootstrap gate is never auto-confirmed (authFormFromImport keeps
  // it off), so create-accounts still requires an explicit confirmation before a run.
  function applyImport(result: ImportResult) {
    applyScenario(result)
    setAuthAdvisories(result.authAdvisories ?? [])
    const authPatch = authFormFromImport(result)
    if (authPatch.authMode && authPatch.authMode !== 'none') {
      setForm((f) => ({ ...f, ...authPatch }))
      setAuthImported(authPatch.authMode)
    } else {
      setAuthImported('')
    }
  }

  async function run() {
    setError('')
    setRunReason('')
    setReport(null)
    setStats(null)
    setOutcome(null)
    setStatus('starting')
    try {
      // Doctor errors block the run — but the DISPLAY derives from the current
      // doctor state on every render (see the blockers alert below), never from
      // a message captured here: only the "show it" bit is set on click. The
      // allowlist gate is one of those doctor errors, so no separate check.
      if (runBlockers(doctorForm(form)).length > 0) {
        setStatus('')
        setShowBlockers(true)
        return
      }
      setShowBlockers(false)
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
      setError(localizeError(e, t))
    }
  }

  function listen(id: string) {
    esRef.current?.close()
    doneRef.current = false
    const es = new EventSource(streamURL(id))
    esRef.current = es
    es.onmessage = (ev) => {
      try {
        const frame = JSON.parse(ev.data) as StreamFrame
        if (frame.status) setStatus(frame.status)
        // The terminal frame carries WHY a run died (mirrors run.killReason) —
        // e.g. a prewarm login failure. Keep it so the alert region can explain
        // the failure instead of leaving a bare "failed" pill.
        if (frame.reason) setRunReason(frame.reason)
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
  const blockers = runBlockers(doctorIssues)
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
        {/* Attach fallback: a ?run link pointed at a run this server does not
            know (finished and cleaned up, or a stale share). Quietly fall back
            to the form with a short notice instead of a dead end. */}
        {attachMissingId && (
          <div className="notice" role="status">
            <span className="notice__icon" aria-hidden="true">
              <AlertIcon />
            </span>
            <span>{t('attach.notFound', { id: attachMissingId })}</span>
          </div>
        )}

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
            {/* Import coverage (import honesty): what the last import kept vs
                dropped, kept next to the graph preview so a partial miniature is
                visible the moment it appears, not after a run. */}
            <ImportCoveragePanel report={importCoverage} />
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

            {/* Import (Feature B): build a scenario from a spec or recording. The
                import additionally auto-fills the Auth section (P7) when the backend
                derives auth, so onImported carries the whole result, not just the
                scenario. */}
            <ImportPanel onImported={applyImport} />
          </div>
        </section>

        {/* ---- Auth (P5 / P7) ---- */}
        <AuthCard
          form={form}
          set={set}
          imported={authImported}
          advisories={authAdvisories}
          issues={doctorIssues}
        />

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

        {/* Live run blockers: shown after a blocked Run click, but the CONTENT is
            derived from the current doctor state on every render — fix a
            condition and its line disappears; switch language and it
            re-translates; every current blocker is listed, not just the first. */}
        {showBlockers && blockers.length > 0 && (
          <div className="alert" role="alert">
            <span className="alert__icon" aria-hidden="true">
              <AlertIcon />
            </span>
            <span>
              <strong>{t('run.blocked')}</strong>
              <ul className="alert__list">
                {blockers.map((b, i) => (
                  <li key={`${b.code}-${i}`}>{t(b.messageKey, b.vars)}</li>
                ))}
              </ul>
            </span>
          </div>
        )}

        {error && (
          <div className="alert" role="alert">
            <span className="alert__icon" aria-hidden="true">
              <AlertIcon />
            </span>
            <span>{error}</span>
          </div>
        )}

        {/* Failure reason (why the run died): the terminal SSE frame's `reason`.
            Known prewarm failures get a friendly, translated headline (derived at
            render time so it follows the language); the raw backend reason always
            shows beneath so nothing is hidden. */}
        {runReason && statusKind(status) === 'danger' && (
          <div className="alert" role="alert">
            <span className="alert__icon" aria-hidden="true">
              <AlertIcon />
            </span>
            <span>
              {runFailureHintKey(runReason) ? (
                <>
                  <strong>{t(runFailureHintKey(runReason)!)}</strong>
                  <span className="alert__sub">{runReason}</span>
                </>
              ) : (
                runReason
              )}
            </span>
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

// ImportCoveragePanel is the import honesty report: what the last import kept
// and what it dropped ("N requests used / M lines skipped / …"). It renders
// beside the graph preview so a capped or noisy import is visible the moment
// the learned miniature appears. Quiet by default — nothing renders until an
// import actually carries coverage stats (the access-log learner does; presets
// and OpenAPI/HAR conversions do not).
function ImportCoveragePanel({ report }: { report: CoverageReport | null }) {
  const { t } = useI18n()
  if (!report) return null
  return (
    <div className={`coverage coverage--${report.partial ? 'warning' : 'ok'}`} role="status">
      <div className="coverage__head">
        <span className="coverage__title">{t('import.coverage.title')}</span>
        <span className="coverage__summary">
          {t('import.coverage.summary', {
            requests: report.requests,
            skipped: report.skipped,
            sessions: report.sessions,
            clients: report.clients,
            dropped: report.droppedEndpoints,
          })}
        </span>
        {report.format && (
          <span className="coverage__format">{t('import.coverage.format', { format: report.format })}</span>
        )}
      </div>
      {report.partial ? (
        <p className="coverage__warning">
          <AlertIcon />
          <span>
            {t('import.coverage.partial', {
              skipped: report.skipped,
              total: report.totalLines,
              pct: report.skippedPct,
            })}
          </span>
        </p>
      ) : (
        <p className="coverage__note">{t('import.coverage.full')}</p>
      )}
      {report.droppedEndpoints > 0 && (
        <p className="coverage__note">
          {t('import.coverage.folded', { count: report.droppedEndpoints })}
        </p>
      )}
      {report.samples.length > 0 && (
        <div className="coverage__scroll">
          <span className="coverage__samplesTitle">{t('import.coverage.samples')}</span>
          <table className="coverage__table">
            <thead>
              <tr>
                <th className="num">{t('import.coverage.sample.line')}</th>
                <th>{t('import.coverage.sample.text')}</th>
                <th>{t('import.coverage.sample.reason')}</th>
              </tr>
            </thead>
            <tbody>
              {report.samples.map((sample, i) => (
                <tr key={i}>
                  <td className="num">{sample.line ?? '—'}</td>
                  <td>
                    <code className="coverage__line">{sample.text || '—'}</code>
                  </td>
                  <td>{sample.reason || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
  onImported: (result: ImportResult) => void
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

// AuthModeRadios renders one radio group's worth of auth entry options. The
// grouping itself (which entries are entry points, which fold behind Advanced)
// lives in authEntryModel — presentation only, the wire values are untouched.
function AuthModeRadios({
  options,
  selected,
  onPick,
}: {
  options: AuthEntryOption[]
  selected: AuthEntry
  onPick: (entry: AuthEntry) => void
}) {
  const { t } = useI18n()
  return (
    <div className="authmodes" role="radiogroup" aria-label={t('card.auth')}>
      {options.map(({ entry, labelKey, descKey }) => (
        <label key={entry} className={`authmode${selected === entry ? ' authmode--on' : ''}`}>
          <input
            className="authmode__radio"
            type="radio"
            name="authMode"
            checked={selected === entry}
            onChange={() => onPick(entry)}
          />
          <span className="authmode__body">
            <span className="authmode__label">{t(labelKey)}</span>
            <span className="authmode__desc">{t(descKey)}</span>
          </span>
        </label>
      ))}
    </div>
  )
}

// AuthCard is the Auth section (P5 / P7): it picks how the simulated traffic
// authenticates and authors the chosen strategy's material. None (the default) attaches
// nothing and keeps the run anonymous. Account list pastes/uploads pre-issued tokens
// (parsed in the browser into inline entries — the server rejects a file source over the
// wire, D1). Login mints a token from a standalone flow authored through a simple
// mini-form (raw JSON behind Advanced). Create-accounts provisions real accounts and is
// gated behind an explicit non-production confirmation. It writes straight to the form
// via the parent's set(); buildRunSpec turns the fields into the wire credentialPool.
//
// P7 (Easy-Auth): when an import auto-detects auth, `imported` names the populated mode
// and the card leads with a success banner so the operator knows the heavy lifting is
// done and only the highlighted secret is left.
function AuthCard({
  form,
  set,
  imported,
  advisories,
  issues,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
  imported: '' | AuthMode
  advisories: AuthAdvisory[]
  // The full doctor issue list; the card echoes only the auth-scoped ones so a
  // misconfiguration is visible NEXT TO the fields that caused it, not just in
  // the far-away Scenario card.
  issues: DoctorIssue[]
}) {
  const { t } = useI18n()
  const authIssues = authDoctorIssues(issues)
  // advOpened: the operator opened the expert fold this session — keep it open.
  const [advOpened, setAdvOpened] = useState(false)
  // The managed-IdP mint footgun: when the imported spec's token issuer holds the
  // signing key (Auth0/Cognito/Firebase/Okta or any openIdConnect issuer), a
  // self-issued (mint) token WILL be rejected — warn the moment mint is selected.
  const mintAdvisory = mintManagedIdPAdvisory(advisories)
  // The UI-layer selection lives ON THE FORM (authEntryOAuth2): 'oauth2' is a
  // pseudo-entry whose guide compiles onto the login wire mode, so it cannot be
  // derived from form.authMode alone — and the scenario doctor needs to see it to
  // speak the guide's language. selectedEntry self-heals: whenever the flag no
  // longer maps onto the current wire mode (an import or a round-trip changed
  // it), the wire mode wins.
  const selected: AuthEntry = selectedEntry(form.authMode, form.authEntryOAuth2)
  function pickEntry(e: AuthEntry) {
    const patch = entryPatch(e)
    set('authEntryOAuth2', patch.authEntryOAuth2)
    set('authMode', patch.authMode)
  }
  // Surface every REPLACE_ME_* placeholder the active flow's body still carries as a
  // highlighted input the operator must fill — so after an auto-detected import the ONLY
  // thing left is the secret. Only the relevant mode's body is scanned.
  const placeholders =
    form.authMode === 'login'
      ? findReplaceMePlaceholders(form.loginBodyTemplate)
      : form.authMode === 'bootstrap'
        ? findReplaceMePlaceholders(form.signupBodyTemplate)
        : []
  // The banner only makes sense while the auto-filled mode is still selected.
  const showBanner = imported !== '' && imported === form.authMode

  return (
    <section className="card" aria-labelledby="card-auth">
      <div className="card__head">
        <span className="card__step" aria-hidden="true">4</span>
        <h2 className="card__title" id="card-auth">{t('card.auth')}</h2>
      </div>
      <p className="card__hint">{t('card.auth.hint')}</p>

      <div className="stack" style={{ gap: 16 }}>
        {showBanner && (
          <div className="auth-banner" role="status">
            <span className="auth-banner__icon" aria-hidden="true">
              <CheckMini />
            </span>
            <span>
              {placeholders.length > 0
                ? t(`auth.imported.${imported}.secret`)
                : t(`auth.imported.${imported}`)}
            </span>
          </div>
        )}

        <AuthModeRadios options={PRIMARY_AUTH_ENTRIES} selected={selected} onPick={pickEntry} />

        {/* Expert strategies fold behind Advanced (auto-open when one is selected,
            e.g. a round-tripped exec/mint spec): a normal operator never needs mint
            or exec, and surfacing them beside the entry points invited the
            managed-IdP mint footgun. Once the operator opens the fold themselves
            it stays open for the session — leaving mint/exec must not snap it
            shut under their cursor (advOpened tracks the user's open). */}
        <details
          className="advanced"
          open={advancedFoldOpen(form.authMode, advOpened)}
          onToggle={(e) => {
            if ((e.target as HTMLDetailsElement).open) setAdvOpened(true)
          }}
        >
          <summary className="advanced__summary">
            {t('auth.advanced.modes')}
            <span className="field__badge">{t('badge.advanced')}</span>
          </summary>
          <div className="stack advanced__body" style={{ gap: 16 }}>
            <AuthModeRadios options={ADVANCED_AUTH_ENTRIES} selected={selected} onPick={pickEntry} />
          </div>
        </details>

        {placeholders.length > 0 && <ReplaceMeFields form={form} set={set} placeholders={placeholders} />}

        {/* Auth-scoped doctor issues, echoed here next to the panel that caused
            them (the Scenario card's doctor panel still shows the full list). */}
        {authIssues.length > 0 && (
          <div
            className={`doctor doctor--${authIssues.some((i) => i.severity === 'error') ? 'error' : 'warning'}`}
            role="status"
          >
            <ul className="doctor__list">
              {authIssues.map((item, i) => (
                <li key={`${item.code}-${i}`} className={`doctor__item doctor__item--${item.severity}`}>
                  <span className="doctor__severity">{t(`doctor.severity.${item.severity}`)}</span>
                  <span>{t(item.messageKey, item.vars)}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {form.authMode === 'pool' && <AuthPoolFields form={form} set={set} />}
        {selected === 'oauth2' && (
          <AuthOAuth2GuideFields form={form} set={set} discoveryUrl={openIdConnectDiscoveryUrl(advisories)} />
        )}
        {selected !== 'oauth2' && form.authMode === 'login' && <AuthLoginFields form={form} set={set} />}
        {form.authMode === 'bootstrap' && <AuthBootstrapFields form={form} set={set} />}
        {form.authMode === 'mint' && (
          <>
            {mintAdvisory && (
              <div className="authpanel__warn" role="alert">
                <AlertIcon />
                <span>
                  {mintAdvisory.detail
                    ? t('auth.advisory.mintManagedIdp', { host: mintAdvisory.detail })
                    : t('auth.advisory.mintManagedIdp.generic')}
                </span>
              </div>
            )}
            <AuthMintFields form={form} set={set} />
          </>
        )}
        {form.authMode === 'exec' && <AuthExecFields form={form} set={set} />}
      </div>
    </section>
  )
}

// ReplaceMeFields renders one highlighted input per REPLACE_ME_* placeholder the active
// flow body carries, so the secret an import could not derive is the one obvious thing
// left to fill. Each input is bound to form.replaceMeValues[placeholder]; buildAuth
// substitutes the value into the body at send time (the placeholder literal never
// leaves the browser once filled). The label is derived from the placeholder suffix
// (REPLACE_ME_PASSWORD -> "Password") so no per-secret translation is needed.
function ReplaceMeFields({
  form,
  set,
  placeholders,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
  placeholders: string[]
}) {
  const { t } = useI18n()
  function setValue(placeholder: string, value: string) {
    set('replaceMeValues', { ...form.replaceMeValues, [placeholder]: value })
  }
  // A password-ish placeholder masks its input; everything else stays visible.
  const isSecret = (p: string) => /PASS|SECRET|TOKEN|KEY/i.test(p)
  return (
    <div className="auth-secrets" role="group" aria-label={t('auth.secrets.title')}>
      <p className="auth-secrets__head">
        <span className="auth-secrets__icon" aria-hidden="true">
          <AlertIcon />
        </span>
        <span>{t('auth.secrets.hint')}</span>
      </p>
      <div className="field-row field-row--2">
        {placeholders.map((p) => (
          <Field key={p} label={placeholderLabel(p)} help={t('auth.secrets.fieldHint')}>
            <input
              className="input input--highlight"
              type={isSecret(p) ? 'password' : 'text'}
              value={form.replaceMeValues[p] ?? ''}
              onChange={(e) => setValue(p, e.target.value)}
              placeholder={placeholderLabel(p)}
              spellCheck={false}
              autoComplete="off"
            />
          </Field>
        ))}
      </div>
    </div>
  )
}

// AuthPreflight is the "Test login / Test signup / Test token" button every auth
// panel carries: one click sends the SAME RunSpec buildRunSpec would submit to
// POST /api/auth/preflight, where the server performs exactly ONE credential
// acquisition — no load, no run. Success shows a compact confirmation naming
// where the token was found; a failed acquisition (200 ok:false) shows the
// server's reason INLINE in the panel, next to the fields that caused it, so the
// operator never has to hunt in the far-away run bar. A local build error (e.g.
// malformed credential text) surfaces the same way. `kind` only picks the copy.
function AuthPreflight({ form, kind }: { form: ExperimentForm; kind: 'login' | 'signup' | 'token' }) {
  const { t } = useI18n()
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<PreflightResult | null>(null)
  const [err, setErr] = useState('')

  async function test() {
    setBusy(true)
    setErr('')
    setResult(null)
    try {
      const spec = buildRunSpec(form)
      setResult(await preflightAuth(spec))
    } catch (e) {
      setErr(localizeError(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack" style={{ gap: 8 }}>
      <div className="import__actions">
        <button type="button" className="btn btn--ghost" onClick={test} disabled={busy}>
          <CheckMini />
          {busy ? t('auth.preflight.testing') : t(`auth.preflight.button.${kind}`)}
        </button>
        <span className="field__help">{t('auth.preflight.hint')}</span>
      </div>
      {result?.ok && (
        <p className="authpanel__ok" role="status">
          <CheckMini />
          <span>
            {t(`auth.preflight.ok.${kind}`)}
            {result.tokenSource &&
              ' — ' +
                (result.tokenPrefix
                  ? t('auth.preflight.okDetail', { source: result.tokenSource, prefix: result.tokenPrefix })
                  : t('auth.preflight.okSource', { source: result.tokenSource }))}
            {result.subject && ' · ' + t('auth.preflight.okSubject', { subject: result.subject })}
          </span>
        </p>
      )}
      {result && !result.ok && (
        <div className="authpanel__err" role="alert">
          <AlertIcon />
          <span>
            {result.httpStatus
              ? t('auth.preflight.fail', { status: result.httpStatus })
              : t('auth.preflight.failPlain')}
            {result.reason && <span className="alert__sub">{result.reason}</span>}
          </span>
        </div>
      )}
      {err && (
        <div className="authpanel__err" role="alert">
          <AlertIcon />
          <span>{t('auth.preflight.error', { error: err })}</span>
        </div>
      )}
    </div>
  )
}

// AuthPoolFields authors a token pool: a format selector, a file upload, and a textarea
// of pasted credentials. The file and textarea are BOTH parsed in the browser — the
// file's text is loaded into the textarea on pick (so the operator sees exactly what
// will be sent), and the live parsed-count / error is computed from the textarea on
// every keystroke. Nothing but inline { subject, token } entries ever leave the
// browser (the server rejects a file source over the wire — D1).
function AuthPoolFields({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()

  // Live parse of the pasted text for the count / inline error. It never throws out of
  // render: a malformed body surfaces as a short message, an empty body as nothing.
  let count = 0
  let parseError = ''
  if (form.authPoolText.trim()) {
    try {
      count = parseCredentials(form.authPoolFormat, form.authPoolText).length
    } catch (e) {
      parseError = localizeError(e, t)
    }
  }

  async function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    // Pick the format from the extension so the strongest signal isn't lost; .csv ->
    // csv, .jsonl -> jsonl, anything else stays on the current selection.
    const lower = file.name.toLowerCase()
    if (lower.endsWith('.csv')) set('authPoolFormat', 'csv')
    else if (lower.endsWith('.jsonl')) set('authPoolFormat', 'jsonl')
    else if (lower.endsWith('.txt') || lower.endsWith('.tokens')) set('authPoolFormat', 'tokens')
    try {
      // Read the file client-side so the textarea reflects exactly what will be sent;
      // the operator can still edit it before running.
      set('authPoolText', await file.text())
    } catch {
      /* ignore an unreadable file; the operator can paste instead */
    }
  }

  return (
    <div className="authpanel">
      <div className="import__grid">
        <label className="field import__file">
          <span className="field__label">{t('auth.pool.file')}</span>
          <input className="filepick" type="file" accept=".csv,.jsonl,.txt,.tokens" onChange={onFile} />
          <span className="field__help">{t('auth.pool.fileHint')}</span>
        </label>
        <Field label={t('auth.pool.format')} help={t('auth.pool.formatHint')}>
          <select
            className="select"
            value={form.authPoolFormat}
            onChange={(e) => set('authPoolFormat', e.target.value as CredFormat)}
          >
            <option value="csv">{t('auth.pool.format.csv')}</option>
            <option value="jsonl">{t('auth.pool.format.jsonl')}</option>
            <option value="tokens">{t('auth.pool.format.tokens')}</option>
          </select>
        </Field>
      </div>

      <Field label={t('auth.pool.paste')} help={t('auth.pool.pasteHint')}>
        <textarea
          className="textarea"
          value={form.authPoolText}
          onChange={(e) => set('authPoolText', e.target.value)}
          rows={6}
          placeholder={t(`auth.pool.placeholder.${form.authPoolFormat}`)}
          spellCheck={false}
        />
      </Field>

      {/* Discoverability: the pool carries Basic-auth credentials just fine —
          spell out the {{basicAuth}} recipe so nobody hand-base64s a header. */}
      <p className="card__hint">{t('auth.pool.basicHint')}</p>

      {parseError ? (
        <div className="authpanel__err" role="alert">
          <AlertIcon />
          <span>{parseError}</span>
        </div>
      ) : count > 0 ? (
        <p className="authpanel__ok" role="status">
          <CheckMini />
          {t('auth.pool.count', { count })}
        </p>
      ) : null}

      <PatternGenerator
        format={form.authPoolFormat === 'jsonl' ? 'tokens' : (form.authPoolFormat as 'csv' | 'tokens')}
        onGenerated={(text) => {
          // A subject,password pattern is CSV; a bare-token pattern is tokens. Match
          // the pool format to what was generated so the live parse reads it.
          set('authPoolFormat', text.startsWith('username,password') ? 'csv' : 'tokens')
          set('authPoolText', text)
        }}
      />

      <AuthPreflight form={form} kind="token" />
    </div>
  )
}

// PatternGenerator is the "generate accounts from a pattern" panel shared by the
// pool and login cards: a subject/secret template pair and a count that
// generateCredentialRows materializes into the target textarea (client-side, so
// it flows through the same parse into inline entries — no new wire shape). For a
// very large pool the CLI scenario file's usersPattern generates server-side; the
// help text points there. onGenerated receives the generated text so the caller
// drops it into its own field.
function PatternGenerator({
  format,
  onGenerated,
}: {
  format: 'csv' | 'tokens'
  onGenerated: (text: string) => void
}) {
  const { t } = useI18n()
  const [subject, setSubject] = useState('user{{.userIndex}}')
  const [token, setToken] = useState(format === 'tokens' ? 'tok-{{.userIndex}}' : 'pw-{{.userIndex}}')
  const [count, setCount] = useState(100)
  const [note, setNote] = useState('')
  const [err, setErr] = useState('')

  function generate() {
    setNote('')
    setErr('')
    try {
      const text = generateCredentialRows(subject, token, count, format)
      onGenerated(text)
      setNote(t('auth.pattern.generated', { count }))
    } catch (e) {
      setErr(localizeError(e, t))
    }
  }

  return (
    <details className="advanced">
      <summary className="advanced__summary">
        {t('auth.pattern.toggle')}
        <span className="field__badge">{t('badge.optional')}</span>
      </summary>
      <div className="stack advanced__body" style={{ gap: 16 }}>
        <p className="card__hint">{t('auth.pattern.hint')}</p>
        <div className="field-row field-row--2">
          <Field label={t('auth.pattern.subject')} help={t('auth.pattern.subjectHint')}>
            <input
              className="input"
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              placeholder="user{{.userIndex}}"
              spellCheck={false}
            />
          </Field>
          <Field label={t('auth.pattern.token')} help={t('auth.pattern.tokenHint')}>
            <input
              className="input"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="pw-{{.userIndex}}"
              spellCheck={false}
            />
          </Field>
        </div>
        <div className="field-row field-row--2">
          <Field
            label={t('auth.pattern.count')}
            help={t('auth.pattern.countHint', { max: MAX_WEB_PATTERN_ROWS.toLocaleString() })}
          >
            <input
              className="input"
              type="number"
              min={1}
              max={MAX_WEB_PATTERN_ROWS}
              value={count}
              onChange={(e) =>
                // Clamp to the real browser cap so a typed 50000 can never reach
                // the generator's over-cap error — the field simply stops at it.
                setCount(
                  Math.min(MAX_WEB_PATTERN_ROWS, Math.max(1, Math.floor(Number(e.target.value) || 0))),
                )
              }
            />
          </Field>
          <div style={{ display: 'flex', alignItems: 'flex-end' }}>
            <button type="button" className="btn btn--ghost" onClick={generate}>
              {t('auth.pattern.generate')}
            </button>
          </div>
        </div>
        {err ? (
          <div className="authpanel__err" role="alert">
            <AlertIcon />
            <span>{err}</span>
          </div>
        ) : note ? (
          <p className="authpanel__ok" role="status">
            <CheckMini />
            {note}
          </p>
        ) : null}
      </div>
    </details>
  )
}

// AuthLoginFields authors the standalone login flow that mints a token. The COMMON case
// (P7) is the simple mini-form: a login URL (method + path), a request-body template
// with {username}/{password} markers, and the per-user vs shared scope toggle —
// buildAuth compiles those into the login flow graph/templates under the hood. Token
// capture, subject capture, the start node, and the raw graph/templates JSON live behind
// an Advanced collapsible (collapsed by default): a normal user never opens it. Capture
// defaults empty (auto-detect, E1); the simple flow is single-step so the start is
// implied. The mode toggle picks which authoring path buildAuth reads.
function AuthLoginFields({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  const simple = form.loginMode === 'simple'
  const hasCredList = form.loginCredText.trim().length > 0
  // Smart default: when the operator supplies a credential list, the per-row body should
  // pull the row in via {{.username}}/{{.password}}. Suggest the multi-user body ONLY
  // when the current body is still untouched (the single-identity default) so we never
  // clobber a hand-edited body; the operator can also apply it from the hint.
  const bodyIsDefault = form.loginBodyTemplate.trim() === LOGIN_BODY_SINGLE
  const suggestMultiBody = hasCredList && bodyIsDefault
  function setCredText(text: string) {
    set('loginCredText', text)
    // The first time a list appears, auto-upgrade an untouched body to the per-row
    // template so the common case just works; a hand-edited body is left as-is.
    if (text.trim() && form.loginBodyTemplate.trim() === LOGIN_BODY_SINGLE) {
      set('loginBodyTemplate', LOGIN_BODY_MULTI)
    }
  }
  return (
    <div className="authpanel">
      {simple ? (
        <>
          <div className="field-row field-row--2">
            <Field label={t('auth.login.url')} help={t('auth.login.urlHint')}>
              <div className="methodpath">
                <select
                  className="select methodpath__method"
                  aria-label={t('auth.login.method')}
                  value={form.loginUrlMethod}
                  onChange={(e) => set('loginUrlMethod', e.target.value)}
                >
                  {HTTP_METHODS.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </select>
                <input
                  className="input methodpath__path"
                  value={form.loginUrlPath}
                  onChange={(e) => set('loginUrlPath', e.target.value)}
                  placeholder="/login"
                  spellCheck={false}
                />
              </div>
            </Field>
            <Field
              label={t('auth.login.scope')}
              help={t('auth.login.scopeHint')}
              tip={<HelpTip label={t('auth.login.scope')} text={t('auth.login.scope.tip')} />}
            >
              <select
                className="select"
                value={form.loginScope}
                onChange={(e) => set('loginScope', e.target.value as LoginScope)}
              >
                <option value="per-user">{t('auth.login.scope.perUser')}</option>
                <option value="shared">{t('auth.login.scope.shared')}</option>
              </select>
            </Field>
          </div>

          <AuthLoginCredList form={form} set={set} onCredText={setCredText} />

          <Field
            label={t('auth.login.body')}
            help={t('auth.login.bodyHint')}
            tip={<HelpTip label={t('auth.login.body')} text={t(hasCredList ? 'auth.login.body.multiTip' : 'auth.login.body.tip')} />}
          >
            <textarea
              className="textarea"
              value={form.loginBodyTemplate}
              onChange={(e) => set('loginBodyTemplate', e.target.value)}
              rows={4}
              placeholder={hasCredList ? LOGIN_BODY_MULTI : LOGIN_BODY_SINGLE}
              spellCheck={false}
            />
          </Field>
          {suggestMultiBody && (
            <button
              type="button"
              className="btn btn--ghost"
              style={{ alignSelf: 'flex-start', padding: '6px 12px', fontSize: 13 }}
              onClick={() => set('loginBodyTemplate', LOGIN_BODY_MULTI)}
            >
              {t('auth.login.body.useMulti')}
            </button>
          )}
        </>
      ) : (
        <AuthLoginAdvancedBody form={form} set={set} />
      )}

      <details className="advanced" open={!simple}>
        <summary className="advanced__summary">
          {t('auth.advanced.login')}
          <span className="field__badge">{t('badge.jsonAdvanced')}</span>
        </summary>
        <div className="stack advanced__body" style={{ gap: 16 }}>
          <Check
            checked={!simple}
            onChange={(v) => set('loginMode', v ? 'advanced' : 'simple')}
            label={t('auth.advanced.rawLogin')}
            sub={t('auth.advanced.rawLoginSub')}
          />
          <div className="field-row field-row--2">
            <Field
              label={t('auth.login.tokenVar')}
              help={t('auth.login.tokenVarHint')}
              tip={<HelpTip label={t('auth.login.tokenVar')} text={t('auth.login.tokenVar.tip')} />}
            >
              <input
                className="input"
                value={form.loginTokenVar}
                onChange={(e) => set('loginTokenVar', e.target.value)}
                placeholder={t('auth.tokenVar.autoPlaceholder')}
                spellCheck={false}
              />
            </Field>
            <Field label={t('auth.login.subjectVar')} help={t('auth.login.subjectVarHint')}>
              <input
                className="input"
                value={form.loginSubjectVar}
                onChange={(e) => set('loginSubjectVar', e.target.value)}
                placeholder="$.user_id"
                spellCheck={false}
              />
            </Field>
          </div>
          {!simple && (
            <Field label={t('auth.login.start')} help={t('auth.login.startHint')}>
              <input
                className="input"
                value={form.loginStart}
                onChange={(e) => set('loginStart', e.target.value)}
                placeholder="login"
                spellCheck={false}
              />
            </Field>
          )}
          {/* Explicit refresh-grant OVERRIDE: advanced only, so the simple mini-form stays
              one-field. When a body is given it WINS over the backend's auto-derivation, so
              even a JSON-body login refreshes without re-logging-in. The request line is
              optional (defaults to the login token endpoint). */}
          {!simple && (
            <>
              <Field
                label={t('auth.login.refreshRequest')}
                help={t('auth.login.refreshRequestHint')}
                tip={<HelpTip label={t('auth.login.refresh')} text={t('auth.login.refresh.tip')} />}
              >
                <input
                  className="input"
                  value={form.loginRefreshRequest}
                  onChange={(e) => set('loginRefreshRequest', e.target.value)}
                  placeholder="POST /oauth/token"
                  spellCheck={false}
                />
              </Field>
              <Field label={t('auth.login.refreshBody')} help={t('auth.login.refreshBodyHint')}>
                <textarea
                  className="textarea"
                  value={form.loginRefreshBody}
                  onChange={(e) => set('loginRefreshBody', e.target.value)}
                  rows={3}
                  placeholder="grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=…"
                  spellCheck={false}
                />
              </Field>
            </>
          )}
        </div>
      </details>

      <AuthPreflight form={form} kind="login" />
    </div>
  )
}

// AuthLoginCredList is the OPTIONAL "log in multiple users" panel: a format selector, a
// file upload, and a textarea of username,password rows, all parsed IN THE BROWSER (like
// the token pool) into credentialPool.entries of { subject: username, token: password }.
// Supply a list and each virtual user logs in as the NEXT row (a different account);
// leave it empty and the login mints ONE identity from the body (the prior behavior).
// It shows the live parsed count / error so the operator sees how many accounts were
// recognized before running. Picking a file loads its text into the textarea (so the
// operator sees exactly what will be sent) and routes through onCredText, which also
// upgrades an untouched login body to the per-row template.
function AuthLoginCredList({
  form,
  set,
  onCredText,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
  onCredText: (text: string) => void
}) {
  const { t } = useI18n()

  // Live parse of the pasted text for the count / inline error. It never throws out of
  // render: a malformed body surfaces as a short message, an empty body as nothing.
  let count = 0
  let parseError = ''
  if (form.loginCredText.trim()) {
    try {
      count = parseLoginCredentials(form.loginCredFormat, form.loginCredText).length
    } catch (e) {
      parseError = localizeError(e, t)
    }
  }

  async function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    // Pick the format from the extension; .csv -> csv, .jsonl -> jsonl, else unchanged.
    const lower = file.name.toLowerCase()
    if (lower.endsWith('.csv')) set('loginCredFormat', 'csv')
    else if (lower.endsWith('.jsonl')) set('loginCredFormat', 'jsonl')
    try {
      onCredText(await file.text())
    } catch {
      /* ignore an unreadable file; the operator can paste instead */
    }
  }

  return (
    <details className="advanced" open={form.loginCredText.trim().length > 0}>
      <summary className="advanced__summary">
        {t('auth.login.cred.toggle')}
        <span className="field__badge">{t('badge.optional')}</span>
      </summary>
      <div className="stack advanced__body" style={{ gap: 16 }}>
        <p className="card__hint">{t('auth.login.cred.hint')}</p>
        <div className="import__grid">
          <label className="field import__file">
            <span className="field__label">{t('auth.login.cred.file')}</span>
            <input className="filepick" type="file" accept=".csv,.jsonl" onChange={onFile} />
            <span className="field__help">{t('auth.login.cred.fileHint')}</span>
          </label>
          <Field label={t('auth.pool.format')} help={t('auth.login.cred.formatHint')}>
            <select
              className="select"
              value={form.loginCredFormat}
              onChange={(e) => set('loginCredFormat', e.target.value as LoginCredFormat)}
            >
              <option value="csv">{t('auth.login.cred.format.csv')}</option>
              <option value="jsonl">{t('auth.login.cred.format.jsonl')}</option>
            </select>
          </Field>
        </div>

        <Field
          label={t('auth.login.cred.paste')}
          help={t('auth.login.cred.pasteHint')}
          tip={<HelpTip label={t('auth.login.cred.paste')} text={t('auth.login.cred.tip')} />}
        >
          <textarea
            className="textarea"
            value={form.loginCredText}
            onChange={(e) => onCredText(e.target.value)}
            rows={5}
            placeholder={t(`auth.login.cred.placeholder.${form.loginCredFormat}`)}
            spellCheck={false}
          />
        </Field>

        {parseError ? (
          <div className="authpanel__err" role="alert">
            <AlertIcon />
            <span>{parseError}</span>
          </div>
        ) : count > 0 ? (
          <p className="authpanel__ok" role="status">
            <CheckMini />
            {t('auth.login.cred.count', { count })}
          </p>
        ) : null}

        {/* Login rows are always username,password CSV. */}
        <PatternGenerator format="csv" onGenerated={onCredText} />
      </div>
    </details>
  )
}

// AuthLoginAdvancedBody is the raw graph/templates JSON authoring for the login flow,
// shown when the operator opts into advanced mode. It is the original power-user path
// preserved verbatim — a sibling graph the login transport walks to mint a token.
function AuthLoginAdvancedBody({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  return (
    <>
      <Field
        label={t('auth.login.graph')}
        help={t('auth.login.graphHint')}
        tip={<HelpTip label={t('auth.login.graph')} text={t('auth.login.graph.tip')} />}
      >
        <textarea
          className="textarea"
          value={form.loginGraphJSON}
          onChange={(e) => set('loginGraphJSON', e.target.value)}
          rows={6}
          placeholder={loginGraphPlaceholder}
          spellCheck={false}
        />
      </Field>
      <Field label={t('auth.login.templates')} help={t('auth.login.templatesHint')}>
        <textarea
          className="textarea"
          value={form.loginTemplatesJSON}
          onChange={(e) => set('loginTemplatesJSON', e.target.value)}
          rows={7}
          placeholder={loginTemplatesPlaceholder}
          spellCheck={false}
        />
      </Field>
    </>
  )
}

// OAUTH2_GRANTS is the ordered "how do you log in?" answer set for the OAuth2
// guide, each mapping onto a grant the compiler (authFormFromOAuth2Guide) knows.
const OAUTH2_GRANTS: { grant: OAuth2Grant; labelKey: string; descKey: string }[] = [
  { grant: 'password', labelKey: 'auth.oauth2.grant.password', descKey: 'auth.oauth2.grant.password.desc' },
  { grant: 'clientCredentials', labelKey: 'auth.oauth2.grant.cc', descKey: 'auth.oauth2.grant.cc.desc' },
  { grant: 'refreshToken', labelKey: 'auth.oauth2.grant.refresh', descKey: 'auth.oauth2.grant.refresh.desc' },
  { grant: 'accessToken', labelKey: 'auth.oauth2.grant.access', descKey: 'auth.oauth2.grant.access.desc' },
]

// AuthOAuth2GuideFields is the OAuth2 guided assembler (the "It's an OAuth2
// service" entry): answer a token URL and "how do you log in?" and the guide
// compiles the answers onto the existing login form fields via
// authFormFromOAuth2Guide — a frontend assembly layer, not a new wire strategy.
// The guide's answers live on the FORM (form.oauth2Guide), so switching entry
// points and back preserves every answer. Compilation is guarded two ways:
//   - No clobber: the compiled flow only overwrites loginGraphJSON /
//     loginTemplatesJSON / loginCredText while those are the shipped default or
//     were themselves guide-generated (oauth2GuideCanCompileOver). A hand-authored
//     flow shows an explicit Regenerate button instead.
//   - Credentials compile on blur/apply, not per keystroke, so a masked password
//     never streams into the credential-list textarea in plaintext as it is typed.
// The access-token answer applies via an explicit button because it switches the
// wire mode to pool (which unmounts this panel).
function AuthOAuth2GuideFields({
  form,
  set,
  discoveryUrl,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
  // discoveryUrl is the imported spec's openIdConnect discovery document (from the
  // openidconnect-discovery advisory): its token_endpoint is what belongs in the
  // token URL field, so the guide points the operator straight at it.
  discoveryUrl: string
}) {
  const { t } = useI18n()
  const guide = form.oauth2Guide
  const canCompileOver = oauth2GuideCanCompileOver(form)
  // Issuer discovery ("Fetch endpoints"): transient request state only — the
  // issuer answer itself lives on the form like every other guide answer.
  const [discBusy, setDiscBusy] = useState(false)
  const [discOk, setDiscOk] = useState(false)
  const [discErr, setDiscErr] = useState('')

  async function fetchEndpoints() {
    setDiscBusy(true)
    setDiscOk(false)
    setDiscErr('')
    try {
      const d = await discoverIssuer(guide.issuer.trim())
      // Fill the Token URL (still editable) and recompile like a typed answer.
      update({ tokenUrl: d.tokenEndpoint })
      setDiscOk(true)
    } catch (e) {
      setDiscErr(localizeError(e, t))
    } finally {
      setDiscBusy(false)
    }
  }

  function applyCompiled(g: OAuth2GuideForm) {
    const compiled = authFormFromOAuth2Guide(g)
    for (const [k, v] of Object.entries(compiled)) {
      set(k as keyof ExperimentForm, v as ExperimentForm[keyof ExperimentForm] as never)
    }
  }
  // update stores an answer; unless compile is false (the credential fields,
  // which compile on blur) it also recompiles the flow — but ONLY over a default
  // or guide-generated flow. The access-token answer never auto-compiles: it is
  // applied via the explicit button below, so typing the token does not switch
  // the mode mid-keystroke.
  function update(patch: Partial<OAuth2GuideForm>, compile = true) {
    const next = { ...guide, ...patch }
    set('oauth2Guide', next)
    if (compile && canCompileOver && next.grant !== 'accessToken') applyCompiled(next)
  }
  // compileNow flushes the current answers into the login flow (used on blur of
  // the credential fields). Same guards as update().
  function compileNow() {
    if (canCompileOver && guide.grant !== 'accessToken') applyCompiled(guide)
  }

  const showClient = guide.grant !== 'accessToken'
  return (
    <div className="authpanel">
      <p className="card__hint">{t('auth.oauth2.lead')}</p>

      {/* No-clobber guard: the flow below was hand-edited, so answers no longer
          auto-compile. Regenerate is the explicit confirmation to overwrite. */}
      {!canCompileOver && guide.grant !== 'accessToken' && (
        <div className="authpanel__warn" role="status">
          <AlertIcon />
          <span>
            <span className="authpanel__confirmLabel">{t('auth.oauth2.handAuthored')}</span>
            <button
              type="button"
              className="btn btn--ghost"
              style={{ marginTop: 8, padding: '6px 12px', fontSize: 13 }}
              onClick={() => applyCompiled(guide)}
            >
              {t('auth.oauth2.regenerate')}
            </button>
          </span>
        </div>
      )}

      {/* Issuer discovery: for the operator who does NOT know their token URL.
          Paste the IdP base URL, fetch its discovery document server-side, and
          the Token URL below fills in (still editable). */}
      <Field label={t('auth.oauth2.issuer')} help={t('auth.oauth2.issuerHint')}>
        <input
          className="input"
          value={guide.issuer}
          onChange={(e) => update({ issuer: e.target.value }, false)}
          placeholder="https://idp.example.com"
          spellCheck={false}
        />
      </Field>
      <div className="import__actions">
        <button
          type="button"
          className="btn btn--ghost"
          onClick={fetchEndpoints}
          disabled={discBusy || !guide.issuer.trim()}
        >
          {discBusy ? t('auth.oauth2.discovering') : t('auth.oauth2.discoverButton')}
        </button>
        {discOk && (
          <span className="import__ok" role="status">
            <CheckMini />
            {t('auth.oauth2.discovered')}
          </span>
        )}
      </div>
      {discErr && (
        <div className="authpanel__err" role="alert">
          <AlertIcon />
          <span>{t('auth.oauth2.discoverFailed', { error: discErr })}</span>
        </div>
      )}

      <Field label={t('auth.oauth2.tokenUrl')} help={t('auth.oauth2.tokenUrlHint')}>
        <input
          className="input"
          value={guide.tokenUrl}
          onChange={(e) => update({ tokenUrl: e.target.value })}
          placeholder="https://idp.example.com/oauth/token"
          spellCheck={false}
        />
      </Field>
      {/* Static shape examples for the big managed IdPs, so "what does my token
          URL even look like" has an answer right under the field. */}
      <p className="card__hint">{t('auth.oauth2.tokenUrlExamples')}</p>
      {discoveryUrl && (
        <p className="authpanel__ok" role="status">
          <CheckMini />
          {t('auth.oauth2.discovery', { url: discoveryUrl })}
        </p>
      )}

      <Field label={t('auth.oauth2.grant')} help={t('auth.oauth2.grantHint')}>
        <div className="authmodes" role="radiogroup" aria-label={t('auth.oauth2.grant')}>
          {OAUTH2_GRANTS.map(({ grant, labelKey, descKey }) => (
            <label key={grant} className={`authmode${guide.grant === grant ? ' authmode--on' : ''}`}>
              <input
                className="authmode__radio"
                type="radio"
                name="oauth2Grant"
                checked={guide.grant === grant}
                onChange={() => update({ grant })}
              />
              <span className="authmode__body">
                <span className="authmode__label">{t(labelKey)}</span>
                <span className="authmode__desc">{t(descKey)}</span>
              </span>
            </label>
          ))}
        </div>
      </Field>

      {guide.grant === 'password' && (
        <>
          <div className="field-row field-row--2">
            <Field label={t('auth.oauth2.username')}>
              <input
                className="input"
                value={guide.username}
                onChange={(e) => update({ username: e.target.value }, false)}
                onBlur={compileNow}
                spellCheck={false}
                autoComplete="off"
              />
            </Field>
            <Field label={t('auth.oauth2.password')}>
              <input
                className="input"
                type="password"
                value={guide.password}
                onChange={(e) => update({ password: e.target.value }, false)}
                onBlur={compileNow}
                autoComplete="off"
              />
            </Field>
          </div>
          <details className="advanced" open={guide.users.trim().length > 0}>
            <summary className="advanced__summary">
              {t('auth.oauth2.users.toggle')}
              <span className="field__badge">{t('badge.optional')}</span>
            </summary>
            <div className="stack advanced__body" style={{ gap: 16 }}>
              <Field label={t('auth.oauth2.users')} help={t('auth.oauth2.usersHint')}>
                <textarea
                  className="textarea"
                  value={guide.users}
                  onChange={(e) => update({ users: e.target.value }, false)}
                  onBlur={compileNow}
                  rows={5}
                  placeholder={'username,password\nalice,pw-a\nbob,pw-b'}
                  spellCheck={false}
                />
              </Field>
            </div>
          </details>
        </>
      )}

      {guide.grant === 'refreshToken' && (
        <Field label={t('auth.oauth2.refreshToken')} help={t('auth.oauth2.refreshTokenHint')}>
          <input
            className="input"
            type="password"
            value={guide.refreshToken}
            onChange={(e) => update({ refreshToken: e.target.value })}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
      )}

      {guide.grant === 'accessToken' && (
        <>
          <Field label={t('auth.oauth2.accessToken')} help={t('auth.oauth2.accessTokenHint')}>
            <input
              className="input"
              type="password"
              value={guide.accessToken}
              onChange={(e) => update({ accessToken: e.target.value })}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <button
            type="button"
            className="btn btn--ghost"
            style={{ alignSelf: 'flex-start', padding: '6px 12px', fontSize: 13 }}
            disabled={!guide.accessToken.trim()}
            onClick={() => applyCompiled(guide)}
          >
            {t('auth.oauth2.accessToken.apply')}
          </button>
        </>
      )}

      {showClient && (
        <div className="field-row field-row--2">
          <Field label={t('auth.oauth2.clientId')} help={t('auth.oauth2.clientIdHint')}>
            <input
              className="input"
              value={guide.clientId}
              onChange={(e) => update({ clientId: e.target.value })}
              spellCheck={false}
              autoComplete="off"
            />
          </Field>
          <Field label={t('auth.oauth2.clientSecret')} help={t('auth.oauth2.clientSecretHint')}>
            <input
              className="input"
              type="password"
              value={guide.clientSecret}
              onChange={(e) => update({ clientSecret: e.target.value })}
              autoComplete="off"
            />
          </Field>
        </div>
      )}

      {showClient && (
        <Field label={t('auth.oauth2.scope')} help={t('auth.oauth2.scopeHint')}>
          <input
            className="input"
            value={guide.scope}
            onChange={(e) => update({ scope: e.target.value })}
            placeholder="read write"
            spellCheck={false}
          />
        </Field>
      )}

      {guide.grant !== 'accessToken' && (
        <details className="advanced">
          <summary className="advanced__summary">
            {t('auth.oauth2.advanced')}
            <span className="field__badge">{t('badge.jsonAdvanced')}</span>
          </summary>
          <div className="stack advanced__body" style={{ gap: 16 }}>
            <p className="card__hint">{t('auth.oauth2.advancedHint')}</p>
            <AuthLoginAdvancedBody form={form} set={set} />
          </div>
        </details>
      )}

      {/* The access-token answer becomes a pool via the explicit apply button —
          until then there is no assembled flow to test, so no preflight here. */}
      {guide.grant !== 'accessToken' && <AuthPreflight form={form} kind="login" />}
    </div>
  )
}

// AuthBootstrapFields authors the bootstrap signup flow that provisions real accounts.
// Framed clearly (P7) as the LAST resort — only when you need many DISTINCT accounts you
// cannot pre-make. The COMMON case is the simple mini-form: a signup URL (method + path)
// with a body template, plus an optional teardown URL — buildAuth compiles those into
// the signup/teardown steps. Capture paths, the start steps, and the raw steps JSON live
// behind Advanced (collapsed by default; capture empty = auto-detect, E1). Because it
// creates and deletes REAL accounts on the target, the whole panel is gated behind an
// explicit non-production confirmation — until it is checked the fields are disabled and
// the run is blocked (buildAuth re-checks it). The teardown-or-keepAccounts rule holds:
// with keepAccounts off and no teardown the doctor flags stranded accounts.
function AuthBootstrapFields({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  const confirmed = form.authBootstrapConfirmed
  const simple = form.signupMode === 'simple'
  return (
    <div className="authpanel">
      <p className="authpanel__lead">{t('auth.bootstrap.lead')}</p>

      <div className="authpanel__warn" role="alert">
        <AlertIcon />
        <label className="authpanel__confirm">
          <input
            className="check__box"
            type="checkbox"
            checked={confirmed}
            onChange={(e) => set('authBootstrapConfirmed', e.target.checked)}
          />
          <span>
            <span className="authpanel__confirmLabel">{t('auth.bootstrap.confirm')}</span>
            <span className="authpanel__confirmSub">{t('auth.bootstrap.confirmSub')}</span>
          </span>
        </label>
      </div>

      <fieldset className="authpanel__fields" disabled={!confirmed}>
        {simple ? (
          <>
            <Field
              label={t('auth.bootstrap.signupUrl')}
              help={t('auth.bootstrap.signupUrlHint')}
            >
              <div className="methodpath">
                <select
                  className="select methodpath__method"
                  aria-label={t('auth.bootstrap.signupMethod')}
                  value={form.signupUrlMethod}
                  onChange={(e) => set('signupUrlMethod', e.target.value)}
                >
                  {HTTP_METHODS.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </select>
                <input
                  className="input methodpath__path"
                  value={form.signupUrlPath}
                  onChange={(e) => set('signupUrlPath', e.target.value)}
                  placeholder="/register"
                  spellCheck={false}
                />
              </div>
            </Field>
            <Field
              label={t('auth.bootstrap.body')}
              help={t('auth.bootstrap.bodyHint')}
              tip={<HelpTip label={t('auth.bootstrap.body')} text={t('auth.bootstrap.body.tip')} />}
            >
              <textarea
                className="textarea"
                value={form.signupBodyTemplate}
                onChange={(e) => set('signupBodyTemplate', e.target.value)}
                rows={4}
                placeholder={'{"email": "test+{{.userIndex}}@example.com", "password": "a-real-password"}'}
                spellCheck={false}
              />
            </Field>
          </>
        ) : (
          <AuthBootstrapAdvancedBody form={form} set={set} />
        )}

        <Check
          checked={form.keepAccounts}
          onChange={(v) => set('keepAccounts', v)}
          label={t('auth.bootstrap.keep')}
          sub={t('auth.bootstrap.keepSub')}
        />

        {!form.keepAccounts && simple && (
          <Field label={t('auth.bootstrap.teardownUrl')} help={t('auth.bootstrap.teardownUrlHint')}>
            <div className="methodpath">
              <select
                className="select methodpath__method"
                aria-label={t('auth.bootstrap.teardownMethod')}
                value={form.signupTeardownUrlMethod}
                onChange={(e) => set('signupTeardownUrlMethod', e.target.value)}
              >
                {HTTP_METHODS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
              <input
                className="input methodpath__path"
                value={form.signupTeardownUrlPath}
                onChange={(e) => set('signupTeardownUrlPath', e.target.value)}
                placeholder="/accounts/{{.subject}}"
                spellCheck={false}
              />
            </div>
          </Field>
        )}

        {!form.keepAccounts && !simple && (
          <>
            <Field label={t('auth.bootstrap.teardown')} help={t('auth.bootstrap.teardownHint')}>
              <textarea
                className="textarea"
                value={form.signupTeardownJSON}
                onChange={(e) => set('signupTeardownJSON', e.target.value)}
                rows={4}
                placeholder={signupTeardownPlaceholder}
                spellCheck={false}
              />
            </Field>
            <Field label={t('auth.bootstrap.teardownStart')} help={t('auth.bootstrap.teardownStartHint')}>
              <input
                className="input"
                value={form.signupTeardownStart}
                onChange={(e) => set('signupTeardownStart', e.target.value)}
                placeholder="delete"
                spellCheck={false}
              />
            </Field>
          </>
        )}

        <details className="advanced" open={!simple}>
          <summary className="advanced__summary">
            {t('auth.advanced.bootstrap')}
            <span className="field__badge">{t('badge.jsonAdvanced')}</span>
          </summary>
          <div className="stack advanced__body" style={{ gap: 16 }}>
            <Check
              checked={!simple}
              onChange={(v) => set('signupMode', v ? 'advanced' : 'simple')}
              label={t('auth.advanced.rawBootstrap')}
              sub={t('auth.advanced.rawBootstrapSub')}
            />
            <div className="field-row field-row--2">
              <Field
                label={t('auth.bootstrap.captureToken')}
                help={t('auth.bootstrap.captureTokenHint')}
                tip={<HelpTip label={t('auth.bootstrap.captureToken')} text={t('auth.bootstrap.captureToken.tip')} />}
              >
                <input
                  className="input"
                  value={form.signupCaptureToken}
                  onChange={(e) => set('signupCaptureToken', e.target.value)}
                  placeholder={t('auth.tokenVar.autoPlaceholder')}
                  spellCheck={false}
                />
              </Field>
              <Field label={t('auth.bootstrap.captureSubject')} help={t('auth.bootstrap.captureSubjectHint')}>
                <input
                  className="input"
                  value={form.signupCaptureSubject}
                  onChange={(e) => set('signupCaptureSubject', e.target.value)}
                  placeholder="id"
                  spellCheck={false}
                />
              </Field>
            </div>
            {!simple && (
              <Field label={t('auth.bootstrap.start')} help={t('auth.bootstrap.startHint')}>
                <input
                  className="input"
                  value={form.signupStart}
                  onChange={(e) => set('signupStart', e.target.value)}
                  placeholder="signup"
                  spellCheck={false}
                />
              </Field>
            )}
          </div>
        </details>

        {/* Inside the fieldset: testing a signup CREATES a real account, so the
            button stays disabled until the non-production gate is confirmed. */}
        <AuthPreflight form={form} kind="signup" />
      </fieldset>
    </div>
  )
}

// AuthMintFields authors the mint (local JWT signing) strategy: pick the signing
// algorithm, point at the signing KEY by reference (an env var the server reads, or a
// file on the server — never the key itself, which would forge a token on the wire),
// and shape the token (a per-VU subject template, extra claims, and a lifetime). It
// leads with a "self-issued only" note — the #1 footgun is reaching for mint against a
// managed IdP (Auth0/Cognito/Firebase) whose signing key the operator does NOT hold.
// HS256 additionally needs the secret's encoding; the asymmetric algs read a PEM and
// hide that field. buildMintSpec turns these fields into the wire MintSpec.
function AuthMintFields({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  // Live parse of the claims JSON for an inline error (never throws out of render).
  let claimsError = ''
  if (form.mintClaimsJSON.trim()) {
    try {
      const parsed = JSON.parse(form.mintClaimsJSON)
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        claimsError = t('auth.mint.claimsNotObject')
      }
    } catch {
      claimsError = t('auth.mint.claimsInvalid')
    }
  }

  return (
    <div className="authpanel">
      <p className="card__hint">{t('auth.mint.lead')}</p>

      <div className="import__grid">
        <Field
          label={t('auth.mint.alg')}
          help={t('auth.mint.algHint')}
          tip={<HelpTip label={t('auth.mint.alg')} text={t('auth.mint.alg.tip')} />}
        >
          <select className="select" value={form.mintAlg} onChange={(e) => set('mintAlg', e.target.value as MintAlg)}>
            <option value="HS256">{t('auth.mint.alg.hs256')}</option>
            <option value="RS256">{t('auth.mint.alg.rs256')}</option>
            <option value="ES256">{t('auth.mint.alg.es256')}</option>
          </select>
        </Field>
        {form.mintAlg === 'HS256' && (
          <Field label={t('auth.mint.encoding')} help={t('auth.mint.encodingHint')}>
            <select
              className="select"
              value={form.mintSecretEncoding}
              onChange={(e) => set('mintSecretEncoding', e.target.value as MintEncoding)}
            >
              <option value="raw">{t('auth.mint.encoding.raw')}</option>
              <option value="base64">{t('auth.mint.encoding.base64')}</option>
              <option value="base64url">{t('auth.mint.encoding.base64url')}</option>
            </select>
          </Field>
        )}
      </div>

      <div className="import__grid">
        <Field
          label={t('auth.mint.keyEnv')}
          help={t('auth.mint.keyEnvHint')}
          tip={<HelpTip label={t('auth.mint.keyEnv')} text={t('auth.mint.key.tip')} />}
        >
          <input
            className="input"
            value={form.mintKeyEnv}
            onChange={(e) => set('mintKeyEnv', e.target.value)}
            placeholder={t('auth.mint.keyEnv.placeholder')}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label={t('auth.mint.keyFile')} help={t('auth.mint.keyFileHint')}>
          <input
            className="input"
            value={form.mintKeyFile}
            onChange={(e) => set('mintKeyFile', e.target.value)}
            placeholder={t('auth.mint.keyFile.placeholder')}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
      </div>

      <div className="import__grid">
        <Field
          label={t('auth.mint.subject')}
          help={t('auth.mint.subjectHint')}
          tip={<HelpTip label={t('auth.mint.subject')} text={t('auth.mint.subject.tip')} />}
        >
          <input
            className="input"
            value={form.mintSubject}
            onChange={(e) => set('mintSubject', e.target.value)}
            placeholder="user-{{.userIndex}}"
            spellCheck={false}
          />
        </Field>
        <Field label={t('auth.mint.ttl')} help={t('auth.mint.ttlHint')}>
          <input
            className="input"
            type="number"
            min={1}
            value={form.mintTtlSeconds}
            onChange={(e) => set('mintTtlSeconds', Math.max(1, Math.floor(Number(e.target.value) || 0)))}
          />
        </Field>
      </div>

      <Field
        label={t('auth.mint.claims')}
        help={t('auth.mint.claimsHint')}
        tip={<HelpTip label={t('auth.mint.claims')} text={t('auth.mint.claims.tip')} />}
      >
        <textarea
          className="textarea"
          value={form.mintClaimsJSON}
          onChange={(e) => set('mintClaimsJSON', e.target.value)}
          rows={4}
          placeholder={t('auth.mint.claims.placeholder')}
          spellCheck={false}
        />
      </Field>

      {claimsError && (
        <div className="authpanel__err" role="alert">
          <AlertIcon />
          <span>{claimsError}</span>
        </div>
      )}

      <AuthPreflight form={form} kind="token" />
    </div>
  )
}

// AuthExecFields authors the exec (bring-your-own-token) strategy: tmula runs an
// operator-supplied LOCAL command once per virtual user and reads the token from its
// stdout — the escape hatch for auth schemes none of the built-in strategies model. It is
// the most powerful and most dangerous option, so it is framed like bootstrap: a loud
// warning banner (the command runs locally and its egress is NOT bound by the target
// allowlist or rate cap) plus an explicit opt-in checkbox (execConfirmed) that gates the
// fields and the run. The opt-in is enforced again server-side by the --allow-exec flag;
// buildExecSpec re-checks the confirmation and rejects an empty command, a non-positive
// timeout, or malformed env. Secrets belong in the env (resolved on the server), never in
// argv. Both the command and env values may template {{.userIndex}} per virtual user.
function AuthExecFields({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  const confirmed = form.execConfirmed
  return (
    <div className="authpanel">
      <p className="authpanel__lead">{t('auth.exec.lead')}</p>

      <div className="authpanel__warn" role="alert">
        <AlertIcon />
        <label className="authpanel__confirm">
          <input
            className="check__box"
            type="checkbox"
            checked={confirmed}
            onChange={(e) => set('execConfirmed', e.target.checked)}
          />
          <span>
            <span className="authpanel__confirmLabel">{t('auth.exec.confirm')}</span>
            <span className="authpanel__confirmSub">{t('auth.exec.confirmSub')}</span>
          </span>
        </label>
      </div>

      <fieldset className="authpanel__fields" disabled={!confirmed}>
        <Field
          label={t('auth.exec.command')}
          help={t('auth.exec.commandHint')}
          tip={<HelpTip label={t('auth.exec.command')} text={t('auth.exec.command.tip')} />}
        >
          <textarea
            className="textarea"
            value={form.execCommandText}
            onChange={(e) => set('execCommandText', e.target.value)}
            rows={4}
            placeholder={'./fetch-token.sh\n--user\n{{.userIndex}}'}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>

        <Field
          label={t('auth.exec.env')}
          help={t('auth.exec.envHint')}
          tip={<HelpTip label={t('auth.exec.env')} text={t('auth.exec.env.tip')} />}
        >
          <textarea
            className="textarea"
            value={form.execEnvText}
            onChange={(e) => set('execEnvText', e.target.value)}
            rows={3}
            placeholder={'API_SECRET=...\nUSER_INDEX={{.userIndex}}'}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>

        <Field label={t('auth.exec.timeout')} help={t('auth.exec.timeoutHint')}>
          <input
            className="input"
            type="number"
            min={1}
            value={form.execTimeoutSeconds}
            onChange={(e) => set('execTimeoutSeconds', Math.max(1, Math.floor(Number(e.target.value) || 0)))}
          />
        </Field>
      </fieldset>
    </div>
  )
}

// AuthBootstrapAdvancedBody is the raw signup-steps JSON authoring, shown when the
// operator opts into advanced mode. It is the original power-user path preserved: a JSON
// array of signup requests with their own ids, methods, paths, bodies and extracts.
function AuthBootstrapAdvancedBody({
  form,
  set,
}: {
  form: ExperimentForm
  set: <K extends keyof ExperimentForm>(key: K, value: ExperimentForm[K]) => void
}) {
  const { t } = useI18n()
  return (
    <Field
      label={t('auth.bootstrap.steps')}
      help={t('auth.bootstrap.stepsHint')}
      tip={<HelpTip label={t('auth.bootstrap.steps')} text={t('auth.bootstrap.steps.tip')} />}
    >
      <textarea
        className="textarea"
        value={form.signupStepsJSON}
        onChange={(e) => set('signupStepsJSON', e.target.value)}
        rows={7}
        placeholder={signupStepsPlaceholder}
        spellCheck={false}
      />
    </Field>
  )
}

// HTTP_METHODS is the small set the simple login/signup mini-forms offer in their
// method selector — the verbs a login or signup realistically uses.
const HTTP_METHODS = ['POST', 'PUT', 'GET', 'PATCH', 'DELETE']

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
