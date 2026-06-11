import type { MetricSeries, OutcomeSummary, Report, Stats } from './api'
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

// OutcomeView renders the journey-outcome headline — the completion rate (journeys
// that reached done) and the drop-off rate (journeys that left at exit) — in the
// same stat-card grid as StatsView so it reads as part of the run's headline
// metrics. The summary is accumulated client-side from the live flow/trace stream
// (the report API carries no terminal aggregates), so callers render this only
// when a summary with started journeys exists; the shared-link viewer has none.
export function OutcomeView({ outcome }: { outcome: OutcomeSummary }) {
  const { t } = useI18n()
  const vars = (count: number) => ({
    count: count.toLocaleString(),
    started: outcome.started.toLocaleString(),
  })
  return (
    <div className="statgrid" style={{ marginTop: 10 }}>
      <div className="stat">
        <div className="stat__label">{t('stat.completionRate')}</div>
        {/* Completion is the positive outcome (the done node's calm green). */}
        <div className="stat__value stat__value--ok">
          {(outcome.completionRate * 100).toFixed(1)}
          <span className="stat__unit">%</span>
        </div>
        <div className="stat__sub">{t('stat.completionSub', vars(outcome.completed))}</div>
      </div>
      <div className="stat">
        <div className="stat__label">{t('stat.dropOffRate')}</div>
        {/* A drop-off is normal user behavior, not an error — keep it neutral. */}
        <div className="stat__value">
          {(outcome.dropOffRate * 100).toFixed(1)}
          <span className="stat__unit">%</span>
        </div>
        <div className="stat__sub">{t('stat.dropOffSub', vars(outcome.dropped))}</div>
      </div>
    </div>
  )
}

// ReportView renders a run report read-only: it is shared by the operator view
// and the viewer (shared-link) page so both stay consistent. The findings list
// shows backend-provided text verbatim (it is data); only the heading and the
// empty-state line are translated. `outcome` is the optional journey-outcome
// headline streamed live by the operator console; the viewer has no stream, so
// it simply omits the prop and the cards.
export default function ReportView({
  report,
  outcome,
}: {
  report: Report
  outcome?: OutcomeSummary | null
}) {
  const { t } = useI18n()
  // A Go nil slice marshals to JSON null, so default to an empty list.
  const findings = report.findings ?? []
  const serverMetrics = report.serverMetrics ?? []
  return (
    <div>
      <StatsView stats={report.stats} />
      {outcome && outcome.started > 0 && <OutcomeView outcome={outcome} />}

      {(serverMetrics.length > 0 || report.metricsError) && (
        <div style={{ marginTop: 22 }}>
          <div className="findings__head">
            <h3 className="findings__title">{t('metrics.title')}</h3>
            <span className="findings__count">{serverMetrics.length}</span>
          </div>
          {report.metricsError && (
            <div className="finding">
              <span className="finding__sev finding__sev--warning">warn</span>
              <span className="finding__desc">
                {t('metrics.fetchError')} {report.metricsError}
              </span>
            </div>
          )}
          {serverMetrics.map((s, i) => (
            <Sparkline key={i} series={s} />
          ))}
        </div>
      )}

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

// metricFmt renders a sample value compactly (1.2M / 3.4k / 0.31), so a series
// label stays one short line whatever the metric's magnitude.
export function metricFmt(v: number): string {
  const a = Math.abs(v)
  if (a >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return (v / 1e3).toFixed(1) + 'k'
  if (a >= 100) return v.toFixed(0)
  return String(Math.round(v * 100) / 100)
}

// sparklinePath maps a series onto an SVG path across a fixed viewBox, scaling
// x to the time span and y to the value range (a flat series draws mid-height).
// Exported for tests.
export function sparklinePath(series: MetricSeries, w = 240, h = 36): string {
  const pts = series.points
  if (pts.length === 0) return ''
  const t0 = pts[0].ts
  const t1 = pts[pts.length - 1].ts
  let vMin = Infinity
  let vMax = -Infinity
  for (const p of pts) {
    if (p.v < vMin) vMin = p.v
    if (p.v > vMax) vMax = p.v
  }
  const pad = 2
  const x = (ts: number) => (t1 === t0 ? w / 2 : pad + ((ts - t0) / (t1 - t0)) * (w - 2 * pad))
  const y = (v: number) =>
    vMax === vMin ? h / 2 : h - pad - ((v - vMin) / (vMax - vMin)) * (h - 2 * pad)
  return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.ts).toFixed(1)},${y(p.v).toFixed(1)}`).join(' ')
}

// Sparkline draws one fetched server-side series as a small inline chart with
// its name and min/last/max, sharing the run's wall-clock window so it reads
// against the latency timeline above it.
function Sparkline({ series }: { series: MetricSeries }) {
  const pts = series.points
  if (pts.length === 0) return null
  let vMin = Infinity
  let vMax = -Infinity
  for (const p of pts) {
    if (p.v < vMin) vMin = p.v
    if (p.v > vMax) vMax = p.v
  }
  const last = pts[pts.length - 1].v
  return (
    <div className="finding" style={{ alignItems: 'center', gap: 12 }}>
      <svg width="240" height="36" viewBox="0 0 240 36" aria-hidden="true" style={{ flex: 'none' }}>
        <path d={sparklinePath(series)} fill="none" stroke="currentColor" strokeWidth="1.5" opacity="0.8" />
      </svg>
      <span>
        <span className="finding__cat">{series.name}</span>{' '}
        <span className="finding__desc">
          min {metricFmt(vMin)} · last {metricFmt(last)} · max {metricFmt(vMax)}
        </span>
      </span>
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
