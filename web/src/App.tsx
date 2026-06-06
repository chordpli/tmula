import type React from 'react'
import { useEffect, useRef, useState } from 'react'
import {
  buildRunSpec,
  createExperiment,
  getReport,
  killRun,
  shareTokenFromQuery,
  startRun,
  streamURL,
  type ExperimentForm,
  type Report,
  type Stats,
} from './api'
import ReportView, { StatsView } from './ReportView'
import Viewer from './Viewer'

const defaultGraph = JSON.stringify(
  {
    id: 'checkout',
    nodes: [
      { id: 'browse', apiTemplateId: 't_browse' },
      { id: 'cart', apiTemplateId: 't_cart' },
      { id: 'pay', apiTemplateId: 't_pay' },
    ],
    edges: [
      { from: 'browse', to: 'cart', weight: 0.8 },
      { from: 'cart', to: 'pay', weight: 0.9, dependency: true },
    ],
  },
  null,
  2,
)

const defaultTemplates = JSON.stringify(
  {
    t_browse: { method: 'GET', path: '/browse' },
    t_cart: { method: 'POST', path: '/cart', payloadTemplate: '{"item":"x"}' },
    t_pay: { method: 'POST', path: '/pay', payloadTemplate: '{"amount":10}' },
  },
  null,
  2,
)

const initialForm: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost, 127.0.0.1',
  users: 20,
  maxSteps: 8,
  start: 'browse',
  graphJSON: defaultGraph,
  templatesJSON: defaultTemplates,
  workers: '',
}

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

  return (
    <main style={{ fontFamily: 'system-ui, sans-serif', maxWidth: 880, margin: '2rem auto', padding: '0 1rem' }}>
      <h1>tmula</h1>
      <p style={{ color: '#555' }}>Real-user traffic simulator — configure an experiment and run it.</p>

      <section style={{ display: 'grid', gap: '0.75rem' }}>
        <Field label="Target base URL">
          <input value={form.baseUrl} onChange={(e) => set('baseUrl', e.target.value)} style={inp} />
        </Field>
        <Field label="Allowlist (comma-separated hosts)">
          <input value={form.allowlist} onChange={(e) => set('allowlist', e.target.value)} style={inp} />
        </Field>
        <Field label="Worker addresses (comma-separated, blank = local)">
          <input
            value={form.workers}
            onChange={(e) => set('workers', e.target.value)}
            placeholder="e.g. 127.0.0.1:9101, 127.0.0.1:9102"
            style={inp}
          />
        </Field>
        <div style={{ display: 'flex', gap: '1rem' }}>
          <Field label="Virtual users">
            <input
              type="number"
              min={1}
              value={form.users}
              onChange={(e) => set('users', Math.max(1, Number(e.target.value) || 1))}
              style={inp}
            />
          </Field>
          <Field label="Max steps">
            <input
              type="number"
              min={1}
              value={form.maxSteps}
              onChange={(e) => set('maxSteps', Math.max(1, Number(e.target.value) || 1))}
              style={inp}
            />
          </Field>
          <Field label="Start node">
            <input value={form.start} onChange={(e) => set('start', e.target.value)} style={inp} />
          </Field>
        </div>
        <Field label="Scenario graph (JSON)">
          <textarea value={form.graphJSON} onChange={(e) => set('graphJSON', e.target.value)} rows={10} style={ta} />
        </Field>
        <Field label="API templates (JSON)">
          <textarea
            value={form.templatesJSON}
            onChange={(e) => set('templatesJSON', e.target.value)}
            rows={8}
            style={ta}
          />
        </Field>
        <div>
          <button
            onClick={run}
            disabled={status === 'starting' || status === 'running'}
            style={{ ...btn, opacity: status === 'starting' || status === 'running' ? 0.5 : 1 }}
          >
            Run experiment
          </button>
          {runId && status === 'running' && (
            <button
              onClick={() => killRun(runId).catch((e) => setError(String(e)))}
              style={{ ...btn, background: '#d73a4a', marginLeft: 8 }}
            >
              Kill
            </button>
          )}
        </div>
      </section>

      {error && <p style={{ color: '#d73a4a' }}>{error}</p>}

      {status && (
        <section style={{ marginTop: '1.5rem' }}>
          <h2>
            Run {runId} — <span>{status}</span>
            {runMode && <span style={{ color: '#555', fontWeight: 400, fontSize: 14 }}> · {runMode}</span>}
          </h2>
          {stats && <StatsView stats={stats} />}
        </section>
      )}

      {report && (
        <section style={{ marginTop: '1rem' }}>
          <ReportView report={report} />
        </section>
      )}
    </main>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block' }}>
      <span style={{ display: 'block', fontSize: 13, color: '#444', marginBottom: 4 }}>{label}</span>
      {children}
    </label>
  )
}

const inp: React.CSSProperties = { width: '100%', padding: '6px 8px', border: '1px solid #ccc', borderRadius: 6 }
const ta: React.CSSProperties = { ...inp, fontFamily: 'ui-monospace, monospace', fontSize: 13 }
const btn: React.CSSProperties = {
  padding: '8px 16px',
  background: '#1f6feb',
  color: 'white',
  border: 'none',
  borderRadius: 6,
  cursor: 'pointer',
}
