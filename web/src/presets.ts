// Scenario presets — one-click starting points so a non-developer can fill the
// Scenario card with a working journey instead of writing graph/template JSON by
// hand. Each preset carries the graph + templates as plain JS objects (the card
// stringifies them with JSON.stringify(…, null, 2) on apply) plus the start node,
// max steps, and an optional base URL. The labels reference i18n keys so the chip
// row and the "Loaded template" note are bilingual.

export interface Preset {
  id: string
  nameKey: string
  descKey: string
  graph: unknown
  templates: unknown
  start: string
  maxSteps: number
  baseUrl?: string
}

// The shop preset is the original branching-shop demo, kept byte-for-byte in sync
// with what the Scenario card shipped as its default so "Start from a template"
// reproduces the familiar journey exactly: a shopper browses, may search or jump to
// a category, lands on a product, and a fraction add to cart and check out (the
// cart -> checkout edge is a dependency). The exit edges drain the rest so traffic
// spreads realistically across the graph the instant a run starts.
const shopGraph = {
  id: 'shop',
  nodes: [
    { id: 'browse', apiTemplateId: 't_browse' },
    { id: 'search', apiTemplateId: 't_search' },
    { id: 'category', apiTemplateId: 't_category' },
    { id: 'product', apiTemplateId: 't_product' },
    { id: 'cart', apiTemplateId: 't_cart' },
    { id: 'checkout', apiTemplateId: 't_checkout' },
    { id: 'done' },
    { id: 'exit' },
  ],
  edges: [
    { from: 'browse', to: 'search', weight: 0.4 },
    { from: 'browse', to: 'category', weight: 0.4 },
    { from: 'browse', to: 'exit', weight: 0.2 },
    { from: 'search', to: 'product', weight: 0.65 },
    { from: 'search', to: 'category', weight: 0.15 },
    { from: 'search', to: 'exit', weight: 0.2 },
    { from: 'category', to: 'product', weight: 0.7 },
    { from: 'category', to: 'browse', weight: 0.15 },
    { from: 'category', to: 'exit', weight: 0.15 },
    { from: 'product', to: 'cart', weight: 0.45 },
    { from: 'product', to: 'browse', weight: 0.25 },
    { from: 'product', to: 'exit', weight: 0.3 },
    { from: 'cart', to: 'checkout', weight: 0.6, dependency: true },
    { from: 'cart', to: 'exit', weight: 0.4 },
    { from: 'checkout', to: 'done', weight: 1.0 },
  ],
}

const shopTemplates = {
  t_browse: { method: 'GET', path: '/browse' },
  t_search: { method: 'GET', path: '/search' },
  t_category: { method: 'GET', path: '/category' },
  t_product: { method: 'GET', path: '/product' },
  t_cart: { method: 'POST', path: '/cart', payloadTemplate: '{"productId":"p7","qty":1}' },
  t_checkout: { method: 'POST', path: '/checkout', payloadTemplate: '{"total":42}' },
}

// The ticketing preset is a second full domain (server/examples/ticketing-api): buying
// concert seats. A buyer browses shows, opens one, checks seats, and a fraction
// hold a seat then pay (hold -> pay is a dependency); exit edges drain seat-pickers
// and holders who never pay. It points at the ticketing API's own port so picking
// it switches the whole target, not just the graph.
const ticketingGraph = {
  id: 'ticketing',
  nodes: [
    { id: 'events', apiTemplateId: 't_events' },
    { id: 'event', apiTemplateId: 't_event' },
    { id: 'seats', apiTemplateId: 't_seats' },
    { id: 'hold', apiTemplateId: 't_hold' },
    { id: 'pay', apiTemplateId: 't_pay' },
    { id: 'done' },
    { id: 'exit' },
  ],
  edges: [
    { from: 'events', to: 'event', weight: 0.75 },
    { from: 'events', to: 'exit', weight: 0.25 },
    { from: 'event', to: 'seats', weight: 0.8 },
    { from: 'event', to: 'exit', weight: 0.2 },
    { from: 'seats', to: 'hold', weight: 0.55 },
    { from: 'seats', to: 'exit', weight: 0.45 },
    { from: 'hold', to: 'pay', weight: 0.7, dependency: true },
    { from: 'hold', to: 'exit', weight: 0.3 },
    { from: 'pay', to: 'done', weight: 1.0 },
  ],
}

const ticketingTemplates = {
  t_events: { method: 'GET', path: '/events' },
  t_event: { method: 'GET', path: '/events/e7' },
  t_seats: { method: 'GET', path: '/seats' },
  t_hold: { method: 'POST', path: '/hold', payloadTemplate: '{"eventId":"e7","seat":"A12"}' },
  t_pay: { method: 'POST', path: '/pay', payloadTemplate: '{"total":120}' },
}

// The health preset is the smallest useful scenario: a single GET against /healthz
// with no edges, so one click gives a non-dev an instant "is it up?" probe.
const healthGraph = {
  id: 'health',
  nodes: [{ id: 'check', apiTemplateId: 't_check' }],
  edges: [],
}

const healthTemplates = {
  t_check: { method: 'GET', path: '/healthz' },
}

// The apiflow preset is a small read-only flow: list a collection, drill into one
// item, then exit — a realistic shape for sanity-checking a read API. The weights
// branch a little (most users open the detail, some leave from the list) so the
// flow map shows movement rather than a single straight line.
const apiflowGraph = {
  id: 'apiflow',
  nodes: [
    { id: 'list', apiTemplateId: 't_list' },
    { id: 'detail', apiTemplateId: 't_detail' },
    { id: 'exit' },
  ],
  edges: [
    { from: 'list', to: 'detail', weight: 0.7 },
    { from: 'list', to: 'exit', weight: 0.3 },
    { from: 'detail', to: 'exit', weight: 1.0 },
  ],
}

const apiflowTemplates = {
  t_list: { method: 'GET', path: '/items' },
  t_detail: { method: 'GET', path: '/items/1' },
}

// presets is the ordered list rendered as chips in the Scenario card. Order is the
// display order: the two full demos (shop, then ticketing) first, then the
// lightweight starters (health, apiflow).
export const presets: Preset[] = [
  {
    id: 'shop',
    nameKey: 'preset.shop',
    descKey: 'preset.shop.desc',
    graph: shopGraph,
    templates: shopTemplates,
    start: 'browse',
    maxSteps: 12,
  },
  {
    id: 'ticketing',
    nameKey: 'preset.ticketing',
    descKey: 'preset.ticketing.desc',
    graph: ticketingGraph,
    templates: ticketingTemplates,
    start: 'events',
    maxSteps: 10,
    baseUrl: 'http://localhost:9100',
  },
  {
    id: 'health',
    nameKey: 'preset.health',
    descKey: 'preset.health.desc',
    graph: healthGraph,
    templates: healthTemplates,
    start: 'check',
    maxSteps: 1,
  },
  {
    id: 'apiflow',
    nameKey: 'preset.apiflow',
    descKey: 'preset.apiflow.desc',
    graph: apiflowGraph,
    templates: apiflowTemplates,
    start: 'list',
    maxSteps: 5,
  },
]
