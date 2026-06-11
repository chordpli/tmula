import { useState } from 'react'
import {
  addEdge,
  addNode,
  parseEditableGraph,
  removeEdge,
  removeNode,
  stringifyEditableGraph,
  templateIDsFromJSON,
  templateSummaryFromJSON,
  updateEdge,
  updateNode,
  updateTemplateInJSON,
  type EditableGraph,
} from './graphEditorModel'
import { buildPreviewGeometry, previewGeometryForMode, type PreviewEdgeMode, type PreviewRoute } from './graphPreviewModel'
import { useI18n } from './i18n'

// Selection is what the operator last clicked in the preview: one node or one
// edge, addressed by identity (node id, edge from→to) rather than index, so an
// out-of-band edit — e.g. reordering the raw JSON in the accordion — can never
// silently retarget the panel at a different item. The selection is re-resolved
// against the freshly parsed graph on every render and simply dissolves when the
// addressed item no longer exists.
type Selection = { kind: 'node'; id: string } | { kind: 'edge'; from: string; to: string } | null

export default function GraphEditor({
  graphJSON,
  templatesJSON,
  start,
  onGraphJSONChange,
  onTemplatesJSONChange,
  onStartChange,
}: {
  graphJSON: string
  templatesJSON: string
  start: string
  onGraphJSONChange: (json: string) => void
  onTemplatesJSONChange: (json: string) => void
  onStartChange: (start: string) => void
}) {
  const { t } = useI18n()
  const graph = parseEditableGraph(graphJSON)
  const templateIDs = templateIDsFromJSON(templatesJSON)
  const [selection, setSelection] = useState<Selection>(null)
  const [newNodeID, setNewNodeID] = useState('')
  const [newNodeTemplate, setNewNodeTemplate] = useState('')
  const [newEdgeFrom, setNewEdgeFrom] = useState('')
  const [newEdgeTo, setNewEdgeTo] = useState('')
  const [newEdgeWeight, setNewEdgeWeight] = useState(1)
  const [previewMode, setPreviewMode] = useState<PreviewEdgeMode>('journey')
  const [hoverInfo, setHoverInfo] = useState<string | null>(null)

  if (!graph) {
    return (
      <div className="editor editor--invalid">
        <div className="editor__head">
          <span className="editor__title">{t('editor.title')}</span>
          <span className="editor__hint">{t('editor.invalid')}</span>
        </div>
      </div>
    )
  }

  function commit(next: EditableGraph) {
    onGraphJSONChange(stringifyEditableGraph(next))
  }

  function changeNodeID(index: number, nextID: string) {
    const oldID = graph!.nodes[index]?.id
    commit(updateNode(graph!, index, { id: nextID }))
    setSelection({ kind: 'node', id: nextID.trim() })
    if (oldID && start === oldID) onStartChange(nextID)
  }

  function changeEdgeEndpoint(index: number, patch: { from?: string; to?: string }) {
    const edge = graph!.edges[index]
    if (!edge) return
    commit(updateEdge(graph!, index, patch))
    setSelection({ kind: 'edge', from: patch.from ?? edge.from, to: patch.to ?? edge.to })
  }

  function deleteNode(index: number) {
    const removed = graph!.nodes[index]?.id
    const next = removeNode(graph!, index)
    commit(next)
    setSelection(null)
    if (removed && start === removed) onStartChange(next.nodes[0]?.id ?? '')
  }

  function deleteEdge(index: number) {
    commit(removeEdge(graph!, index))
    setSelection(null)
  }

  function createNode() {
    const nextID = newNodeID.trim()
    if (!nextID) return
    commit(addNode(graph!, nextID, newNodeTemplate))
    if (!start) onStartChange(nextID)
    setNewNodeID('')
    setSelection({ kind: 'node', id: nextID })
  }

  function createEdge() {
    const from = newEdgeFrom || graph!.nodes[0]?.id || ''
    const to = newEdgeTo || graph!.nodes[1]?.id || graph!.nodes[0]?.id || ''
    if (!from || !to) return
    commit(addEdge(graph!, from, to, newEdgeWeight))
    setSelection({ kind: 'edge', from, to })
  }

  const nodeIDs = graph.nodes.map((n) => n.id)
  // Re-resolve the identity-based selection against the current graph; a stale
  // identity (item edited away in the raw JSON) resolves to nothing and the
  // panel quietly closes instead of editing the wrong item.
  const selectedNodeIndex =
    selection?.kind === 'node' ? graph.nodes.findIndex((n) => n.id === selection.id) : -1
  const selectedNode = selectedNodeIndex >= 0 ? graph.nodes[selectedNodeIndex] : undefined
  const selectedEdgeIndex =
    selection?.kind === 'edge'
      ? graph.edges.findIndex((e) => e.from === selection.from && e.to === selection.to)
      : -1
  const selectedEdge = selectedEdgeIndex >= 0 ? graph.edges[selectedEdgeIndex] : undefined

  return (
    <div className="editor">
      <div className="editor__head">
        <div className="editor__headcopy">
          <span className="editor__title">{t('editor.title')}</span>
          <span className="editor__hint">{t('editor.clickHint')}</span>
        </div>
        <div className="editor__actions">
          <div className="editor-mode" role="group" aria-label={t('editor.viewMode')}>
            {(['journey', 'all'] as const).map((mode) => (
              <button
                key={mode}
                type="button"
                className={`editor-mode__btn ${previewMode === mode ? 'editor-mode__btn--on' : ''}`}
                aria-pressed={previewMode === mode}
                onClick={() => setPreviewMode(mode)}
              >
                {mode === 'journey'
                  ? t('editor.viewJourney')
                  : t('editor.viewAll', { count: graph.edges.length })}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="editor-preview-wrap">
        <GraphPreview
          graph={graph}
          start={start}
          mode={previewMode}
          selectedNodeID={selectedNode?.id}
          selectedEdgeIndex={selectedEdgeIndex}
          onSelectNode={(index) => {
            const node = graph.nodes[index]
            if (node) setSelection({ kind: 'node', id: node.id })
          }}
          onSelectEdge={(index) => {
            const edge = graph.edges[index]
            if (edge) setSelection({ kind: 'edge', from: edge.from, to: edge.to })
          }}
          onClearSelection={() => setSelection(null)}
          onHoverInfo={setHoverInfo}
        />
        {hoverInfo && <div className="editor-hoverinfo">{hoverInfo}</div>}
      </div>

      <GraphLegend />

      {selection?.kind === 'node' && selectedNode && (
        <section className="editor-sel" aria-label={t('editor.selNode')}>
          <div className="editor-sel__head">
            <span className="editor-sel__title">
              {t('editor.selNode')} · <code>{selectedNode.id}</code>
            </span>
            <button type="button" className="btn btn--ghost editor-row__button" onClick={() => setSelection(null)}>
              {t('editor.done')}
            </button>
          </div>
          <div className="editor-sel__grid">
            <label className="field">
              <span className="field__label">{t('editor.nodeID')}</span>
              <input
                className="input"
                value={selectedNode.id}
                onChange={(e) => changeNodeID(selectedNodeIndex, e.target.value)}
              />
            </label>
            <label className="field">
              <span className="field__label">{t('editor.template')}</span>
              <TemplateSelect
                value={selectedNode.apiTemplateId ?? ''}
                templateIDs={templateIDs}
                onChange={(value) => commit(updateNode(graph, selectedNodeIndex, { apiTemplateId: value }))}
              />
            </label>
            {selectedNode.apiTemplateId && (
              <TemplateMiniForm
                templatesJSON={templatesJSON}
                templateID={selectedNode.apiTemplateId}
                onTemplatesJSONChange={onTemplatesJSONChange}
              />
            )}
          </div>
          <div className="editor-sel__actions">
            <button
              type="button"
              className="btn btn--ghost editor-row__button"
              onClick={() => onStartChange(selectedNode.id)}
              disabled={start === selectedNode.id}
            >
              {t('editor.start')}
            </button>
            <button
              type="button"
              className="btn btn--ghost editor-row__button"
              onClick={() => deleteNode(selectedNodeIndex)}
            >
              {t('editor.remove')}
            </button>
          </div>
        </section>
      )}

      {selection?.kind === 'edge' && selectedEdge && (
        <section className="editor-sel" aria-label={t('editor.selEdge')}>
          <div className="editor-sel__head">
            <span className="editor-sel__title">
              {t('editor.selEdge')} · <code>{selectedEdge.from} → {selectedEdge.to}</code>
            </span>
            <button type="button" className="btn btn--ghost editor-row__button" onClick={() => setSelection(null)}>
              {t('editor.done')}
            </button>
          </div>
          <div className="editor-sel__grid">
            <label className="field">
              <span className="field__label">{t('editor.from')}</span>
              <NodeSelect
                label={t('editor.from')}
                value={selectedEdge.from}
                nodeIDs={nodeIDs}
                onChange={(value) => changeEdgeEndpoint(selectedEdgeIndex, { from: value })}
              />
            </label>
            <label className="field">
              <span className="field__label">{t('editor.to')}</span>
              <NodeSelect
                label={t('editor.to')}
                value={selectedEdge.to}
                nodeIDs={nodeIDs}
                onChange={(value) => changeEdgeEndpoint(selectedEdgeIndex, { to: value })}
              />
            </label>
            <label className="field">
              <span className="field__label">{t('editor.weight')}</span>
              <input
                className="input editor-row__weight"
                type="number"
                min={0}
                step={0.1}
                value={selectedEdge.weight}
                onChange={(e) => commit(updateEdge(graph, selectedEdgeIndex, { weight: Number(e.target.value) || 0 }))}
              />
            </label>
            <label className="editor-row__check">
              <input
                type="checkbox"
                checked={Boolean(selectedEdge.dependency)}
                onChange={(e) => commit(updateEdge(graph, selectedEdgeIndex, { dependency: e.target.checked }))}
              />
              {t('editor.dependency')}
            </label>
          </div>
          <div className="editor-sel__actions">
            <button
              type="button"
              className="btn btn--ghost editor-row__button"
              onClick={() => deleteEdge(selectedEdgeIndex)}
            >
              {t('editor.remove')}
            </button>
          </div>
        </section>
      )}

      <div className="editor-add">
        <div className="editor-row editor-row--node">
          <input
            className="input editor-row__id"
            value={newNodeID}
            aria-label={t('editor.newNode')}
            onChange={(e) => setNewNodeID(e.target.value)}
            placeholder={t('editor.newNode')}
          />
          <TemplateSelect value={newNodeTemplate} templateIDs={templateIDs} onChange={setNewNodeTemplate} />
          <button type="button" className="btn btn--ghost editor-row__button" onClick={createNode}>
            {t('editor.addNode')}
          </button>
        </div>
        <div className="editor-row editor-row--edge">
          <NodeSelect
            label={t('editor.from')}
            value={newEdgeFrom || nodeIDs[0] || ''}
            nodeIDs={nodeIDs}
            onChange={setNewEdgeFrom}
          />
          <NodeSelect
            label={t('editor.to')}
            value={newEdgeTo || nodeIDs[1] || nodeIDs[0] || ''}
            nodeIDs={nodeIDs}
            onChange={setNewEdgeTo}
          />
          <input
            className="input editor-row__weight"
            type="number"
            min={0}
            step={0.1}
            value={newEdgeWeight}
            aria-label={t('editor.weight')}
            onChange={(e) => setNewEdgeWeight(Math.max(0, Number(e.target.value) || 0))}
          />
          <button type="button" className="btn btn--ghost editor-row__button" onClick={createEdge}>
            {t('editor.addEdge')}
          </button>
        </div>
      </div>
    </div>
  )
}

// GraphLegend decodes the preview's visual language in one quiet line: what the
// solid/dashed/accent strokes mean, that thickness encodes weight, and how a
// terminal node is drawn — so a first-time operator never has to guess.
function GraphLegend() {
  const { t } = useI18n()
  return (
    <div className="editor-legend">
      <span className="editor-legend__item">
        <span className="editor-legend__line editor-legend__line--primary" aria-hidden="true" />
        {t('legend.primary')}
      </span>
      <span className="editor-legend__item">
        <span className="editor-legend__line editor-legend__line--back" aria-hidden="true" />
        {t('legend.back')}
      </span>
      <span className="editor-legend__item">
        <span className="editor-legend__line editor-legend__line--dep" aria-hidden="true" />
        {t('legend.dep')}
      </span>
      <span className="editor-legend__item">
        <span className="editor-legend__box" aria-hidden="true" />
        {t('legend.terminal')}
      </span>
    </div>
  )
}

// TemplateMiniForm edits the method/path of one API template inline, so a node's
// request can be adjusted without opening the raw templates JSON. All other
// template fields are preserved by updateTemplateInJSON.
function TemplateMiniForm({
  templatesJSON,
  templateID,
  onTemplatesJSONChange,
}: {
  templatesJSON: string
  templateID: string
  onTemplatesJSONChange: (json: string) => void
}) {
  const { t } = useI18n()
  const summary = templateSummaryFromJSON(templatesJSON, templateID) ?? { method: '', path: '' }
  return (
    <>
      <label className="field">
        <span className="field__label">{t('editor.method')}</span>
        <select
          className="select"
          value={summary.method}
          onChange={(e) => onTemplatesJSONChange(updateTemplateInJSON(templatesJSON, templateID, { method: e.target.value }))}
        >
          {!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(summary.method) && (
            <option value={summary.method}>{summary.method || '—'}</option>
          )}
          {['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span className="field__label">{t('editor.path')}</span>
        <input
          className="input"
          value={summary.path}
          placeholder="/api/items"
          spellCheck={false}
          onChange={(e) => onTemplatesJSONChange(updateTemplateInJSON(templatesJSON, templateID, { path: e.target.value }))}
        />
      </label>
    </>
  )
}

function TemplateSelect({
  value,
  templateIDs,
  onChange,
}: {
  value: string
  templateIDs: string[]
  onChange: (value: string) => void
}) {
  const { t } = useI18n()
  const ids = value && !templateIDs.includes(value) ? [value, ...templateIDs] : templateIDs
  return (
    <select className="select editor-row__template" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{t('editor.terminal')}</option>
      {ids.map((id) => (
        <option key={id} value={id}>
          {id}
        </option>
      ))}
    </select>
  )
}

function NodeSelect({
  label,
  value,
  nodeIDs,
  onChange,
}: {
  label: string
  value: string
  nodeIDs: string[]
  onChange: (value: string) => void
}) {
  return (
    <select className="select editor-row__node" value={value} aria-label={label} onChange={(e) => onChange(e.target.value)}>
      {nodeIDs.map((id) => (
        <option key={id} value={id}>
          {id}
        </option>
      ))}
    </select>
  )
}

// edgeStrokeWidth encodes the edge's weight (a transition probability) as line
// thickness, so the traffic split reads at a glance instead of only via labels.
// The range is kept narrow (1.1–3.0) so a full-weight edge stays a line, not a bar.
function edgeStrokeWidth(weight: number): number {
  const clamped = Math.max(0, Math.min(1, weight))
  return 1.1 + clamped * 1.9
}

// routeStrokeWidth is the stroke each route actually renders with: exit stubs
// keep their CSS width (no head), completion edges are fixed (weight 1 is not
// a branch probability), everything else encodes its weight.
function routeStrokeWidth(route: PreviewRoute): number | undefined {
  if (route.kind === 'exit') return undefined
  if (route.kind === 'completion') return 1.8
  return edgeStrokeWidth(route.edge.weight)
}

// headSizeFor scales the arrowhead with its line: a thin 1.1px stroke gets a
// ~8.5px head, the thickest 3px stroke a ~11.5px one — so head and line stay
// in proportion ("in sync") across every weight instead of one fixed size
// looking stubby on thick lines and bloated on thin ones. Sizes are rounded
// to 0.5px so routes share a small set of marker definitions.
function headSizeFor(strokeWidth: number): number {
  return Math.round((6.5 + strokeWidth * 1.6) * 2) / 2
}

// markerIDFor names the shared marker definition for a given head size.
function markerIDFor(size: number, dep: boolean): string {
  return `editor-arrow-${String(size).replace('.', '_')}${dep ? '-dep' : ''}`
}

function GraphPreview({
  graph,
  start,
  mode,
  selectedNodeID,
  selectedEdgeIndex,
  onSelectNode,
  onSelectEdge,
  onClearSelection,
  onHoverInfo,
}: {
  graph: EditableGraph
  start: string
  mode: PreviewEdgeMode
  selectedNodeID?: string
  selectedEdgeIndex: number
  onSelectNode: (index: number) => void
  onSelectEdge: (index: number) => void
  onClearSelection: () => void
  onHoverInfo: (info: string | null) => void
}) {
  const { t } = useI18n()
  // hover narrows the view to one node or edge and its neighborhood: everything
  // unrelated dims so a path can be traced through a dense graph at a glance.
  const [hover, setHover] = useState<{ kind: 'node'; id: string } | { kind: 'edge'; index: number } | null>(null)
  const rawPreview = buildPreviewGeometry(graph, start)
  const preview = rawPreview ? previewGeometryForMode(rawPreview, graph, mode, start) : null
  if (!preview) return null
  const { positions: pos, routes, viewBox } = preview
  const drawableRoutes = routes
  const hotEdgeIndexes = new Set<number>()
  const hotNodeIDs = new Set<string>()
  if (hover?.kind === 'node') {
    hotNodeIDs.add(hover.id)
    for (const route of routes) {
      if (route.edge.from === hover.id || route.edge.to === hover.id) {
        hotEdgeIndexes.add(route.index)
        hotNodeIDs.add(route.edge.from)
        hotNodeIDs.add(route.edge.to)
      }
    }
  } else if (hover?.kind === 'edge') {
    const hot = routes.find((route) => route.index === hover.index)
    if (hot) {
      hotEdgeIndexes.add(hot.index)
      hotNodeIDs.add(hot.edge.from)
      hotNodeIDs.add(hot.edge.to)
    }
  }
  const dimming = hover !== null && (hotEdgeIndexes.size > 0 || hotNodeIDs.size > 0)
  const coldEdge = (index: number) => (dimming && !hotEdgeIndexes.has(index) ? ' editor-preview__cold' : '')
  const coldNode = (id: string) => (dimming && !hotNodeIDs.has(id) ? ' editor-preview__cold' : '')
  const headSizes = Array.from(
    new Set(
      drawableRoutes
        .filter((route) => route.kind !== 'exit')
        .map((route) => headSizeFor(routeStrokeWidth(route) ?? 1.8)),
    ),
  ).sort((a, b) => a - b)
  const visibleNodeIDs = new Set<string>([start])
  const hiddenExitNodeIDs = new Set(routes.filter((route) => route.kind === 'exit').map((route) => route.edge.to))
  if (mode === 'all') {
    for (const node of graph.nodes) {
      if (!hiddenExitNodeIDs.has(node.id)) visibleNodeIDs.add(node.id)
    }
  } else {
    for (const route of routes) {
      visibleNodeIDs.add(route.edge.from)
      visibleNodeIDs.add(route.edge.to)
    }
  }
  return (
    <svg
      className={`editor-preview editor-preview--${mode}`}
      viewBox={`${viewBox.minX} ${viewBox.minY} ${viewBox.width} ${viewBox.height}`}
      role="img"
      onClick={(e) => {
        // A click on bare canvas (not bubbled up from a node/edge child)
        // dismisses the selection — the standard editor-canvas affordance.
        if (e.target === e.currentTarget) onClearSelection()
      }}
    >
      <defs>
        {/* One marker definition per head size in use. Heads scale with their
            line's stroke width (headSizeFor) so head and line stay in
            proportion at every weight. refX=9 (in viewBox units) tucks the
            head's body over the line's last stretch, capping the stroke
            directly and covering any trailing dash gap on dashed edges. */}
        {headSizes.flatMap((size) => [
          <marker
            key={markerIDFor(size, false)}
            id={markerIDFor(size, false)}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth={size}
            markerHeight={size}
            markerUnits="userSpaceOnUse"
            orient="auto-start-reverse"
          >
            <path className="editor-preview__arrow" d="M 0 1 L 10 5 L 0 9 z" />
          </marker>,
          <marker
            key={markerIDFor(size, true)}
            id={markerIDFor(size, true)}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth={size}
            markerHeight={size}
            markerUnits="userSpaceOnUse"
            orient="auto-start-reverse"
          >
            <path className="editor-preview__arrow editor-preview__arrow--dep" d="M 0 1 L 10 5 L 0 9 z" />
          </marker>,
        ])}
      </defs>
      {drawableRoutes.map((route) => (
        <path
          key={`${route.edge.from}-${route.edge.to}-${route.index}-halo`}
          className={`editor-preview__edge-halo editor-preview__edge-halo--${route.kind}${coldEdge(route.index)}`}
          d={route.d}
        />
      ))}
      {drawableRoutes.map((route) => (
        <path
          key={`${route.edge.from}-${route.edge.to}-${route.index}`}
          className={[
            'editor-preview__edge',
            `editor-preview__edge--${route.kind}`,
            route.edge.dependency ? 'editor-preview__edge--dep' : '',
            route.index === selectedEdgeIndex ? 'editor-preview__edge--selected' : '',
            coldEdge(route.index).trim(),
          ].join(' ')}
          style={
            // Weight-to-thickness only applies where weight is a branch
            // probability; completion edges keep their fixed CSS stroke and
            // exit stubs have no head at all.
            route.kind === 'exit' || route.kind === 'completion'
              ? undefined
              : { strokeWidth: routeStrokeWidth(route) }
          }
          d={route.d}
          markerEnd={
            route.kind === 'exit'
              ? undefined
              : `url(#${markerIDFor(headSizeFor(routeStrokeWidth(route) ?? 1.8), Boolean(route.edge.dependency))})`
          }
        >
          <title>{`${route.edge.from} -> ${route.edge.to}: ${route.edge.weight}`}</title>
        </path>
      ))}
      {/* Invisible wide hit targets so a thin edge is comfortably clickable. */}
      {drawableRoutes.map((route) => (
        <path
          key={`${route.edge.from}-${route.edge.to}-${route.index}-hit`}
          className="editor-preview__hit"
          d={route.d}
          tabIndex={0}
          role="button"
          aria-label={`${t('editor.selEdge')}: ${route.edge.from} → ${route.edge.to}`}
          onClick={() => onSelectEdge(route.index)}
          onMouseEnter={() => {
            setHover({ kind: 'edge', index: route.index })
            onHoverInfo(`${route.edge.from} → ${route.edge.to} · ${route.edge.weight}`)
          }}
          onMouseLeave={() => {
            setHover(null)
            onHoverInfo(null)
          }}
          onFocus={() => {
            setHover({ kind: 'edge', index: route.index })
            onHoverInfo(`${route.edge.from} → ${route.edge.to} · ${route.edge.weight}`)
          }}
          onBlur={() => {
            setHover(null)
            onHoverInfo(null)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              onSelectEdge(route.index)
            }
          }}
        >
          <title>{`${route.edge.from} -> ${route.edge.to}: ${route.edge.weight}`}</title>
        </path>
      ))}
      {routes.filter((route) => route.showLabel).map((route) => (
        <g
          key={`${route.edge.from}-${route.edge.to}-${route.index}-label`}
          className={[
            `editor-preview__label editor-preview__label--${route.kind}`,
            route.index === selectedEdgeIndex ? 'editor-preview__label--selected' : '',
            coldEdge(route.index).trim(),
          ].join(' ')}
          transform={`translate(${route.label.x}, ${route.label.y})`}
        >
          <rect x={-route.label.width / 2} y="-10" width={route.label.width} height="20" rx="7" />
          <text y="0">{route.label.value}</text>
        </g>
      ))}
      {graph.nodes.map((node, nodeIndex) => {
        if (!visibleNodeIDs.has(node.id)) return null
        const p = pos[node.id]
        if (!p) return null
        const cls = [
          'editor-preview__node',
          node.id === start ? 'editor-preview__node--start' : '',
          node.apiTemplateId ? '' : 'editor-preview__node--terminal',
          node.id === selectedNodeID ? 'editor-preview__node--selected' : '',
          coldNode(node.id).trim(),
        ].join(' ')
        return (
          <g
            key={node.id}
            className={cls}
            transform={`translate(${p.x}, ${p.y})`}
            tabIndex={0}
            role="button"
            aria-label={`${t('editor.selNode')}: ${node.id}`}
            onClick={() => onSelectNode(nodeIndex)}
            onMouseEnter={() => {
              setHover({ kind: 'node', id: node.id })
              onHoverInfo(`${node.id} · ${node.apiTemplateId || 'terminal'}`)
            }}
            onMouseLeave={() => {
              setHover(null)
              onHoverInfo(null)
            }}
            onFocus={() => {
              setHover({ kind: 'node', id: node.id })
              onHoverInfo(`${node.id} · ${node.apiTemplateId || 'terminal'}`)
            }}
            onBlur={() => {
              setHover(null)
              onHoverInfo(null)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onSelectNode(nodeIndex)
              }
            }}
          >
            <rect x="-48" y="-22" width="96" height="44" rx="8" />
            <text y="-2">{node.id}</text>
            <text className="editor-preview__template" y="14">
              {node.apiTemplateId || 'terminal'}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
