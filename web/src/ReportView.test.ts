import { describe, expect, it } from 'vitest'
import type { Finding } from './api'
import { bucketWidthPct, formatPath, hasEvidence, metricFmt, sparklinePath, statusCountRows } from './ReportView'

describe('sparklinePath', () => {
  it('maps a series across the viewBox, low values near the bottom', () => {
    const d = sparklinePath(
      { name: 'cpu', points: [{ ts: 0, v: 0 }, { ts: 1000, v: 10 }] },
      240,
      36,
    )
    // First point: left edge, min value -> near the bottom (y = h - pad).
    expect(d.startsWith('M2.0,34.0')).toBe(true)
    // Last point: right edge, max value -> near the top.
    expect(d.endsWith('L238.0,2.0')).toBe(true)
  })

  it('draws a flat series mid-height instead of dividing by zero', () => {
    const d = sparklinePath(
      { name: 'flat', points: [{ ts: 0, v: 5 }, { ts: 1000, v: 5 }] },
      240,
      36,
    )
    expect(d).toContain(',18.0')
    expect(d).not.toContain('NaN')
  })

  it('handles a single point and an empty series', () => {
    expect(sparklinePath({ name: 'one', points: [{ ts: 5, v: 1 }] })).toBe('M120.0,18.0')
    expect(sparklinePath({ name: 'none', points: [] })).toBe('')
  })
})

describe('metricFmt', () => {
  it('scales magnitudes into short labels', () => {
    expect(metricFmt(1_234_567)).toBe('1.2M')
    expect(metricFmt(12_345)).toBe('12.3k')
    expect(metricFmt(123.4)).toBe('123')
    expect(metricFmt(0.3149)).toBe('0.31')
  })
})

// baseFinding builds a minimal finding so each test only spells out the fields it
// is about.
function baseFinding(overrides: Partial<Finding> = {}): Finding {
  return { runId: 'r1', category: 'http-5xx', severity: 'warning', description: 'errors', ...overrides }
}

describe('finding evidence model', () => {
  // A finding exactly as the server marshals domain.Finding — wire names included
  // ("vus"/"vu" for sessions, string-keyed statusCounts, RFC 3339 ts). This is the
  // same JSON the shared-report path serves after PII masking, so parsing it here
  // pins both the operator and the viewer contract.
  const wire = JSON.stringify({
    runId: 'r1',
    category: 'http-5xx',
    severity: 'critical',
    evidenceRef: 'checkout',
    firstSeen: '2026-06-11T08:00:00Z',
    description: '12 errors on checkout',
    count: 12,
    evidence: {
      vus: [
        {
          vu: 'r1-user-3-buyer',
          seed: 45,
          userIndex: 3,
          persona: 'buyer',
          path: ['browse', 'cart', 'checkout'],
          statusCode: 503,
          latencyMs: 812.5,
          errorClass: 'http_5xx',
          ts: '2026-06-11T08:00:02Z',
        },
        // A transport-level failure: no status code, error class only.
        { vu: 'r1-user-9', seed: 51, userIndex: 9, latencyMs: 30000, errorClass: 'timeout', ts: '2026-06-11T08:03:10Z' },
      ],
      timeBuckets: [
        { label: '0–25%', count: 9 },
        { label: '25–50%', count: 3 },
      ],
      statusCounts: { '503': 11, '500': 1 },
    },
  })

  it('parses the server wire shape, including count and reproduce coordinates', () => {
    const f = JSON.parse(wire) as Finding
    expect(f.count).toBe(12)
    const s = f.evidence?.vus?.[0]
    expect(s?.vu).toBe('r1-user-3-buyer')
    expect(s?.seed).toBe(45)
    expect(s?.userIndex).toBe(3)
    expect(s?.persona).toBe('buyer')
    expect(s?.path).toEqual(['browse', 'cart', 'checkout'])
    expect(s?.statusCode).toBe(503)
    expect(f.evidence?.statusCounts?.['503']).toBe(11)
    expect(f.evidence?.timeBuckets?.[0]).toEqual({ label: '0–25%', count: 9 })
    expect(hasEvidence(f)).toBe(true)
  })

  it('keeps the panel hidden for legacy findings without an evidence bundle', () => {
    // Pre-evidence findings carry neither count nor evidence — exactly what older
    // persisted reports replay. They must not grow a panel.
    const legacy = JSON.parse(
      '{"runId":"r1","category":"http-5xx","severity":"warning","firstSeen":"2026-06-11T08:00:00Z","description":"5 errors"}',
    ) as Finding
    expect(legacy.count).toBeUndefined()
    expect(legacy.evidence).toBeUndefined()
    expect(hasEvidence(legacy)).toBe(false)
  })

  it('treats an empty bundle as no evidence', () => {
    expect(hasEvidence(baseFinding({ evidence: {} }))).toBe(false)
  })

  it('shows the panel when any single section is populated', () => {
    expect(hasEvidence(baseFinding({ evidence: { statusCounts: { '500': 2 } } }))).toBe(true)
    expect(hasEvidence(baseFinding({ evidence: { timeBuckets: [{ label: '0–25%', count: 1 }] } }))).toBe(true)
    expect(hasEvidence(baseFinding({ evidence: { rootCauseClass: 'db-pool-exhausted' } }))).toBe(true)
  })
})

describe('statusCountRows', () => {
  it('turns the Go string-keyed map into rows sorted by code', () => {
    expect(statusCountRows({ '503': 11, '404': 2, '500': 1 })).toEqual([
      { code: '404', count: 2 },
      { code: '500', count: 1 },
      { code: '503', count: 11 },
    ])
  })

  it('returns an empty list when the map is absent or empty', () => {
    expect(statusCountRows(undefined)).toEqual([])
    expect(statusCountRows({})).toEqual([])
  })
})

describe('formatPath', () => {
  it('joins the node chain with arrows', () => {
    expect(formatPath(['browse', 'cart', 'checkout'])).toBe('browse → cart → checkout')
  })

  it('renders a dash when the producing path carried no journey', () => {
    expect(formatPath(undefined)).toBe('—')
    expect(formatPath([])).toBe('—')
  })
})

describe('bucketWidthPct', () => {
  it('scales a bucket against the densest one, keeping small counts visible', () => {
    expect(bucketWidthPct(9, 9)).toBe(100)
    expect(bucketWidthPct(3, 9)).toBe(33)
    // A tiny-but-nonzero bucket still draws a sliver instead of disappearing.
    expect(bucketWidthPct(1, 1000)).toBe(4)
  })

  it('draws nothing for an empty bucket or an empty window', () => {
    expect(bucketWidthPct(0, 9)).toBe(0)
    expect(bucketWidthPct(5, 0)).toBe(0)
  })
})
