import { describe, it, expect } from 'vitest'
import {
  buildRunSpec,
  compareURL,
  formatCount,
  HEAT_ERR,
  HEAT_MAX_W,
  HEAT_MIN_W,
  HEAT_OK,
  heatColor,
  heatmapURL,
  heatWidth,
  layoutGraph,
  lerpColor,
  parseHeatFrame,
  parseSSEData,
  parseSegments,
  parseTraceFrame,
  reportHTMLURL,
  runDisabled,
  shareTokenFromQuery,
  traceable,
  traceURL,
  type ExperimentForm,
} from './api'

// rgb parses a color into channels for assertions, accepting both the "rgb(r, g, b)"
// form heatColor/lerpColor emit and the "#rrggbb" form of the ramp endpoints.
function rgb(s: string): [number, number, number] {
  const m = s.match(/rgb\((\d+), (\d+), (\d+)\)/)
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3])]
  if (/^#[0-9a-fA-F]{6}$/.test(s)) {
    const n = parseInt(s.slice(1), 16)
    return [(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff]
  }
  throw new Error(`unrecognized color: ${s}`)
}

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

  it('attaches trace whenever enabled, at any run size (gating now picks render mode)', () => {
    // Disabled → never attaches.
    expect(buildRunSpec({ ...form, users: 10, traceEnabled: false }).trace).toBeUndefined()
    // Enabled on a small run → attaches.
    expect(buildRunSpec({ ...form, users: 10, traceEnabled: true }).trace).toBe(true)
    // Enabled at the old boundary → attaches.
    expect(buildRunSpec({ ...form, users: 200, traceEnabled: true }).trace).toBe(true)
    // Enabled above the old cap → still attaches (honored as a heatmap now).
    expect(buildRunSpec({ ...form, users: 201, traceEnabled: true }).trace).toBe(true)
    expect(buildRunSpec({ ...form, users: 5_000_000, traceEnabled: true }).trace).toBe(true)
  })

  it('attaches trace for the open model regardless of max concurrency', () => {
    const open = { ...form, workloadKind: 'open' as const, traceEnabled: true, users: 999 }
    // Open: a small max-concurrency attaches (small enough for per-request events).
    expect(buildRunSpec({ ...open, maxConcurrency: 100 }).trace).toBe(true)
    // Open: a large max-concurrency still attaches (honored as a heatmap).
    expect(buildRunSpec({ ...open, maxConcurrency: 500 }).trace).toBe(true)
    // Open: uncapped (0) still attaches.
    expect(buildRunSpec({ ...open, maxConcurrency: 0 }).trace).toBe(true)
  })
})

describe('traceable', () => {
  it('selects per-request events for a small closed run, heatmap for a large one', () => {
    expect(traceable({ ...form, users: 10 })).toBe(true)
    // At the cap → still per-request.
    expect(traceable({ ...form, users: 200 })).toBe(true)
    // Above the cap → heatmap.
    expect(traceable({ ...form, users: 201 })).toBe(false)
    expect(traceable({ ...form, users: 1_000_000 })).toBe(false)
    // Zero/empty → not per-request (no run to animate).
    expect(traceable({ ...form, users: 0 })).toBe(false)
  })

  it('selects the mode by max concurrency for an open run, ignoring the user count', () => {
    const open = { ...form, workloadKind: 'open' as const, users: 999 }
    // Small back-pressure cap → per-request, even with a large nominal user count.
    expect(traceable({ ...open, maxConcurrency: 100 })).toBe(true)
    expect(traceable({ ...open, maxConcurrency: 200 })).toBe(true)
    // Large cap → heatmap.
    expect(traceable({ ...open, maxConcurrency: 201 })).toBe(false)
    expect(traceable({ ...open, maxConcurrency: 500 })).toBe(false)
    // Uncapped (0) → heatmap (effectively unbounded concurrency).
    expect(traceable({ ...open, maxConcurrency: 0 })).toBe(false)
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

describe('heatmapURL', () => {
  it('builds the per-run heatmap SSE URL', () => {
    expect(heatmapURL('run-1')).toBe('/api/runs/run-1/heatmap')
  })
})

describe('parseHeatFrame', () => {
  it('parses a data line of per-edge aggregates', () => {
    const frame = parseHeatFrame(
      'data: {"edges":[{"from":"a","to":"b","requests":12345,"errors":3}],"done":false}',
    )
    expect(frame?.done).toBe(false)
    expect(frame?.edges).toHaveLength(1)
    expect(frame?.edges[0]).toEqual({ from: 'a', to: 'b', requests: 12345, errors: 3 })
  })

  it('parses an entry edge (empty from) and the terminal frame', () => {
    const entry = parseHeatFrame('data: {"edges":[{"from":"","to":"browse","requests":900000,"errors":0}]}')
    expect(entry?.edges[0].from).toBe('')
    expect(entry?.edges[0].requests).toBe(900000)
    // done is optional and absent here.
    expect(entry?.done).toBeUndefined()

    const last = parseHeatFrame('data: {"edges":[],"done":true}')
    expect(last?.edges).toEqual([])
    expect(last?.done).toBe(true)
  })

  it('ignores non-data, blank, and malformed lines', () => {
    expect(parseHeatFrame('')).toBeNull()
    expect(parseHeatFrame(': comment')).toBeNull()
    expect(parseHeatFrame('event: ping')).toBeNull()
    expect(parseHeatFrame('data:')).toBeNull()
    expect(parseHeatFrame('data: {bad json')).toBeNull()
  })
})

describe('heatWidth', () => {
  it('returns the floor for no traffic or no peak', () => {
    expect(heatWidth(0, 0)).toBe(HEAT_MIN_W)
    expect(heatWidth(0, 100)).toBe(HEAT_MIN_W)
    expect(heatWidth(100, 0)).toBe(HEAT_MIN_W)
    expect(heatWidth(-5, 100)).toBe(HEAT_MIN_W)
  })

  it('gives the busiest edge the max width', () => {
    expect(heatWidth(1000, 1000)).toBeCloseTo(HEAT_MAX_W, 6)
  })

  it('scales logarithmically so a huge range stays legible in one frame', () => {
    // A 12-request edge and a 12-million-request edge against a 12M peak: the
    // small edge is still visibly above the floor, the big edge is at the max.
    const small = heatWidth(12, 12_000_000)
    const big = heatWidth(12_000_000, 12_000_000)
    expect(big).toBeCloseTo(HEAT_MAX_W, 6)
    expect(small).toBeGreaterThan(HEAT_MIN_W)
    expect(small).toBeLessThan(big)
    // Monotonic in the request count.
    expect(heatWidth(100, 1_000_000)).toBeLessThan(heatWidth(10_000, 1_000_000))
    // Stays within bounds.
    expect(small).toBeGreaterThanOrEqual(HEAT_MIN_W)
    expect(big).toBeLessThanOrEqual(HEAT_MAX_W)
  })
})

describe('heatColor', () => {
  it('is pure green when nothing has failed (including zero requests)', () => {
    expect(heatColor(0, 0)).toBe(lerpColor(HEAT_OK, HEAT_ERR, 0))
    expect(rgb(heatColor(0, 1000))).toEqual(rgb(HEAT_OK))
  })

  it('is pure red when every request failed', () => {
    expect(rgb(heatColor(1000, 1000))).toEqual(rgb(HEAT_ERR))
  })

  it('lands between green and red at a partial error ratio', () => {
    const [r, g, b] = rgb(heatColor(1, 2)) // 50% errors
    const [okR, okG, okB] = rgb(HEAT_OK)
    const [errR, errG, errB] = rgb(HEAT_ERR)
    // Red channel rises toward the error color; green channel falls.
    expect(r).toBeGreaterThan(okR)
    expect(r).toBeLessThan(errR)
    expect(g).toBeLessThan(okG)
    expect(g).toBeGreaterThan(errG)
    expect(b).toBeGreaterThanOrEqual(Math.min(okB, errB))
  })

  it('clamps an out-of-range error ratio', () => {
    // More errors than requests (shouldn't happen, but stay safe) → clamps to red.
    expect(rgb(heatColor(5, 1))).toEqual(rgb(HEAT_ERR))
  })
})

describe('lerpColor', () => {
  it('returns the endpoints at t = 0 and t = 1', () => {
    expect(rgb(lerpColor('#000000', '#ffffff', 0))).toEqual([0, 0, 0])
    expect(rgb(lerpColor('#000000', '#ffffff', 1))).toEqual([255, 255, 255])
  })

  it('interpolates the midpoint and clamps t', () => {
    expect(rgb(lerpColor('#000000', '#ffffff', 0.5))).toEqual([128, 128, 128])
    // Out-of-range t is clamped to [0,1].
    expect(rgb(lerpColor('#000000', '#ffffff', -1))).toEqual([0, 0, 0])
    expect(rgb(lerpColor('#000000', '#ffffff', 2))).toEqual([255, 255, 255])
  })
})

describe('formatCount', () => {
  it('shows small counts verbatim', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(7)).toBe('7')
    expect(formatCount(999)).toBe('999')
  })

  it('compacts thousands, millions, and billions', () => {
    expect(formatCount(1000)).toBe('1k')
    expect(formatCount(1234)).toBe('1.2k')
    expect(formatCount(12_345)).toBe('12.3k')
    expect(formatCount(5_000_000)).toBe('5M')
    expect(formatCount(12_345_678)).toBe('12.3M')
    expect(formatCount(2_000_000_000)).toBe('2B')
  })

  it('drops a trailing .0 so round values read cleanly', () => {
    expect(formatCount(2000)).toBe('2k')
    expect(formatCount(3_000_000)).toBe('3M')
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
