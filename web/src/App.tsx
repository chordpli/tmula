import type React from 'react'
import { useEffect, useRef, useState } from 'react'
import {
  buildRunSpec,
  createExperiment,
  getReport,
  killRun,
  startRun,
  streamURL,
  type ExperimentForm,
  type Report,
  type Stats,
} from './api'

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
}

export default function App() {
  const [form, setForm] = useState<ExperimentForm>(initialForm)
  const [runId, setRunId] = useState<string>('')
  const [status, setStatus] = useState<string>('')
  const [stats, setStats] = useState<Stats | null>(null)
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState<string>('')
  const esRef = useRef<EventSource | null>(null)

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
      const expId = await createExperiment(spec)
      const id = await startRun(expId)
      setRunId(id)
      listen(id)
    } catch (e) {
      setStatus('')
      setError(String(e))
    }
  }

  function listen(id: string) {
    esRef.current?.close()
    const es = new EventSource(streamURL(id))
    esRef.current = es
    es.onmessage = (ev) => {
      try {
        const frame = JSON.parse(ev.data) as { status?: string; stats?: Stats }
        if (frame.status) setStatus(frame.status)
        if (frame.stats) setStats(frame.stats)
        if (frame.status && frame.status !== 'running' && frame.status !== 'pending') {
          es.close()
          getReport(id).then(setReport).catch((e) => setError(String(e)))
        }
      } catch {
        /* ignore malformed frame */
      }
    }
    es.onerror = () => es.close()
  }

  const sev: Record<string, string> = { critical: '#d73a4a', warning: '#d4a72c', info: '#0969da' }

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
        <div style={{ display: 'flex', gap: '1rem' }}>
          <Field label="Virtual users">
            <input
              type="number"
              value={form.users}
              onChange={(e) => set('users', Number(e.target.value))}
              style={inp}
            />
          </Field>
          <Field label="Max steps">
            <input
              type="number"
              value={form.maxSteps}
              onChange={(e) => set('maxSteps', Number(e.target.value))}
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
          <button onClick={run} style={btn}>
            Run experiment
          </button>
          {runId && status === 'running' && (
            <button onClick={() => killRun(runId)} style={{ ...btn, background: '#d73a4a', marginLeft: 8 }}>
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
          </h2>
          {stats && (
            <ul style={{ lineHeight: 1.7 }}>
              <li>requests: {stats.total}</li>
              <li>error rate: {(stats.errorRate * 100).toFixed(1)}%</li>
              <li>
                latency p50/p95/p99: {stats.p50.toFixed(0)} / {stats.p95.toFixed(0)} / {stats.p99.toFixed(0)} ms
              </li>
              <li>timeouts: {stats.timeouts}</li>
            </ul>
          )}
        </section>
      )}

      {report && (
        <section style={{ marginTop: '1rem' }}>
          <h3>Findings ({report.findings.length})</h3>
          {report.findings.length === 0 ? (
            <p style={{ color: '#1a7f37' }}>No issues detected.</p>
          ) : (
            <ul style={{ lineHeight: 1.7 }}>
              {report.findings.map((f, i) => (
                <li key={i}>
                  <span style={{ color: sev[f.severity] ?? '#555', fontWeight: 600 }}>[{f.category}]</span>{' '}
                  {f.description}
                </li>
              ))}
            </ul>
          )}
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
