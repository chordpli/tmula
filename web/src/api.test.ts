import { describe, it, expect } from 'vitest'
import {
  buildRunSpec,
  compareURL,
  layoutGraph,
  parseSSEData,
  parseSegments,
  parseTraceFrame,
  reportHTMLURL,
  runDisabled,
  shareTokenFromQuery,
  traceURL,
  type ExperimentForm,
} from './api'

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
  traceEnabled: false,
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

  it('attaches trace only when enabled and the run is small (closed: users)', () => {
    // Disabled → never attaches.
    expect(buildRunSpec({ ...form, users: 10, traceEnabled: false }).trace).toBeUndefined()
    // Enabled and within the small-run cap → attaches.
    expect(buildRunSpec({ ...form, users: 10, traceEnabled: true }).trace).toBe(true)
    // The boundary (exactly 200) is still honored.
    expect(buildRunSpec({ ...form, users: 200, traceEnabled: true }).trace).toBe(true)
    // Above the cap → omitted even when requested (backend would ignore it).
    expect(buildRunSpec({ ...form, users: 201, traceEnabled: true }).trace).toBeUndefined()
  })

  it('gates trace on max concurrency for the open model, not the user count', () => {
    const open = { ...form, workloadKind: 'open' as const, traceEnabled: true, users: 999 }
    // Open: a small max-concurrency traces even with a large nominal user count.
    expect(buildRunSpec({ ...open, maxConcurrency: 100 }).trace).toBe(true)
    // Open: a large max-concurrency does not trace (matches the backend).
    expect(buildRunSpec({ ...open, maxConcurrency: 500 }).trace).toBeUndefined()
    // Open: uncapped (0) does not trace.
    expect(buildRunSpec({ ...open, maxConcurrency: 0 }).trace).toBeUndefined()
  })
})

describe('report URLs', () => {
  it('builds the HTML report URL', () => {
    expect(reportHTMLURL('run-1')).toBe('/api/runs/run-1/report.html')
  })
  it('builds the compare URL with encoded ids', () => {
    expect(compareURL('run a', 'run-2')).toBe('/api/runs/compare?a=run%20a&b=run-2')
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

describe('parseSegments', () => {
  it('returns an empty array for blank input', () => {
    expect(parseSegments('')).toEqual([])
    expect(parseSegments('   ')).toEqual([])
  })

  it('parses a well-formed persona mix', () => {
    const segs = parseSegments('[{"name":"a","weight":0.7,"start":"x"},{"name":"b","weight":0.3}]')
    expect(segs).toEqual([
      { name: 'a', weight: 0.7, start: 'x' },
      { name: 'b', weight: 0.3 },
    ])
  })

  it('throws when the JSON is not an array', () => {
    expect(() => parseSegments('{"name":"a","weight":1}')).toThrow()
  })

  it('throws when an element is missing or mistypes name/weight', () => {
    // weight is a string, not a number.
    expect(() => parseSegments('[{"name":"a","weight":"1"}]')).toThrow()
    // name is missing.
    expect(() => parseSegments('[{"weight":1}]')).toThrow()
    // element is not an object.
    expect(() => parseSegments('[42]')).toThrow()
    expect(() => parseSegments('[null]')).toThrow()
  })
})

describe('runDisabled', () => {
  it('disables the Run button while a run is in flight', () => {
    expect(runDisabled('starting')).toBe(true)
    expect(runDisabled('pending')).toBe(true) // SSE can emit pending before running
    expect(runDisabled('running')).toBe(true)
  })

  it('enables the Run button when idle or terminal', () => {
    expect(runDisabled('')).toBe(false)
    expect(runDisabled('completed')).toBe(false)
    expect(runDisabled('failed')).toBe(false)
    expect(runDisabled('killed')).toBe(false)
  })
})

describe('traceURL', () => {
  it('builds the per-run trace SSE URL', () => {
    expect(traceURL('run-1')).toBe('/api/runs/run-1/trace')
  })
})

describe('parseTraceFrame', () => {
  it('parses a data line of step events', () => {
    const frame = parseTraceFrame(
      'data: {"events":[{"seq":1,"userId":"u3","from":"cart","to":"checkout","status":200,"latencyMs":7.3,"ok":true}],"done":false}',
    )
    expect(frame?.done).toBe(false)
    expect(frame?.events).toHaveLength(1)
    expect(frame?.events[0]).toEqual({
      seq: 1,
      userId: 'u3',
      from: 'cart',
      to: 'checkout',
      status: 200,
      latencyMs: 7.3,
      ok: true,
    })
  })

  it('parses an entry event (empty from) and a transport error', () => {
    const frame = parseTraceFrame(
      'data: {"events":[{"seq":1,"userId":"u0","from":"","to":"browse","status":0,"latencyMs":0,"ok":false}]}',
    )
    expect(frame?.events[0].from).toBe('')
    expect(frame?.events[0].status).toBe(0)
    expect(frame?.events[0].ok).toBe(false)
    // done is optional and absent here.
    expect(frame?.done).toBeUndefined()
  })

  it('parses the terminal frame', () => {
    const frame = parseTraceFrame('data: {"events":[],"done":true}')
    expect(frame?.events).toEqual([])
    expect(frame?.done).toBe(true)
  })

  it('ignores non-data, blank, and malformed lines', () => {
    expect(parseTraceFrame('')).toBeNull()
    expect(parseTraceFrame(': comment')).toBeNull()
    expect(parseTraceFrame('event: ping')).toBeNull()
    expect(parseTraceFrame('data:')).toBeNull()
    expect(parseTraceFrame('data: {bad json')).toBeNull()
  })
})

describe('layoutGraph', () => {
  it('places nodes in columns by BFS depth from the start', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'c' },
    ]
    const pos = layoutGraph(nodes, edges, 'a')
    // x increases with depth; a < b < c.
    expect(pos.a.x).toBeLessThan(pos.b.x)
    expect(pos.b.x).toBeLessThan(pos.c.x)
    // A linear chain shares the same vertical lane.
    expect(pos.a.y).toBe(pos.b.y)
    expect(pos.b.y).toBe(pos.c.y)
  })

  it('spreads same-depth nodes vertically and keeps them column-aligned', () => {
    const nodes = [{ id: 'root' }, { id: 'x' }, { id: 'y' }]
    const edges = [
      { from: 'root', to: 'x' },
      { from: 'root', to: 'y' },
    ]
    const pos = layoutGraph(nodes, edges, 'root')
    // x and y siblings sit in the same column...
    expect(pos.x.x).toBe(pos.y.x)
    // ...one column right of the root...
    expect(pos.root.x).toBeLessThan(pos.x.x)
    // ...and are separated vertically.
    expect(pos.x.y).not.toBe(pos.y.y)
  })

  it('uses the shortest path for the column (diamond converges)', () => {
    // a -> b -> d and a -> d: d should be at the deeper of its reachable depths
    // is ambiguous; BFS assigns the first (shortest) depth = 1.
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'd' }]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'd' },
      { from: 'a', to: 'd' },
    ]
    const pos = layoutGraph(nodes, edges, 'a')
    // d is reachable directly from a (depth 1) and via b (depth 2); BFS keeps 1.
    expect(pos.d.x).toBe(pos.b.x)
  })

  it('parks nodes unreachable from the start in a trailing column', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'orphan' }]
    const edges = [{ from: 'a', to: 'b' }]
    const pos = layoutGraph(nodes, edges, 'a')
    // The orphan sits strictly to the right of every reachable node.
    expect(pos.orphan.x).toBeGreaterThan(pos.a.x)
    expect(pos.orphan.x).toBeGreaterThan(pos.b.x)
  })

  it('is deterministic for the same input', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'a', to: 'c' },
    ]
    expect(layoutGraph(nodes, edges, 'a')).toEqual(layoutGraph(nodes, edges, 'a'))
  })

  it('positions every node, even with a missing start', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }]
    const edges = [{ from: 'a', to: 'b' }]
    const pos = layoutGraph(nodes, edges, 'nope')
    // No start match → all nodes are unreachable but still placed.
    expect(Object.keys(pos).sort()).toEqual(['a', 'b'])
  })
})
