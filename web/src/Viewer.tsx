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
    <main style={{ fontFamily: 'system-ui, sans-serif', maxWidth: 720, margin: '2rem auto', padding: '0 1rem' }}>
      <h1>tmula — shared report</h1>
      <p style={{ color: '#777', fontSize: 13 }}>Read-only. Sensitive fields are redacted.</p>
      {error && <p style={{ color: '#d73a4a' }}>{error}</p>}
      {!error && !report && <p>Loading…</p>}
      {report && (
        <>
          <h2>
            Run {report.run.id} — {report.run.status}
          </h2>
          <ReportView report={report} />
        </>
      )}
    </main>
  )
}
