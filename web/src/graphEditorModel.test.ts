import { describe, expect, it } from 'vitest'
import {
  addEdge,
  addNode,
  parseEditableGraph,
  removeNode,
  templateIDsFromJSON,
  templateSummaryFromJSON,
  updateEdge,
  updateNode,
  updateTemplateInJSON,
  type EditableGraph,
} from './graphEditorModel'

const graph: EditableGraph = {
  id: 'g',
  nodes: [
    { id: 'browse', apiTemplateId: 'browse' },
    { id: 'cart', apiTemplateId: 'cart' },
  ],
  edges: [{ from: 'browse', to: 'cart', weight: 0.7 }],
}

describe('graph editor model', () => {
  it('parses graph JSON into an editable graph', () => {
    expect(parseEditableGraph(JSON.stringify(graph))).toEqual(graph)
    expect(parseEditableGraph('not json')).toBeNull()
  })

  it('adds unique nodes and ignores duplicate ids', () => {
    const withNode = addNode(graph, 'checkout', 'pay')
    expect(withNode.nodes).toContainEqual({ id: 'checkout', apiTemplateId: 'pay' })
    expect(addNode(withNode, 'checkout').nodes).toHaveLength(withNode.nodes.length)
  })

  it('renames a node and rewrites connected edges', () => {
    const renamed = updateNode(graph, 1, { id: 'checkout' })
    expect(renamed.nodes[1].id).toBe('checkout')
    expect(renamed.edges[0]).toEqual({ from: 'browse', to: 'checkout', weight: 0.7 })
  })

  it('removes a node and prunes its edges', () => {
    const pruned = removeNode(graph, 1)
    expect(pruned.nodes).toEqual([{ id: 'browse', apiTemplateId: 'browse' }])
    expect(pruned.edges).toEqual([])
  })

  it('adds and updates edges', () => {
    const extended = addEdge(graph, 'cart', 'browse')
    expect(extended.edges).toContainEqual({ from: 'cart', to: 'browse', weight: 1 })
    expect(addEdge(graph, 'cart', 'browse', 0.3).edges).toContainEqual({ from: 'cart', to: 'browse', weight: 0.3 })
    expect(addEdge(graph, 'cart', 'browse', -2).edges).toContainEqual({ from: 'cart', to: 'browse', weight: 0 })
    const updated = updateEdge(extended, 1, { weight: -3, dependency: true })
    expect(updated.edges[1]).toEqual({ from: 'cart', to: 'browse', weight: 0, dependency: true })
  })

  it('lists template ids from JSON', () => {
    expect(templateIDsFromJSON('{"cart":{},"browse":{}}')).toEqual(['browse', 'cart'])
    expect(templateIDsFromJSON('not json')).toEqual([])
  })

  it('summarizes a template as its editable method and path', () => {
    const json = '{"t_browse":{"method":"GET","path":"/products","extract":{"id":"$.items[0].id"}}}'
    expect(templateSummaryFromJSON(json, 't_browse')).toEqual({ method: 'GET', path: '/products' })
    expect(templateSummaryFromJSON(json, 'missing')).toBeNull()
    expect(templateSummaryFromJSON('not json', 't_browse')).toBeNull()
  })

  it('patches method/path on a template while preserving its other fields', () => {
    const json = '{"t_browse":{"method":"GET","path":"/products","extract":{"id":"$.items[0].id"}}}'
    const next = JSON.parse(updateTemplateInJSON(json, 't_browse', { path: '/catalog' }))
    expect(next.t_browse).toEqual({ method: 'GET', path: '/catalog', extract: { id: '$.items[0].id' } })
  })

  it('creates a template on patch when it does not exist yet', () => {
    const next = JSON.parse(updateTemplateInJSON('{}', 't_new', { method: 'POST', path: '/orders' }))
    expect(next.t_new).toEqual({ method: 'POST', path: '/orders' })
  })

  it('returns the original text when the templates JSON does not parse', () => {
    expect(updateTemplateInJSON('not json', 't', { path: '/x' })).toBe('not json')
  })
})
