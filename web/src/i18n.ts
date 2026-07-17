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
  'badge.optional': 'optional',
  'help.personas':
    'Optional JSON mix of weighted user types, each with its own entry node and pacing. Leave blank for one uniform population.',

  // Card: Scenario
  'card.scenario': 'Scenario',
  'card.scenario.hint':
    'The journey users take. Each run starts at the start node and walks the graph for up to the max steps. Click any node or edge in the graph to edit it in place.',
  'field.start': 'Start node',
  'help.start': 'Where every user begins.',
  'field.maxSteps': 'Max steps',
  'help.maxSteps': 'Longest path a user may take before stopping.',
  'field.deviation': 'Deviation rate',
  'help.deviation': 'Chance a user wanders off the weighted path at each step. 0 follows the scenario exactly.',
  'unit.percent': '%',
  'field.users': 'Virtual users',
  'help.users': 'The size of the fixed user pool that loops through the scenario.',
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
  'editor.clickHint': 'Click a node or edge in the graph to edit it right here.',
  'editor.invalid': 'Fix the graph JSON before visual editing.',
  'editor.viewMode': 'Graph view mode',
  'editor.viewJourney': 'Journey',
  'editor.viewAll': 'All edges ({count})',
  'editor.selNode': 'Selected node',
  'editor.selEdge': 'Selected edge',
  'editor.template': 'API template',
  'editor.method': 'Method',
  'editor.path': 'Path',
  'editor.done': 'Done',
  'editor.nodeID': 'Node ID',
  'editor.terminal': 'Terminal node',
  'editor.start': 'Set as start',
  'editor.remove': 'Remove',
  'editor.addNode': 'Add node',
  'editor.newNode': 'New node id',
  'editor.from': 'From',
  'editor.to': 'To',
  'editor.weight': 'Weight',
  'editor.dependency': 'dependency',
  'editor.addEdge': 'Add edge',
  'advanced.json': 'Edit the raw JSON',
  'legend.primary': 'Journey — thicker = higher weight',
  'legend.back': 'Back / exit',
  'legend.dep': 'Dependency',
  'legend.terminal': 'Terminal node',

  // Card: Auth (P5)
  'card.auth': 'Auth',
  'card.auth.hint':
    'How the simulated traffic authenticates. Leave it off to run anonymously, supply a pool of tokens, mint one from a login flow, or generate throwaway accounts.',
  'auth.mode.none': 'None',
  'auth.mode.none.desc': 'Run anonymously — no credentials are sent.',
  'auth.mode.pool': 'I already have tokens',
  'auth.mode.pool.desc': 'Easiest. Paste one bearer token or API key, or a list of pre-issued tokens — one per user.',
  'auth.mode.login': 'Log in to get tokens',
  'auth.mode.login.desc': 'Give your login URL and a body — tmula logs in and captures the token for you.',
  'auth.mode.bootstrap': 'Create test accounts',
  'auth.mode.bootstrap.desc': 'Advanced. Sign up a real account per user, then tear it down (non-prod only).',
  'auth.mode.mint': 'Sign a token locally (self-issued JWT)',
  'auth.mode.mint.desc':
    'For services whose tokens are self-issued JWTs you control the key for. tmula signs a fresh JWT per user — no login. Not for Auth0/Cognito/Firebase.',
  'auth.mode.exec': 'Run a command for the token (escape hatch)',
  'auth.mode.exec.desc':
    'Last resort. Run your own local command per user and read the token from its stdout — for auth no other strategy can model. Gated, runs locally.',

  // Auth · pattern generator (generate N rows from a subject/token template)
  'auth.pattern.toggle': 'Generate accounts from a pattern',
  'auth.pattern.hint':
    'Fill a subject and secret template with {{.userIndex}} and a count \u2014 tmula generates the rows into the box above. For a very large pool (100k+), use the CLI scenario file\u2019s usersPattern instead (generated server-side).',
  'auth.pattern.subject': 'Subject template',
  'auth.pattern.subjectHint': 'e.g. user{{.userIndex}} \u2014 leave empty for a bare token list.',
  'auth.pattern.token': 'Secret template',
  'auth.pattern.tokenHint': 'e.g. pw-{{.userIndex}} (the password for login, or a token for a pool). Not for opaque JWTs \u2014 those come from mint.',
  'auth.pattern.count': 'Count',
  'auth.pattern.generate': 'Generate',
  'auth.pattern.generated': 'Generated {count} rows.',

  // Auth · OAuth2 guide (the "It's an OAuth2 service" assembler)
  'auth.mode.oauth2': 'It\u2019s an OAuth2 service',
  'auth.mode.oauth2.desc':
    'Answer two questions \u2014 the token URL and how you log in \u2014 and tmula assembles the login flow for you.',
  'auth.oauth2.lead':
    'No OAuth2 knowledge needed: give the token URL, say how you log in, and tmula builds the token exchange (and keeps it refreshed mid-run).',
  'auth.oauth2.tokenUrl': 'Token URL',
  'auth.oauth2.tokenUrlHint':
    'The endpoint that issues tokens (e.g. https://idp.example.com/oauth/token). For an openIdConnect service, use the token_endpoint from its discovery document.',
  'auth.oauth2.discovery':
    'This service publishes an OpenID Connect discovery document at {url} \u2014 open it and copy its token_endpoint into the Token URL above.',
  'auth.oauth2.grant': 'How do you log in?',
  'auth.oauth2.grantHint': 'Pick the answer that matches what you have \u2014 tmula picks the grant.',
  'auth.oauth2.grant.password': 'With a username and password',
  'auth.oauth2.grant.password.desc': 'Each virtual user logs in with its own account (or one shared account).',
  'auth.oauth2.grant.cc': 'With a client key (server-to-server)',
  'auth.oauth2.grant.cc.desc': 'A machine identity: client_id + client_secret, one token shared by every user.',
  'auth.oauth2.grant.refresh': 'I\u2019m already logged in on an app or browser',
  'auth.oauth2.grant.refresh.desc':
    'Paste a refresh token copied once from the app/devtools \u2014 the answer for services that need a human consent screen (Auth0, Cognito, social login).',
  'auth.oauth2.grant.access': 'I only have an access token',
  'auth.oauth2.grant.access.desc': 'Use it as a token pool. If it expires mid-run, requests will start failing.',
  'auth.oauth2.username': 'Username',
  'auth.oauth2.password': 'Password',
  'auth.oauth2.users.toggle': 'Log in multiple users (optional)',
  'auth.oauth2.users': 'Accounts (CSV)',
  'auth.oauth2.usersHint':
    'A username,password header plus one account per row \u2014 each virtual user logs in as the next row.',
  'auth.oauth2.refreshToken': 'Refresh token',
  'auth.oauth2.refreshTokenHint':
    'Copy it ONCE from the logged-in app or the browser devtools (Application \u2192 Storage, or the token response). tmula exchanges it for fresh access tokens for the whole run. It is stored in the run\u2019s spec on this control plane (like any login body).',
  'auth.oauth2.accessToken': 'Access token',
  'auth.oauth2.accessTokenHint':
    'Becomes a one-entry token pool. Access tokens expire \u2014 a long run may start failing when it does; prefer a refresh token if you have one.',
  'auth.oauth2.accessToken.apply': 'Use as a token pool',
  'auth.oauth2.clientId': 'Client ID',
  'auth.oauth2.clientIdHint': 'Sent as client_id when your IdP requires it (optional otherwise).',
  'auth.oauth2.clientSecret': 'Client secret',
  'auth.oauth2.clientSecretHint': 'Sent as client_secret \u2014 required for server-to-server, sometimes for refresh. Stored in the run\u2019s spec on this control plane (like any login body); prefer a throwaway test client.',
  'auth.oauth2.scope': 'Scope (optional)',
  'auth.oauth2.scopeHint': 'Space-separated scopes, sent as scope when set.',
  'auth.oauth2.advanced': 'Generated login flow (JSON)',
  'auth.oauth2.advancedHint':
    'This is what the guide assembled \u2014 the same raw flow the Login mode edits. Changing an answer above regenerates it.',

  // Auth · the Advanced fold hiding the expert strategies (mint / exec)
  'auth.advanced.modes': 'More ways to authenticate (expert)',

  // Auth · imported (P7) — success banner shown when an import auto-detects auth
  'auth.imported.login': 'Imported — your login flow is ready. Review it below, or just start the run.',
  'auth.imported.login.secret': 'Imported your login flow — just fill in the highlighted secret below and you’re ready.',
  'auth.imported.pool': 'Imported — your token pool is ready. Review it below, or just start the run.',
  'auth.imported.bootstrap': 'Imported — your account-creation flow is ready. Confirm the target is non-production below, then start the run.',
  'auth.imported.bootstrap.secret': 'Imported your account-creation flow — just fill in the highlighted secret below, confirm the target is non-production, and you’re ready.',

  // Auth · token pool
  'auth.pool.file': 'Upload a file',
  'auth.pool.fileHint': 'A CSV (subject,token header), JSONL ({subject,token} per line), or plain tokens (.txt).',
  'auth.pool.format': 'Format',
  'auth.pool.formatHint': 'How the pasted text and file are encoded.',
  'auth.pool.format.csv': 'CSV (subject,token)',
  'auth.pool.format.jsonl': 'JSONL ({subject,token})',
  'auth.pool.format.tokens': 'Plain tokens (one per line)',
  'auth.pool.paste': 'Paste credentials',
  'auth.pool.pasteHint':
    'Parsed in your browser into inline entries — no file path is ever sent to the server. Plain tokens carry no subject (the bearer token stands alone).',
  'auth.pool.placeholder.csv': 'subject,token\nalice,eyJhbGci...\nbob,eyJhbGci...',
  'auth.pool.placeholder.jsonl': '{"subject":"alice","token":"eyJhbGci..."}\n{"subject":"bob","token":"eyJhbGci..."}',
  'auth.pool.placeholder.tokens': 'eyJhbGciOiJIUzI1Ni...\neyJhbGciOiJIUzI1Ni...',
  'auth.pool.count': '{count} credential(s) parsed',

  // Auth · login
  'auth.tokenVar.autoPlaceholder': 'auto-detect',
  'auth.login.tokenVar': 'Token capture (optional)',
  'auth.login.tokenVarHint': 'Leave empty to auto-detect — or name the variable that holds the token, e.g. $.access_token.',
  'auth.login.tokenVar.tip':
    'Leave this empty and tmula auto-detects the token from the login response — it looks for the common fields (access_token, accessToken, token, id_token, jwt, …) and a session/jwt/auth cookie. To override, name the variable your login template extracts that holds the bearer token. Either way the token is captured from the live response at run time and never stored.',
  'auth.login.subjectVar': 'Subject capture',
  'auth.login.subjectVarHint': 'Optional captured variable that becomes the principal id.',
  'auth.login.start': 'Start node',
  'auth.login.startHint': 'The login flow node every mint begins at.',
  'auth.login.refresh': 'Refresh override',
  'auth.login.refresh.tip':
    'By default tmula auto-derives a grant_type=refresh_token request from an OAuth2 form login, and re-logs-in otherwise. Set an explicit override here to force a specific refresh exchange — it WINS over the auto-derivation, so even a JSON-body login refreshes its token instead of re-logging-in on a mid-run 401. Leave both fields empty to keep the automatic behavior.',
  'auth.login.refreshRequest': 'Refresh request (optional)',
  'auth.login.refreshRequestHint': 'The METHOD and path the refresh posts to, e.g. POST /oauth/token. Leave empty to reuse the login endpoint.',
  'auth.login.refreshBody': 'Refresh body (optional)',
  'auth.login.refreshBodyHint': 'The refresh request body. Reference the captured refresh token with {{.refreshToken}} — tmula fills and url-encodes it at run time.',
  'auth.login.scope': 'Scope',
  'auth.login.scopeHint': 'Per-user mints one token each; shared mints one for everyone (client_credentials).',
  'auth.login.scope.tip':
    'Per-user: every virtual user logs in and gets its OWN token — and with a credential list below, each logs in as a different account. Shared: tmula logs in ONCE (client_credentials) and every virtual user reuses that single token. Pick per-user to simulate many real users; shared to model one service principal.',
  'auth.login.scope.perUser': 'Per user — one token per virtual user',
  'auth.login.scope.shared': 'Shared — one token for all (client_credentials)',
  'auth.login.body': 'Login body',
  'auth.login.bodyHint': 'The request body tmula sends to log in. Use {username}/{password} markers, or template a credential-list row.',
  'auth.login.body.tip':
    'The body sent to your login URL. For a single identity, put your credentials inline (or use the {username}/{password} markers). To log in many users, supply a credential list below and reference each row with {{.username}}/{{.password}}.',
  'auth.login.body.multiTip':
    'You supplied a credential list, so each virtual user logs in as the NEXT row. Reference the row with {{.username}} and {{.password}} — tmula fills them in per user. {{.userIndex}} is the virtual-user number; use a pattern like user{{.userIndex}} when you have no list. The list wraps (user i uses row i mod N).',
  'auth.login.body.useMulti': 'Use the credential-list body ({{.username}} / {{.password}})',

  // Auth · login · simple-form URL, the import "secrets to fill" panel, and Advanced toggle
  'auth.login.url': 'Login URL',
  'auth.login.urlHint': 'The endpoint tmula posts to in order to log in. Pick the method and give the path, e.g. POST /login.',
  'auth.login.method': 'Login HTTP method',
  'auth.secrets.title': 'Secrets to fill in',
  'auth.secrets.hint': 'Almost done — the import filled in everything except the secret(s) below. Enter them and the login is ready.',
  'auth.secrets.fieldHint': 'Filled in here in your browser and substituted at send time — the value is never stored.',
  'auth.advanced.login': 'Advanced',
  'auth.advanced.rawLogin': 'Edit the raw login flow (JSON)',
  'auth.advanced.rawLoginSub': 'Switch off the simple form and author the login graph and templates as raw JSON.',

  // Auth · login · "log in multiple users" credential list (P8)
  'auth.login.cred.toggle': 'Log in multiple users (credential list)',
  'auth.login.cred.hint':
    'Optional. Paste a username,password per account so each virtual user logs in as a different account. Leave it empty to log everyone in with the single body above.',
  'auth.login.cred.file': 'Upload a file',
  'auth.login.cred.fileHint': 'A CSV (username,password header) or JSONL ({username,password} per line).',
  'auth.login.cred.formatHint': 'How the pasted text and file are encoded.',
  'auth.login.cred.format.csv': 'CSV (username,password)',
  'auth.login.cred.format.jsonl': 'JSONL ({username,password})',
  'auth.login.cred.paste': 'Paste credentials',
  'auth.login.cred.pasteHint':
    'Parsed in your browser into login inputs — no file path is ever sent to the server. Each row becomes one account the login body templates in.',
  'auth.login.cred.tip':
    'These are login INPUTS — a username and password per account — not pre-issued tokens. tmula logs virtual user i in with row i (wrapping past the end), exposing the row to the login body as {{.username}} and {{.password}}. Parsed entirely in your browser; nothing but the resolved rows is ever sent.',
  'auth.login.cred.placeholder.csv': 'username,password\nalice,pw-alice\nbob,pw-bob',
  'auth.login.cred.placeholder.jsonl': '{"username":"alice","password":"pw-alice"}\n{"username":"bob","password":"pw-bob"}',
  'auth.login.cred.count': '{count} account(s) parsed',

  'auth.login.graph': 'Login graph',
  'auth.login.graphHint': 'The login journey, authored like the scenario graph — a sibling, never a node in it.',
  'auth.login.graph.tip':
    'A standalone graph the login transport walks to mint a token. Its nodes bind to the login templates below; the simulated traffic never observes it.',
  'auth.login.templates': 'Login templates',
  'auth.login.templatesHint': 'The request the login flow sends, with an extract map that captures the token.',

  // Auth · bootstrap
  'auth.bootstrap.confirm': 'This targets a non-production system.',
  'auth.bootstrap.confirmSub':
    'Generating accounts creates and deletes REAL accounts on the target. Confirm this is not production before continuing.',
  'auth.bootstrap.lead': 'tmula signs up a real account for each virtual user, logs in as it, then deletes it after the run. Use a disposable, non-production target only.',
  'auth.bootstrap.signupUrl': 'Signup URL',
  'auth.bootstrap.signupUrlHint': 'The endpoint that registers a new account. Pick the method and give the path, e.g. POST /register.',
  'auth.bootstrap.signupMethod': 'Signup HTTP method',
  'auth.bootstrap.body': 'Signup body',
  'auth.bootstrap.bodyHint': 'The request body tmula sends to sign up. Use {{.userIndex}} so each account is unique, and {password} for the password.',
  'auth.bootstrap.body.tip':
    'The body posted to your signup URL, rendered once per account. {{.userIndex}} is the virtual-user number — put it in the email or username (e.g. test+{{.userIndex}}@example.com) so every signup is distinct. {password} fills in the password tmula reuses to log in.',
  'auth.bootstrap.teardownUrl': 'Teardown URL',
  'auth.bootstrap.teardownUrlHint': 'The endpoint that deletes one account after the run. {{.subject}} is the account id, e.g. DELETE /accounts/{{.subject}}.',
  'auth.bootstrap.teardownMethod': 'Teardown HTTP method',
  'auth.advanced.bootstrap': 'Advanced',
  'auth.advanced.rawBootstrap': 'Edit the raw signup flow (JSON)',
  'auth.advanced.rawBootstrapSub': 'Switch off the simple form and author the signup and teardown steps as raw JSON.',
  'auth.bootstrap.captureToken': 'Token capture (optional)',
  'auth.bootstrap.captureTokenHint': 'Leave empty to auto-detect — or name the variable that holds the new account’s token.',
  'auth.bootstrap.captureToken.tip':
    'Leave this empty and tmula auto-detects the token from the signup response — it looks for the common fields (access_token, accessToken, token, id_token, jwt, …) and a session/jwt/auth cookie. To override, name the variable a signup step extracts that holds the new account’s token. Either way it is captured from the live response and never stored.',
  'auth.bootstrap.captureSubject': 'Subject capture',
  'auth.bootstrap.captureSubjectHint': 'Optional captured variable that becomes the account id (threaded into teardown).',
  'auth.bootstrap.start': 'Start step',
  'auth.bootstrap.startHint': 'Optional entry step (defaults to the first).',
  'auth.bootstrap.steps': 'Signup steps',
  'auth.bootstrap.stepsHint': 'A JSON array of steps: id, method, path, optional body and extract.',
  'auth.bootstrap.steps.tip':
    'Each step is one signup request: a bare method and rooted path, an optional body, and an extract map that captures the token/subject. {{.userIndex}} is rendered per account so each signs up distinctly.',
  'auth.bootstrap.keep': 'Keep accounts (skip teardown)',
  'auth.bootstrap.keepSub': 'Leave the provisioned accounts in place after the run instead of deleting them.',
  'auth.bootstrap.teardown': 'Teardown steps',
  'auth.bootstrap.teardownHint': 'A JSON array of steps that delete each account. {{.subject}} is the account id.',
  'auth.bootstrap.teardownStart': 'Teardown start step',
  'auth.bootstrap.teardownStartHint': 'Optional teardown entry step (defaults to the first).',

  // Auth · mint advisory (import detected a managed IdP — mint cannot work there)
  'auth.advisory.mintManagedIdp':
    'This service\u2019s tokens are issued by {host}, a managed identity provider that holds the signing key \u2014 a locally minted (self-issued) token will be rejected. Use the OAuth2 mode instead.',
  'auth.advisory.mintManagedIdp.generic':
    'This service\u2019s tokens are issued by a managed identity provider that holds the signing key \u2014 a locally minted (self-issued) token will be rejected. Use the OAuth2 mode instead.',

  // Auth · mint (local JWT signing, M1)
  'auth.mint.lead':
    'tmula signs a fresh JWT for each virtual user locally — no login, no token capture. Use this ONLY when the target self-issues JWTs and you hold the signing key. It cannot sign for a key you do not control (Auth0/Cognito/Firebase) — use Login for those.',
  'auth.mint.alg': 'Algorithm',
  'auth.mint.algHint': 'How the JWT is signed. HS256 uses a shared secret; RS256/ES256 use a PEM private key.',
  'auth.mint.alg.tip':
    'HS256 signs with a symmetric secret (the same value verifies the token). RS256 (RSA) and ES256 (ECDSA P-256) sign with a PEM private key — the verifier holds the public half. Pick whatever the target’s verifier expects.',
  'auth.mint.alg.hs256': 'HS256 — shared secret (HMAC)',
  'auth.mint.alg.rs256': 'RS256 — RSA private key (PEM)',
  'auth.mint.alg.es256': 'ES256 — ECDSA P-256 (PEM)',
  'auth.mint.encoding': 'Secret encoding',
  'auth.mint.encodingHint': 'How the HS256 secret is stored: raw bytes, base64, or base64url.',
  'auth.mint.encoding.raw': 'Raw (verbatim bytes)',
  'auth.mint.encoding.base64': 'Base64',
  'auth.mint.encoding.base64url': 'Base64URL',
  'auth.mint.keyEnv': 'Key environment variable',
  'auth.mint.keyEnvHint': 'Name of the env var the server reads the signing key from. Use this OR a file, not both.',
  'auth.mint.keyEnv.placeholder': 'TMULA_MINT_SECRET',
  'auth.mint.key.tip':
    'The signing key is a REFERENCE only — tmula reads it on the server from this env var (or the file below) and never sends the key over the wire. For HS256 it is the shared secret; for RS256/ES256 a PEM private key. Set exactly one of env or file.',
  'auth.mint.keyFile': 'Key file (on the server)',
  'auth.mint.keyFileHint': 'Path to the signing-key file the server reads. Use this OR an env var, not both.',
  'auth.mint.keyFile.placeholder': 'signing-key.pem',
  'auth.mint.subject': 'Subject (sub claim)',
  'auth.mint.subjectHint': 'The per-user sub claim. Template {{.userIndex}} so each user is a distinct principal. Empty = no sub.',
  'auth.mint.subject.tip':
    'The token’s sub claim, rendered per virtual user. Reference {{.userIndex}} (the VU number) so user 0 and user 1 sign distinct principals, e.g. user-{{.userIndex}}. Leave it empty to mint a token with no sub.',
  'auth.mint.ttl': 'Token lifetime (seconds)',
  'auth.mint.ttlHint': 'How long each minted token is valid; it sets the exp claim to now + this many seconds.',
  'auth.mint.claims': 'Extra claims (JSON)',
  'auth.mint.claimsHint': 'Optional JSON object of extra claims signed into every token. Values may template {{.userIndex}}/{{.subject}}.',
  'auth.mint.claims.tip':
    'A JSON object of additional claims merged into every token alongside iat/exp/sub. Values are templates: reference {{.userIndex}} (the VU number) or {{.subject}} (the rendered sub). Leave it empty for just the standard claims.',
  'auth.mint.claims.placeholder': '{"role": "tester", "tenant": "acme"}',
  'auth.mint.claimsInvalid': 'Extra claims must be valid JSON.',
  'auth.mint.claimsNotObject': 'Extra claims must be a JSON object (e.g. {"role":"tester"}).',

  // Auth · exec (bring-your-own-token escape hatch, X1)
  'auth.exec.lead':
    'tmula runs your own local command once per virtual user and uses its stdout as the token — the escape hatch for auth no built-in strategy can model. Reach for this only when nothing else fits.',
  'auth.exec.confirm': 'This runs an arbitrary local command on this machine.',
  'auth.exec.confirmSub':
    'The command runs on the box driving the test, and its egress is NOT bound by the target allowlist or rate cap. The run is also gated server-side by --allow-exec.',
  'auth.exec.command': 'Command',
  'auth.exec.commandHint':
    'The command to run, one argv element per line. The first line is the program; the rest are arguments. Lines may template {{.userIndex}}.',
  'auth.exec.command.tip':
    'tmula runs this exact argv per virtual user (no shell) and reads the token from stdout. argv[0] is the program (a path or a name on PATH); each following line is one argument. Use {{.userIndex}} so user 0 and user 1 fetch distinct tokens. Keep secrets out of here — put them in the env below.',
  'auth.exec.env': 'Extra environment (KEY=VALUE)',
  'auth.exec.envHint':
    'Extra environment variables for the command, one KEY=VALUE per line. Put secrets here, not in the command. Values may template {{.userIndex}}.',
  'auth.exec.env.tip':
    'Each line adds one environment variable to the command’s process, on top of the inherited environment. Use this for secrets (an API key, a client secret) so they never appear in the argv. Values may reference {{.userIndex}} for a per-user value.',
  'auth.exec.timeout': 'Timeout (seconds)',
  'auth.exec.timeoutHint': 'How long each invocation may run before tmula kills it and fails that user’s token fetch.',

  // Auth · scenario doctor hints
  'doctor.authPoolEmpty': 'Token pool is selected but no credentials are pasted or uploaded.',
  'doctor.authPoolInvalid': 'The pasted credentials could not be parsed: {error}',
  'doctor.authLoginUrl': 'Login is selected but the login URL is empty — tmula has nowhere to send the login request.',
  'doctor.authBootstrapUrl': 'Account generation is selected but the signup URL is empty — tmula has nowhere to register the accounts.',
  'doctor.authLoginGraphJson': 'Login graph JSON is invalid: {error}',
  'doctor.authLoginTemplatesJson': 'Login templates JSON is invalid: {error}',
  'doctor.authLoginCredInvalid': 'The login credential list could not be parsed: {error}',
  'doctor.authLoginCredUnused':
    'A login credential list is supplied, but the login body never references {{.username}}/{{.password}} — every virtual user would log in with the same body. Template the row into the body to log them in as different accounts.',
  'doctor.authBootstrapUnconfirmed':
    'Generating accounts requires confirming the target is non-production before it can run.',
  'doctor.authBootstrapStepsJson': 'Signup steps JSON is invalid: {error}',
  'doctor.authBootstrapNoTeardown':
    'Account generation has no teardown flow and keep-accounts is off — provisioned accounts would be stranded.',
  'doctor.authBootstrapTeardownJson': 'Teardown steps JSON is invalid: {error}',
  'doctor.authMintKey':
    'Local signing is selected but no signing key is referenced — set a key environment variable or a key file so tmula can sign.',
  'doctor.authMintClaims': 'Extra claims JSON is invalid: {error}',

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
  'help.deviation.tip':
    'Virtual users probabilistically deviate from the journey — exploring other paths or giving up mid-way. Dependency edges are never violated.',

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
  'import.coverage.title': 'Import coverage',
  'import.coverage.summary':
    '{requests} requests used · {skipped} lines skipped · {sessions} sessions · {clients} clients · {dropped} endpoints folded',
  'import.coverage.partial':
    'This import reflects only part of the captured traffic — {skipped} of {total} lines ({pct}%) were skipped.',
  'import.coverage.full': 'Every usable line is reflected in the learned graph.',
  'import.coverage.folded':
    '{count} colder endpoint(s) beyond the graph cap were folded out; their traffic bridges across the kept nodes.',
  'import.coverage.format': 'Detected as {format} format',
  'import.coverage.samples': 'Skipped line samples',
  'import.coverage.sample.line': 'Line',
  'import.coverage.sample.text': 'Content',
  'import.coverage.sample.reason': 'Reason',

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

  // Attach mode (?run=<run-id> links, e.g. opened by `tmula demo`)
  'attach.notFound':
    'Run "{id}" was not found on this server — it may have finished and been cleaned up. Set up a new run below.',

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
  // Journey-outcome headline: how journeys ended (reached done vs left at exit).
  'stat.completionRate': 'Completion rate',
  'stat.completionSub': '{count} of {started} journeys reached done',
  'stat.dropOffRate': 'Drop-off rate',
  'stat.dropOffSub': '{count} of {started} journeys left at exit',

  // Findings (ReportView)
  'metrics.title': 'Server metrics',
  'metrics.fetchError': 'Some series could not be fetched:',
  'findings.title': 'Findings',
  'findings.empty': 'No issues detected.',

  // Finding evidence panel (ReportView). Session ids, personas, error classes and
  // bucket labels are backend data and shown verbatim — only the chrome below is
  // translated.
  'evidence.summary': 'Evidence',
  'evidence.summaryOne': 'Evidence · {count} representative session',
  'evidence.summaryMany': 'Evidence · {count} representative sessions',
  'evidence.sessionsTitle': 'Representative sessions',
  'evidence.grepHint':
    "Each session sent its ID as the X-Tmula-Session-ID header on every request — grep the target server's logs for an ID below to see exactly what that session did. Seed and user # are the coordinates to reproduce it.",
  'evidence.col.session': 'Session',
  'evidence.col.persona': 'Persona',
  'evidence.col.seed': 'Seed',
  'evidence.col.user': 'User #',
  'evidence.col.path': 'Path to failure',
  'evidence.col.status': 'Status',
  'evidence.col.latency': 'Latency',
  'evidence.col.error': 'Error',
  'evidence.col.time': 'At',
  'evidence.statusTitle': 'Status codes (all occurrences)',
  'evidence.timingTitle': 'When in the run',
  'evidence.rootCause': 'Root cause class:',

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
  'badge.optional': '선택',
  'help.personas':
    '가중치가 있는 사용자 유형을 JSON으로 섞어 정의합니다(선택). 유형마다 시작 노드와 속도를 따로 가질 수 있습니다. 비워 두면 단일 균일 집단으로 실행합니다.',

  // Card: Scenario
  'card.scenario': '시나리오',
  'card.scenario.hint':
    '사용자가 따라가는 여정입니다. 각 실행은 시작 노드에서 출발해 최대 단계 수만큼 그래프를 따라 이동합니다. 그래프의 노드나 엣지를 클릭하면 그 자리에서 바로 편집할 수 있습니다.',
  'field.start': '시작 노드',
  'help.start': '모든 사용자가 출발하는 지점입니다.',
  'field.maxSteps': '최대 단계',
  'help.maxSteps': '사용자가 멈추기 전까지 거칠 수 있는 가장 긴 경로입니다.',
  'field.deviation': '경로 이탈률',
  'help.deviation': '각 단계에서 사용자가 가중치 경로를 벗어날 확률입니다. 0이면 시나리오를 그대로 따릅니다.',
  'unit.percent': '%',
  'field.users': '가상 사용자',
  'help.users': '시나리오를 반복하는 고정 사용자 풀의 크기입니다.',
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
  'editor.clickHint': '그래프의 노드나 엣지를 클릭하면 바로 아래에서 편집할 수 있습니다.',
  'editor.invalid': '시각 편집을 하려면 먼저 그래프 JSON을 고쳐야 합니다.',
  'editor.viewMode': '그래프 보기 모드',
  'editor.viewJourney': '주요 여정',
  'editor.viewAll': '전체 엣지 ({count})',
  'editor.selNode': '선택한 노드',
  'editor.selEdge': '선택한 엣지',
  'editor.template': 'API 템플릿',
  'editor.method': '메서드',
  'editor.path': '경로',
  'editor.done': '완료',
  'editor.nodeID': '노드 ID',
  'editor.terminal': '터미널 노드',
  'editor.start': '시작 노드로 지정',
  'editor.remove': '삭제',
  'editor.addNode': '노드 추가',
  'editor.newNode': '새 노드 id',
  'editor.from': '출발',
  'editor.to': '도착',
  'editor.weight': 'Weight',
  'editor.dependency': '의존',
  'editor.addEdge': '엣지 추가',
  'advanced.json': 'JSON으로 직접 편집',
  'legend.primary': '여정 — 굵을수록 가중치 높음',
  'legend.back': '되돌아가기 · 이탈',
  'legend.dep': '의존',
  'legend.terminal': '터미널 노드',

  // Card: Auth (P5)
  'card.auth': '인증',
  'card.auth.hint':
    '시뮬레이션 트래픽이 인증하는 방식입니다. 끄면 익명으로 실행하고, 토큰 풀을 제공하거나, 로그인 흐름으로 토큰을 발급받거나, 일회용 계정을 생성할 수 있습니다.',
  'auth.mode.none': '없음',
  'auth.mode.none.desc': '익명으로 실행 — 자격 증명을 보내지 않습니다.',
  'auth.mode.pool': '이미 토큰이 있어요',
  'auth.mode.pool.desc': '가장 쉬운 길. bearer 토큰이나 API 키 하나, 또는 미리 발급한 토큰 목록을 붙여넣거나 업로드합니다 — 사용자마다 하나씩 배정됩니다.',
  'auth.mode.login': '로그인해서 토큰을 받아요',
  'auth.mode.login.desc': '로그인 URL과 본문만 주면 tmula가 로그인하고 토큰을 캡처합니다.',
  'auth.mode.bootstrap': '계정을 만들어서 테스트해요',
  'auth.mode.bootstrap.desc': '사용자마다 실제 계정을 가입시킨 뒤 정리합니다. 비프로덕션 전용, 확인 게이트가 있습니다.',
  'auth.mode.mint': '토큰을 로컬에서 서명(자체 발급 JWT)',
  'auth.mode.mint.desc':
    '토큰이 자체 발급 JWT이고 서명 키를 직접 보유한 서비스용입니다. tmula가 사용자마다 JWT를 새로 서명합니다 — 로그인이 없습니다. Auth0/Cognito/Firebase에는 사용할 수 없습니다.',
  'auth.mode.exec': '명령으로 토큰 가져오기(탈출구)',
  'auth.mode.exec.desc':
    '최후의 수단입니다. 사용자마다 직접 만든 로컬 명령을 실행해 stdout에서 토큰을 읽습니다 — 다른 전략으로 표현할 수 없는 인증용입니다. 게이트가 걸려 있고, 로컬에서 실행됩니다.',

  // Auth · 패턴 생성기 (subject/token 템플릿으로 N개 행 생성)
  'auth.pattern.toggle': '패턴으로 계정 생성',
  'auth.pattern.hint':
    'subject/secret 템플릿에 {{.userIndex}}와 개수를 채우면 tmula가 위 상자에 행을 생성합니다. 아주 큰 풀(10만+)은 CLI 시나리오 파일의 usersPattern을 쓰세요(서버에서 생성).',
  'auth.pattern.subject': 'Subject 템플릿',
  'auth.pattern.subjectHint': '예: user{{.userIndex}} \u2014 비우면 토큰만 있는 목록이 됩니다.',
  'auth.pattern.token': 'Secret 템플릿',
  'auth.pattern.tokenHint': '예: pw-{{.userIndex}} (로그인의 비밀번호 또는 풀의 토큰). 불투명 JWT에는 쓸 수 없습니다 \u2014 그건 mint의 몫입니다.',
  'auth.pattern.count': '개수',
  'auth.pattern.generate': '생성',
  'auth.pattern.generated': '{count}개 행을 생성했습니다.',

  // Auth · OAuth2 가이드 ("OAuth2 서비스예요" 조립기)
  'auth.mode.oauth2': 'OAuth2 서비스예요',
  'auth.mode.oauth2.desc': '토큰 URL과 로그인 방식, 두 가지만 답하면 tmula가 로그인 흐름을 만들어 줍니다.',
  'auth.oauth2.lead':
    'OAuth2 지식이 없어도 됩니다: 토큰 URL을 넣고 로그인 방식을 고르면 tmula가 토큰 교환을 조립하고, 실행 중에도 갱신합니다.',
  'auth.oauth2.tokenUrl': '토큰 URL',
  'auth.oauth2.tokenUrlHint':
    '토큰을 발급하는 엔드포인트입니다(예: https://idp.example.com/oauth/token). openIdConnect 서비스라면 discovery 문서의 token_endpoint를 넣으세요.',
  'auth.oauth2.discovery':
    '이 서비스는 {url} 에 OpenID Connect discovery 문서를 게시합니다 \u2014 열어서 token_endpoint 값을 위의 토큰 URL에 붙여넣으세요.',
  'auth.oauth2.grant': '어떻게 로그인하나요?',
  'auth.oauth2.grantHint': '가진 것에 맞는 답을 고르면 tmula가 grant를 알아서 고릅니다.',
  'auth.oauth2.grant.password': '아이디/비밀번호로',
  'auth.oauth2.grant.password.desc': '가상 사용자마다 자기 계정으로(또는 한 계정을 공유해서) 로그인합니다.',
  'auth.oauth2.grant.cc': '클라이언트 키로 (서버 간)',
  'auth.oauth2.grant.cc.desc': '머신 아이덴티티: client_id + client_secret, 토큰 하나를 모든 사용자가 공유합니다.',
  'auth.oauth2.grant.refresh': '앱/브라우저에서 이미 로그인했어요',
  'auth.oauth2.grant.refresh.desc':
    '앱이나 개발자도구에서 refresh token을 1회 복사해 붙여넣으세요 \u2014 사람 동의 화면이 필요한 서비스(Auth0, Cognito, 소셜 로그인)의 정답 경로입니다.',
  'auth.oauth2.grant.access': 'access token만 있어요',
  'auth.oauth2.grant.access.desc': '토큰 풀로 사용합니다. 실행 중에 만료되면 요청이 실패하기 시작할 수 있습니다.',
  'auth.oauth2.username': '아이디',
  'auth.oauth2.password': '비밀번호',
  'auth.oauth2.users.toggle': '여러 사용자로 로그인 (선택)',
  'auth.oauth2.users': '계정 목록 (CSV)',
  'auth.oauth2.usersHint': 'username,password 헤더와 행마다 계정 하나 \u2014 가상 사용자마다 다음 행으로 로그인합니다.',
  'auth.oauth2.refreshToken': 'Refresh token',
  'auth.oauth2.refreshTokenHint':
    '로그인된 앱이나 브라우저 개발자도구(Application \u2192 Storage, 또는 토큰 응답)에서 한 번만 복사하세요. tmula가 실행 내내 새 access token으로 교환합니다. 다른 로그인 본문과 마찬가지로 이 컨트롤 플레인의 실행 spec에 저장됩니다.',
  'auth.oauth2.accessToken': 'Access token',
  'auth.oauth2.accessTokenHint':
    '항목 1개짜리 토큰 풀이 됩니다. access token은 만료됩니다 \u2014 긴 실행은 만료 시점부터 실패할 수 있으니, refresh token이 있다면 그쪽을 쓰세요.',
  'auth.oauth2.accessToken.apply': '토큰 풀로 사용',
  'auth.oauth2.clientId': 'Client ID',
  'auth.oauth2.clientIdHint': 'IdP가 요구하면 client_id로 전송됩니다(그 외에는 선택).',
  'auth.oauth2.clientSecret': 'Client secret',
  'auth.oauth2.clientSecretHint': 'client_secret으로 전송됩니다 \u2014 서버 간 통신에는 필수, refresh에도 필요할 수 있습니다. 다른 로그인 본문과 마찬가지로 이 컨트롤 플레인의 실행 spec에 저장되니, 일회용 테스트 클라이언트를 권장합니다.',
  'auth.oauth2.scope': 'Scope (선택)',
  'auth.oauth2.scopeHint': '공백으로 구분한 scope 목록입니다. 입력하면 scope로 전송됩니다.',
  'auth.oauth2.advanced': '생성된 로그인 흐름 (JSON)',
  'auth.oauth2.advancedHint':
    '가이드가 조립한 결과입니다 \u2014 로그인 모드가 편집하는 것과 같은 raw 흐름입니다. 위 답을 바꾸면 다시 생성됩니다.',

  // Auth · 전문가 전략(mint / exec)을 감추는 Advanced 접힘
  'auth.advanced.modes': '다른 인증 방법 (전문가)',

  // Auth · imported (P7) — 가져오기가 인증을 자동 감지했을 때 표시되는 성공 배너
  'auth.imported.login': '가져왔습니다 — 로그인 흐름이 준비됐습니다. 아래에서 확인하거나 바로 실행하세요.',
  'auth.imported.login.secret': '로그인 흐름을 가져왔습니다 — 아래 강조된 비밀값만 입력하면 준비가 끝납니다.',
  'auth.imported.pool': '가져왔습니다 — 토큰 풀이 준비됐습니다. 아래에서 확인하거나 바로 실행하세요.',
  'auth.imported.bootstrap': '가져왔습니다 — 계정 생성 흐름이 준비됐습니다. 아래에서 대상이 비프로덕션임을 확인한 뒤 실행하세요.',
  'auth.imported.bootstrap.secret': '계정 생성 흐름을 가져왔습니다 — 아래 강조된 비밀값을 입력하고, 대상이 비프로덕션임을 확인하면 준비가 끝납니다.',

  // Auth · 토큰 풀
  'auth.pool.file': '파일 올리기',
  'auth.pool.fileHint': 'CSV(subject,token 헤더), JSONL(줄마다 {subject,token}), 또는 일반 토큰(.txt).',
  'auth.pool.format': '형식',
  'auth.pool.formatHint': '붙여넣은 텍스트와 파일의 인코딩 방식입니다.',
  'auth.pool.format.csv': 'CSV (subject,token)',
  'auth.pool.format.jsonl': 'JSONL ({subject,token})',
  'auth.pool.format.tokens': '일반 토큰 (한 줄에 하나)',
  'auth.pool.paste': '자격 증명 붙여넣기',
  'auth.pool.pasteHint':
    '브라우저에서 인라인 항목으로 파싱됩니다 — 파일 경로는 서버로 전송되지 않습니다. 일반 토큰은 subject 없이 사용됩니다(베어러 토큰 단독).',
  'auth.pool.placeholder.csv': 'subject,token\nalice,eyJhbGci...\nbob,eyJhbGci...',
  'auth.pool.placeholder.jsonl': '{"subject":"alice","token":"eyJhbGci..."}\n{"subject":"bob","token":"eyJhbGci..."}',
  'auth.pool.placeholder.tokens': 'eyJhbGciOiJIUzI1Ni...\neyJhbGciOiJIUzI1Ni...',
  'auth.pool.count': '자격 증명 {count}개 파싱됨',

  // Auth · 로그인
  'auth.tokenVar.autoPlaceholder': '자동 감지',
  'auth.login.tokenVar': '토큰 캡처 (선택)',
  'auth.login.tokenVarHint': '비워 두면 자동 감지합니다 — 또는 토큰을 담은 변수 이름을 지정하세요. 예: $.access_token.',
  'auth.login.tokenVar.tip':
    '비워 두면 tmula가 로그인 응답에서 토큰을 자동 감지합니다 — 흔한 필드(access_token, accessToken, token, id_token, jwt 등)와 session/jwt/auth 쿠키를 찾습니다. 직접 지정하려면 로그인 템플릿이 extract 맵으로 추출하는, 베어러 토큰을 담은 변수 이름을 적으세요. 어느 쪽이든 실행 시 실제 응답에서 캡처되며 저장되지 않습니다.',
  'auth.login.subjectVar': 'Subject 캡처',
  'auth.login.subjectVarHint': '주체(principal) id가 되는 선택 캡처 변수입니다.',
  'auth.login.start': '시작 노드',
  'auth.login.startHint': '토큰 발급이 시작되는 로그인 흐름 노드입니다.',
  'auth.login.refresh': '갱신 재정의',
  'auth.login.refresh.tip':
    '기본적으로 tmula는 OAuth2 폼 로그인에서 grant_type=refresh_token 요청을 자동 도출하고, 그 외에는 다시 로그인합니다. 여기에 명시적 재정의를 설정하면 특정 갱신 교환을 강제할 수 있습니다 — 자동 도출보다 우선하므로, JSON 본문 로그인이라도 실행 중 401에서 다시 로그인하지 않고 토큰을 갱신합니다. 두 필드를 모두 비워 두면 자동 동작이 유지됩니다.',
  'auth.login.refreshRequest': '갱신 요청 (선택)',
  'auth.login.refreshRequestHint': '갱신이 요청을 보내는 메서드와 경로입니다. 예: POST /oauth/token. 비워 두면 로그인 엔드포인트를 재사용합니다.',
  'auth.login.refreshBody': '갱신 본문 (선택)',
  'auth.login.refreshBodyHint': '갱신 요청 본문입니다. 캡처된 갱신 토큰을 {{.refreshToken}}로 참조하세요 — tmula가 실행 시점에 값을 채우고 url 인코딩합니다.',
  'auth.login.scope': '범위',
  'auth.login.scopeHint': '사용자별은 각자 하나씩, 공유는 전체가 하나를 공유합니다(client_credentials).',
  'auth.login.scope.tip':
    '사용자별: 모든 가상 사용자가 로그인해 각자 토큰을 받습니다 — 아래 자격 증명 목록을 함께 주면 각자 다른 계정으로 로그인합니다. 공유: tmula가 한 번만 로그인하고(client_credentials) 모든 가상 사용자가 그 토큰 하나를 공유합니다. 여러 실제 사용자를 흉내 내려면 사용자별, 단일 서비스 주체를 모델링하려면 공유를 고르세요.',
  'auth.login.scope.perUser': '사용자별 — 가상 사용자마다 토큰 1개',
  'auth.login.scope.shared': '공유 — 전체가 토큰 1개 (client_credentials)',
  'auth.login.body': '로그인 본문',
  'auth.login.bodyHint': '로그인할 때 tmula가 보내는 요청 본문입니다. {username}/{password} 마커를 쓰거나, 자격 증명 목록의 행을 템플릿으로 넣으세요.',
  'auth.login.body.tip':
    '로그인 URL로 보내는 본문입니다. 단일 신원이면 자격 증명을 직접 넣거나 {username}/{password} 마커를 쓰세요. 여러 사용자를 로그인시키려면 아래에 자격 증명 목록을 주고, {{.username}}/{{.password}}로 각 행을 참조하세요.',
  'auth.login.body.multiTip':
    '자격 증명 목록을 주었으므로 각 가상 사용자는 다음 행으로 로그인합니다. {{.username}}와 {{.password}}로 행을 참조하면 tmula가 사용자마다 채워 넣습니다. {{.userIndex}}는 가상 사용자 번호로, 목록이 없을 때 user{{.userIndex}} 같은 패턴에 쓰세요. 목록은 순환합니다(사용자 i는 i mod N 행을 사용).',
  'auth.login.body.useMulti': '자격 증명 목록 본문 사용 ({{.username}} / {{.password}})',

  // Auth · 로그인 · 간단 양식 URL, 가져오기 "비밀값 입력" 패널, 고급 토글
  'auth.login.url': '로그인 URL',
  'auth.login.urlHint': 'tmula가 로그인을 위해 요청을 보내는 엔드포인트입니다. 메서드를 고르고 경로를 입력하세요. 예: POST /login.',
  'auth.login.method': '로그인 HTTP 메서드',
  'auth.secrets.title': '입력할 비밀값',
  'auth.secrets.hint': '거의 다 됐습니다 — 가져오기가 아래 비밀값만 남기고 모두 채웠습니다. 입력하면 로그인 준비가 끝납니다.',
  'auth.secrets.fieldHint': '브라우저에서 입력되어 전송 시점에 본문에 채워집니다 — 값은 저장되지 않습니다.',
  'auth.advanced.login': '고급',
  'auth.advanced.rawLogin': '원본 로그인 흐름 편집 (JSON)',
  'auth.advanced.rawLoginSub': '간단 양식을 끄고 로그인 그래프와 템플릿을 원본 JSON으로 직접 작성합니다.',

  // Auth · 로그인 · "여러 사용자 로그인" 자격 증명 목록 (P8)
  'auth.login.cred.toggle': '여러 사용자 로그인 (자격 증명 목록)',
  'auth.login.cred.hint':
    '선택. 계정마다 username,password를 붙여넣으면 각 가상 사용자가 다른 계정으로 로그인합니다. 비워 두면 위 단일 본문으로 모두 로그인합니다.',
  'auth.login.cred.file': '파일 올리기',
  'auth.login.cred.fileHint': 'CSV(username,password 헤더) 또는 JSONL(줄마다 {username,password}).',
  'auth.login.cred.formatHint': '붙여넣은 텍스트와 파일의 인코딩 방식입니다.',
  'auth.login.cred.format.csv': 'CSV (username,password)',
  'auth.login.cred.format.jsonl': 'JSONL ({username,password})',
  'auth.login.cred.paste': '자격 증명 붙여넣기',
  'auth.login.cred.pasteHint':
    '브라우저에서 로그인 입력값으로 파싱됩니다 — 파일 경로는 서버로 전송되지 않습니다. 각 행이 로그인 본문이 채워 넣는 하나의 계정이 됩니다.',
  'auth.login.cred.tip':
    '이것은 로그인 입력값입니다 — 계정마다 username과 password — 미리 발급한 토큰이 아닙니다. tmula는 가상 사용자 i를 i번째 행으로 로그인시키며(끝을 지나면 순환), 그 행을 로그인 본문에 {{.username}}·{{.password}}로 노출합니다. 전부 브라우저에서 파싱되며, 변환된 행 외에는 아무것도 전송되지 않습니다.',
  'auth.login.cred.placeholder.csv': 'username,password\nalice,pw-alice\nbob,pw-bob',
  'auth.login.cred.placeholder.jsonl': '{"username":"alice","password":"pw-alice"}\n{"username":"bob","password":"pw-bob"}',
  'auth.login.cred.count': '계정 {count}개 파싱됨',

  'auth.login.graph': '로그인 그래프',
  'auth.login.graphHint': '로그인 여정입니다. 시나리오 그래프처럼 작성하며, 본 그래프의 노드가 아닌 별도 그래프입니다.',
  'auth.login.graph.tip':
    '로그인 트랜스포트가 토큰을 발급하기 위해 따라가는 독립 그래프입니다. 노드는 아래 로그인 템플릿에 연결되며, 시뮬레이션 트래픽은 이를 관찰하지 않습니다.',
  'auth.login.templates': '로그인 템플릿',
  'auth.login.templatesHint': '로그인 흐름이 보내는 요청과, 토큰을 캡처하는 extract 맵입니다.',

  // Auth · 부트스트랩
  'auth.bootstrap.confirm': '이 대상은 비프로덕션 시스템입니다.',
  'auth.bootstrap.confirmSub':
    '계정 생성은 대상에 실제 계정을 만들고 삭제합니다. 계속하기 전에 프로덕션이 아님을 확인하세요.',
  'auth.bootstrap.lead': 'tmula가 가상 사용자마다 실제 계정을 가입시키고 그 계정으로 로그인한 뒤, 실행이 끝나면 삭제합니다. 폐기해도 되는 비프로덕션 대상에만 사용하세요.',
  'auth.bootstrap.signupUrl': '가입 URL',
  'auth.bootstrap.signupUrlHint': '새 계정을 등록하는 엔드포인트입니다. 메서드를 고르고 경로를 입력하세요. 예: POST /register.',
  'auth.bootstrap.signupMethod': '가입 HTTP 메서드',
  'auth.bootstrap.body': '가입 본문',
  'auth.bootstrap.bodyHint': 'tmula가 가입할 때 보내는 요청 본문입니다. 계정마다 고유하도록 {{.userIndex}}를, 비밀번호에는 {password}를 사용하세요.',
  'auth.bootstrap.body.tip':
    '가입 URL로 보내는 본문이며 계정마다 한 번씩 렌더링됩니다. {{.userIndex}}는 가상 사용자 번호입니다 — 이메일이나 사용자명에 넣어(예: test+{{.userIndex}}@example.com) 가입이 서로 겹치지 않게 하세요. {password}에는 tmula가 로그인에 재사용할 비밀번호가 채워집니다.',
  'auth.bootstrap.teardownUrl': '정리 URL',
  'auth.bootstrap.teardownUrlHint': '실행 후 계정 하나를 삭제하는 엔드포인트입니다. {{.subject}}는 계정 id입니다. 예: DELETE /accounts/{{.subject}}.',
  'auth.bootstrap.teardownMethod': '정리 HTTP 메서드',
  'auth.advanced.bootstrap': '고급',
  'auth.advanced.rawBootstrap': '원본 가입 흐름 편집 (JSON)',
  'auth.advanced.rawBootstrapSub': '간단 양식을 끄고 가입·정리 단계를 원본 JSON으로 직접 작성합니다.',
  'auth.bootstrap.captureToken': '토큰 캡처 (선택)',
  'auth.bootstrap.captureTokenHint': '비워 두면 자동 감지합니다 — 또는 새 계정의 토큰을 담은 변수 이름을 지정하세요.',
  'auth.bootstrap.captureToken.tip':
    '비워 두면 tmula가 가입 응답에서 토큰을 자동 감지합니다 — 흔한 필드(access_token, accessToken, token, id_token, jwt 등)와 session/jwt/auth 쿠키를 찾습니다. 직접 지정하려면 가입 단계가 추출하는, 새 계정의 토큰을 담은 변수 이름을 적으세요. 어느 쪽이든 실제 응답에서 캡처되며 저장되지 않습니다.',
  'auth.bootstrap.captureSubject': 'Subject 캡처',
  'auth.bootstrap.captureSubjectHint': '계정 id가 되는 선택 캡처 변수입니다(정리 단계로 전달됨).',
  'auth.bootstrap.start': '시작 단계',
  'auth.bootstrap.startHint': '선택 진입 단계입니다(기본값: 첫 단계).',
  'auth.bootstrap.steps': '가입 단계',
  'auth.bootstrap.stepsHint': '단계의 JSON 배열입니다: id, method, path, 선택적 body·extract.',
  'auth.bootstrap.steps.tip':
    '각 단계는 가입 요청 하나입니다: 메서드와 루트 경로, 선택적 body, 그리고 token/subject를 캡처하는 extract 맵. {{.userIndex}}가 계정마다 렌더링되어 서로 다르게 가입합니다.',
  'auth.bootstrap.keep': '계정 유지 (정리 건너뜀)',
  'auth.bootstrap.keepSub': '실행 후 생성한 계정을 삭제하지 않고 그대로 둡니다.',
  'auth.bootstrap.teardown': '정리 단계',
  'auth.bootstrap.teardownHint': '각 계정을 삭제하는 단계의 JSON 배열입니다. {{.subject}}는 계정 id입니다.',
  'auth.bootstrap.teardownStart': '정리 시작 단계',
  'auth.bootstrap.teardownStartHint': '선택 정리 진입 단계입니다(기본값: 첫 단계).',

  // Auth · mint 경고 (임포트가 관리형 IdP를 감지 — mint가 동작할 수 없는 대상)
  'auth.advisory.mintManagedIdp':
    '이 서비스의 토큰은 서명 키를 보유한 관리형 IdP({host})가 발급합니다 \u2014 로컬에서 서명(mint)한 토큰은 거부됩니다. OAuth2 모드를 사용하세요.',
  'auth.advisory.mintManagedIdp.generic':
    '이 서비스의 토큰은 서명 키를 보유한 관리형 IdP가 발급합니다 \u2014 로컬에서 서명(mint)한 토큰은 거부됩니다. OAuth2 모드를 사용하세요.',

  // Auth · mint (로컬 JWT 서명, M1)
  'auth.mint.lead':
    'tmula가 각 가상 사용자마다 JWT를 로컬에서 새로 서명합니다 — 로그인도, 토큰 캡처도 없습니다. 대상이 JWT를 자체 발급하고 서명 키를 직접 보유한 경우에만 사용하세요. 보유하지 않은 키(Auth0/Cognito/Firebase)로는 서명할 수 없으니 그런 경우엔 로그인을 사용하세요.',
  'auth.mint.alg': '알고리즘',
  'auth.mint.algHint': 'JWT 서명 방식입니다. HS256은 공유 비밀키를, RS256/ES256은 PEM 개인키를 사용합니다.',
  'auth.mint.alg.tip':
    'HS256은 대칭 비밀키로 서명합니다(같은 값으로 검증). RS256(RSA)과 ES256(ECDSA P-256)은 PEM 개인키로 서명하고 검증자는 공개키 절반을 보유합니다. 대상 검증자가 기대하는 방식을 고르세요.',
  'auth.mint.alg.hs256': 'HS256 — 공유 비밀키(HMAC)',
  'auth.mint.alg.rs256': 'RS256 — RSA 개인키(PEM)',
  'auth.mint.alg.es256': 'ES256 — ECDSA P-256(PEM)',
  'auth.mint.encoding': '비밀키 인코딩',
  'auth.mint.encodingHint': 'HS256 비밀키 저장 방식입니다: 원본 바이트, base64, base64url.',
  'auth.mint.encoding.raw': '원본(바이트 그대로)',
  'auth.mint.encoding.base64': 'Base64',
  'auth.mint.encoding.base64url': 'Base64URL',
  'auth.mint.keyEnv': '키 환경 변수',
  'auth.mint.keyEnvHint': '서버가 서명 키를 읽어올 환경 변수 이름입니다. 파일과 함께가 아니라 둘 중 하나만 사용하세요.',
  'auth.mint.keyEnv.placeholder': 'TMULA_MINT_SECRET',
  'auth.mint.key.tip':
    '서명 키는 참조일 뿐입니다 — tmula가 서버에서 이 환경 변수(또는 아래 파일)로 읽으며 키 자체는 절대 전송되지 않습니다. HS256에서는 공유 비밀키, RS256/ES256에서는 PEM 개인키입니다. env 또는 file 중 정확히 하나만 설정하세요.',
  'auth.mint.keyFile': '키 파일(서버에 위치)',
  'auth.mint.keyFileHint': '서버가 읽을 서명 키 파일 경로입니다. 환경 변수와 함께가 아니라 둘 중 하나만 사용하세요.',
  'auth.mint.keyFile.placeholder': 'signing-key.pem',
  'auth.mint.subject': 'Subject(sub 클레임)',
  'auth.mint.subjectHint': '사용자별 sub 클레임입니다. {{.userIndex}}를 템플릿으로 넣어 사용자마다 서로 다른 주체가 되게 하세요. 비우면 sub 없음.',
  'auth.mint.subject.tip':
    '가상 사용자마다 렌더링되는 토큰의 sub 클레임입니다. {{.userIndex}}(VU 번호)를 참조하면 사용자 0과 1이 서로 다른 주체로 서명됩니다(예: user-{{.userIndex}}). 비우면 sub 없는 토큰을 발급합니다.',
  'auth.mint.ttl': '토큰 수명(초)',
  'auth.mint.ttlHint': '발급된 각 토큰의 유효 시간입니다. exp 클레임을 현재 시각 + 이 초만큼으로 설정합니다.',
  'auth.mint.claims': '추가 클레임(JSON)',
  'auth.mint.claimsHint': '모든 토큰에 서명되는 추가 클레임 JSON 객체(선택)입니다. 값에 {{.userIndex}}/{{.subject}}를 템플릿으로 넣을 수 있습니다.',
  'auth.mint.claims.tip':
    'iat/exp/sub와 함께 모든 토큰에 병합되는 추가 클레임 JSON 객체입니다. 값은 템플릿입니다: {{.userIndex}}(VU 번호)나 {{.subject}}(렌더링된 sub)를 참조하세요. 비우면 표준 클레임만 사용합니다.',
  'auth.mint.claims.placeholder': '{"role": "tester", "tenant": "acme"}',
  'auth.mint.claimsInvalid': '추가 클레임은 올바른 JSON이어야 합니다.',
  'auth.mint.claimsNotObject': '추가 클레임은 JSON 객체여야 합니다(예: {"role":"tester"}).',

  // Auth · exec (토큰을 직접 가져오는 탈출구, X1)
  'auth.exec.lead':
    'tmula가 사용자마다 직접 만든 로컬 명령을 한 번씩 실행하고 그 stdout을 토큰으로 사용합니다 — 어떤 내장 전략으로도 표현할 수 없는 인증을 위한 탈출구입니다. 다른 방법이 모두 맞지 않을 때만 사용하세요.',
  'auth.exec.confirm': '이 기기에서 임의의 로컬 명령을 실행합니다.',
  'auth.exec.confirmSub':
    '명령은 테스트를 구동하는 기기에서 실행되며, 그 egress(외부 통신)는 대상 허용 목록이나 속도 제한의 적용을 받지 않습니다. 실행은 서버 측 --allow-exec 플래그로도 게이트됩니다.',
  'auth.exec.command': '명령',
  'auth.exec.commandHint':
    '실행할 명령입니다. argv 요소를 한 줄에 하나씩 입력하세요. 첫 줄은 프로그램, 나머지는 인자입니다. 각 줄에 {{.userIndex}}를 템플릿으로 넣을 수 있습니다.',
  'auth.exec.command.tip':
    'tmula가 이 argv를 사용자마다 그대로(셸 없이) 실행하고 stdout에서 토큰을 읽습니다. argv[0]은 프로그램(경로 또는 PATH의 이름)이고, 이후 각 줄이 인자 하나입니다. {{.userIndex}}를 사용해 사용자 0과 사용자 1이 서로 다른 토큰을 가져오게 하세요. 비밀값은 여기에 두지 말고 아래 환경 변수에 넣으세요.',
  'auth.exec.env': '추가 환경 변수(KEY=VALUE)',
  'auth.exec.envHint':
    '명령에 전달할 추가 환경 변수입니다. KEY=VALUE를 한 줄에 하나씩 입력하세요. 비밀값은 명령이 아니라 여기에 넣으세요. 값에 {{.userIndex}}를 템플릿으로 넣을 수 있습니다.',
  'auth.exec.env.tip':
    '각 줄이 상속된 환경에 더해 명령 프로세스에 환경 변수 하나를 추가합니다. 비밀값(API 키, 클라이언트 시크릿)을 여기에 넣어 argv에 노출되지 않게 하세요. 값에 {{.userIndex}}를 참조해 사용자별 값을 만들 수 있습니다.',
  'auth.exec.timeout': '제한 시간(초)',
  'auth.exec.timeoutHint': '각 호출이 실행될 수 있는 최대 시간입니다. 이 시간을 넘기면 tmula가 해당 호출을 종료하고 그 사용자의 토큰 가져오기를 실패 처리합니다.',

  // Auth · 시나리오 점검 힌트
  'doctor.authPoolEmpty': '토큰 풀이 선택되었지만 붙여넣거나 업로드한 자격 증명이 없습니다.',
  'doctor.authPoolInvalid': '붙여넣은 자격 증명을 파싱할 수 없습니다: {error}',
  'doctor.authLoginUrl': '로그인이 선택되었지만 로그인 URL이 비어 있습니다 — tmula가 로그인 요청을 보낼 곳이 없습니다.',
  'doctor.authBootstrapUrl': '계정 생성이 선택되었지만 가입 URL이 비어 있습니다 — tmula가 계정을 등록할 곳이 없습니다.',
  'doctor.authLoginGraphJson': '로그인 그래프 JSON이 올바르지 않습니다: {error}',
  'doctor.authLoginTemplatesJson': '로그인 템플릿 JSON이 올바르지 않습니다: {error}',
  'doctor.authLoginCredInvalid': '로그인 자격 증명 목록을 파싱할 수 없습니다: {error}',
  'doctor.authLoginCredUnused':
    '로그인 자격 증명 목록을 주었지만 로그인 본문이 {{.username}}/{{.password}}를 참조하지 않습니다 — 모든 가상 사용자가 같은 본문으로 로그인하게 됩니다. 본문에 행을 템플릿으로 넣어 서로 다른 계정으로 로그인시키세요.',
  'doctor.authBootstrapUnconfirmed':
    '계정 생성은 실행 전에 대상이 비프로덕션임을 확인해야 합니다.',
  'doctor.authBootstrapStepsJson': '가입 단계 JSON이 올바르지 않습니다: {error}',
  'doctor.authBootstrapNoTeardown':
    '계정 생성에 정리 흐름이 없고 계정 유지도 꺼져 있어 생성한 계정이 방치됩니다.',
  'doctor.authBootstrapTeardownJson': '정리 단계 JSON이 올바르지 않습니다: {error}',
  'doctor.authMintKey':
    '로컬 서명이 선택되었지만 서명 키가 참조되지 않았습니다 — tmula가 서명할 수 있도록 키 환경 변수나 키 파일을 설정하세요.',
  'doctor.authMintClaims': '추가 클레임 JSON이 올바르지 않습니다: {error}',

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
  'help.deviation.tip':
    '가상 유저가 확률적으로 경로를 이탈(탐험/중도포기)합니다. 의존성 엣지는 절대 위반되지 않습니다.',

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
  'import.coverage.title': '임포트 커버리지',
  'import.coverage.summary':
    '요청 {requests}건 사용 · {skipped}줄 스킵 · 세션 {sessions}개 · 클라이언트 {clients} · 접힌 엔드포인트 {dropped}개',
  'import.coverage.partial':
    '이 임포트는 캡처된 트래픽의 일부만 반영합니다 — 전체 {total}줄 중 {skipped}줄({pct}%)을 건너뛰었습니다.',
  'import.coverage.full': '사용 가능한 모든 줄이 학습된 그래프에 반영되었습니다.',
  'import.coverage.folded':
    '그래프 상한을 넘는 한산한 엔드포인트 {count}개를 접었습니다 — 해당 트래픽은 남은 노드 사이로 이어집니다.',
  'import.coverage.format': '{format} 포맷으로 감지됨',
  'import.coverage.samples': '건너뛴 줄 샘플',
  'import.coverage.sample.line': '줄',
  'import.coverage.sample.text': '내용',
  'import.coverage.sample.reason': '사유',

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

  // 어태치 모드 (?run=<run-id> 링크 — 예: `tmula demo`가 여는 주소)
  'attach.notFound':
    '실행 "{id}"을(를) 이 서버에서 찾을 수 없습니다 — 이미 끝나 정리되었을 수 있습니다. 아래에서 새 실행을 설정하세요.',

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
  // 여정 결과 헤드라인: 완주(done 도달) 대 이탈(exit 도달).
  'stat.completionRate': '완주율',
  'stat.completionSub': '{started}개 여정 중 {count}개가 완료(done)에 도달했습니다',
  'stat.dropOffRate': '이탈률',
  'stat.dropOffSub': '{started}개 여정 중 {count}개가 중간에 이탈(exit)했습니다',

  // Findings (ReportView)
  'metrics.title': '서버 메트릭',
  'metrics.fetchError': '일부 시계열을 가져오지 못했습니다:',
  'findings.title': '발견 항목',
  'findings.empty': '발견된 문제가 없습니다.',

  // 발견 항목의 증거 패널 (ReportView). 세션 ID·페르소나·오류 분류·구간 라벨은
  // 백엔드 데이터라 그대로 보여 주고, 주변 UI 문구만 번역합니다.
  'evidence.summary': '증거',
  'evidence.summaryOne': '증거 · 대표 세션 {count}건',
  'evidence.summaryMany': '증거 · 대표 세션 {count}건',
  'evidence.sessionsTitle': '대표 세션',
  'evidence.grepHint':
    '각 세션은 모든 요청에 자기 ID를 X-Tmula-Session-ID 헤더로 보냈습니다. 대상 서버 로그에서 아래 ID로 grep 하면 그 세션이 한 일을 정확히 볼 수 있습니다. 시드와 사용자 번호는 세션을 재현하는 좌표입니다.',
  'evidence.col.session': '세션',
  'evidence.col.persona': '페르소나',
  'evidence.col.seed': '시드',
  'evidence.col.user': '사용자 #',
  'evidence.col.path': '실패까지의 경로',
  'evidence.col.status': '상태',
  'evidence.col.latency': '지연',
  'evidence.col.error': '오류',
  'evidence.col.time': '시각',
  'evidence.statusTitle': '상태 코드 분포 (전체 발생 기준)',
  'evidence.timingTitle': '실행 중 발생 시점',
  'evidence.rootCause': '근본 원인 분류:',

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
