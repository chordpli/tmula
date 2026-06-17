import { afterEach, describe, it, expect, vi } from 'vitest'
import {
  addBaseUrlHostToAllowlist,
  allowlistMatchesHost,
  AUTH_FORM_DEFAULTS,
  authFormFromSpec,
  buildAuth,
  buildRunSpec,
  classifyEdge,
  compareURL,
  formatCount,
  formFromRunSpec,
  getExperimentSpec,
  graphDepths,
  HEAT_ERR,
  HEAT_MAX_W,
  HEAT_MIN_W,
  HEAT_OK,
  heatColor,
  heatmapURL,
  heatWidth,
  hostFromBaseUrl,
  importScenario,
  LAT_CELL_EMPTY,
  LAT_CELL_HOT,
  latencyCellColor,
  latencyHeatmapURL,
  layoutGraph,
  lerpColor,
  outcomeRates,
  outcomeSummary,
  parseCredentials,
  parseHeatFrame,
  parseLatencyFrame,
  parseAllowlist,
  parseSignupSteps,
  parseSSEData,
  parseSegments,
  parseTraceFrame,
  probeRun,
  reportHTMLURL,
  requestTotal,
  runDisabled,
  runIdFromQuery,
  shareTokenFromQuery,
  terminalNodeIds,
  terminalRole,
  traceable,
  traceURL,
  type ExperimentForm,
  type RunSpec,
} from './api'

// expParams unwraps the experiment params for assertions (experiment is typed
// `unknown` on the wire so the UI never depends on its shape elsewhere).
function expParams(spec: RunSpec): { virtualUserCount: number; deviationRate: number } {
  return (spec.experiment as { params: { virtualUserCount: number; deviationRate: number } }).params
}

// expAuthStrategy unwraps the experiment params' authStrategy for the auth-wiring
// assertions (same reason expParams reaches through the `unknown` experiment).
function expAuthStrategy(spec: RunSpec): string {
  return (spec.experiment as { params: { authStrategy: string } }).params.authStrategy
}

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
  deviationPct: 0,
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
  ...AUTH_FORM_DEFAULTS,
}

describe('allowlist helpers', () => {
  it('extracts the host from full URLs and bare hosts without keeping the port', () => {
    expect(hostFromBaseUrl('http://localhost:9000')).toBe('localhost')
    expect(hostFromBaseUrl('https://api.example.com:8443/v1')).toBe('api.example.com')
    expect(hostFromBaseUrl('sample-api:9000')).toBe('sample-api')
    expect(hostFromBaseUrl('')).toBeNull()
  })

  it('trims allowlist entries and ignores blanks', () => {
    expect(parseAllowlist(' localhost, , 127.0.0.1 ')).toEqual(['localhost', '127.0.0.1'])
  })

  it('matches exact hosts and leading wildcard hosts like the backend guard', () => {
    expect(allowlistMatchesHost(['localhost'], 'LOCALHOST')).toBe(true)
    expect(allowlistMatchesHost(['*.example.com'], 'api.example.com')).toBe(true)
    expect(allowlistMatchesHost(['*.example.com'], 'example.com')).toBe(false)
  })

  it('adds the Base URL host when the allowlist does not already cover it', () => {
    expect(addBaseUrlHostToAllowlist('http://sample-api:9000', 'localhost, 127.0.0.1')).toBe(
      'localhost, 127.0.0.1, sample-api',
    )
    expect(addBaseUrlHostToAllowlist('http://api.example.com', '*.example.com')).toBe(
      '*.example.com',
    )
  })
})

describe('buildRunSpec', () => {
  it('sends the closed pool as a count, not a per-user array', () => {
    const spec = buildRunSpec(form)
    // The pool is requested as a number; the server synthesizes u0..uN-1.
    expect(spec.users).toHaveLength(0)
    expect(spec.userCount).toBe(3)
    expect(spec.start).toBe('a')
    expect(spec.maxSteps).toBe(5)
  })

  it('does not materialize a user-per-user for the open model (bounded body)', () => {
    // The open model generates sessions from the arrival rate, so a huge "virtual
    // users" count must NOT balloon the request body with one object each (that was
    // the "request body too large" bug at ~900k users).
    const spec = buildRunSpec({ ...form, workloadKind: 'open', users: 899999 })
    expect(spec.users).toHaveLength(1)
    // The open model generates its own sessions, so it carries no operational pool
    // count — only the metadata virtualUserCount records the requested number.
    expect(spec.userCount).toBeUndefined()
    expect((spec.experiment as { params: { virtualUserCount: number } }).params.virtualUserCount).toBe(899999)
  })

  it('does not materialize a giant user array for large closed runs', () => {
    // A huge closed pool must NOT balloon the request body with one object per
    // user — that was the "request body too large" bug above ~270k users. The pool
    // is sent as a count and the server synthesizes it instead.
    const spec = buildRunSpec({ ...form, workloadKind: 'closed', users: 500_000 })
    expect(spec.users).toHaveLength(0)
    expect(spec.userCount).toBe(500_000)
    // The count is still recorded as experiment metadata too.
    expect((spec.experiment as { params: { virtualUserCount: number } }).params.virtualUserCount).toBe(
      500_000,
    )
  })

  it('sizes the safety rate cap to the configured open load (no silent throttle)', () => {
    const spec = buildRunSpec({ ...form, workloadKind: 'open', arrivalRate: 12000, maxConcurrency: 0 }) as {
      targetEnv: { rateCap: { maxRps: number; maxConcurrency: number } }
    }
    // The cap must not throttle below the requested arrival rate...
    expect(spec.targetEnv.rateCap.maxRps).toBeGreaterThanOrEqual(12000)
    // ...and an uncapped (0) max-concurrency maps to a generous, > 0 ceiling.
    expect(spec.targetEnv.rateCap.maxConcurrency).toBeGreaterThan(200)
  })

  it('floors the safety cap for small runs', () => {
    const spec = buildRunSpec(form) as { targetEnv: { rateCap: { maxRps: number; maxConcurrency: number } } }
    expect(spec.targetEnv.rateCap.maxRps).toBeGreaterThanOrEqual(1000)
    expect(spec.targetEnv.rateCap.maxConcurrency).toBeGreaterThanOrEqual(200)
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

  it('sends the deviation percent as a 0..1 deviationRate fraction', () => {
    // The default (0%) keeps the exact 0 the server reads as "follow the path".
    expect(expParams(buildRunSpec(form)).deviationRate).toBe(0)
    // A friendly percent converts to the server's fraction contract.
    expect(expParams(buildRunSpec({ ...form, deviationPct: 25 })).deviationRate).toBeCloseTo(0.25, 9)
    expect(expParams(buildRunSpec({ ...form, deviationPct: 100 })).deviationRate).toBe(1)
  })

  it('clamps an out-of-range deviation percent into [0,1]', () => {
    // The server hard-rejects deviationRate outside [0,1] with a 400; the builder
    // degrades a hand-typed out-of-range percent gracefully instead.
    expect(expParams(buildRunSpec({ ...form, deviationPct: 150 })).deviationRate).toBe(1)
    expect(expParams(buildRunSpec({ ...form, deviationPct: -10 })).deviationRate).toBe(0)
  })
})

describe('parseCredentials', () => {
  it('parses CSV with a token column and an optional subject column', () => {
    const out = parseCredentials('csv', 'subject,token\nalice,tok-a\nbob,tok-b\n')
    expect(out).toEqual([
      { subject: 'alice', token: 'tok-a' },
      { subject: 'bob', token: 'tok-b' },
    ])
  })

  it('parses a CSV with only a token column (no subject)', () => {
    const out = parseCredentials('csv', 'token\ntok-a\ntok-b')
    expect(out).toEqual([{ token: 'tok-a' }, { token: 'tok-b' }])
  })

  it('honors quoted CSV fields and column order independent of position', () => {
    const out = parseCredentials('csv', 'token,subject\n"tok,with,commas","alice, the user"')
    expect(out).toEqual([{ subject: 'alice, the user', token: 'tok,with,commas' }])
  })

  it('throws when a CSV has no token column header', () => {
    expect(() => parseCredentials('csv', 'subject,secret\nalice,tok')).toThrow(/token/)
  })

  it('parses JSONL with token and optional subject', () => {
    const body = '{"subject":"alice","token":"tok-a"}\n{"token":"tok-b"}\n'
    expect(parseCredentials('jsonl', body)).toEqual([{ subject: 'alice', token: 'tok-a' }, { token: 'tok-b' }])
  })

  it('throws when a JSONL line is missing its token or is not JSON', () => {
    expect(() => parseCredentials('jsonl', '{"subject":"a"}')).toThrow(/token/)
    expect(() => parseCredentials('jsonl', 'not json')).toThrow()
  })

  it('parses plain tokens, one secret per non-blank line, with no subject', () => {
    expect(parseCredentials('tokens', 'tok-a\n\n  tok-b  \n')).toEqual([{ token: 'tok-a' }, { token: 'tok-b' }])
  })

  it('throws on an empty body for every format', () => {
    expect(() => parseCredentials('csv', '   ')).toThrow()
    expect(() => parseCredentials('jsonl', '   ')).toThrow()
    expect(() => parseCredentials('tokens', '   ')).toThrow()
  })
})

describe('parseSignupSteps', () => {
  it('parses a well-formed signup step array', () => {
    const steps = parseSignupSteps('[{"id":"signup","method":"POST","path":"/signup"}]', 'signup')
    expect(steps).toEqual([{ id: 'signup', method: 'POST', path: '/signup' }])
  })

  it('throws on a non-array, or a step missing id/method/path', () => {
    expect(() => parseSignupSteps('{"id":"x"}', 'signup')).toThrow(/array/)
    expect(() => parseSignupSteps('[{"method":"POST","path":"/x"}]', 'signup')).toThrow(/id/)
    expect(() => parseSignupSteps('[{"id":"x","path":"/x"}]', 'signup')).toThrow(/method/)
    expect(() => parseSignupSteps('[{"id":"x","method":"POST"}]', 'teardown')).toThrow(/path/)
  })
})

describe('buildAuth', () => {
  it('returns null for the None mode (anonymous run)', () => {
    expect(buildAuth(form)).toBeNull()
    expect(buildAuth({ ...form, authMode: 'none' })).toBeNull()
  })

  it('builds a pool from pasted CSV, resolved to inline entries (never a source)', () => {
    const build = buildAuth({
      ...form,
      authMode: 'pool',
      authPoolFormat: 'csv',
      authPoolText: 'subject,token\nalice,tok-a\nbob,tok-b',
    })
    expect(build).not.toBeNull()
    expect(build!.authStrategy).toBe('pool')
    expect(build!.credentialPool.strategy).toBe('pool')
    expect(build!.credentialPool.entries).toEqual([
      { subject: 'alice', token: 'tok-a' },
      { subject: 'bob', token: 'tok-b' },
    ])
    // D1: the browser only ever sends inline entries, never a file/env source.
    expect((build!.credentialPool as { source?: unknown }).source).toBeUndefined()
    expect(build!.loginFlow).toBeUndefined()
  })

  it('builds a login pool plus the standalone login flow, defaulting the scope', () => {
    const build = buildAuth({
      ...form,
      authMode: 'login',
      loginGraphJSON: '{"id":"login","nodes":[{"id":"login","apiTemplateId":"t"}],"edges":[]}',
      loginTemplatesJSON: '{"t":{"method":"POST","path":"/login","extract":{"access_token":"$.access_token"}}}',
      loginStart: 'login',
      loginTokenVar: 'access_token',
      loginSubjectVar: 'user_id',
      loginScope: 'per-user',
    })
    expect(build!.authStrategy).toBe('login')
    expect(build!.credentialPool.strategy).toBe('login')
    // The pool references the flow by id; the flow itself rides at the top level.
    expect(build!.credentialPool.loginFlowId).toBe('login')
    // Per-user is the default scope, so it is omitted from the pool to stay minimal.
    expect(build!.credentialPool.loginScope).toBeUndefined()
    expect(build!.loginFlow).toEqual({
      graph: { id: 'login', nodes: [{ id: 'login', apiTemplateId: 't' }], edges: [] },
      templates: { t: { method: 'POST', path: '/login', extract: { access_token: '$.access_token' } } },
      start: 'login',
      tokenVar: 'access_token',
      subjectVar: 'user_id',
    })
  })

  it('sends the shared (client_credentials) scope when selected', () => {
    const build = buildAuth({
      ...form,
      authMode: 'login',
      loginGraphJSON: '{"id":"login","nodes":[{"id":"login"}],"edges":[]}',
      loginTemplatesJSON: '{}',
      loginStart: 'login',
      loginTokenVar: 'tok',
      loginScope: 'shared',
    })
    expect(build!.credentialPool.loginScope).toBe('shared')
  })

  it('throws when login has no token capture path', () => {
    expect(() =>
      buildAuth({
        ...form,
        authMode: 'login',
        loginGraphJSON: '{"id":"login","nodes":[{"id":"login"}],"edges":[]}',
        loginTemplatesJSON: '{}',
        loginStart: 'login',
        loginTokenVar: '   ',
      }),
    ).toThrow(/token/)
  })

  it('builds a bootstrap pool with a signup flow, capture, teardown and keepAccounts', () => {
    const build = buildAuth({
      ...form,
      authMode: 'bootstrap',
      authBootstrapConfirmed: true,
      signupStepsJSON: '[{"id":"signup","method":"POST","path":"/signup","extract":{"tok":"$.token"}}]',
      signupStart: 'signup',
      signupCaptureToken: 'tok',
      signupCaptureSubject: 'id',
      signupTeardownJSON: '[{"id":"del","method":"DELETE","path":"/accounts/{{.subject}}"}]',
      signupTeardownStart: 'del',
      keepAccounts: false,
    })
    expect(build!.authStrategy).toBe('bootstrap-signup')
    const pool = build!.credentialPool
    expect(pool.strategy).toBe('bootstrap-signup')
    expect(pool.keepAccounts).toBe(false)
    expect(pool.signupFlow).toEqual({
      steps: [{ id: 'signup', method: 'POST', path: '/signup', extract: { tok: '$.token' } }],
      start: 'signup',
      capture: { token: 'tok', subject: 'id' },
      teardown: [{ id: 'del', method: 'DELETE', path: '/accounts/{{.subject}}' }],
      teardownStart: 'del',
    })
  })

  it('refuses bootstrap until the non-production safety gate is confirmed', () => {
    const base = {
      ...form,
      authMode: 'bootstrap' as const,
      signupStepsJSON: '[{"id":"signup","method":"POST","path":"/signup"}]',
      signupCaptureToken: 'tok',
      keepAccounts: true,
    }
    // Unconfirmed → throws.
    expect(() => buildAuth(base)).toThrow(/non-production|confirm/i)
    // Confirmed → builds.
    expect(buildAuth({ ...base, authBootstrapConfirmed: true })!.authStrategy).toBe('bootstrap-signup')
  })
})

describe('buildRunSpec auth wiring', () => {
  it('keeps the None path byte-identical (no credentialPool, authStrategy pool)', () => {
    const spec = buildRunSpec(form)
    expect(spec.credentialPool).toBeUndefined()
    expect(spec.loginFlow).toBeUndefined()
    expect(expAuthStrategy(spec)).toBe('pool')
  })

  it('attaches a pool credentialPool and the pool authStrategy', () => {
    const spec = buildRunSpec({
      ...form,
      authMode: 'pool',
      authPoolFormat: 'tokens',
      authPoolText: 'tok-a\ntok-b',
    })
    expect(expAuthStrategy(spec)).toBe('pool')
    expect(spec.credentialPool?.strategy).toBe('pool')
    expect(spec.credentialPool?.entries).toEqual([{ token: 'tok-a' }, { token: 'tok-b' }])
    expect(spec.loginFlow).toBeUndefined()
  })

  it('attaches a login credentialPool + loginFlow and the login authStrategy', () => {
    const spec = buildRunSpec({
      ...form,
      authMode: 'login',
      loginGraphJSON: '{"id":"login","nodes":[{"id":"login","apiTemplateId":"t"}],"edges":[]}',
      loginTemplatesJSON: '{"t":{"method":"POST","path":"/login","extract":{"at":"$.access_token"}}}',
      loginStart: 'login',
      loginTokenVar: 'at',
    })
    expect(expAuthStrategy(spec)).toBe('login')
    expect(spec.credentialPool?.strategy).toBe('login')
    expect(spec.credentialPool?.loginFlowId).toBe('login')
    expect(spec.loginFlow?.tokenVar).toBe('at')
    expect(spec.loginFlow?.start).toBe('login')
  })

  it('attaches a bootstrap credentialPool and the bootstrap authStrategy (confirmed)', () => {
    const spec = buildRunSpec({
      ...form,
      authMode: 'bootstrap',
      authBootstrapConfirmed: true,
      signupStepsJSON: '[{"id":"signup","method":"POST","path":"/signup"}]',
      signupCaptureToken: 'tok',
      keepAccounts: true,
    })
    expect(expAuthStrategy(spec)).toBe('bootstrap-signup')
    expect(spec.credentialPool?.strategy).toBe('bootstrap-signup')
    expect(spec.credentialPool?.keepAccounts).toBe(true)
    expect(spec.loginFlow).toBeUndefined()
  })

  it('propagates an invalid auth config as a throw (fail-fast, no partial spec)', () => {
    expect(() => buildRunSpec({ ...form, authMode: 'pool', authPoolText: '' })).toThrow()
  })
})

describe('authFormFromSpec', () => {
  it('maps no credentialPool to the None mode', () => {
    expect(authFormFromSpec({})).toEqual({ authMode: 'none' })
  })

  it('maps a pool spec to pool mode (entries are never restored — secret is masked)', () => {
    const patch = authFormFromSpec({ credentialPool: { id: 'p', strategy: 'pool', entries: [{ subject: 'a' }] } })
    expect(patch.authMode).toBe('pool')
    // The masked secret cannot round-trip, so no text is restored.
    expect(patch.authPoolText).toBeUndefined()
  })

  it('restores the login flow shape and scope (no secret involved)', () => {
    const patch = authFormFromSpec({
      credentialPool: { id: 'p', strategy: 'login', loginFlowId: 'login', loginScope: 'shared' },
      loginFlow: {
        graph: { id: 'login', nodes: [{ id: 'login' }], edges: [] },
        templates: { t: { method: 'POST', path: '/login' } },
        start: 'login',
        tokenVar: 'at',
        subjectVar: 'uid',
      },
    })
    expect(patch.authMode).toBe('login')
    expect(patch.loginScope).toBe('shared')
    expect(patch.loginStart).toBe('login')
    expect(patch.loginTokenVar).toBe('at')
    expect(patch.loginSubjectVar).toBe('uid')
    expect(JSON.parse(patch.loginGraphJSON!)).toEqual({ id: 'login', nodes: [{ id: 'login' }], edges: [] })
  })

  it('restores the bootstrap flow but never pre-confirms the safety gate', () => {
    const patch = authFormFromSpec({
      credentialPool: {
        id: 'p',
        strategy: 'bootstrap-signup',
        keepAccounts: true,
        signupFlow: {
          steps: [{ id: 'signup', method: 'POST', path: '/signup' }],
          capture: { token: 'tok', subject: 'id' },
        },
      },
    })
    expect(patch.authMode).toBe('bootstrap')
    // Re-confirmation is required: attach mode does not pre-tick the non-prod gate.
    expect(patch.authBootstrapConfirmed).toBe(false)
    expect(patch.keepAccounts).toBe(true)
    expect(patch.signupCaptureToken).toBe('tok')
    expect(JSON.parse(patch.signupStepsJSON!)).toEqual([{ id: 'signup', method: 'POST', path: '/signup' }])
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

describe('importScenario', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // mockFetch installs a fetch stub returning the given response and records the
  // last call so the URL/method/body can be asserted.
  function mockFetch(response: { ok: boolean; status: number; body: string }) {
    const calls: { url: string; init?: RequestInit }[] = []
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      return Promise.resolve({
        ok: response.ok,
        status: response.status,
        text: () => Promise.resolve(response.body),
        json: () => Promise.resolve(JSON.parse(response.body)),
      } as Response)
    })
    return calls
  }

  it('POSTs the raw spec to the format-scoped import endpoint and returns the scenario', async () => {
    const scenario = {
      graph: { id: 'g', nodes: [{ id: 'a' }], edges: [] },
      templates: { ta: { method: 'GET', path: '/a' } },
      start: 'a',
      maxSteps: 3,
    }
    const calls = mockFetch({ ok: true, status: 200, body: JSON.stringify(scenario) })
    const out = await importScenario('openapi: 3.0.0', 'openapi')
    expect(out).toEqual(scenario)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/import?format=openapi')
    expect(calls[0].init?.method).toBe('POST')
    expect(calls[0].init?.body).toBe('openapi: 3.0.0')
  })

  it('passes the optional coverage stats through, and tolerates their absence', async () => {
    // A stats-aware server attaches `stats` (the import coverage report)…
    const withStats = {
      graph: { id: 'g', nodes: [{ id: 'a' }], edges: [] },
      templates: {},
      start: 'a',
      maxSteps: 3,
      stats: { requests: 120, skipped: 7, sessions: 32, clients: 21, droppedEndpoints: 3 },
    }
    mockFetch({ ok: true, status: 200, body: JSON.stringify(withStats) })
    const out = await importScenario('log line', 'accesslog')
    expect(out.stats).toEqual(withStats.stats)

    // …while an old server (pre-stats response shape) simply leaves it undefined.
    const { stats: _stats, ...withoutStats } = withStats
    mockFetch({ ok: true, status: 200, body: JSON.stringify(withoutStats) })
    const legacy = await importScenario('log line', 'accesslog')
    expect(legacy.stats).toBeUndefined()
    expect(legacy.start).toBe('a')
  })

  it('passes the chosen format through in the query string', async () => {
    const calls = mockFetch({ ok: true, status: 200, body: '{"graph":{},"templates":{},"start":"x","maxSteps":1}' })
    await importScenario('{}', 'har')
    expect(calls[0].url).toBe('/api/import?format=har')
    await importScenario('{}', 'auto')
    expect(calls[1].url).toBe('/api/import?format=auto')
  })

  it('throws the server error message from a 400 { error } body', async () => {
    mockFetch({ ok: false, status: 400, body: '{"error":"unrecognized spec"}' })
    await expect(importScenario('garbage', 'auto')).rejects.toThrow('unrecognized spec')
  })

  it('throws the raw body when a failure is not JSON', async () => {
    mockFetch({ ok: false, status: 400, body: 'plain text failure' })
    await expect(importScenario('garbage', 'auto')).rejects.toThrow('plain text failure')
  })

  it('falls back to the status code when the failure body is empty', async () => {
    mockFetch({ ok: false, status: 501, body: '' })
    await expect(importScenario('x', 'auto')).rejects.toThrow('501')
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

describe('latencyHeatmapURL', () => {
  it('builds the per-run latency-heatmap SSE URL', () => {
    expect(latencyHeatmapURL('run-1')).toBe('/api/runs/run-1/latency-heatmap')
  })
})

describe('parseLatencyFrame', () => {
  it('parses a data line of the latency histogram', () => {
    const frame = parseLatencyFrame(
      'data: {"binWidthMs":1000,"rows":[{"loMs":0,"hiMs":100,"label":"0–100ms"},{"loMs":100,"hiMs":0,"label":"100ms+"}],"cells":[[3,1],[0,2]],"maxCount":3,"done":false}',
    )
    expect(frame?.done).toBe(false)
    expect(frame?.binWidthMs).toBe(1000)
    expect(frame?.maxCount).toBe(3)
    expect(frame?.rows).toHaveLength(2)
    expect(frame?.rows[0]).toEqual({ loMs: 0, hiMs: 100, label: '0–100ms' })
    // hiMs === 0 marks the unbounded top bucket.
    expect(frame?.rows[1]).toEqual({ loMs: 100, hiMs: 0, label: '100ms+' })
    // cells[rowIndex][colIndex] = count in that band × time bucket.
    expect(frame?.cells).toEqual([
      [3, 1],
      [0, 2],
    ])
  })

  it('parses the terminal frame', () => {
    const frame = parseLatencyFrame('data: {"binWidthMs":500,"rows":[],"cells":[],"maxCount":0,"done":true}')
    expect(frame?.rows).toEqual([])
    expect(frame?.cells).toEqual([])
    expect(frame?.maxCount).toBe(0)
    expect(frame?.done).toBe(true)
  })

  it('ignores non-data, blank, and malformed lines', () => {
    expect(parseLatencyFrame('')).toBeNull()
    expect(parseLatencyFrame(': comment')).toBeNull()
    expect(parseLatencyFrame('event: ping')).toBeNull()
    expect(parseLatencyFrame('data:')).toBeNull()
    expect(parseLatencyFrame('data: {bad json')).toBeNull()
  })
})

describe('latencyCellColor', () => {
  it('is the near-blank tint for zero density or no peak', () => {
    expect(latencyCellColor(0, 0)).toBe(LAT_CELL_EMPTY)
    expect(latencyCellColor(0, 100)).toBe(LAT_CELL_EMPTY)
    expect(latencyCellColor(50, 0)).toBe(LAT_CELL_EMPTY)
    expect(latencyCellColor(-3, 100)).toBe(LAT_CELL_EMPTY)
  })

  it('is the strong accent at peak density', () => {
    expect(rgb(latencyCellColor(100, 100))).toEqual(rgb(LAT_CELL_HOT))
  })

  it('darkens monotonically with density between the endpoints', () => {
    const [emptyR, emptyG, emptyB] = rgb(LAT_CELL_EMPTY)
    const [hotR, hotG, hotB] = rgb(LAT_CELL_HOT)
    const [lowR, lowG, lowB] = rgb(latencyCellColor(10, 100))
    const [hiR, hiG, hiB] = rgb(latencyCellColor(90, 100))
    // A denser cell sits closer to the hot endpoint on every channel (the ramp
    // runs light indigo -> dark indigo, so each channel decreases toward the peak).
    expect(lowR).toBeLessThanOrEqual(emptyR)
    expect(lowR).toBeGreaterThanOrEqual(hotR)
    expect(hiR).toBeLessThan(lowR)
    expect(hiG).toBeLessThan(lowG)
    expect(hiB).toBeLessThanOrEqual(lowB)
    // Bounded by the ramp endpoints on every channel.
    expect(hiR).toBeGreaterThanOrEqual(hotR)
    expect(hiG).toBeGreaterThanOrEqual(hotG)
    expect(hiB).toBeGreaterThanOrEqual(hotB)
    expect(lowG).toBeLessThanOrEqual(emptyG)
    expect(lowB).toBeLessThanOrEqual(emptyB)
  })

  it('clamps an out-of-range density to the peak color', () => {
    expect(rgb(latencyCellColor(500, 100))).toEqual(rgb(LAT_CELL_HOT))
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

describe('graphDepths', () => {
  it('assigns shortest-path BFS depth from the start', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'c' },
      { from: 'a', to: 'd' },
      { from: 'd', to: 'c' }, // c reachable at depth 2 via b and via d; BFS keeps 2
    ]
    const depth = graphDepths(nodes, edges, 'a')
    expect(depth.get('a')).toBe(0)
    expect(depth.get('b')).toBe(1)
    expect(depth.get('d')).toBe(1)
    expect(depth.get('c')).toBe(2)
  })

  it('omits nodes unreachable from the start (and all nodes when start is missing)', () => {
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'orphan' }]
    const edges = [{ from: 'a', to: 'b' }]
    const depth = graphDepths(nodes, edges, 'a')
    expect(depth.has('orphan')).toBe(false)
    // A missing start leaves every node unreachable.
    expect(graphDepths(nodes, edges, 'nope').size).toBe(0)
  })

  it('agrees with the columns layoutGraph draws (same BFS)', () => {
    // depth d maps to x = d * COL_GAP, so equal depth ⇒ equal x and a deeper node
    // sits strictly to the right. This pins graphDepths to the layout it feeds.
    const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'c' },
    ]
    const depth = graphDepths(nodes, edges, 'a')
    const pos = layoutGraph(nodes, edges, 'a')
    expect(depth.get('a')! < depth.get('b')!).toBe(pos.a.x < pos.b.x)
    expect(depth.get('b')! < depth.get('c')!).toBe(pos.b.x < pos.c.x)
  })
})

describe('terminalNodeIds', () => {
  it('treats a node with no apiTemplateId as terminal, and one with a template as not', () => {
    const term = terminalNodeIds([
      { id: 'browse', apiTemplateId: 't_browse' },
      { id: 'done' }, // no template → terminal
      { id: 'exit', apiTemplateId: '' }, // empty template → terminal
    ])
    expect(term.has('done')).toBe(true)
    expect(term.has('exit')).toBe(true)
    expect(term.has('browse')).toBe(false)
  })

  it('is empty when every node has a template', () => {
    const term = terminalNodeIds([
      { id: 'a', apiTemplateId: 't_a' },
      { id: 'b', apiTemplateId: 't_b' },
    ])
    expect(term.size).toBe(0)
  })
})

describe('classifyEdge', () => {
  // Mirrors the shop preset's funnel shape so the classes match what the UI draws.
  const terminals = new Set(['done', 'exit'])
  const depth = new Map<string, number>([
    ['browse', 0],
    ['search', 1],
    ['category', 1],
    ['product', 2],
    ['cart', 3],
    ['checkout', 4],
    ['done', 5],
    ['exit', 1],
  ])

  it('labels an edge into a terminal node as terminal (even from deep in the funnel)', () => {
    expect(classifyEdge('checkout', 'done', terminals, depth)).toBe('terminal')
    expect(classifyEdge('browse', 'exit', terminals, depth)).toBe('terminal')
  })

  it('labels an edge to an equal-or-shallower depth as a back/loop edge', () => {
    // category (1) -> browse (0): a loop back to the entry.
    expect(classifyEdge('category', 'browse', terminals, depth)).toBe('back')
    // product (2) -> browse (0): another loop.
    expect(classifyEdge('product', 'browse', terminals, depth)).toBe('back')
  })

  it('labels an edge that advances the funnel as forward', () => {
    expect(classifyEdge('browse', 'search', terminals, depth)).toBe('forward')
    expect(classifyEdge('search', 'product', terminals, depth)).toBe('forward')
    expect(classifyEdge('cart', 'checkout', terminals, depth)).toBe('forward')
  })

  it('defaults to forward when a depth is unknown (so unreachable edges still draw bold)', () => {
    expect(classifyEdge('mystery', 'browse', terminals, depth)).toBe('forward')
    expect(classifyEdge('browse', 'mystery', terminals, depth)).toBe('forward')
  })

  it('treats a terminal destination as terminal regardless of depth ordering', () => {
    // Even if a terminal somehow sat shallower than its source, terminal wins.
    expect(classifyEdge('browse', 'exit', terminals, depth)).toBe('terminal')
  })
})

describe('terminalRole', () => {
  it("classifies 'exit' as the drop-off", () => {
    expect(terminalRole('exit')).toBe('dropoff')
  })

  it('reads any other terminal as a completion (an unnamed endpoint stays positive)', () => {
    expect(terminalRole('done')).toBe('completion')
    expect(terminalRole('finished')).toBe('completion')
  })
})

describe('outcomeRates', () => {
  it('derives the completion and drop-off rates from raw counts', () => {
    expect(outcomeRates(200, 30, 50)).toEqual({
      started: 200,
      completed: 30,
      dropped: 50,
      completionRate: 0.15,
      dropOffRate: 0.25,
    })
  })

  it('is all-zero rates when nothing started (never NaN)', () => {
    const o = outcomeRates(0, 0, 0)
    expect(o.completionRate).toBe(0)
    expect(o.dropOffRate).toBe(0)
  })
})

describe('outcomeSummary', () => {
  const terminals = new Set(['done', 'exit'])

  it('folds entry volume and terminal inflow into journey-outcome rates', () => {
    const edges = [
      { from: '', to: 'browse', requests: 100 }, // journeys started
      { from: 'browse', to: 'search', requests: 40 }, // mid-journey request → not an outcome
      { from: 'browse', to: 'exit', requests: 20 }, // drop-off
      { from: 'cart', to: 'exit', requests: 10 }, // drop-off from a second source
      { from: 'checkout', to: 'done', requests: 15 }, // completion
    ]
    const o = outcomeSummary(edges, terminals)
    expect(o.started).toBe(100)
    expect(o.completed).toBe(15)
    expect(o.dropped).toBe(30)
    expect(o.completionRate).toBeCloseTo(0.15, 9)
    expect(o.dropOffRate).toBeCloseTo(0.3, 9)
  })

  it('sums multiple entry edges (personas can start at different nodes)', () => {
    const edges = [
      { from: '', to: 'browse', requests: 70 },
      { from: '', to: 'cart', requests: 30 },
      { from: 'checkout', to: 'done', requests: 25 },
    ]
    const o = outcomeSummary(edges, terminals)
    expect(o.started).toBe(100)
    expect(o.completionRate).toBeCloseTo(0.25, 9)
  })

  it('counts an unnamed terminal as a completion, matching the flow view', () => {
    const edges = [
      { from: '', to: 'a', requests: 10 },
      { from: 'a', to: 'finished', requests: 4 },
    ]
    const o = outcomeSummary(edges, new Set(['finished']))
    expect(o.completed).toBe(4)
    expect(o.dropped).toBe(0)
  })

  it('is all zeros with no traffic', () => {
    expect(outcomeSummary([], terminals)).toEqual({
      started: 0,
      completed: 0,
      dropped: 0,
      completionRate: 0,
      dropOffRate: 0,
    })
  })
})

describe('requestTotal', () => {
  const terminals = new Set(['done', 'exit'])

  it('sums request edges but excludes flow into terminal nodes', () => {
    const edges = [
      { from: '', to: 'browse', requests: 100 }, // entry request → counted
      { from: 'browse', to: 'search', requests: 40 }, // request → counted
      { from: 'browse', to: 'exit', requests: 20 }, // drop-off → excluded
      { from: 'checkout', to: 'done', requests: 15 }, // completion → excluded
    ]
    // 100 + 40 = 140; the 20 + 15 terminal flow is not requests.
    expect(requestTotal(edges, terminals)).toBe(140)
  })

  it('counts everything when there are no terminals', () => {
    const edges = [
      { from: '', to: 'a', requests: 5 },
      { from: 'a', to: 'b', requests: 7 },
    ]
    expect(requestTotal(edges, new Set())).toBe(12)
  })

  it('is zero when every edge flows into a terminal', () => {
    const edges = [
      { from: 'a', to: 'done', requests: 9 },
      { from: 'b', to: 'exit', requests: 3 },
    ]
    expect(requestTotal(edges, terminals)).toBe(0)
  })
})

// --- Attach mode (?run=<run-id>) -------------------------------------------------
// `tmula demo` (and any shared link) opens the console as /?run=<run-id>; these
// helpers parse the parameter, probe the run, and re-hydrate the form from the
// run's stored spec so the attached live view matches what the run executes.

describe('runIdFromQuery', () => {
  it('extracts a run id', () => {
    expect(runIdFromQuery('?run=run-1')).toBe('run-1')
    expect(runIdFromQuery('?foo=1&run=run-7')).toBe('run-7')
  })

  it('trims surrounding whitespace', () => {
    expect(runIdFromQuery('?run=%20run-2%20')).toBe('run-2')
  })

  it('returns null when absent or blank', () => {
    expect(runIdFromQuery('')).toBeNull()
    expect(runIdFromQuery('?foo=1')).toBeNull()
    expect(runIdFromQuery('?run=')).toBeNull()
    expect(runIdFromQuery('?run=%20%20')).toBeNull()
  })
})

describe('probeRun', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // mockFetch installs a fetch stub returning the given response and records the
  // requested URLs (same shape as the importScenario helper above).
  function mockFetch(response: { ok: boolean; status: number; body: string }) {
    const calls: { url: string }[] = []
    vi.stubGlobal('fetch', (url: string) => {
      calls.push({ url })
      return Promise.resolve({
        ok: response.ok,
        status: response.status,
        text: () => Promise.resolve(response.body),
        json: () => Promise.resolve(JSON.parse(response.body)),
      } as Response)
    })
    return calls
  }

  it('returns the report when the run exists', async () => {
    const report = {
      run: { id: 'run-1', status: 'running', experimentId: 'exp-1' },
      stats: { total: 0, errors: 0, timeouts: 0, errorRate: 0, statusCounts: {}, p50: 0, p95: 0, p99: 0, max: 0 },
      findings: [],
    }
    const calls = mockFetch({ ok: true, status: 200, body: JSON.stringify(report) })
    const out = await probeRun('run-1')
    expect(out).toEqual(report)
    expect(calls[0].url).toBe('/api/runs/run-1/report')
  })

  it('returns null on 404 so an unknown run falls back to the form', async () => {
    mockFetch({ ok: false, status: 404, body: '{"error":"run \\"x\\" not found"}' })
    expect(await probeRun('x')).toBeNull()
  })

  it('throws on a non-404 failure', async () => {
    mockFetch({ ok: false, status: 500, body: 'boom' })
    await expect(probeRun('run-1')).rejects.toThrow('500')
  })

  it('escapes the run id into the URL', async () => {
    const calls = mockFetch({ ok: false, status: 404, body: '' })
    await probeRun('a/b')
    expect(calls[0].url).toBe('/api/runs/a%2Fb/report')
  })
})

describe('getExperimentSpec', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns the stored spec', async () => {
    const spec = { start: 'browse', maxSteps: 9 }
    vi.stubGlobal('fetch', (url: string) => {
      expect(url).toBe('/api/experiments/exp-1')
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve(spec),
      } as Response)
    })
    expect(await getExperimentSpec('exp-1')).toEqual(spec)
  })

  it('returns null when the spec is gone (404) or any other failure', async () => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) } as Response),
    )
    expect(await getExperimentSpec('exp-1')).toBeNull()
    vi.stubGlobal('fetch', () => Promise.reject(new Error('network down')))
    expect(await getExperimentSpec('exp-1')).toBeNull()
  })
})

describe('formFromRunSpec', () => {
  // A demo-like open-model spec, shaped exactly as GET /experiments/{id} returns
  // it (the server's RunSpec JSON).
  const openSpec = {
    experiment: {
      id: 'exp-1',
      name: 'demo',
      targetEnvId: 'env',
      scenarioGraphId: 'graph',
      params: { virtualUserCount: 1, deviationRate: 0.05, authStrategy: 'pool' },
    },
    targetEnv: {
      id: 'env',
      baseUrl: 'http://127.0.0.1:55330',
      allowlist: ['127.0.0.1', 'localhost'],
      rateCap: { maxRps: 1000, maxConcurrency: 2000 },
      envClass: 'dev',
    },
    graph: {
      id: 'learned',
      nodes: [{ id: 'browse', apiTemplateId: 'b' }, { id: 'exit' }],
      edges: [{ from: 'browse', to: 'exit', weight: 1 }],
    },
    templates: { b: { method: 'GET', path: '/products' } },
    start: 'browse',
    maxSteps: 12,
    users: [{ id: 'u0' }],
    seed: 1,
    workload: {
      kind: 'open',
      arrival: { shape: 'constant', startRate: 8, peakRate: 8 },
      durationSeconds: 60,
      maxConcurrency: 200,
      thinkTime: { minMs: 50, maxMs: 250 },
    },
    trace: true,
  }

  it('maps an open-model spec onto the form so attach converges with the form path', () => {
    const patch = formFromRunSpec(openSpec)
    expect(patch).not.toBeNull()
    expect(patch!.baseUrl).toBe('http://127.0.0.1:55330')
    expect(patch!.allowlist).toBe('127.0.0.1, localhost')
    expect(patch!.start).toBe('browse')
    expect(patch!.maxSteps).toBe(12)
    expect(patch!.workloadKind).toBe('open')
    expect(patch!.arrivalRate).toBe(8)
    expect(patch!.durationSeconds).toBe(60)
    expect(patch!.maxConcurrency).toBe(200)
    expect(patch!.thinkMinMs).toBe(50)
    expect(patch!.thinkMaxMs).toBe(250)
    expect(patch!.traceEnabled).toBe(true)
    expect(patch!.deviationPct).toBe(5)
    // The graph/templates land as pretty-printed JSON the scenario card can edit
    // and the live view can parse.
    expect(JSON.parse(patch!.graphJSON!)).toEqual(openSpec.graph)
    expect(JSON.parse(patch!.templatesJSON!)).toEqual(openSpec.templates)
  })

  it('keeps the form usable by the run path (buildRunSpec round-trips)', () => {
    const base: ExperimentForm = {
      baseUrl: 'http://localhost:9000',
      allowlist: 'localhost',
      users: 20,
      maxSteps: 8,
      deviationPct: 0,
      start: 'a',
      graphJSON: '{}',
      templatesJSON: '{}',
      workers: '',
      aggregateWorkers: false,
      workloadKind: 'closed',
      arrivalRate: 12,
      durationSeconds: 30,
      maxConcurrency: 80,
      thinkMinMs: 300,
      thinkMaxMs: 900,
      segmentsJSON: '',
      traceEnabled: true,
      ...AUTH_FORM_DEFAULTS,
    }
    const patch = formFromRunSpec(openSpec)!
    const spec = buildRunSpec({ ...base, ...patch })
    expect(spec.start).toBe('browse')
    expect(spec.workload?.arrival.peakRate).toBe(8)
    expect(spec.trace).toBe(true)
  })

  it('maps a closed-model spec: user count, no open fields', () => {
    const closed = {
      ...openSpec,
      workload: undefined,
      userCount: 300,
      trace: false,
    }
    const patch = formFromRunSpec(closed)!
    expect(patch.workloadKind).toBe('closed')
    expect(patch.users).toBe(300)
    expect(patch.traceEnabled).toBe(false)
    expect(patch.arrivalRate).toBeUndefined()
  })

  it('falls back to the users list length when userCount is absent', () => {
    const closed = { ...openSpec, workload: undefined, users: [{ id: 'u0' }, { id: 'u1' }] }
    expect(formFromRunSpec(closed)!.users).toBe(2)
  })

  it('carries the persona mix of an open run', () => {
    const segs = [{ name: 'buyer', weight: 0.3, start: 'browse' }]
    const patch = formFromRunSpec({ ...openSpec, segments: segs })!
    expect(JSON.parse(patch.segmentsJSON!)).toEqual(segs)
  })

  it('joins workers and keeps aggregate mode', () => {
    const patch = formFromRunSpec({
      ...openSpec,
      workers: ['127.0.0.1:9101', '127.0.0.1:9102'],
      aggregateWorkers: true,
    })!
    expect(patch.workers).toBe('127.0.0.1:9101, 127.0.0.1:9102')
    expect(patch.aggregateWorkers).toBe(true)
  })

  it('returns null without a usable scenario graph', () => {
    expect(formFromRunSpec(null)).toBeNull()
    expect(formFromRunSpec('nope')).toBeNull()
    expect(formFromRunSpec({})).toBeNull()
    expect(formFromRunSpec({ graph: { nodes: 'x', edges: [] } })).toBeNull()
  })

  it('omits fields the spec does not carry instead of clobbering the form', () => {
    const minimal = {
      graph: { nodes: [{ id: 'a' }], edges: [] },
      templates: {},
      start: 'a',
      maxSteps: 3,
    }
    const patch = formFromRunSpec(minimal)!
    expect(patch.baseUrl).toBeUndefined()
    expect(patch.allowlist).toBeUndefined()
    expect(patch.workers).toBeUndefined()
    // No workload block = the closed default, with no pool size to apply.
    expect(patch.workloadKind).toBe('closed')
    expect(patch.users).toBeUndefined()
  })
})
