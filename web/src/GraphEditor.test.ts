import { describe, expect, it } from 'vitest'
import { buildPreviewGeometry, previewGeometryForMode } from './graphPreviewModel'
import type { EditableGraph } from './graphEditorModel'

const branchingGraph: EditableGraph = {
  id: 'branching',
  nodes: [
    { id: 'browse', apiTemplateId: 't_browse' },
    { id: 'search', apiTemplateId: 't_search' },
    { id: 'category', apiTemplateId: 't_category' },
    { id: 'product', apiTemplateId: 't_product' },
    { id: 'exit' },
  ],
  edges: [
    { from: 'browse', to: 'search', weight: 0.4 },
    { from: 'browse', to: 'category', weight: 0.4 },
    { from: 'browse', to: 'exit', weight: 0.2 },
    { from: 'search', to: 'category', weight: 0.15 },
    { from: 'category', to: 'browse', weight: 0.15 },
    { from: 'category', to: 'product', weight: 0.7 },
  ],
}

describe('buildPreviewGeometry', () => {
  it('routes every edge but only labels the readable overview edges', () => {
    const preview = buildPreviewGeometry(branchingGraph, 'browse')

    expect(preview).not.toBeNull()
    expect(preview!.routes).toHaveLength(branchingGraph.edges.length)
    for (const route of preview!.routes) {
      expect(route.d).toMatch(/^M /)
      if (route.kind === 'exit') {
        // Exit edges stay a chip with a short connector stub to the node.
        expect(route.d).toContain(' L ')
      } else {
        // Everything else — including back/lateral edges — is a real curved arrow.
        expect(route.d).toContain(' C ')
      }
      const expectedLabel = route.kind === 'exit' ? `exit ${route.edge.weight}` : String(route.edge.weight)
      expect(route.label.value).toBe(expectedLabel)
      expect(route.label.width).toBeGreaterThan(0)
    }
    const labels = preview!.routes
      .filter((route) => route.showLabel)
      .map((route) => `${route.edge.from}->${route.edge.to}:${route.label.value}`)
      .sort()
    expect(labels).toEqual([
      'browse->category:0.4',
      'browse->exit:exit 0.2',
      'browse->search:0.4',
      'category->browse:0.15',
      'category->product:0.7',
      'search->category:0.15',
    ])
    expect(findRoute(preview!, 'browse', 'exit').kind).toBe('exit')
    expect(findRoute(preview!, 'browse', 'exit').showLabel).toBe(true)
    expect(findRoute(preview!, 'browse', 'exit').label.value).toBe('exit 0.2')
    expect(findRoute(preview!, 'category', 'browse').kind).toBe('back')
    expect(findRoute(preview!, 'category', 'browse').showLabel).toBe(true)
    expect(findRoute(preview!, 'category', 'product').kind).toBe('primary')
    expect(findRoute(preview!, 'category', 'product').showLabel).toBe(true)
  })

  it('spreads several outgoing edges across separate source ports', () => {
    const preview = buildPreviewGeometry(branchingGraph, 'browse')!
    const browseRoutes = preview.routes.filter((route) => route.edge.from === 'browse')
    const startYs = browseRoutes.map((route) => Number(route.d.match(/^M [-\d.]+ ([-\d.]+)/)?.[1]))

    expect(new Set(startYs).size).toBe(3)
  })

  it('arcs lateral and back edges around the node row as real arrows', () => {
    const preview = buildPreviewGeometry(branchingGraph, 'browse')!
    const sameColumn = preview.routes.find((route) => route.edge.from === 'search' && route.edge.to === 'category')!
    const back = preview.routes.find((route) => route.edge.from === 'category' && route.edge.to === 'browse')!
    const exit = preview.routes.find((route) => route.edge.from === 'browse' && route.edge.to === 'exit')!

    // Vertical neighbors arc out the left side of both nodes.
    expect(sameColumn.kind).toBe('back')
    expect(sameColumn.d).toContain(' C ')
    expect(sameColumn.label.value).toBe('0.15')
    expect(sameColumn.bounds.minX).toBeLessThan(preview.positions.search.x - 48)
    // Backward edges arc around the row from source to target.
    expect(back.d).toContain(' C ')
    expect(back.d).not.toContain(' L ')
    expect(back.label.value).toBe('0.15')
    expect(back.bounds.minX).toBeLessThanOrEqual(preview.positions.browse.x)
    // Exit edges remain a chip with a short connector stub.
    expect(exit.d).toContain(' L ')
    expect(exit.bounds.maxX - exit.bounds.minX).toBeLessThan(120)
    expect(preview.positions.exit.y).toBeGreaterThan(preview.positions.category.y)
  })

  it('keeps journey mode focused on the main readable path', () => {
    const preview = buildPreviewGeometry(branchingGraph, 'browse')!
    const journey = previewGeometryForMode(preview, branchingGraph, 'journey', 'browse')
    const all = previewGeometryForMode(preview, branchingGraph, 'all', 'browse')

    expect(journey.routes.map((route) => route.kind)).toEqual(['primary', 'primary', 'primary'])
    expect(journey.routes.some((route) => route.kind === 'exit' || route.kind === 'back')).toBe(false)
    expect(journey.viewBox.height).toBeLessThan(preview.viewBox.height)
    expect(all.viewBox.height).toBeLessThan(preview.viewBox.height)
  })
})

function findRoute(preview: NonNullable<ReturnType<typeof buildPreviewGeometry>>, from: string, to: string) {
  const route = preview.routes.find((candidate) => candidate.edge.from === from && candidate.edge.to === to)
  if (!route) throw new Error(`missing route ${from} -> ${to}`)
  return route
}
