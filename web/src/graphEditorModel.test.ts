import { describe, expect, it } from 'vitest'
import {
  addEdge,
  addNode,
  parseEditableGraph,
  removeNode,
  templateIDsFromJSON,
  updateEdge,
  updateNode,
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
    const updated = updateEdge(extended, 1, { weight: -3, dependency: true })
    expect(updated.edges[1]).toEqual({ from: 'cart', to: 'browse', weight: 0, dependency: true })
  })

  it('lists template ids from JSON', () => {
    expect(templateIDsFromJSON('{"cart":{},"browse":{}}')).toEqual(['browse', 'cart'])
    expect(templateIDsFromJSON('not json')).toEqual([])
  })
})
