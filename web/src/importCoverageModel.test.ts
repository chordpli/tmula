import { describe, expect, it } from 'vitest'
import { coverageFromStats, MAX_COVERAGE_SAMPLES } from './importCoverageModel'

describe('coverageFromStats', () => {
  it('returns null when the response carries no stats (old servers, spec imports)', () => {
    // Backward compatibility: a pre-stats server (or an OpenAPI/HAR conversion,
    // which has no coverage to report) yields no panel at all.
    expect(coverageFromStats(undefined)).toBeNull()
    expect(coverageFromStats(null)).toBeNull()
  })

  it('returns null for malformed stats values instead of throwing', () => {
    expect(coverageFromStats(42)).toBeNull()
    expect(coverageFromStats('stats')).toBeNull()
    expect(coverageFromStats([1, 2, 3])).toBeNull()
    expect(coverageFromStats(true)).toBeNull()
  })

  it('returns null when no input lines were observed at all', () => {
    // A zero-line report has nothing honest to say; stay quiet rather than
    // rendering "0 requests used" noise.
    expect(coverageFromStats({})).toBeNull()
    expect(coverageFromStats({ requests: 0, skipped: 0, sessions: 0, clients: 0 })).toBeNull()
  })

  it('builds a full report from complete stats', () => {
    const report = coverageFromStats({
      requests: 120,
      skipped: 7,
      sessions: 32,
      clients: 21,
      droppedEndpoints: 3,
      skippedSamples: [{ line: 14, text: 'GARBAGE LINE', reason: 'unparsable line' }],
    })
    expect(report).not.toBeNull()
    expect(report?.requests).toBe(120)
    expect(report?.skipped).toBe(7)
    expect(report?.sessions).toBe(32)
    expect(report?.clients).toBe(21)
    expect(report?.droppedEndpoints).toBe(3)
    expect(report?.totalLines).toBe(127)
    expect(report?.skippedPct).toBe(6) // 7/127 ≈ 5.5% → rounds up to 6
    expect(report?.partial).toBe(true)
    expect(report?.samples).toEqual([{ line: 14, text: 'GARBAGE LINE', reason: 'unparsable line' }])
  })

  it('reports a clean import as not partial', () => {
    const report = coverageFromStats({ requests: 50, skipped: 0, sessions: 5, clients: 4 })
    expect(report?.partial).toBe(false)
    expect(report?.skippedPct).toBe(0)
    expect(report?.samples).toEqual([])
  })

  it('flags partial coverage whenever lines were skipped, even without samples', () => {
    const report = coverageFromStats({ requests: 10, skipped: 1 })
    expect(report?.partial).toBe(true)
    expect(report?.totalLines).toBe(11)
  })

  it('never rounds a real skip down to 0% (the warning must justify itself)', () => {
    const report = coverageFromStats({ requests: 9999, skipped: 1 })
    expect(report?.partial).toBe(true)
    expect(report?.skippedPct).toBe(1)
  })

  it('defaults missing or junk numeric fields to 0', () => {
    const report = coverageFromStats({
      requests: 5,
      skipped: 'many', // junk type
      sessions: -3, // negative is meaningless
      clients: Number.NaN,
      droppedEndpoints: Number.POSITIVE_INFINITY,
    })
    expect(report?.requests).toBe(5)
    expect(report?.skipped).toBe(0)
    expect(report?.sessions).toBe(0)
    expect(report?.clients).toBe(0)
    expect(report?.droppedEndpoints).toBe(0)
    expect(report?.partial).toBe(false)
  })

  it('truncates fractional counts to whole numbers', () => {
    const report = coverageFromStats({ requests: 12.9, skipped: 2.2 })
    expect(report?.requests).toBe(12)
    expect(report?.skipped).toBe(2)
  })

  it('tolerates sample rows with missing fields and filters out junk entries', () => {
    const report = coverageFromStats({
      requests: 10,
      skipped: 4,
      skippedSamples: [
        { text: 'no line number', reason: 'asset' }, // missing line → null
        { line: 7 }, // bare line number is still useful
        'not an object', // junk: dropped
        null, // junk: dropped
        { line: -2, text: '', reason: '' }, // nothing usable: dropped
      ],
    })
    expect(report?.samples).toEqual([
      { line: null, text: 'no line number', reason: 'asset' },
      { line: 7, text: '', reason: '' },
    ])
  })

  it('caps the rendered sample rows', () => {
    const samples = Array.from({ length: MAX_COVERAGE_SAMPLES + 5 }, (_, i) => ({
      line: i + 1,
      text: `line ${i + 1}`,
      reason: 'unparsable line',
    }))
    const report = coverageFromStats({ requests: 100, skipped: 20, skippedSamples: samples })
    expect(report?.samples).toHaveLength(MAX_COVERAGE_SAMPLES)
    expect(report?.samples[0].line).toBe(1)
  })

  it('truncates very long sample text so one pathological line cannot flood the table', () => {
    const report = coverageFromStats({
      requests: 1,
      skipped: 1,
      skippedSamples: [{ line: 1, text: 'x'.repeat(500), reason: 'unparsable line' }],
    })
    const text = report?.samples[0].text ?? ''
    expect(text.length).toBeLessThanOrEqual(161) // 160 chars + ellipsis
    expect(text.endsWith('…')).toBe(true)
  })
})
