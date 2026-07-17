import {
  allowlistMatchesHost,
  hostFromBaseUrl,
  loginBodyReferencesRow,
  parseAllowlist,
  parseCredentials,
  parseLoginCredentials,
  parseSegments,
  parseSignupSteps,
  type ExperimentForm,
} from './api'

export type DoctorSeverity = 'error' | 'warning'

export interface DoctorIssue {
  severity: DoctorSeverity
  code: string
  messageKey: string
  vars?: Record<string, string | number>
}

interface GraphNode {
  id?: unknown
  apiTemplateId?: unknown
}

interface GraphEdge {
  from?: unknown
  to?: unknown
  weight?: unknown
  dependency?: unknown
}

interface ScenarioGraphShape {
  nodes?: unknown
  edges?: unknown
}

interface TemplateShape {
  method?: unknown
  path?: unknown
  extract?: unknown
}

type TemplateMap = Record<string, TemplateShape>

export function doctorForm(form: ExperimentForm): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  const baseHost = hostFromBaseUrl(form.baseUrl)
  if (baseHost && !allowlistMatchesHost(parseAllowlist(form.allowlist), baseHost)) {
    issues.push(issue('error', 'allowlist-missing-host', 'doctor.allowlistMissingHost', { host: baseHost }))
  }

  const graphResult = parseJSON<ScenarioGraphShape>(form.graphJSON)
  if (!graphResult.ok) {
    issues.push(issue('error', 'graph-json', 'doctor.graphJson', { error: graphResult.error }))
  }
  const templatesResult = parseJSON<TemplateMap>(form.templatesJSON)
  if (!templatesResult.ok) {
    issues.push(issue('error', 'templates-json', 'doctor.templatesJson', { error: templatesResult.error }))
  }

  if (form.workloadKind === 'open') {
    try {
      const segments = parseSegments(form.segmentsJSON)
      if (graphResult.ok) {
        const nodeIDs = nodeIDSet(graphResult.value)
        for (const seg of segments) {
          if (seg.start && !nodeIDs.has(seg.start)) {
            issues.push(issue('error', 'segment-start', 'doctor.segmentStartMissing', { name: seg.name, node: seg.start }))
          }
        }
      }
    } catch (e) {
      issues.push(issue('error', 'segments-json', 'doctor.segmentsJson', { error: messageOf(e) }))
    }
  } else if (form.segmentsJSON.trim()) {
    issues.push(issue('warning', 'segments-closed', 'doctor.segmentsClosed'))
  }

  if (graphResult.ok) {
    issues.push(...doctorGraph(graphResult.value, form.start, templatesResult.ok ? templatesResult.value : null))
  }
  if (templatesResult.ok && graphResult.ok) {
    issues.push(...doctorTemplates(templatesResult.value, graphResult.value))
  }
  issues.push(...doctorAuth(form))
  return issues
}

// doctorAuth surfaces the auth-section blockers so a misconfigured pool/login/bootstrap
// is caught here (before a 400 from the server), mirroring the scenario checks. None
// has nothing to check. Each strategy reports its own missing/invalid pieces; a parse
// failure is an error (the run cannot send it), while a stranded-accounts bootstrap is
// the load-bearing safety warning the gating rule enforces.
function doctorAuth(form: ExperimentForm): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  if (form.authMode === 'pool') {
    if (!form.authPoolText.trim()) {
      issues.push(issue('error', 'auth-pool-empty', 'doctor.authPoolEmpty'))
    } else {
      try {
        parseCredentials(form.authPoolFormat, form.authPoolText)
      } catch (e) {
        issues.push(issue('error', 'auth-pool-invalid', 'doctor.authPoolInvalid', { error: messageOf(e) }))
      }
    }
  } else if (form.authMode === 'login') {
    // An empty token capture is fine: tmula auto-detects the token from the login
    // response (E1), so no "missing capture path" warning is raised. The simple
    // mini-form only needs a request path (buildAuth compiles the rest); the advanced
    // mode still validates the raw graph/templates JSON.
    if (form.loginMode === 'simple') {
      if (!form.loginUrlPath.trim()) {
        issues.push(issue('error', 'auth-login-url', 'doctor.authLoginUrl'))
      }
    } else {
      const graph = parseJSON(form.loginGraphJSON)
      if (!graph.ok) {
        issues.push(issue('error', 'auth-login-graph-json', 'doctor.authLoginGraphJson', { error: graph.error }))
      }
      const templates = parseJSON(form.loginTemplatesJSON)
      if (!templates.ok) {
        issues.push(
          issue('error', 'auth-login-templates-json', 'doctor.authLoginTemplatesJson', { error: templates.error }),
        )
      }
    }
    // "Log in multiple users": when a credential list is supplied, validate it parses
    // (a malformed list cannot be sent) and warn when the login body never references a
    // row — every virtual user would then log in with the same literal body, defeating
    // the list. The body warning only applies to the simple mini-form, where the body is
    // the editable template; the advanced mode authors the body inside raw templates.
    if (form.loginCredText.trim()) {
      let credsOk = false
      try {
        parseLoginCredentials(form.loginCredFormat, form.loginCredText)
        credsOk = true
      } catch (e) {
        issues.push(issue('error', 'auth-login-cred-invalid', 'doctor.authLoginCredInvalid', { error: messageOf(e) }))
      }
      if (credsOk && form.loginMode === 'simple' && !loginBodyReferencesRow(form.loginBodyTemplate)) {
        issues.push(issue('warning', 'auth-login-cred-unused', 'doctor.authLoginCredUnused'))
      }
    }
  } else if (form.authMode === 'bootstrap') {
    if (!form.authBootstrapConfirmed) {
      issues.push(issue('error', 'auth-bootstrap-unconfirmed', 'doctor.authBootstrapUnconfirmed'))
    }
    // An empty token capture is fine: tmula auto-detects the token from the signup
    // response (E1), so no "missing capture path" warning is raised. The simple
    // mini-form only needs a signup path; advanced validates the raw steps JSON.
    const simple = form.signupMode === 'simple'
    if (simple) {
      if (!form.signupUrlPath.trim()) {
        issues.push(issue('error', 'auth-bootstrap-url', 'doctor.authBootstrapUrl'))
      }
    } else {
      try {
        parseSignupSteps(form.signupStepsJSON, 'signup')
      } catch (e) {
        issues.push(issue('error', 'auth-bootstrap-steps-json', 'doctor.authBootstrapStepsJson', { error: messageOf(e) }))
      }
    }
    // Gating safety: no teardown and not keeping accounts strands real accounts. The
    // teardown is the simple teardown URL in simple mode, the raw teardown JSON in
    // advanced mode — either satisfies the rule.
    const hasTeardown = simple
      ? form.signupTeardownUrlPath.trim().length > 0
      : form.signupTeardownJSON.trim().length > 0
    if (!form.keepAccounts && !hasTeardown) {
      issues.push(issue('warning', 'auth-bootstrap-no-teardown', 'doctor.authBootstrapNoTeardown'))
    }
    if (!simple && !form.keepAccounts && hasTeardown) {
      try {
        parseSignupSteps(form.signupTeardownJSON, 'teardown')
      } catch (e) {
        issues.push(
          issue('error', 'auth-bootstrap-teardown-json', 'doctor.authBootstrapTeardownJson', { error: messageOf(e) }),
        )
      }
    }
  } else if (form.authMode === 'mint') {
    // Mint self-issues a JWT locally; its blockers are a missing signing-key reference
    // (the run cannot sign without it) and malformed extra claims (cannot be signed).
    // The TTL is a positive number from a clamped numeric input, so no separate check.
    if (!form.mintKeyEnv.trim() && !form.mintKeyFile.trim()) {
      issues.push(issue('error', 'auth-mint-key', 'doctor.authMintKey'))
    }
    if (form.mintClaimsJSON.trim()) {
      try {
        const parsed = JSON.parse(form.mintClaimsJSON)
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          issues.push(issue('error', 'auth-mint-claims', 'doctor.authMintClaims', { error: 'not a JSON object' }))
        }
      } catch (e) {
        issues.push(issue('error', 'auth-mint-claims', 'doctor.authMintClaims', { error: messageOf(e) }))
      }
    }
  }
  return issues
}

function doctorGraph(g: ScenarioGraphShape, start: string, templates: TemplateMap | null): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  if (!Array.isArray(g.nodes) || g.nodes.length === 0) {
    return [issue('error', 'graph-empty', 'doctor.graphEmpty')]
  }
  const nodes = g.nodes as GraphNode[]
  const edges = Array.isArray(g.edges) ? (g.edges as GraphEdge[]) : []
  const known = new Set<string>()
  const seen = new Set<string>()
  const incoming = new Map<string, number>()
  const outgoingWeight = new Map<string, number>()

  for (const n of nodes) {
    if (typeof n.id !== 'string' || !n.id.trim()) {
      issues.push(issue('error', 'node-id', 'doctor.nodeIDMissing'))
      continue
    }
    const id = n.id
    if (seen.has(id)) {
      issues.push(issue('error', 'duplicate-node', 'doctor.duplicateNode', { node: id }))
    }
    seen.add(id)
    known.add(id)
    incoming.set(id, 0)
    if (typeof n.apiTemplateId === 'string' && n.apiTemplateId && templates && !templates[n.apiTemplateId]) {
      issues.push(issue('error', 'node-template-missing', 'doctor.nodeTemplateMissing', { node: id, template: n.apiTemplateId }))
    }
  }

  if (!known.has(start)) {
    issues.push(issue('error', 'start-missing', 'doctor.startMissing', { node: start }))
  } else {
    const startNode = nodes.find((n) => n.id === start)
    if (startNode && !startNode.apiTemplateId) {
      issues.push(issue('warning', 'start-terminal', 'doctor.startTerminal', { node: start }))
    }
  }

  for (const e of edges) {
    const from = typeof e.from === 'string' ? e.from : ''
    const to = typeof e.to === 'string' ? e.to : ''
    if (!known.has(from) || !known.has(to)) {
      issues.push(issue('error', 'edge-unknown-node', 'doctor.edgeUnknownNode', { from: from || '?', to: to || '?' }))
      continue
    }
    const weight = typeof e.weight === 'number' ? e.weight : 0
    if (!(weight >= 0) || !Number.isFinite(weight)) {
      issues.push(issue('error', 'edge-weight', 'doctor.edgeWeightInvalid', { from, to, weight: String(e.weight) }))
    }
    incoming.set(to, (incoming.get(to) ?? 0) + 1)
    outgoingWeight.set(from, (outgoingWeight.get(from) ?? 0) + Math.max(0, weight))
  }

  for (const [node, count] of incoming) {
    if (node !== start && count === 0) {
      issues.push(issue('warning', 'node-no-incoming', 'doctor.nodeNoIncoming', { node }))
    }
  }
  for (const [node, sum] of outgoingWeight) {
    if (sum > 1.000000001) {
      issues.push(issue('warning', 'outgoing-weight-high', 'doctor.outgoingWeightHigh', { node, weight: round(sum) }))
    }
  }
  return issues
}

function doctorTemplates(templates: TemplateMap, g: ScenarioGraphShape): DoctorIssue[] {
  const issues: DoctorIssue[] = []
  const used = new Set<string>()
  const nodes = Array.isArray(g.nodes) ? (g.nodes as GraphNode[]) : []
  for (const n of nodes) {
    if (typeof n.apiTemplateId === 'string' && n.apiTemplateId) used.add(n.apiTemplateId)
  }
  for (const [id, tmpl] of Object.entries(templates)) {
    if (!tmpl || typeof tmpl !== 'object') {
      issues.push(issue('error', 'template-shape', 'doctor.templateShape', { template: id }))
      continue
    }
    if (typeof tmpl.method !== 'string' || !tmpl.method.trim()) {
      issues.push(issue('error', 'template-method', 'doctor.templateMethodMissing', { template: id }))
    }
    if (typeof tmpl.path !== 'string' || !tmpl.path.trim()) {
      issues.push(issue('error', 'template-path', 'doctor.templatePathMissing', { template: id }))
    }
    if (tmpl.extract !== undefined) {
      if (!isRecord(tmpl.extract)) {
        issues.push(issue('error', 'template-extract-shape', 'doctor.templateExtractShape', { template: id }))
      } else {
        for (const [name, path] of Object.entries(tmpl.extract)) {
          if (!name.trim() || typeof path !== 'string' || !path.trim()) {
            issues.push(issue('error', 'template-extract-entry', 'doctor.templateExtractEntry', { template: id }))
            break
          }
        }
      }
    }
    if (!used.has(id)) {
      issues.push(issue('warning', 'template-unused', 'doctor.templateUnused', { template: id }))
    }
  }
  return issues
}

function nodeIDSet(g: ScenarioGraphShape): Set<string> {
  const ids = new Set<string>()
  if (!Array.isArray(g.nodes)) return ids
  for (const n of g.nodes as GraphNode[]) {
    if (typeof n.id === 'string') ids.add(n.id)
  }
  return ids
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function parseJSON<T>(text: string): { ok: true; value: T } | { ok: false; error: string } {
  try {
    return { ok: true, value: JSON.parse(text) as T }
  } catch (e) {
    return { ok: false, error: messageOf(e) }
  }
}

function issue(
  severity: DoctorSeverity,
  code: string,
  messageKey: string,
  vars?: Record<string, string | number>,
): DoctorIssue {
  return { severity, code, messageKey, vars }
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function round(n: number): number {
  return Math.round(n * 1000) / 1000
}
