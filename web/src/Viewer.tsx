import { useEffect, useState } from 'react'
import { getSharedReport, type Report } from './api'
import ReportView from './ReportView'

// Viewer is the read-only shared-report page. It has no run controls — a holder
// of the share token can read the (PII-masked) report and nothing else.
export default function Viewer({ token }: { token: string }) {
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    getSharedReport(token)
      .then((r) => active && setReport(r))
      .catch((e) => active && setError(String(e instanceof Error ? e.message : e)))
    return () => {
      active = false
    }
  }, [token])

  return (
    <main className="app app--narrow">
      <header className="masthead">
        <span className="brand">
          <span className="brand__mark" aria-hidden="true">
            <Glyph />
          </span>
          <span>
            <h1 className="brand__name">tmula</h1>
            <p className="brand__tag">Shared report</p>
          </span>
        </span>
      </header>

      <p className="viewer-note">Read-only. Sensitive fields are redacted.</p>

      {error && (
        <div className="alert" role="alert">
          <span>{error}</span>
        </div>
      )}
      {!error && !report && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}

      {report && (
        <section className="card">
          <div className="runhead">
            <h2 className="runhead__title">Run</h2>
            <span className="runhead__id">{report.run.id}</span>
            <span className="runhead__mode">· {report.run.status}</span>
          </div>
          <ReportView report={report} />
        </section>
      )}
    </main>
  )
}

function Glyph() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect x="3" y="13" width="4.5" height="8" rx="1.4" fill="currentColor" opacity="0.7" />
      <rect x="9.75" y="8" width="4.5" height="13" rx="1.4" fill="currentColor" opacity="0.85" />
      <rect x="16.5" y="3" width="4.5" height="18" rx="1.4" fill="currentColor" />
    </svg>
  )
}
