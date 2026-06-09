import { useState } from 'react'
import {
  addEdge,
  addNode,
  parseEditableGraph,
  removeEdge,
  removeNode,
  stringifyEditableGraph,
  templateIDsFromJSON,
  updateEdge,
  updateNode,
  type EditableGraph,
} from './graphEditorModel'
import { buildPreviewGeometry, previewGeometryForMode, type PreviewEdgeMode } from './graphPreviewModel'
import { useI18n } from './i18n'

export default function GraphEditor({
  graphJSON,
  templatesJSON,
  start,
  onGraphJSONChange,
  onStartChange,
}: {
  graphJSON: string
  templatesJSON: string
  start: string
  onGraphJSONChange: (json: string) => void
  onStartChange: (start: string) => void
}) {
  const { t } = useI18n()
  const graph = parseEditableGraph(graphJSON)
  const templateIDs = templateIDsFromJSON(templatesJSON)
  const [newNodeID, setNewNodeID] = useState('')
  const [newNodeTemplate, setNewNodeTemplate] = useState('')
  const [newEdgeFrom, setNewEdgeFrom] = useState('')
  const [newEdgeTo, setNewEdgeTo] = useState('')
  const [controlsOpen, setControlsOpen] = useState(false)
  const [nodesOpen, setNodesOpen] = useState(true)
  const [edgesOpen, setEdgesOpen] = useState(() => !isNarrowViewport())
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
    if (removed && start === removed) onStartChange(next.nodes[0]?.id ?? '')
  }

  function createNode() {
    const nextID = newNodeID.trim()
    if (!nextID) return
    const next = addNode(graph!, nextID, newNodeTemplate)
    commit(next)
    if (!start) onStartChange(nextID)
    setNewNodeID('')
  }

  function createEdge() {
    const from = newEdgeFrom || graph!.nodes[0]?.id || ''
    const to = newEdgeTo || graph!.nodes[1]?.id || graph!.nodes[0]?.id || ''
    commit(addEdge(graph!, from, to))
  }

  const nodeIDs = graph.nodes.map((n) => n.id)

  return (
    <div className="editor">
      <div className="editor__head">
        <div className="editor__headcopy">
          <span className="editor__title">{t('editor.title')}</span>
          <span className="editor__hint">{controlsOpen ? t('editor.hint') : t('editor.previewHint')}</span>
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
          <button
            type="button"
            className="btn btn--ghost editor__toggle"
            aria-expanded={controlsOpen}
            onClick={() => setControlsOpen((open) => !open)}
          >
            {controlsOpen ? t('editor.hideControls') : t('editor.editControls')}
          </button>
        </div>
      </div>

      <div className="editor-preview-wrap">
        <GraphPreview graph={graph} start={start} mode={previewMode} />
      </div>

      {controlsOpen && (
      <div className="editor__grid">
        <section className="editor__panel" aria-label={t('editor.nodes')}>
          <button
            type="button"
            className="editor__panelhead"
            aria-expanded={nodesOpen}
            onClick={() => setNodesOpen((open) => !open)}
          >
            <span>{t('editor.nodes')}</span>
            <span className="editor__panelmeta">
              {t('editor.countNodes', { count: graph.nodes.length })} ·{' '}
              {nodesOpen ? t('editor.collapse') : t('editor.expand')}
            </span>
          </button>
          {nodesOpen && (
            <div className="editor__rows">
              {graph.nodes.map((node, i) => (
                <div className="editor-row editor-row--node" key={`${node.id}-${i}`}>
                  <input
                    className="input editor-row__id"
                    value={node.id}
                    aria-label={t('editor.nodeID')}
                    onChange={(e) => changeNodeID(i, e.target.value)}
                  />
                  <TemplateSelect
                    value={node.apiTemplateId ?? ''}
                    templateIDs={templateIDs}
                    onChange={(value) => commit(updateNode(graph, i, { apiTemplateId: value }))}
                  />
                  <button
                    type="button"
                    className="btn btn--ghost editor-row__button"
                    onClick={() => onStartChange(node.id)}
                    disabled={start === node.id}
                  >
                    {t('editor.start')}
                  </button>
                  <button
                    type="button"
                    className="btn btn--ghost editor-row__button"
                    onClick={() => deleteNode(i)}
                  >
                    {t('editor.remove')}
                  </button>
                </div>
              ))}
              <div className="editor-row editor-row--node">
                <input
                  className="input editor-row__id"
                  value={newNodeID}
                  aria-label={t('editor.newNode')}
                  onChange={(e) => setNewNodeID(e.target.value)}
                  placeholder={t('editor.newNode')}
                />
                <TemplateSelect
                  value={newNodeTemplate}
                  templateIDs={templateIDs}
                  onChange={setNewNodeTemplate}
                />
                <button type="button" className="btn btn--primary editor-row__button" onClick={createNode}>
                  {t('editor.addNode')}
                </button>
              </div>
            </div>
          )}
        </section>

        <section className="editor__panel" aria-label={t('editor.edges')}>
          <button
            type="button"
            className="editor__panelhead"
            aria-expanded={edgesOpen}
            onClick={() => setEdgesOpen((open) => !open)}
          >
            <span>{t('editor.edges')}</span>
            <span className="editor__panelmeta">
              {t('editor.countEdges', { count: graph.edges.length })} ·{' '}
              {edgesOpen ? t('editor.collapse') : t('editor.expand')}
            </span>
          </button>
          {edgesOpen && (
            <div className="editor__rows">
              {graph.edges.map((edge, i) => (
                <div className="editor-row editor-row--edge" key={`${edge.from}-${edge.to}-${i}`}>
                  <NodeSelect
                    label={t('editor.from')}
                    value={edge.from}
                    nodeIDs={nodeIDs}
                    onChange={(value) => commit(updateEdge(graph, i, { from: value }))}
                  />
                  <NodeSelect
                    label={t('editor.to')}
                    value={edge.to}
                    nodeIDs={nodeIDs}
                    onChange={(value) => commit(updateEdge(graph, i, { to: value }))}
                  />
                  <input
                    className="input editor-row__weight"
                    type="number"
                    min={0}
                    step={0.1}
                    value={edge.weight}
                    aria-label={t('editor.weight')}
                    onChange={(e) => commit(updateEdge(graph, i, { weight: Number(e.target.value) || 0 }))}
                  />
                  <label className="editor-row__check">
                    <input
                      type="checkbox"
                      checked={Boolean(edge.dependency)}
                      onChange={(e) => commit(updateEdge(graph, i, { dependency: e.target.checked }))}
                    />
                    {t('editor.dependency')}
                  </label>
                  <button
                    type="button"
                    className="btn btn--ghost editor-row__button"
                    onClick={() => commit(removeEdge(graph, i))}
                  >
                    {t('editor.remove')}
                  </button>
                </div>
              ))}
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
                <button type="button" className="btn btn--primary editor-row__button" onClick={createEdge}>
                  {t('editor.addEdge')}
                </button>
              </div>
            </div>
          )}
        </section>
      </div>
      )}
    </div>
  )
}

function isNarrowViewport(): boolean {
  return typeof window !== 'undefined' && window.matchMedia('(max-width: 720px)').matches
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

function GraphPreview({ graph, start, mode }: { graph: EditableGraph; start: string; mode: PreviewEdgeMode }) {
  const rawPreview = buildPreviewGeometry(graph, start)
  const preview = rawPreview ? previewGeometryForMode(rawPreview, graph, mode, start) : null
  if (!preview) return null
  const { positions: pos, routes, viewBox } = preview
  const drawableRoutes = routes.filter((route) => route.kind !== 'back' && route.kind !== 'exit')
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
      style={{ minWidth: `${mode === 'all' ? Math.max(720, Math.ceil(viewBox.width)) : 720}px` }}
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
          ].join(' ')}
          d={route.d}
          markerEnd={route.edge.dependency ? 'url(#editor-arrow-dep)' : 'url(#editor-arrow)'}
        >
          <title>{`${route.edge.from} -> ${route.edge.to}: ${route.edge.weight}`}</title>
        </path>
      ))}
      {routes.filter((route) => route.showLabel).map((route) => (
        <g
          key={`${route.edge.from}-${route.edge.to}-${route.index}-label`}
          className={`editor-preview__label editor-preview__label--${route.kind}`}
          transform={`translate(${route.label.x}, ${route.label.y})`}
        >
          <rect x={-route.label.width / 2} y="-11" width={route.label.width} height="22" rx="7" />
          <text y="0">{route.label.value}</text>
        </g>
      ))}
      {graph.nodes.filter((node) => visibleNodeIDs.has(node.id)).map((node) => {
        const p = pos[node.id]
        if (!p) return null
        const cls = [
          'editor-preview__node',
          node.id === start ? 'editor-preview__node--start' : '',
          node.apiTemplateId ? '' : 'editor-preview__node--terminal',
        ].join(' ')
        return (
          <g key={node.id} className={cls} transform={`translate(${p.x}, ${p.y})`}>
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
