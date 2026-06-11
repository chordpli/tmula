// Hand-rolled internationalization — no library, no dependency. A flat
// key -> string dictionary per language, a React context, and a useI18n() hook
// that returns { lang, setLang, t }. t(key, vars?) looks the key up in the active
// language, falls back to English, then to the key itself, and interpolates
// {var} placeholders. setLang persists the choice to localStorage so a reload
// keeps the operator's language.
//
// Translations are written for a non-technical Korean operator: natural phrasing
// over literal word-for-word, while keeping the few load-testing terms of art
// (p50/p95, RPS) recognizable. Backend-provided finding text is data and is shown
// verbatim — only the surrounding chrome is translated.

import { createContext, createElement, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'

export type Lang = 'en' | 'ko'

// LANGS is the ordered set of supported languages, used both to validate a stored
// value and to render the header toggle.
export const LANGS: { code: Lang; label: string }[] = [
  { code: 'en', label: 'EN' },
  { code: 'ko', label: '한국어' },
]

const STORAGE_KEY = 'tmula.lang'

// isLang narrows an arbitrary string to a supported Lang.
function isLang(v: string | null | undefined): v is Lang {
  return v === 'en' || v === 'ko'
}

// detectLang resolves the initial language: a previously stored valid choice wins;
// otherwise the browser preference picks Korean for ko* locales and English for
// everything else. It reads localStorage/navigator defensively so it is safe to
// call in any environment (tests, SSR-less builds).
export function detectLang(): Lang {
  try {
    const stored = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
    if (isLang(stored)) return stored
  } catch {
    /* localStorage unavailable (private mode, etc.) — fall through to the browser */
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language : ''
  return nav && nav.toLowerCase().startsWith('ko') ? 'ko' : 'en'
}

// interpolate replaces {name} placeholders in a template with vars[name]. An
// unknown placeholder is left untouched so a missing var is visible, not silently
// blanked.
function interpolate(template: string, vars?: Record<string, string | number>): string {
  if (!vars) return template
  return template.replace(/\{(\w+)\}/g, (match, key: string) =>
    key in vars ? String(vars[key]) : match,
  )
}

// translate is the pure lookup behind t(): active language, then English, then the
// raw key, with {var} interpolation applied to whichever string was found. It is
// exported so it can be unit-tested without a React tree.
export function translate(
  dict: Record<Lang, Record<string, string>>,
  lang: Lang,
  key: string,
  vars?: Record<string, string | number>,
): string {
  const template = dict[lang]?.[key] ?? dict.en[key] ?? key
  return interpolate(template, vars)
}

// en is the source of truth: every user-visible string the app renders. Keys are
// dotted by area (card.*, field.*, help.*, preset.*, import.*, run.*, live.*,
// report.*, viewer.*) so they read self-documentingly at the call site.
const en: Record<string, string> = {
  // Brand / header
  'brand.tagline': 'Real-user traffic simulator',
  'lang.label': 'Language',

  // Card: Target
  'card.target': 'Target',
  'card.target.hint':
    'Where the simulated traffic goes, and the hosts it is allowed to reach. Add worker addresses to fan the load out across machines.',
  'field.baseUrl': 'Base URL',
  'help.baseUrl': 'The service under test, e.g. your staging or local server.',
  'field.allowlist': 'Allowlist',
  'help.allowlist':
    'Comma-separated hosts traffic may hit — a guardrail so a run can never escape your target.',
  'allowlist.missingHost':
    'Base URL host "{host}" is not in the allowlist, so the safety guard would block this run.',
  'allowlist.addHost': 'Add host',
  'field.workers': 'Workers',
  'help.workers':
    'Optional. Comma-separated worker addresses to distribute the load. Leave blank to run on this machine.',
  'check.aggregate': 'Aggregate on workers (one summary per shard)',
  'check.aggregate.sub':
    'Scales to millions of users — each worker summarizes its shard instead of streaming every request. Findings stay run-wide.',

  // Card: Load model
  'card.load': 'Load model',
  'card.load.hintLead': 'How users hit your service.',
  'card.load.hintOpen': 'Open',
  'card.load.hintOpenRest': 'mimics organic traffic — users arrive at a rate over time.',
  'card.load.hintClosed': 'Closed',
  'card.load.hintClosedRest': 'holds a fixed pool that loops.',
  'field.workload': 'Workload',
  'help.workload': 'Open is the most realistic for a public-facing service.',
  'workload.open': 'Open — users arrive at a rate over time (organic)',
  'workload.closed': 'Closed — a fixed pool of virtual users that loop',
  'field.arrivalRate': 'Arrival rate',
  'help.arrivalRate': 'New users per second.',
  'unit.perSec': '/ sec',
  'field.duration': 'Duration',
  'help.duration': 'How long users keep arriving.',
  'unit.sec': 'sec',
  'field.maxConcurrency': 'Max concurrency',
  'help.maxConcurrency': 'Back-pressure cap. 0 = uncapped.',
  'field.thinkTime': 'Think time',
  'help.thinkTime': "Pause between a user's steps (ms, min–max).",
  'aria.thinkMin': 'Think time minimum (ms)',
  'aria.thinkMax': 'Think time maximum (ms)',
  'field.personas': 'Personas',
  'badge.advanced': 'advanced',
  'help.personas':
    'Optional JSON mix of weighted user types, each with its own entry node and pacing. Leave blank for one uniform population.',

  // Card: Scenario
  'card.scenario': 'Scenario',
  'card.scenario.hint':
    'The journey users take. Each run starts at the start node and walks the graph for up to the max steps; the JSON below defines the nodes, edges, and the API each node calls.',
  'field.start': 'Start node',
  'help.start': 'Where every user begins.',
  'field.maxSteps': 'Max steps',
  'help.maxSteps': 'Longest path a user may take before stopping.',
  'field.users': 'Virtual users',
  'help.users': 'Closed: the pool size. Open: a nominal upper bound.',
  'check.trace': 'Show live traffic while the run streams',
  'check.trace.sub': 'Per-request animation for small runs, an aggregate flow map for large ones',
  'check.trace.subWith':
    'Per-request animation for small runs, an aggregate flow map for large ones · {mode}',
  'field.graph': 'Scenario graph',
  'badge.jsonAdvanced': 'JSON · advanced',
  'help.graph': 'Nodes and weighted edges. A dependency edge must complete before its target runs.',
  'field.templates': 'API templates',
  'help.templates': 'The request each node sends: method, path, optional payloadTemplate, and response extractors.',
  'doctor.title': 'Scenario doctor',
  'doctor.clean': 'No obvious blockers.',
  'doctor.summary': '{errors} errors · {warnings} warnings',
  'doctor.severity.error': 'Error',
  'doctor.severity.warning': 'Warning',
  'doctor.more': '+{count} more',
  'doctor.allowlistMissingHost':
    'Base URL host "{host}" is not covered by the allowlist.',
  'doctor.graphJson': 'Scenario graph JSON is invalid: {error}',
  'doctor.templatesJson': 'API templates JSON is invalid: {error}',
  'doctor.segmentsJson': 'Personas JSON is invalid: {error}',
  'doctor.segmentsClosed': 'Personas are ignored in the closed workload model.',
  'doctor.segmentStartMissing': 'Persona "{name}" starts at "{node}", but that node is not in the graph.',
  'doctor.graphEmpty': 'The graph needs at least one node.',
  'doctor.nodeIDMissing': 'A graph node is missing an id.',
  'doctor.duplicateNode': 'Node "{node}" is duplicated.',
  'doctor.nodeTemplateMissing': 'Node "{node}" references missing template "{template}".',
  'doctor.startMissing': 'Start node "{node}" is not in the graph.',
  'doctor.startTerminal': 'Start node "{node}" is terminal, so a run can finish without sending a request.',
  'doctor.edgeUnknownNode': 'Edge "{from}" → "{to}" references a node that does not exist.',
  'doctor.edgeWeightInvalid': 'Edge "{from}" → "{to}" has invalid weight "{weight}".',
  'doctor.earlyTerminal': 'Start edge "{from}" → "{to}" can end the journey immediately.',
  'doctor.nodeNoIncoming': 'Node "{node}" has no incoming edge, so most users can never reach it.',
  'doctor.outgoingWeightHigh': 'Node "{node}" has outgoing weight sum {weight}; check whether the branch mix is intentional.',
  'doctor.templateShape': 'Template "{template}" must be an object.',
  'doctor.templateMethodMissing': 'Template "{template}" is missing method.',
  'doctor.templatePathMissing': 'Template "{template}" is missing path.',
  'doctor.templateExtractShape': 'Template "{template}" extract must be an object mapping variable names to JSON paths.',
  'doctor.templateExtractEntry': 'Template "{template}" extract entries need non-empty variable names and JSON paths.',
  'doctor.templateUnused': 'Template "{template}" is not used by any node.',
  'editor.title': 'Visual graph editor',
  'editor.hint': 'Edit nodes and edges; the JSON below updates with every change.',
  'editor.previewHint': 'Preview the journey; expand editing controls only when you need to change it.',
  'editor.invalid': 'Fix the graph JSON before visual editing.',
  'editor.editControls': 'Edit nodes and edges',
  'editor.hideControls': 'Hide editing controls',
  'editor.viewMode': 'Graph view mode',
  'editor.viewJourney': 'Journey',
  'editor.viewAll': 'All edges',
  'editor.nodes': 'Nodes',
  'editor.edges': 'Edges',
  'editor.countNodes': '{count} nodes',
  'editor.countEdges': '{count} edges',
  'editor.expand': 'Expand',
  'editor.collapse': 'Collapse',
  'editor.nodeID': 'Node ID',
  'editor.terminal': 'Terminal node',
  'editor.start': 'Start',
  'editor.remove': 'Remove',
  'editor.addNode': 'Add node',
  'editor.newNode': 'New node id',
  'editor.from': 'From',
  'editor.to': 'To',
  'editor.weight': 'Weight',
  'editor.dependency': 'dependency',
  'editor.addEdge': 'Add edge',

  // Presets (Feature A)
  'presets.label': 'Start from a template',
  'presets.hint': 'One click fills the scenario below — then tweak it however you like.',
  'preset.shop': 'Branching shop',
  'preset.shop.desc': 'A shopper browses, searches, and a few check out.',
  'preset.ticketing': 'Concert tickets',
  'preset.ticketing.desc': 'Browse shows, pick seats, and a few buy — under the on-sale rush.',
  'preset.health': 'Health check',
  'preset.health.desc': 'A single GET to /healthz — the simplest probe.',
  'preset.apiflow': 'API read flow',
  'preset.apiflow.desc': 'List items, open one, then leave.',
  'presets.loaded': 'Loaded template: {name}',

  // Help tooltips (Feature C) — one-line, plain-language explanations.
  'helptip.show': 'Help',
  'help.graph.tip':
    'Nodes are states bound to an apiTemplateId; edges are weighted transitions between them. weight sets how likely a path is; a dependency edge must finish before its target can run.',
  'help.templates.tip':
    'Each template is one request: method (GET/POST/…), path, optional payloadTemplate, and optional extract map for response JSON values used by later steps.',
  'help.allowlist.tip':
    'The only hosts a run is allowed to call. Anything off this list is blocked, so a test can never hit the wrong server.',
  'help.arrivalRate.tip': 'How many new users start every second in an open run.',
  'help.maxConcurrency.tip':
    'The most requests allowed in flight at once. It caps back-pressure; 0 means no cap.',
  'help.thinkTime.tip':
    'A random pause between each step of a user, picked between the min and max milliseconds — so traffic looks human, not instant.',
  'help.personas.tip':
    'Split arrivals into weighted user types, each able to start at a different node and use its own think time. Leave empty for one uniform crowd.',

  // Import (Feature B)
  'import.title': 'Import from OpenAPI / HAR / access log',
  'import.hint':
    'Turn an API spec, a recorded session, or an access log into a scenario, then review and run it. A log goes further: the branching graph is learned from how the traffic actually moved.',
  'import.file': 'Upload a file',
  'import.fileHint': 'OpenAPI (.json/.yaml), a recording (.har), or an access log (.log/.jsonl).',
  'import.paste': 'Paste spec',
  'import.pastePlaceholder': 'Paste your OpenAPI, HAR, or access-log lines here…',
  'import.format': 'Format',
  'import.format.auto': 'Auto-detect',
  'import.format.openapi': 'OpenAPI',
  'import.format.har': 'HAR',
  'import.format.accesslog': 'Access log',
  'import.button': 'Import',
  'import.importing': 'Importing…',
  'import.success': 'Imported — review the scenario below.',
  'import.emptyError': 'Choose a file or paste a spec first.',
  'import.unavailable': 'Import is not available on this server.',

  // Run
  'run.button': 'Run experiment',
  'run.running': 'Running…',
  'run.kill': 'Kill run',
  'run.allowlistBlocked':
    'Base URL host "{host}" is not in the allowlist. Add it before running so the safety guard does not block every request.',
  'run.noteOpen': '~**{rate}** users/sec for **{duration}s**',
  'run.noteClosed': '**{users}** virtual users · up to **{steps}** steps',
  'run.connLost': 'Connection lost while streaming progress.',
  'mode.local': 'local',
  'mode.distributed': 'distributed ({count} worker{plural})',
  'live.events': 'animating each request (≤{max} {unit})',
  'live.flow': 'aggregate flow map (>{max} {unit})',
  'unit.maxConcurrency': 'max concurrency',
  'unit.users': 'users',

  // Live run section
  'run.title': 'Run',
  'viz.flow.title': 'Traffic flow',
  'viz.flow.sub': 'where requests travel across your scenario',
  'viz.latency.title': 'Latency heatmap',
  'viz.latency.sub': 'request density by latency band over time',
  'viz.metrics.title': 'Live metrics',

  // Report links
  'report.viewHtml': 'View full HTML report',
  'report.compare': 'Compare with previous run',

  // Stats (StatsView)
  'stat.requests': 'Requests',
  'stat.errorRate': 'Error rate',
  'stat.errorsOne': '{count} error',
  'stat.errorsMany': '{count} errors',
  'stat.p50': 'Latency p50',
  'stat.p95': 'Latency p95',
  'stat.p99': 'Latency p99',
  'stat.max': 'max {ms} ms',
  'stat.timeouts': 'Timeouts',

  // Findings (ReportView)
  'metrics.title': 'Server metrics',
  'metrics.fetchError': 'Some series could not be fetched:',
  'findings.title': 'Findings',
  'findings.empty': 'No issues detected.',

  // LiveGraph captions
  'graph.events.title': 'Live traffic',
  'graph.events.sub': '— each dot is one request',
  'graph.legend.ok': 'ok',
  'graph.legend.error': 'error',
  'graph.flow.title': 'Traffic flow',
  'graph.flow.sub': '— edge thickness is request volume',
  'graph.flow.requests': 'requests',
  'graph.legend.healthy': 'healthy',
  'graph.legend.errors': 'errors',
  'graph.aria.events': 'Live request traffic over the scenario graph',
  'graph.aria.flow': 'Aggregate request traffic flow over the scenario graph',
  'graph.in': 'in',
  'graph.err': 'err',
  // Terminal endpoints (done/exit): inflow is an outcome, not a request.
  'graph.completed': 'completed',
  'graph.left': 'left',

  // LatencyHeatmap
  'latheat.capMain': 'Requests per latency × time bucket',
  'latheat.capSub': 'darker = more requests · high latency at top',
  'latheat.peak': 'peak',
  'latheat.perCell': '/ cell',
  'latheat.waiting': 'Waiting for traffic…',
  'latheat.waitingSub': 'cells fill in as requests complete, building a map of latency over the run',
  'latheat.waitingAria': 'Latency heatmap — waiting for the first requests',
  'latheat.cellOne': '{band} · {time} — {count} request',
  'latheat.cellMany': '{band} · {time} — {count} requests',
  'latheat.aria': 'Latency heatmap: {rows} latency bands over {cols} time buckets, color shows request density',

  // Viewer (shared report)
  'viewer.tagline': 'Shared report',
  'viewer.note': 'Read-only. Sensitive fields are redacted.',
  'viewer.loading': 'Loading…',
  'viewer.expired': 'This shared report has expired.',
  'viewer.notFound': 'This shared report was not found.',
  'viewer.unavailable': 'Shared report unavailable ({status}).',
}

// ko mirrors every key in en. Translations favor natural Korean for a
// non-technical operator; metrics terms of art (p50/p95, RPS) are kept as-is
// because that is how Korean engineers read them.
const ko: Record<string, string> = {
  // Brand / header
  'brand.tagline': '실사용자 트래픽 시뮬레이터',
  'lang.label': '언어',

  // Card: Target
  'card.target': '대상',
  'card.target.hint':
    '시뮬레이션 트래픽을 보낼 곳과, 트래픽이 닿아도 되는 호스트입니다. 워커 주소를 추가하면 부하를 여러 대에 나눠 보낼 수 있습니다.',
  'field.baseUrl': '기본 URL',
  'help.baseUrl': '테스트할 서비스입니다. 예: 스테이징 서버나 로컬 서버.',
  'field.allowlist': '허용 목록',
  'help.allowlist': '트래픽이 닿아도 되는 호스트(쉼표로 구분)입니다. 실행이 대상 밖으로 새어 나가지 않도록 막는 안전장치입니다.',
  'allowlist.missingHost':
    '기본 URL의 호스트 "{host}"가 허용 목록에 없어 안전장치가 이 실행을 차단합니다.',
  'allowlist.addHost': '호스트 추가',
  'field.workers': '워커',
  'help.workers':
    '선택 사항입니다. 부하를 분산할 워커 주소를 쉼표로 구분해 입력하세요. 비워 두면 이 컴퓨터에서 실행합니다.',
  'check.aggregate': '워커에서 집계 (샤드당 요약 1건)',
  'check.aggregate.sub':
    '수백만 사용자까지 확장됩니다. 각 워커가 모든 요청을 보내는 대신 자기 샤드를 요약합니다. 발견 항목은 실행 전체 기준으로 유지됩니다.',

  // Card: Load model
  'card.load': '부하 모델',
  'card.load.hintLead': '사용자가 서비스에 접속하는 방식입니다.',
  'card.load.hintOpen': '오픈',
  'card.load.hintOpenRest': '모델은 실제 트래픽처럼 사용자가 시간에 따라 일정 비율로 도착합니다.',
  'card.load.hintClosed': '클로즈드',
  'card.load.hintClosedRest': '모델은 고정된 사용자 풀이 반복합니다.',
  'field.workload': '부하 유형',
  'help.workload': '공개 서비스에는 오픈 모델이 가장 현실적입니다.',
  'workload.open': '오픈 — 사용자가 시간에 따라 일정 비율로 도착 (실제 트래픽형)',
  'workload.closed': '클로즈드 — 고정된 가상 사용자 풀이 반복',
  'field.arrivalRate': '도착률',
  'help.arrivalRate': '초당 새로 시작하는 사용자 수입니다.',
  'unit.perSec': '/ 초',
  'field.duration': '지속 시간',
  'help.duration': '사용자가 계속 도착하는 시간입니다.',
  'unit.sec': '초',
  'field.maxConcurrency': '최대 동시 실행',
  'help.maxConcurrency': '백프레셔 상한입니다. 0이면 제한 없음.',
  'field.thinkTime': '생각 시간',
  'help.thinkTime': '사용자의 단계 사이 대기 시간(ms, 최소–최대)입니다.',
  'aria.thinkMin': '생각 시간 최소값 (ms)',
  'aria.thinkMax': '생각 시간 최대값 (ms)',
  'field.personas': '페르소나',
  'badge.advanced': '고급',
  'help.personas':
    '가중치가 있는 사용자 유형을 JSON으로 섞어 정의합니다(선택). 유형마다 시작 노드와 속도를 따로 가질 수 있습니다. 비워 두면 단일 균일 집단으로 실행합니다.',

  // Card: Scenario
  'card.scenario': '시나리오',
  'card.scenario.hint':
    '사용자가 따라가는 여정입니다. 각 실행은 시작 노드에서 출발해 최대 단계 수만큼 그래프를 따라 이동합니다. 아래 JSON이 노드, 엣지, 그리고 각 노드가 호출하는 API를 정의합니다.',
  'field.start': '시작 노드',
  'help.start': '모든 사용자가 출발하는 지점입니다.',
  'field.maxSteps': '최대 단계',
  'help.maxSteps': '사용자가 멈추기 전까지 거칠 수 있는 가장 긴 경로입니다.',
  'field.users': '가상 사용자',
  'help.users': '클로즈드: 풀 크기. 오픈: 대략적인 상한.',
  'check.trace': '실행이 스트리밍되는 동안 실시간 트래픽 보기',
  'check.trace.sub': '작은 실행은 요청별 애니메이션, 큰 실행은 집계 흐름도로 표시합니다',
  'check.trace.subWith': '작은 실행은 요청별 애니메이션, 큰 실행은 집계 흐름도로 표시합니다 · {mode}',
  'field.graph': '시나리오 그래프',
  'badge.jsonAdvanced': 'JSON · 고급',
  'help.graph': '노드와 가중치 엣지입니다. 의존 엣지는 대상이 실행되기 전에 먼저 완료되어야 합니다.',
  'field.templates': 'API 템플릿',
  'help.templates': '각 노드가 보내는 요청입니다: 메서드, 경로, 선택적 payloadTemplate, 응답값 추출 설정.',
  'doctor.title': '시나리오 점검',
  'doctor.clean': '뚜렷한 차단 요소가 없습니다.',
  'doctor.summary': '오류 {errors}개 · 경고 {warnings}개',
  'doctor.severity.error': '오류',
  'doctor.severity.warning': '경고',
  'doctor.more': '+{count}개 더 있음',
  'doctor.allowlistMissingHost': '기본 URL의 호스트 "{host}"가 허용 목록에 포함되어 있지 않습니다.',
  'doctor.graphJson': '시나리오 그래프 JSON이 올바르지 않습니다: {error}',
  'doctor.templatesJson': 'API 템플릿 JSON이 올바르지 않습니다: {error}',
  'doctor.segmentsJson': '페르소나 JSON이 올바르지 않습니다: {error}',
  'doctor.segmentsClosed': '클로즈드 부하 모델에서는 페르소나가 무시됩니다.',
  'doctor.segmentStartMissing': '페르소나 "{name}"의 시작 노드 "{node}"가 그래프에 없습니다.',
  'doctor.graphEmpty': '그래프에는 최소 하나의 노드가 필요합니다.',
  'doctor.nodeIDMissing': '그래프 노드에 id가 없습니다.',
  'doctor.duplicateNode': '노드 "{node}"가 중복되었습니다.',
  'doctor.nodeTemplateMissing': '노드 "{node}"가 없는 템플릿 "{template}"을 참조합니다.',
  'doctor.startMissing': '시작 노드 "{node}"가 그래프에 없습니다.',
  'doctor.startTerminal': '시작 노드 "{node}"가 터미널이라 요청 없이 실행이 끝날 수 있습니다.',
  'doctor.edgeUnknownNode': '엣지 "{from}" → "{to}"가 존재하지 않는 노드를 참조합니다.',
  'doctor.edgeWeightInvalid': '엣지 "{from}" → "{to}"의 weight "{weight}"가 올바르지 않습니다.',
  'doctor.earlyTerminal': '시작 엣지 "{from}" → "{to}" 때문에 여정이 즉시 끝날 수 있습니다.',
  'doctor.nodeNoIncoming': '노드 "{node}"에는 들어오는 엣지가 없어 대부분의 사용자가 도달할 수 없습니다.',
  'doctor.outgoingWeightHigh': '노드 "{node}"의 outgoing weight 합이 {weight}입니다. 분기 비율이 의도한 값인지 확인하세요.',
  'doctor.templateShape': '템플릿 "{template}"은 객체여야 합니다.',
  'doctor.templateMethodMissing': '템플릿 "{template}"에 method가 없습니다.',
  'doctor.templatePathMissing': '템플릿 "{template}"에 path가 없습니다.',
  'doctor.templateExtractShape': '템플릿 "{template}"의 extract는 변수 이름을 JSON 경로에 매핑하는 객체여야 합니다.',
  'doctor.templateExtractEntry': '템플릿 "{template}"의 extract 항목에는 비어 있지 않은 변수 이름과 JSON 경로가 필요합니다.',
  'doctor.templateUnused': '템플릿 "{template}"은 어떤 노드에서도 사용되지 않습니다.',
  'editor.title': '시각 그래프 편집기',
  'editor.hint': '노드와 엣지를 편집하면 아래 JSON이 함께 갱신됩니다.',
  'editor.previewHint': '사용자 흐름을 먼저 보고, 수정이 필요할 때만 편집 컨트롤을 펼치세요.',
  'editor.invalid': '시각 편집을 하려면 먼저 그래프 JSON을 고쳐야 합니다.',
  'editor.editControls': '노드/엣지 편집',
  'editor.hideControls': '편집 컨트롤 접기',
  'editor.viewMode': '그래프 보기 모드',
  'editor.viewJourney': '주요 여정',
  'editor.viewAll': '전체 엣지',
  'editor.nodes': '노드',
  'editor.edges': '엣지',
  'editor.countNodes': '노드 {count}개',
  'editor.countEdges': '엣지 {count}개',
  'editor.expand': '펼치기',
  'editor.collapse': '접기',
  'editor.nodeID': '노드 ID',
  'editor.terminal': '터미널 노드',
  'editor.start': '시작',
  'editor.remove': '삭제',
  'editor.addNode': '노드 추가',
  'editor.newNode': '새 노드 id',
  'editor.from': '출발',
  'editor.to': '도착',
  'editor.weight': 'Weight',
  'editor.dependency': '의존',
  'editor.addEdge': '엣지 추가',

  // Presets (Feature A)
  'presets.label': '템플릿으로 시작하기',
  'presets.hint': '클릭 한 번으로 아래 시나리오가 채워집니다. 이후 원하는 대로 수정하세요.',
  'preset.shop': '분기형 쇼핑',
  'preset.shop.desc': '방문자가 둘러보고 검색하며, 일부가 결제합니다.',
  'preset.ticketing': '공연 티켓',
  'preset.ticketing.desc': '공연을 둘러보고 좌석을 고른 뒤, 일부가 구매합니다 — 예매 오픈 혼잡 상황.',
  'preset.health': '헬스 체크',
  'preset.health.desc': '/healthz로 GET 한 번 — 가장 단순한 점검입니다.',
  'preset.apiflow': 'API 조회 흐름',
  'preset.apiflow.desc': '목록을 보고 하나를 열어 본 뒤 나갑니다.',
  'presets.loaded': '템플릿 적용됨: {name}',

  // Help tooltips (Feature C)
  'helptip.show': '도움말',
  'help.graph.tip':
    '노드는 apiTemplateId에 연결된 상태이고, 엣지는 노드 사이의 가중치 있는 전이입니다. weight는 경로가 선택될 확률을 정하고, dependency 엣지는 대상이 실행되기 전에 먼저 끝나야 합니다.',
  'help.templates.tip':
    '각 템플릿은 요청 하나입니다: 메서드(GET/POST 등), 경로, 선택적 payloadTemplate, 그리고 다음 단계에서 쓸 응답 JSON extract map.',
  'help.allowlist.tip':
    '실행이 호출해도 되는 호스트만 적습니다. 목록에 없는 곳은 차단되므로 테스트가 엉뚱한 서버에 닿을 일이 없습니다.',
  'help.arrivalRate.tip': '오픈 실행에서 매초 새로 시작하는 사용자 수입니다.',
  'help.maxConcurrency.tip':
    '동시에 진행될 수 있는 최대 요청 수입니다. 백프레셔를 제한하며, 0이면 제한이 없습니다.',
  'help.thinkTime.tip':
    '사용자의 각 단계 사이에 두는 무작위 대기 시간으로, 최소~최대 밀리초 사이에서 정해집니다. 덕분에 트래픽이 즉각적이지 않고 사람처럼 보입니다.',
  'help.personas.tip':
    '도착하는 사용자를 가중치 있는 유형으로 나눕니다. 유형마다 다른 노드에서 시작하고 자기 생각 시간을 쓸 수 있습니다. 비우면 단일 균일 집단으로 실행합니다.',

  // Import (Feature B)
  'import.title': 'OpenAPI / HAR / 액세스 로그에서 가져오기',
  'import.hint':
    'API 명세, 기록된 세션, 또는 액세스 로그를 시나리오로 변환한 뒤 검토하고 실행하세요. 로그는 한 걸음 더 나아가 실제 트래픽이 움직인 대로 분기 그래프를 학습합니다.',
  'import.file': '파일 올리기',
  'import.fileHint': 'OpenAPI(.json/.yaml), 기록 파일(.har), 또는 액세스 로그(.log/.jsonl).',
  'import.paste': '명세 붙여넣기',
  'import.pastePlaceholder': 'OpenAPI, HAR, 또는 액세스 로그를 여기에 붙여 넣으세요…',
  'import.format': '형식',
  'import.format.auto': '자동 감지',
  'import.format.openapi': 'OpenAPI',
  'import.format.har': 'HAR',
  'import.format.accesslog': '액세스 로그',
  'import.button': '가져오기',
  'import.importing': '가져오는 중…',
  'import.success': '가져왔습니다 — 아래 시나리오를 검토하세요.',
  'import.emptyError': '먼저 파일을 고르거나 명세를 붙여 넣으세요.',
  'import.unavailable': '이 서버에서는 가져오기를 사용할 수 없습니다.',

  // Run
  'run.button': '실험 실행',
  'run.running': '실행 중…',
  'run.kill': '실행 중단',
  'run.allowlistBlocked':
    '기본 URL의 호스트 "{host}"가 허용 목록에 없습니다. 모든 요청이 안전장치에 막히지 않도록 실행 전에 추가하세요.',
  'run.noteOpen': '약 초당 **{rate}**명씩 **{duration}초** 동안',
  'run.noteClosed': '가상 사용자 **{users}**명 · 최대 **{steps}**단계',
  'run.connLost': '진행 상황을 스트리밍하는 중 연결이 끊겼습니다.',
  'mode.local': '로컬',
  'mode.distributed': '분산 (워커 {count}대)',
  'live.events': '요청마다 애니메이션 (≤{max} {unit})',
  'live.flow': '집계 흐름도 (>{max} {unit})',
  'unit.maxConcurrency': '최대 동시 실행',
  'unit.users': '사용자',

  // Live run section
  'run.title': '실행',
  'viz.flow.title': '트래픽 흐름',
  'viz.flow.sub': '요청이 시나리오를 따라 어디로 이동하는지',
  'viz.latency.title': '지연 시간 히트맵',
  'viz.latency.sub': '시간에 따른 지연 구간별 요청 밀도',
  'viz.metrics.title': '실시간 지표',

  // Report links
  'report.viewHtml': '전체 HTML 보고서 보기',
  'report.compare': '이전 실행과 비교',

  // Stats (StatsView)
  'stat.requests': '요청 수',
  'stat.errorRate': '오류율',
  'stat.errorsOne': '오류 {count}건',
  'stat.errorsMany': '오류 {count}건',
  'stat.p50': '지연 p50',
  'stat.p95': '지연 p95',
  'stat.p99': '지연 p99',
  'stat.max': '최대 {ms} ms',
  'stat.timeouts': '타임아웃',

  // Findings (ReportView)
  'metrics.title': '서버 메트릭',
  'metrics.fetchError': '일부 시계열을 가져오지 못했습니다:',
  'findings.title': '발견 항목',
  'findings.empty': '발견된 문제가 없습니다.',

  // LiveGraph captions
  'graph.events.title': '실시간 트래픽',
  'graph.events.sub': '— 점 하나가 요청 하나입니다',
  'graph.legend.ok': '정상',
  'graph.legend.error': '오류',
  'graph.flow.title': '트래픽 흐름',
  'graph.flow.sub': '— 엣지 두께는 요청량입니다',
  'graph.flow.requests': '요청',
  'graph.legend.healthy': '정상',
  'graph.legend.errors': '오류',
  'graph.aria.events': '시나리오 그래프 위의 실시간 요청 트래픽',
  'graph.aria.flow': '시나리오 그래프 위의 집계 요청 트래픽 흐름',
  'graph.in': '유입',
  'graph.err': '오류',
  // 종료 노드(done/exit): 유입은 요청이 아니라 결과(완료/이탈)입니다.
  'graph.completed': '완료',
  'graph.left': '이탈',

  // LatencyHeatmap
  'latheat.capMain': '지연 × 시간 구간별 요청 수',
  'latheat.capSub': '진할수록 요청이 많음 · 높은 지연은 위쪽',
  'latheat.peak': '최대',
  'latheat.perCell': '/ 셀',
  'latheat.waiting': '트래픽 대기 중…',
  'latheat.waitingSub': '요청이 완료될 때마다 셀이 채워지며 실행 동안의 지연 지도를 그립니다',
  'latheat.waitingAria': '지연 시간 히트맵 — 첫 요청을 기다리는 중',
  'latheat.cellOne': '{band} · {time} — 요청 {count}건',
  'latheat.cellMany': '{band} · {time} — 요청 {count}건',
  'latheat.aria': '지연 시간 히트맵: {cols}개 시간 구간에 걸친 {rows}개 지연 구간, 색이 요청 밀도를 나타냅니다',

  // Viewer (shared report)
  'viewer.tagline': '공유 보고서',
  'viewer.note': '읽기 전용입니다. 민감한 필드는 가려졌습니다.',
  'viewer.loading': '불러오는 중…',
  'viewer.expired': '이 공유 보고서는 만료되었습니다.',
  'viewer.notFound': '이 공유 보고서를 찾을 수 없습니다.',
  'viewer.unavailable': '공유 보고서를 사용할 수 없습니다 ({status}).',
}

// dict bundles both languages. en is also the fallback source inside translate().
export const dict: Record<Lang, Record<string, string>> = { en, ko }

// I18nValue is what useI18n() exposes: the active language, a setter that persists,
// and the translate function bound to the active language + dictionary.
export interface I18nValue {
  lang: Lang
  setLang: (lang: Lang) => void
  t: (key: string, vars?: Record<string, string | number>) => string
}

const I18nContext = createContext<I18nValue | null>(null)

// I18nProvider holds the active language in state, persists changes to
// localStorage, and provides a memoized value so consumers only re-render when the
// language actually changes. It uses createElement (not JSX) so this stays a .ts
// file alongside the pure dictionary/helpers.
export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(detectLang)

  const setLang = useCallback((next: Lang) => {
    setLangState(next)
    try {
      if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, next)
    } catch {
      /* persisting is best-effort; ignore a storage failure */
    }
  }, [])

  const value = useMemo<I18nValue>(
    () => ({
      lang,
      setLang,
      t: (key, vars) => translate(dict, lang, key, vars),
    }),
    [lang, setLang],
  )

  return createElement(I18nContext.Provider, { value }, children)
}

// useI18n returns the active i18n value. It throws when used outside the provider
// so a missing <I18nProvider> is caught immediately rather than silently using
// English.
export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext)
  if (!ctx) throw new Error('useI18n must be used within an I18nProvider')
  return ctx
}
