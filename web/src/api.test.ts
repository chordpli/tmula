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

  it('omits workers when the field is blank or only separators', () => {
    expect(buildRunSpec({ ...form, workers: '' }).workers).toBeUndefined()
    expect(buildRunSpec({ ...form, workers: '  ' }).workers).toBeUndefined()
    expect(buildRunSpec({ ...form, workers: ' , , ' }).workers).toBeUndefined()
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
