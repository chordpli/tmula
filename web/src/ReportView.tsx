import type { Report, Stats } from './api'

const sevColor: Record<string, string> = { critical: '#d73a4a', warning: '#d4a72c', info: '#0969da' }

export function StatsView({ stats }: { stats: Stats }) {
  return (
    <ul style={{ lineHeight: 1.7 }}>
      <li>requests: {stats.total}</li>
      <li>error rate: {(stats.errorRate * 100).toFixed(1)}%</li>
      <li>
        latency p50/p95/p99: {stats.p50.toFixed(0)} / {stats.p95.toFixed(0)} / {stats.p99.toFixed(0)} ms
      </li>
      <li>timeouts: {stats.timeouts}</li>
    </ul>
  )
}

// ReportView renders a run report read-only: it is shared by the operator view
// and the viewer (shared-link) page so both stay consistent.
export default function ReportView({ report }: { report: Report }) {
  return (
    <div>
      <StatsView stats={report.stats} />
      <h3>Findings ({report.findings.length})</h3>
      {report.findings.length === 0 ? (
        <p style={{ color: '#1a7f37' }}>No issues detected.</p>
      ) : (
        <ul style={{ lineHeight: 1.7 }}>
          {report.findings.map((f, i) => (
            <li key={i}>
              <span style={{ color: sevColor[f.severity] ?? '#555', fontWeight: 600 }}>[{f.category}]</span>{' '}
              {f.description}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
