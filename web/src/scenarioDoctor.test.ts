import { describe, expect, it } from 'vitest'
import type { ExperimentForm } from './api'
import { doctorForm } from './scenarioDoctor'

const form: ExperimentForm = {
  baseUrl: 'http://localhost:9000',
  allowlist: 'localhost',
  users: 3,
  maxSteps: 5,
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
})
