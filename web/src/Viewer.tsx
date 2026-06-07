import { useEffect, useState } from 'react'
import { getSharedReport, SharedReportError, type Report } from './api'
import { useI18n } from './i18n'
import ReportView from './ReportView'

// Viewer is the read-only shared-report page. It has no run controls — a holder
// of the share token can read the (PII-masked) report and nothing else. Errors
// come back as a SharedReportError carrying a stable code, which is mapped to a
// localized message here so the viewer is bilingual too.
export default function Viewer({ token }: { token: string }) {
  const { t } = useI18n()
  const [report, setReport] = useState<Report | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    getSharedReport(token)
      .then((r) => active && setReport(r))
      .catch((e) => {
        if (!active) return
        if (e instanceof SharedReportError) {
          if (e.code === 'expired') setError(t('viewer.expired'))
          else if (e.code === 'notFound') setError(t('viewer.notFound'))
          else setError(t('viewer.unavailable', { status: e.status }))
        } else {
          setError(String(e instanceof Error ? e.message : e))
        }
      })
    return () => {
      active = false
    }
  }, [token, t])

  return (
    <main className="app app--narrow">
      <header className="masthead">
        <span className="brand">
          <span className="brand__mark" aria-hidden="true">
            <Glyph />
          </span>
          <span>
            <h1 className="brand__name">tmula</h1>
            <p className="brand__tag">{t('viewer.tagline')}</p>
          </span>
        </span>
      </header>

      <p className="viewer-note">{t('viewer.note')}</p>

      {error && (
        <div className="alert" role="alert">
          <span>{error}</span>
        </div>
      )}
      {!error && !report && <p style={{ color: 'var(--text-muted)' }}>{t('viewer.loading')}</p>}

      {report && (
        <section className="card">
          <div className="runhead">
            <h2 className="runhead__title">{t('run.title')}</h2>
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
