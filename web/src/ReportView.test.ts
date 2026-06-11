import { describe, expect, it } from 'vitest'
import { metricFmt, sparklinePath } from './ReportView'

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
