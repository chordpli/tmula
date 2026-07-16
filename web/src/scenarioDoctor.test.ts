import { describe, expect, it } from 'vitest'
import { AUTH_FORM_DEFAULTS, type ExperimentForm } from './api'
import { doctorForm } from './scenarioDoctor'

const form: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost',
  users: 3,
  maxSteps: 5,
  deviationPct: 0,
  start: 'browse',
  graphJSON: JSON.stringify({
    id: 'g',
    nodes: [
      { id: 'browse', apiTemplateId: 'browse' },
      { id: 'product', apiTemplateId: 'product' },
      { id: 'done' },
    ],
    edges: [
      { from: 'browse', to: 'product', weight: 0.9 },
      { from: 'product', to: 'done', weight: 1 },
    ],
  }),
  templatesJSON: JSON.stringify({
    browse: { method: 'GET', path: '/browse' },
    product: { method: 'GET', path: '/products/1' },
  }),
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

function codes(f: ExperimentForm = form): string[] {
  return doctorForm(f).map((i) => i.code)
}

describe('doctorForm', () => {
  it('returns no issues for a connected runnable scenario', () => {
    expect(doctorForm(form)).toEqual([])
  })

  it('flags Base URL hosts that are not covered by the allowlist', () => {
    expect(codes({ ...form, baseUrl: 'http://sample-api:9000' })).toContain('allowlist-missing-host')
  })

  it('flags malformed graph and templates JSON', () => {
    const got = codes({ ...form, graphJSON: 'not json', templatesJSON: '{' })
    expect(got).toContain('graph-json')
    expect(got).toContain('templates-json')
  })

  it('flags broken graph references and missing templates', () => {
    const broken = {
      id: 'g',
      nodes: [
        { id: 'browse', apiTemplateId: 'missing' },
        { id: 'orphan', apiTemplateId: 'browse' },
      ],
      edges: [
        { from: 'browse', to: 'ghost', weight: -1 },
        { from: 'browse', to: 'orphan', weight: 1.2 },
      ],
    }
    const got = codes({ ...form, graphJSON: JSON.stringify(broken) })
    expect(got).toContain('node-template-missing')
    expect(got).toContain('edge-unknown-node')
    expect(got).toContain('outgoing-weight-high')
  })

  it('flags unused and incomplete templates', () => {
    const got = codes({
      ...form,
      templatesJSON: JSON.stringify({
        browse: { method: 'GET', path: '/browse' },
        product: { method: 'GET', path: '/products/1' },
        spare: { method: '', path: '' },
      }),
    })
    expect(got).toContain('template-unused')
    expect(got).toContain('template-method')
    expect(got).toContain('template-path')
  })

  it('flags malformed response extractors', () => {
    expect(
      codes({
        ...form,
        templatesJSON: JSON.stringify({
          browse: { method: 'GET', path: '/browse', extract: ['bad'] },
          product: { method: 'GET', path: '/products/1' },
        }),
      }),
    ).toContain('template-extract-shape')
    expect(
      codes({
        ...form,
        templatesJSON: JSON.stringify({
          browse: { method: 'GET', path: '/browse', extract: { productId: '' } },
          product: { method: 'GET', path: '/products/1' },
        }),
      }),
    ).toContain('template-extract-entry')
  })

  it('checks open-model persona JSON and segment start nodes', () => {
    expect(codes({ ...form, workloadKind: 'open', segmentsJSON: 'not json' })).toContain('segments-json')
    expect(
      codes({
        ...form,
        workloadKind: 'open',
        segmentsJSON: '[{"name":"buyer","weight":1,"start":"checkout"}]',
      }),
    ).toContain('segment-start')
  })

  it('warns that personas are ignored by the closed model', () => {
    expect(codes({ ...form, segmentsJSON: '[{"name":"buyer","weight":1}]' })).toContain('segments-closed')
  })

  it('flags a malformed multi-user login credential list', () => {
    const got = codes({
      ...form,
      authMode: 'login',
      loginMode: 'simple',
      loginUrlPath: '/login',
      loginCredFormat: 'csv',
      loginCredText: 'user,pass\nalice,pw', // wrong header names → cannot parse
    })
    expect(got).toContain('auth-login-cred-invalid')
  })

  it('warns when a login credential list is supplied but the body never references a row', () => {
    const got = codes({
      ...form,
      authMode: 'login',
      loginMode: 'simple',
      loginUrlPath: '/login',
      loginCredFormat: 'csv',
      loginCredText: 'username,password\nalice,pw-a\nbob,pw-b',
      loginBodyTemplate: '{"username": "fixed", "password": "fixed"}', // no {{.username}}
    })
    expect(got).toContain('auth-login-cred-unused')
  })

  it('does not warn when the login body templates the credential-list row in', () => {
    const got = codes({
      ...form,
      authMode: 'login',
      loginMode: 'simple',
      loginUrlPath: '/login',
      loginCredFormat: 'csv',
      loginCredText: 'username,password\nalice,pw-a\nbob,pw-b',
      loginBodyTemplate: '{"username": "{{.username}}", "password": "{{.password}}"}',
    })
    expect(got).not.toContain('auth-login-cred-unused')
    expect(got).not.toContain('auth-login-cred-invalid')
  })

  it('does not flag the credential list when no login list is supplied (single identity)', () => {
    const got = codes({
      ...form,
      authMode: 'login',
      loginMode: 'simple',
      loginUrlPath: '/login',
      // loginCredText stays empty → single-identity login, nothing to check.
    })
    expect(got).not.toContain('auth-login-cred-invalid')
    expect(got).not.toContain('auth-login-cred-unused')
  })

  it('flags a simple-mode login body that still carries literal {username}/{password} markers', () => {
    // The shipped default body carries the markers — nothing substitutes them, so
    // they would be POSTed literally; the doctor must block that run.
    const got = codes({ ...form, authMode: 'login', loginUrlPath: '/login' })
    expect(got).toContain('auth-login-body-marker')
    // A body with real credentials (or row templates) is clean.
    expect(
      codes({
        ...form,
        authMode: 'login',
        loginUrlPath: '/login',
        loginBodyTemplate: '{"username": "alice", "password": "pw-a"}',
      }),
    ).not.toContain('auth-login-body-marker')
    expect(
      codes({
        ...form,
        authMode: 'login',
        loginUrlPath: '/login',
        loginBodyTemplate: '{"username": "{{.username}}", "password": "{{.password}}"}',
      }),
    ).not.toContain('auth-login-body-marker')
  })

  it('flags a simple-mode signup body that still carries a literal {password} marker', () => {
    const base = {
      ...form,
      authMode: 'bootstrap' as const,
      authBootstrapConfirmed: true,
      signupUrlPath: '/register',
    }
    // The shipped default signup body carries {password}.
    expect(codes(base)).toContain('auth-signup-body-marker')
    expect(
      codes({ ...base, signupBodyTemplate: '{"email":"t+{{.userIndex}}@x.com","password":"pw"}' }),
    ).not.toContain('auth-signup-body-marker')
  })

  it('flags an unfilled REPLACE_ME placeholder in the active auth material', () => {
    const login = {
      ...form,
      authMode: 'login' as const,
      loginUrlPath: '/login',
      loginBodyTemplate: '{"username": "alice", "password": "REPLACE_ME_PASSWORD"}',
    }
    expect(codes(login)).toContain('auth-replace-me')
    // Filling the secret through the highlighted input clears the error (the
    // doctor checks the SAME substituted body buildAuth would send).
    expect(
      codes({ ...login, replaceMeValues: { REPLACE_ME_PASSWORD: 's3cret' } }),
    ).not.toContain('auth-replace-me')
    // A pool with a REPLACE_ME token is equally blocked.
    expect(
      codes({ ...form, authMode: 'pool', authPoolFormat: 'tokens', authPoolText: 'REPLACE_ME_TOKEN' }),
    ).toContain('auth-replace-me')
  })

  it('warns when auth is configured but no scenario template references the token', () => {
    const got = codes({
      ...form,
      authMode: 'pool',
      authPoolFormat: 'tokens',
      authPoolText: 'tok-1',
    })
    expect(got).toContain('auth-token-unreferenced')
    // Referencing {{.token}} (or the {{basicAuth …}} helper) satisfies the check.
    const withToken = {
      ...form,
      authMode: 'pool' as const,
      authPoolFormat: 'tokens' as const,
      authPoolText: 'tok-1',
      templatesJSON: JSON.stringify({
        browse: { method: 'GET', path: '/browse', headers: { Authorization: 'Bearer {{.token}}' } },
        product: { method: 'GET', path: '/products/1' },
      }),
    }
    expect(codes(withToken)).not.toContain('auth-token-unreferenced')
    const withBasic = {
      ...withToken,
      templatesJSON: JSON.stringify({
        browse: {
          method: 'GET',
          path: '/browse',
          headers: { Authorization: 'Basic {{basicAuth .subject .token}}' },
        },
        product: { method: 'GET', path: '/products/1' },
      }),
    }
    expect(codes(withBasic)).not.toContain('auth-token-unreferenced')
  })

  it('warns when templates reference {{.token}} but auth is off', () => {
    const got = codes({
      ...form,
      templatesJSON: JSON.stringify({
        browse: { method: 'GET', path: '/browse', headers: { Authorization: 'Bearer {{.token}}' } },
        product: { method: 'GET', path: '/products/1' },
      }),
    })
    expect(got).toContain('auth-token-without-auth')
    // The clean baseline (no auth, no token reference) stays silent.
    expect(codes(form)).not.toContain('auth-token-without-auth')
  })

  it('flags a mint run with no signing-key reference', () => {
    const got = codes({ ...form, authMode: 'mint', mintKeyEnv: '', mintKeyFile: '' })
    expect(got).toContain('auth-mint-key')
  })

  it('does not flag a mint run that references a key, and flags malformed claims', () => {
    const ok = codes({ ...form, authMode: 'mint', mintKeyEnv: 'TMULA_MINT_SECRET' })
    expect(ok).not.toContain('auth-mint-key')
    expect(ok).not.toContain('auth-mint-claims')
    const bad = codes({ ...form, authMode: 'mint', mintKeyEnv: 'K', mintClaimsJSON: '{not json}' })
    expect(bad).toContain('auth-mint-claims')
  })
})
