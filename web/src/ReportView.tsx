import type { Report, Stats } from './api'
import { useI18n } from './i18n'

// errorRateKind picks a stat color by how alarming the error rate is, so a glance
// reads green/amber/red without needing to parse the number.
function errorRateKind(rate: number): '' | 'warn' | 'danger' {
  if (rate >= 0.05) return 'danger'
  if (rate > 0) return 'warn'
  return ''
}

// StatsView renders the headline metrics as a compact card grid: requests, error
// rate, latency percentiles, and timeouts. Shared by the live run, the report, and
// the shared-link viewer so all three stay consistent. Only the chrome (labels,
// units, the error sub-line) is translated — the numbers are formatted with
// toLocaleString so thousands separators follow the operator's locale.
export function StatsView({ stats }: { stats: Stats }) {
  const { t } = useI18n()
  const errKind = errorRateKind(stats.errorRate)
  const errorsLine =
    stats.errors === 1
      ? t('stat.errorsOne', { count: stats.errors.toLocaleString() })
      : t('stat.errorsMany', { count: stats.errors.toLocaleString() })
  return (
    <div className="statgrid">
      <div className="stat">
        <div className="stat__label">{t('stat.requests')}</div>
        <div className="stat__value">{stats.total.toLocaleString()}</div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.errorRate')}</div>
        <div className={`stat__value${errKind ? ` stat__value--${errKind}` : ' stat__value--ok'}`}>
          {(stats.errorRate * 100).toFixed(1)}
          <span className="stat__unit">%</span>
        </div>
        <div className="stat__sub">{errorsLine}</div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.p50')}</div>
        <div className="stat__value">
          {stats.p50.toFixed(0)}
          <span className="stat__unit">ms</span>
        </div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.p95')}</div>
        <div className="stat__value">
          {stats.p95.toFixed(0)}
          <span className="stat__unit">ms</span>
        </div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.p99')}</div>
        <div className="stat__value">
          {stats.p99.toFixed(0)}
          <span className="stat__unit">ms</span>
        </div>
        <div className="stat__sub">{t('stat.max', { ms: stats.max.toFixed(0) })}</div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.timeouts')}</div>
        <div className={`stat__value${stats.timeouts > 0 ? ' stat__value--warn' : ''}`}>
          {stats.timeouts.toLocaleString()}
        </div>
      </div>
    </div>
  )
}

// ReportView renders a run report read-only: it is shared by the operator view
// and the viewer (shared-link) page so both stay consistent. The findings list
// shows backend-provided text verbatim (it is data); only the heading and the
// empty-state line are translated.
export default function ReportView({ report }: { report: Report }) {
  const { t } = useI18n()
  // A Go nil slice marshals to JSON null, so default to an empty list.
  const findings = report.findings ?? []
  return (
    <div>
      <StatsView stats={report.stats} />

      <div className="findings__head" style={{ marginTop: 22 }}>
        <h3 className="findings__title">{t('findings.title')}</h3>
        <span className="findings__count">{findings.length}</span>
      </div>

      {findings.length === 0 ? (
        <div className="findings__empty">
          <CheckIcon />
          {t('findings.empty')}
        </div>
      ) : (
        <div>
          {findings.map((f, i) => {
            const sev = (f.severity || 'info').toLowerCase()
            const sevClass = sev === 'critical' || sev === 'warning' ? sev : 'info'
            return (
              <div className="finding" key={i}>
                <span className={`finding__sev finding__sev--${sevClass}`}>{sev}</span>
                <span>
                  <span className="finding__cat">[{f.category}]</span> <span className="finding__desc">{f.description}</span>
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function CheckIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2" />
      <path d="M8.5 12.2l2.3 2.3 4.7-4.9" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
