import { describe, it, expect } from 'vitest'
import { buildRunSpec, parseSSEData, shareTokenFromQuery, type ExperimentForm } from './api'

const form: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost, 127.0.0.1 ',
  users: 3,
  maxSteps: 5,
  start: 'a',
  graphJSON: '{"id":"g","nodes":[{"id":"a"}],"edges":[]}',
  templatesJSON: '{"ta":{"method":"GET","path":"/a"}}',
  workers: '',
  aggregateWorkers: false,
  workloadKind: 'closed',
  arrivalRate: 50,
  durationSeconds: 10,
  maxConcurrency: 500,
  thinkMinMs: 0,
  thinkMaxMs: 0,
  segmentsJSON: '',
}

describe('buildRunSpec', () => {
  it('creates one virtual user per requested user', () => {
    const spec = buildRunSpec(form)
    expect(spec.users).toHaveLength(3)
    expect(spec.users[0]).toEqual({ id: 'u0' })
    expect(spec.start).toBe('a')
    expect(spec.maxSteps).toBe(5)
  })

  it('trims and splits the allowlist', () => {
    const spec = buildRunSpec(form) as { targetEnv: { allowlist: string[] } }
    expect(spec.targetEnv.allowlist).toEqual(['localhost', '127.0.0.1'])
  })

  it('throws on invalid graph JSON', () => {
    expect(() => buildRunSpec({ ...form, graphJSON: 'not json' })).toThrow()
  })

  it('includes trimmed worker addresses when provided', () => {
    const spec = buildRunSpec({ ...form, workers: ' 127.0.0.1:9101 , 127.0.0.1:9102 ' })
    expect(spec.workers).toEqual(['127.0.0.1:9101', '127.0.0.1:9102'])
  })

  it('attaches aggregateWorkers only with workers set', () => {
    // No workers → flag never attaches even if requested.
    expect(buildRunSpec({ ...form, aggregateWorkers: true }).aggregateWorkers).toBeUndefined()
    // Workers + flag → attaches.
    const spec = buildRunSpec({ ...form, workers: '127.0.0.1:9101', aggregateWorkers: true })
    expect(spec.workers).toEqual(['127.0.0.1:9101'])
    expect(spec.aggregateWorkers).toBe(true)
    // Workers without the flag → omitted (default streaming path).
    expect(buildRunSpec({ ...form, workers: '127.0.0.1:9101' }).aggregateWorkers).toBeUndefined()
  })

  it('omits workers when the field is blank or only separators', () => {
    expect(buildRunSpec({ ...form, workers: '' }).workers).toBeUndefined()
    expect(buildRunSpec({ ...form, workers: '  ' }).workers).toBeUndefined()
    expect(buildRunSpec({ ...form, workers: ' , , ' }).workers).toBeUndefined()
  })

  it('omits the workload for the closed model', () => {
    expect(buildRunSpec(form).workload).toBeUndefined()
  })

  it('attaches an open workload when selected', () => {
    const spec = buildRunSpec({
      ...form,
      workloadKind: 'open',
      arrivalRate: 100,
      durationSeconds: 30,
      maxConcurrency: 1000,
      thinkMinMs: 100,
      thinkMaxMs: 500,
    })
    expect(spec.workload).toEqual({
      kind: 'open',
      arrival: { shape: 'constant', startRate: 100, peakRate: 100 },
      durationSeconds: 30,
      maxConcurrency: 1000,
      thinkTime: { minMs: 100, maxMs: 500 },
    })
  })

  it('omits segments when blank or on the closed model', () => {
    expect(buildRunSpec({ ...form, workloadKind: 'open' }).segments).toBeUndefined()
    const withMix = '[{"name":"a","weight":1}]'
    // Closed model ignores the persona mix entirely.
    expect(buildRunSpec({ ...form, workloadKind: 'closed', segmentsJSON: withMix }).segments).toBeUndefined()
  })

  it('attaches the persona mix for an open run', () => {
    const spec = buildRunSpec({
      ...form,
      workloadKind: 'open',
      segmentsJSON: '[{"name":"browser","weight":0.7,"start":"a"},{"name":"buyer","weight":0.3,"start":"b"}]',
    })
    expect(spec.segments).toEqual([
      { name: 'browser', weight: 0.7, start: 'a' },
      { name: 'buyer', weight: 0.3, start: 'b' },
    ])
  })

  it('throws on invalid segments JSON', () => {
    expect(() => buildRunSpec({ ...form, workloadKind: 'open', segmentsJSON: 'not json' })).toThrow()
    expect(() => buildRunSpec({ ...form, workloadKind: 'open', segmentsJSON: '{"name":"a"}' })).toThrow()
  })
})

describe('parseSSEData', () => {
  it('parses a data line', () => {
    const frame = parseSSEData('data: {"status":"running","stats":{"total":2}}')
    expect(frame?.status).toBe('running')
    expect(frame?.stats?.total).toBe(2)
  })

  it('ignores non-data and malformed lines', () => {
    expect(parseSSEData('')).toBeNull()
    expect(parseSSEData(': comment')).toBeNull()
    expect(parseSSEData('data: {bad json')).toBeNull()
    expect(parseSSEData('event: ping')).toBeNull()
  })
})

describe('shareTokenFromQuery', () => {
  it('extracts a share token', () => {
    expect(shareTokenFromQuery('?share=abc123')).toBe('abc123')
    expect(shareTokenFromQuery('?foo=1&share=tok')).toBe('tok')
  })

  it('returns null when absent or blank', () => {
    expect(shareTokenFromQuery('')).toBeNull()
    expect(shareTokenFromQuery('?foo=1')).toBeNull()
    expect(shareTokenFromQuery('?share=')).toBeNull()
  })
})
