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
import { buildPreviewGeometry, previewGeometryForMode, type PreviewEdgeMode } from './graphPreviewModel'
import { useI18n } from './i18n'

// Selection is what the operator last clicked in the preview: one node or one
// edge, addressed by its index in the parsed graph. The graph itself stays the
// single source of truth (in the JSON the parent owns); selection is only a view
// state, cleared whenever the addressed item is removed.
type Selection = { kind: 'node'; index: number } | { kind: 'edge'; index: number } | null

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
  const [previewMode, setPreviewMode] = useState<PreviewEdgeMode>('journey')

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
    if (oldID && start === oldID) onStartChange(nextID)
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
    const next = addNode(graph!, nextID, newNodeTemplate)
    commit(next)
    if (!start) onStartChange(nextID)
    setNewNodeID('')
    const created = next.nodes.findIndex((n) => n.id === nextID)
    if (created >= 0) setSelection({ kind: 'node', index: created })
  }

  function createEdge() {
    const from = newEdgeFrom || graph!.nodes[0]?.id || ''
    const to = newEdgeTo || graph!.nodes[1]?.id || graph!.nodes[0]?.id || ''
    const next = addEdge(graph!, from, to)
    commit(next)
    const created = next.edges.findIndex((e) => e.from === from && e.to === to)
    if (created >= 0) setSelection({ kind: 'edge', index: created })
  }

  const nodeIDs = graph.nodes.map((n) => n.id)
  const selectedNode = selection?.kind === 'node' ? graph.nodes[selection.index] : undefined
  const selectedEdge = selection?.kind === 'edge' ? graph.edges[selection.index] : undefined

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
                {mode === 'journey' ? t('editor.viewJourney') : t('editor.viewAll')}
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
          selection={selection}
          onSelectNode={(index) => setSelection({ kind: 'node', index })}
          onSelectEdge={(index) => setSelection({ kind: 'edge', index })}
        />
      </div>

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
                onChange={(e) => changeNodeID(selection.index, e.target.value)}
              />
            </label>
            <label className="field">
              <span className="field__label">{t('editor.template')}</span>
              <TemplateSelect
                value={selectedNode.apiTemplateId ?? ''}
                templateIDs={templateIDs}
                onChange={(value) => commit(updateNode(graph, selection.index, { apiTemplateId: value }))}
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
              onClick={() => deleteNode(selection.index)}
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
                onChange={(value) => commit(updateEdge(graph, selection.index, { from: value }))}
              />
            </label>
            <label className="field">
              <span className="field__label">{t('editor.to')}</span>
              <NodeSelect
                label={t('editor.to')}
                value={selectedEdge.to}
                nodeIDs={nodeIDs}
                onChange={(value) => commit(updateEdge(graph, selection.index, { to: value }))}
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
                onChange={(e) => commit(updateEdge(graph, selection.index, { weight: Number(e.target.value) || 0 }))}
              />
            </label>
            <label className="editor-row__check">
              <input
                type="checkbox"
                checked={Boolean(selectedEdge.dependency)}
                onChange={(e) => commit(updateEdge(graph, selection.index, { dependency: e.target.checked }))}
              />
              {t('editor.dependency')}
            </label>
          </div>
          <div className="editor-sel__actions">
            <button
              type="button"
              className="btn btn--ghost editor-row__button"
              onClick={() => deleteEdge(selection.index)}
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
          <button type="button" className="btn btn--ghost editor-row__button" onClick={createEdge}>
            {t('editor.addEdge')}
          </button>
        </div>
      </div>
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
      <label className="field editor-sel__method">
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
function edgeStrokeWidth(weight: number): number {
  const clamped = Math.max(0, Math.min(1, weight))
  return 1.2 + clamped * 2.6
}

function GraphPreview({
  graph,
  start,
  mode,
  selection,
  onSelectNode,
  onSelectEdge,
}: {
  graph: EditableGraph
  start: string
  mode: PreviewEdgeMode
  selection: Selection
  onSelectNode: (index: number) => void
  onSelectEdge: (index: number) => void
}) {
  const { t } = useI18n()
  const rawPreview = buildPreviewGeometry(graph, start)
  const preview = rawPreview ? previewGeometryForMode(rawPreview, graph, mode, start) : null
  if (!preview) return null
  const { positions: pos, routes, viewBox } = preview
  const drawableRoutes = routes
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
  const selectedEdgeIndex = selection?.kind === 'edge' ? selection.index : -1
  const selectedNodeID = selection?.kind === 'node' ? graph.nodes[selection.index]?.id : undefined
  return (
    <svg
      className={`editor-preview editor-preview--${mode}`}
      viewBox={`${viewBox.minX} ${viewBox.minY} ${viewBox.width} ${viewBox.height}`}
      role="img"
    >
      <defs>
        <marker id="editor-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path className="editor-preview__arrow" d="M 0 0 L 10 5 L 0 10 z" />
        </marker>
        <marker
          id="editor-arrow-dep"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path className="editor-preview__arrow editor-preview__arrow--dep" d="M 0 0 L 10 5 L 0 10 z" />
        </marker>
      </defs>
      {drawableRoutes.map((route) => (
        <path
          key={`${route.edge.from}-${route.edge.to}-${route.index}-halo`}
          className={`editor-preview__edge-halo editor-preview__edge-halo--${route.kind}`}
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
          ].join(' ')}
          style={route.kind === 'back' || route.kind === 'exit' ? undefined : { strokeWidth: edgeStrokeWidth(route.edge.weight) }}
          d={route.d}
          markerEnd={
            route.kind === 'back' || route.kind === 'exit'
              ? undefined
              : route.edge.dependency
                ? 'url(#editor-arrow-dep)'
                : 'url(#editor-arrow)'
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
          ].join(' ')}
          transform={`translate(${route.label.x}, ${route.label.y})`}
          onClick={() => onSelectEdge(route.index)}
        >
          <rect x={-route.label.width / 2} y="-11" width={route.label.width} height="22" rx="7" />
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
