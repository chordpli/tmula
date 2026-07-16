// API helpers for the tmula control plane. Pure functions live here (and are
// unit-tested) so the React component stays thin.

export interface ExperimentForm {
  baseUrl: string
  allowlist: string // comma-separated
  users: number
  maxSteps: number
  // Chance (a friendly 0–100 percent) that a user wanders off the weighted path at
  // a step — exploring another branch or abandoning mid-journey. Sent to the server
  // as its 0..1 deviationRate fraction; dependency edges are never violated.
  deviationPct: number
  start: string
  graphJSON: string
  templatesJSON: string
  workers: string // comma-separated gRPC worker addresses; blank = run locally
  aggregateWorkers: boolean // distributed: workers summarize their shard instead of streaming
  // Workload: 'closed' = fixed `users`; 'open' = arrival-rate sessions over time.
  workloadKind: 'closed' | 'open'
  arrivalRate: number // open: users arriving per second
  durationSeconds: number // open: how long to keep users arriving
  maxConcurrency: number // open: back-pressure cap (0 = uncapped)
  thinkMinMs: number // pause between a user's steps (uniform [min,max])
  thinkMaxMs: number
  segmentsJSON: string // open: persona mix as a JSON array (blank/[] = homogeneous)
  traceEnabled: boolean // visualize live traffic (per-request for small runs, flow map for large)

  // --- Auth (P5) -------------------------------------------------------------
  // How the simulated traffic authenticates. 'none' (the default) runs anonymously
  // and is the EXACT prior behavior: no credentialPool is attached. The other modes
  // attach a credentialPool the server reads (see buildCredentialPool / buildRunSpec).
  authMode: AuthMode

  // 'pool' mode: a textarea of pasted credential lines and/or an uploaded file, both
  // parsed IN THE BROWSER into entries. We never send a file/env source ref from the
  // browser — the server rejects an unresolved source over the wire (D1), so the
  // console resolves the text/file into inline { subject, token } entries here.
  authPoolText: string // raw pasted lines (csv / jsonl / plain tokens)
  authPoolFormat: CredFormat // how authPoolText (and the file) is encoded

  // 'login' mode: the standalone login flow that mints a token. The COMMON case is
  // authored through the simple mini-form (a method+path, a request-body template with
  // {username}/{password} markers, and a scope); buildAuth compiles those into the
  // loginFlow graph/templates under the hood. Power users (and round-tripped imports)
  // switch loginMode to 'advanced' and author the graph/templates JSON directly, the
  // same way the scenario graph/templates are. tokenVar/subjectVar name the captured
  // variables that become the credential (mirrors LoginFlowSpec.tokenVar/subjectVar).
  loginMode: AuthAuthoringMode // 'simple' mini-form (default) or 'advanced' raw JSON
  loginUrlMethod: string // simple: the login request method (POST by default)
  loginUrlPath: string // simple: the login request path, e.g. /login
  loginBodyTemplate: string // simple: the request body, with {username}/{password} or REPLACE_ME_* markers
  loginGraphJSON: string
  loginTemplatesJSON: string
  loginStart: string
  loginTokenVar: string // captured variable that becomes the token (optional; empty = auto-detect)
  loginSubjectVar: string // captured variable that becomes the subject (optional)
  // Explicit refresh-grant OVERRIDE (advanced only): when loginRefreshBody is set it WINS
  // over the backend's auto-derivation of a grant_type=refresh_token request, so even a
  // JSON-body login gets a real refresh exchange instead of re-logging-in on a mid-run 401.
  // loginRefreshRequest is the optional "METHOD /path" the refresh POSTs to (defaults to the
  // login token endpoint). Both blank = auto-derive / re-login, unchanged. Surfaced ONLY in
  // the advanced login panel — the simple mini-form stays one-field.
  loginRefreshRequest: string // optional refresh request line, e.g. POST /oauth/token
  loginRefreshBody: string // optional refresh form body, may reference {{.refreshToken}}
  loginScope: LoginScope // 'per-user' (default) or 'shared' (client_credentials)

  // "Log in multiple users" (P8): an OPTIONAL list of login credentials so each virtual
  // user logs in as a different account. It is parsed IN THE BROWSER (like the token
  // pool) into credentialPool.entries of { subject: username, token: password } — login
  // INPUTS, not pre-issued tokens. Empty text = the single-identity login (unchanged):
  // every virtual user logs in with the same body. The backend logs virtual user i in
  // with entries[i % N], exposing the row to the body as {{.username}} (= subject) and
  // {{.password}} (= token), plus {{.userIndex}} (the VU number).
  loginCredText: string // raw pasted username,password rows (csv or jsonl); blank = single-identity
  loginCredFormat: LoginCredFormat // how loginCredText (and the file) is encoded

  // 'bootstrap' mode: a signup flow that provisions a real account, a capture mapping,
  // and an optional teardown flow. The COMMON case (and an imported suggestedSignup) is
  // authored through the simple mini-form — a signup method+path with a body template,
  // plus an optional teardown method+path — which buildAuth compiles into the signup /
  // teardown steps. Power users switch signupMode to 'advanced' and author the steps
  // JSON directly. keepAccounts opts out of teardown. It is gated behind an explicit
  // non-production confirmation (authBootstrapConfirmed) because it creates/deletes
  // REAL accounts on the target.
  signupMode: AuthAuthoringMode // 'simple' mini-form (default) or 'advanced' raw JSON
  signupUrlMethod: string // simple: the signup request method (POST by default)
  signupUrlPath: string // simple: the signup request path, e.g. /register
  signupBodyTemplate: string // simple: the signup request body, with {{.userIndex}} / REPLACE_ME_* markers
  signupTeardownUrlMethod: string // simple: the teardown request method (DELETE by default)
  signupTeardownUrlPath: string // simple: the teardown path, with {{.subject}}, e.g. /accounts/{{.subject}}
  signupStepsJSON: string // JSON array of SignupStep objects
  signupStart: string // optional entry-step override
  signupCaptureToken: string // captured variable that becomes the token (optional; empty = auto-detect)
  signupCaptureSubject: string // captured variable that becomes the subject (optional)
  signupTeardownJSON: string // optional JSON array of teardown SignupStep objects
  signupTeardownStart: string // optional teardown entry-step override
  keepAccounts: boolean // leave provisioned accounts in place (no teardown)
  // The operator has confirmed bootstrap targets a non-production system. Required
  // before the bootstrap mode can be selected/submitted, mirroring tmula's non-prod
  // safety stance (this creates/deletes real accounts on the target).
  authBootstrapConfirmed: boolean

  // 'mint' mode: self-issue a JWT per virtual user by signing one LOCALLY with a key
  // the operator holds (the M1 case — a service whose tokens are self-issued JWTs). It
  // SKIPS token acquisition entirely: no login/refresh/capture, each VU gets a token
  // instantly. The signing key is a REFERENCE only (an env var the server reads, or a
  // file on the server) — never inlined on the wire — so buildAuth sends mintKeyEnv /
  // mintKeyFile as the pointer and the backend resolves it in-process. It does NOT help
  // a third-party/managed IdP (Auth0/Cognito/Firebase) — you cannot sign for a key you
  // do not hold — only self-issued JWT.
  mintAlg: MintAlg // HS256 | RS256 | ES256
  mintSecretEncoding: MintEncoding // HS256 only: how the secret body is encoded
  mintKeyEnv: string // env var holding the signing key (mutually exclusive with file)
  mintKeyFile: string // file path (on the server) holding the signing key
  mintSubject: string // sub-claim template, e.g. user-{{.userIndex}} (blank = no sub)
  mintClaimsJSON: string // extra claims as a JSON object (blank = none); values may template {{.userIndex}}/{{.subject}}
  mintTtlSeconds: number // token lifetime in seconds → exp = now+ttl

  // exec (bring-your-own-token escape hatch): run an operator-supplied COMMAND per
  // virtual user whose stdout is the token — the universal fallback for auth tmula
  // cannot model declaratively (social/SDK login, third-party IdP consent). It runs an
  // arbitrary LOCAL command, so the operator must explicitly confirm the opt-in
  // (execConfirmed), like bootstrap; the run-start server gate enforces it again. The
  // command is argv (never a shell), its egress is NOT bound by the target allowlist /
  // rate cap, and operator secrets belong in the env (KEY=VALUE), never argv.
  execConfirmed: boolean // operator acknowledged exec runs a local command (the opt-in)
  execCommandText: string // argv, one element per line (argv[0] is the program); may template {{.userIndex}}
  execEnvText: string // extra env as KEY=VALUE lines (secrets go here, not argv); values may template {{.userIndex}}
  execTimeoutSeconds: number // per-invocation timeout in seconds

  // replaceMeValues holds the user-supplied secret(s) for any REPLACE_ME_* placeholder
  // an import surfaced (e.g. REPLACE_ME_PASSWORD -> the real password). It is keyed by
  // the placeholder literal; buildAuth substitutes each occurrence in the login/signup
  // bodies right before sending, so the ONLY thing the operator must fill after an
  // auto-detected import is the highlighted secret. Empty for hand-authored flows.
  replaceMeValues: Record<string, string>

  // --- UI-only fields (NEVER serialized into the RunSpec) ----------------------
  // authEntryOAuth2 remembers that the operator's Auth-card entry is the OAuth2
  // guide — a pseudo-entry that compiles onto the 'login' wire mode, so it cannot
  // be derived from authMode alone. Living on the form (instead of component
  // state) lets the scenario doctor speak the guide's language and survives any
  // unmount. buildRunSpec never reads it.
  authEntryOAuth2: boolean
  // oauth2Guide holds the OAuth2 guide's answers. Hoisted onto the form so
  // switching entry points and back preserves every answer; compiled onto the
  // login/pool fields via authFormFromOAuth2Guide. buildRunSpec never reads it.
  oauth2Guide: OAuth2GuideForm
}

// AuthAuthoringMode picks how the login / signup material is authored: 'simple' is the
// guided mini-form a normal developer uses (a method+path and a body template), while
// 'advanced' exposes the raw graph/templates/steps JSON for power users and for
// round-tripping an imported flow that is too rich for the mini-form.
export type AuthAuthoringMode = 'simple' | 'advanced'

// AuthMode is the console's selected authentication strategy. It is a UI concept:
// 'none' attaches no credentialPool (anonymous, the default), while the others map
// onto a backend CredentialStrategy (pool / login / bootstrap-signup / mint / exec).
export type AuthMode = 'none' | 'pool' | 'login' | 'bootstrap' | 'mint' | 'exec'

// MintAlg is the JWS signing algorithm the mint strategy self-issues a token with,
// mirroring the backend domain.MintAlg: HS256 (symmetric HMAC secret), RS256 (RSA) or
// ES256 (ECDSA P-256). Only these three are signed with the standard library.
export type MintAlg = 'HS256' | 'RS256' | 'ES256'

// MintEncoding declares how an HS256 secret body is encoded, mirroring the backend
// domain.MintEncoding: raw (verbatim bytes), base64, or base64url. It is meaningful
// only for HS256; an asymmetric alg reads a PEM and ignores it.
export type MintEncoding = 'raw' | 'base64' | 'base64url'

// CredFormat is how a pasted/uploaded credential body is encoded, matching the
// backend credential source formats (auth.Format): csv (a header row with a token
// column and optional subject column), jsonl ({subject,token} per line), or tokens
// (one secret per non-blank line, no subject).
export type CredFormat = 'csv' | 'jsonl' | 'tokens'

// LoginScope selects how many principals a login pool mints, mirroring the backend
// domain.LoginScope: per-user (one token per virtual user, the default) or shared
// (one client_credentials token for every session).
export type LoginScope = 'per-user' | 'shared'

// LoginCredFormat is how the "log in multiple users" credential list is encoded. Unlike
// CredFormat (which carries pre-issued tokens) these rows are login INPUTS — a username
// and a password per account — so the formats name those columns: csv (a header row with
// username + password columns) or jsonl ({"username":..,"password":..} per line). There
// is no plain-tokens variant: a login always needs both halves of the credential.
export type LoginCredFormat = 'csv' | 'jsonl'

// CredentialEntry is one inline credential the console sends in credentialPool.entries.
// The field name `token` matches the backend's authoring shape (scenariofile.Credential
// / auth.jsonlCred), which maps token -> the domain credential's secret. (The domain's
// own Credential.Secret is json:"-", so the wire entry must carry the secret under a
// readable name; see the backend-assumptions note in the PR summary.)
export interface CredentialEntry {
  subject?: string
  token: string
}

// SignupStepSpec is one request in a bootstrap signup/teardown journey, matching the
// backend domain.SignupStep wire shape (transport-free: bare method/path, not the
// "METHOD /path" shorthand a config file authors).
export interface SignupStepSpec {
  id: string
  method: string
  path: string
  headers?: Record<string, string>
  body?: string
  extract?: Record<string, string>
  dependsOn?: string
  weight?: number
}

// LoginFlowSpec is the standalone login flow a 'login' pool mints tokens from,
// matching the backend runspec.LoginFlowSpec: its own graph + templates + start, plus
// the captured-variable names that become the token and subject. tokenVar is
// optional — an empty/omitted tokenVar means tmula auto-detects the token from the
// login response (the common access_token/token/jwt/session shapes).
export interface LoginFlowSpec {
  graph: unknown
  templates: unknown
  start: string
  maxSteps?: number
  tokenVar?: string
  subjectVar?: string
  // refreshRequest / refreshBody are an OPTIONAL explicit refresh-grant override the
  // backend builds the mid-run refresh transport from (matching runspec.LoginFlowSpec).
  // When refreshBody is set it WINS over the backend's auto-derivation — so even a
  // JSON-body login (which cannot be auto-rewritten) gets a real grant_type=refresh_token
  // exchange instead of a re-login. refreshRequest is the "METHOD /path" the refresh
  // POSTs to; it is optional and defaults to the login token endpoint when omitted. Both
  // omitted is the unchanged auto-derive / re-login behavior. Neither carries a secret —
  // the refresh token is captured at run time from the live login response.
  refreshRequest?: string
  refreshBody?: string
}

// SignupFlowSpec is the declarative bootstrap-signup journey, matching the backend
// domain.SignupFlow: signup steps + a capture mapping, plus an optional teardown
// journey. The orchestrator compiles it to a graph + templates at run time. The
// capture token is optional — an omitted token means tmula auto-detects it from the
// signup response.
export interface SignupFlowSpec {
  steps: SignupStepSpec[]
  start?: string
  capture: { token?: string; subject?: string }
  teardown?: SignupStepSpec[]
  teardownStart?: string
}

// MintSpec is the wire shape the server reads to self-issue a JWT per virtual user,
// mirroring the backend domain.MintSpec (only the fields the console emits): the alg,
// the HS256 secret encoding, the NON-SECRET signing-key reference (an env var or a
// file the server resolves IN-PROCESS — never the key itself), the subject template,
// the extra claims and the TTL (a Go duration string, e.g. "1h0m0s"). The console
// NEVER sends a key body — only the reference, so the secret never crosses the wire.
export interface MintSpec {
  alg: MintAlg
  secretEncoding?: MintEncoding
  key?: { env?: string; file?: string }
  subject?: string
  claims?: Record<string, string>
  ttl: string // Go duration string (e.g. "1h0m0s") the backend parses into a time.Duration
}

// OAuth2Grant is how the operator answers "how do you log in?" in the OAuth2
// guide: a username+password (password grant), a client key (client_credentials),
// a refresh token pasted from an app/browser session (refresh_token — the answer
// for human-consent services like Auth0/Cognito/social login), or just an access
// token (no grant at all — becomes a token pool).
export type OAuth2Grant = 'password' | 'clientCredentials' | 'refreshToken' | 'accessToken'

// OAuth2GuideForm is the OAuth2 guide's own input state: an optional issuer URL
// (for endpoint discovery), a token URL, plus the grant's fields. It lives on
// ExperimentForm as a UI-ONLY field (never serialized into the RunSpec) so guide
// answers survive switching entry points; authFormFromOAuth2Guide compiles it
// onto the existing login/pool form fields, so the wire payload is untouched.
export interface OAuth2GuideForm {
  // issuer is the optional IdP base URL the operator pastes when they do NOT know
  // their token URL; "Fetch endpoints" resolves it via the server's discovery
  // proxy and fills tokenUrl. It never crosses into the run spec itself.
  issuer: string
  tokenUrl: string
  grant: OAuth2Grant
  username: string
  password: string
  // users is an optional multi-user CSV (username,password header + rows); when
  // non-empty it wins over the single username/password identity.
  users: string
  clientId: string
  clientSecret: string
  scope: string
  refreshToken: string
  accessToken: string
}

export const OAUTH2_GUIDE_DEFAULTS: OAuth2GuideForm = {
  issuer: '',
  tokenUrl: '',
  grant: 'password',
  username: '',
  password: '',
  users: '',
  clientId: '',
  clientSecret: '',
  scope: '',
  refreshToken: '',
  accessToken: '',
}

// AUTH_FORM_DEFAULTS is the off (anonymous) baseline for the form's auth fields, so
// the initial form, presets, and tests can spread one shared default instead of
// repeating the 18 fields. authMode 'none' means no credentialPool is attached and
// the run is byte-identical to the pre-auth behavior.
export const AUTH_FORM_DEFAULTS: Pick<
  ExperimentForm,
  | 'authMode'
  | 'authPoolText'
  | 'authPoolFormat'
  | 'loginMode'
  | 'loginUrlMethod'
  | 'loginUrlPath'
  | 'loginBodyTemplate'
  | 'loginGraphJSON'
  | 'loginTemplatesJSON'
  | 'loginStart'
  | 'loginTokenVar'
  | 'loginSubjectVar'
  | 'loginRefreshRequest'
  | 'loginRefreshBody'
  | 'loginScope'
  | 'loginCredText'
  | 'loginCredFormat'
  | 'signupMode'
  | 'signupUrlMethod'
  | 'signupUrlPath'
  | 'signupBodyTemplate'
  | 'signupTeardownUrlMethod'
  | 'signupTeardownUrlPath'
  | 'signupStepsJSON'
  | 'signupStart'
  | 'signupCaptureToken'
  | 'signupCaptureSubject'
  | 'signupTeardownJSON'
  | 'signupTeardownStart'
  | 'keepAccounts'
  | 'authBootstrapConfirmed'
  | 'mintAlg'
  | 'mintSecretEncoding'
  | 'mintKeyEnv'
  | 'mintKeyFile'
  | 'mintSubject'
  | 'mintClaimsJSON'
  | 'mintTtlSeconds'
  | 'execConfirmed'
  | 'execCommandText'
  | 'execEnvText'
  | 'execTimeoutSeconds'
  | 'replaceMeValues'
  | 'authEntryOAuth2'
  | 'oauth2Guide'
> = {
  authMode: 'none',
  authPoolText: '',
  authPoolFormat: 'csv',
  loginMode: 'simple',
  loginUrlMethod: 'POST',
  loginUrlPath: '',
  loginBodyTemplate: '{"username": "{username}", "password": "{password}"}',
  loginGraphJSON: '',
  loginTemplatesJSON: '',
  loginStart: '',
  loginTokenVar: '',
  loginSubjectVar: '',
  loginRefreshRequest: '',
  loginRefreshBody: '',
  loginScope: 'per-user',
  loginCredText: '',
  loginCredFormat: 'csv',
  signupMode: 'simple',
  signupUrlMethod: 'POST',
  signupUrlPath: '',
  signupBodyTemplate: '{"email": "test+{{.userIndex}}@example.com", "password": "{password}"}',
  signupTeardownUrlMethod: 'DELETE',
  signupTeardownUrlPath: '',
  signupStepsJSON: '',
  signupStart: '',
  signupCaptureToken: '',
  signupCaptureSubject: '',
  signupTeardownJSON: '',
  signupTeardownStart: '',
  keepAccounts: false,
  authBootstrapConfirmed: false,
  mintAlg: 'HS256',
  mintSecretEncoding: 'raw',
  mintKeyEnv: '',
  mintKeyFile: '',
  mintSubject: 'user-{{.userIndex}}',
  mintClaimsJSON: '',
  mintTtlSeconds: 3600,
  execConfirmed: false,
  execCommandText: '',
  execEnvText: '',
  execTimeoutSeconds: 30,
  replaceMeValues: {},
  authEntryOAuth2: false,
  oauth2Guide: OAUTH2_GUIDE_DEFAULTS,
}

// CredentialPool is the wire shape the server reads to authenticate a run, matching
// the backend domain.CredentialPool (only the fields the console emits): the strategy,
// inline entries for 'pool', the loginFlowId + scope for 'login', and the signupFlow +
// keepAccounts for 'bootstrap-signup'. The console NEVER sends a file/env `source` —
// it resolves any pasted/uploaded pool into inline entries in the browser (D1).
export interface CredentialPool {
  id: string
  strategy: 'pool' | 'login' | 'bootstrap-signup' | 'mint' | 'exec'
  entries?: CredentialEntry[]
  loginFlowId?: string
  loginScope?: LoginScope
  signupFlow?: SignupFlowSpec
  keepAccounts?: boolean
  // mint carries the local-JWT-signing config when strategy is 'mint' (the M1 case).
  mint?: MintSpec
  // exec carries the bring-your-own-token command config when strategy is 'exec'.
  exec?: ExecSpec
}

// ExecSpec is the wire shape for the exec (bring-your-own-token) strategy: the argv
// command run per virtual user (argv[0] is the program — never a shell string), optional
// extra env (where operator secrets belong, NOT argv), and an optional per-invocation
// timeout as a Go duration string. It carries no secret inline; the command and its env
// references resolve on the server, behind the operator opt-in gate.
export interface ExecSpec {
  command: string[]
  env?: Record<string, string>
  timeout?: string
  maxOutputBytes?: number
}

// Segment is one persona in an open run: a weighted share of arrivals with its
// own entry node and pacing overrides.
export interface Segment {
  name: string
  weight: number
  start?: string
  maxSteps?: number
  thinkTime?: { minMs: number; maxMs: number }
}

export interface WorkloadSpec {
  kind: 'open'
  arrival: { shape: 'constant'; startRate: number; peakRate: number }
  durationSeconds: number
  maxConcurrency: number
  thinkTime: { minMs: number; maxMs: number }
}

export interface RunSpec {
  experiment: unknown
  targetEnv: unknown
  graph: unknown
  templates: unknown
  start: string
  maxSteps: number
  users: { id: string }[]
  // Closed-run pool size. The server synthesizes the pool (u0..uN-1) from this when
  // `users` is empty, so a large closed run is a small body instead of one object
  // per user; the open model ignores it.
  userCount?: number
  seed: number
  workers?: string[]
  aggregateWorkers?: boolean
  workload?: WorkloadSpec
  segments?: Segment[]
  trace?: boolean // opt the run into visualization (per-step events for small runs, per-edge aggregates at any scale)
  // Auth (P5): attached only when auth is configured (authMode !== 'none'). The
  // credentialPool carries the strategy + its material; a 'login' pool additionally
  // needs the standalone loginFlow at the top level (the orchestrator mints tokens
  // from it). Both are omitted on the None path so an anonymous run is byte-identical
  // to before.
  credentialPool?: CredentialPool
  loginFlow?: LoginFlowSpec
}

// parseSegments reads the persona-mix JSON. A blank value means no personas
// (homogeneous run); anything else must be a JSON array of objects each with a
// string `name` and numeric `weight`, or it throws, so a malformed mix is caught
// here rather than rejected confusingly by the server — same contract as the
// graph/templates fields.
export function parseSegments(json: string): Segment[] {
  if (!json.trim()) return []
  const parsed = JSON.parse(json)
  if (!Array.isArray(parsed)) throw new Error('segments must be a JSON array')
  parsed.forEach((seg, i) => {
    if (typeof seg !== 'object' || seg === null) {
      throw new Error(`segment ${i} must be an object with a name and weight`)
    }
    const { name, weight } = seg as { name?: unknown; weight?: unknown }
    if (typeof name !== 'string') throw new Error(`segment ${i} name must be a string`)
    if (typeof weight !== 'number') throw new Error(`segment ${i} weight must be a number`)
  })
  return parsed as Segment[]
}

// parseCredentials resolves a pasted/uploaded credential body INTO INLINE ENTRIES in
// the browser, mirroring the backend auth.Parse contract for each format so the
// resolved entries are identical to what a file/env source would yield server-side:
//
//   - 'csv'    — a header row that must include a "token" column (and may include a
//                "subject" column), then one credential per data row (indexed by the
//                header position, ragged rows tolerated).
//   - 'jsonl'  — one {"subject":..,"token":..} object per non-blank line; token
//                required, subject optional.
//   - 'tokens' — one secret per non-blank line, no subject (the plain-token case).
//
// Blank lines (and a trailing newline) are ignored. It throws a clear, format-named
// error on malformed input or when zero credentials are parsed, so the console can
// surface the reason inline rather than POSTing an empty pool. The browser must do
// this because the server REJECTS an unresolved file/env source over the wire (D1):
// the console may only ever send inline entries it resolved here.
export function parseCredentials(format: CredFormat, body: string): CredentialEntry[] {
  switch (format) {
    case 'csv':
      return parseCredsCSV(body)
    case 'jsonl':
      return parseCredsJSONL(body)
    case 'tokens':
      return parseCredsTokens(body)
    default:
      throw new Error(`unknown credential format "${format}"`)
  }
}

// parseCredsCSV reads a header row (which must carry a "token" column and may carry a
// "subject" column) then one credential per data row. It is a minimal RFC-4180-lite
// reader: it handles double-quoted fields (with "" escapes) and ignores blank lines,
// matching the common shape the Go csv reader accepts for credential pools.
function parseCredsCSV(body: string): CredentialEntry[] {
  const rows = body
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map(splitCSVRow)
  if (rows.length === 0) throw new Error('CSV credential text is empty')
  const header = rows[0].map((h) => h.trim())
  const subjectIdx = header.indexOf('subject')
  const tokenIdx = header.indexOf('token')
  if (tokenIdx < 0) throw new Error('CSV credentials need a "token" column header')
  const out: CredentialEntry[] = []
  for (const rec of rows.slice(1)) {
    if (tokenIdx >= rec.length || !rec[tokenIdx].trim()) {
      throw new Error('a CSV row is missing its token column')
    }
    const subject = subjectIdx >= 0 && subjectIdx < rec.length ? rec[subjectIdx] : ''
    out.push(subject ? { subject, token: rec[tokenIdx] } : { token: rec[tokenIdx] })
  }
  if (out.length === 0) throw new Error('CSV credentials have no data rows (need at least one credential)')
  return out
}

// splitCSVRow splits one CSV line into fields, honoring double-quoted fields and ""
// as an escaped quote. It keeps the reader dependency-free while tolerating commas
// inside quoted tokens (a JWT never contains a comma, but a subject might).
function splitCSVRow(line: string): string[] {
  const fields: string[] = []
  let field = ''
  let inQuotes = false
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (inQuotes) {
      if (ch === '"') {
        if (line[i + 1] === '"') {
          field += '"'
          i++
        } else {
          inQuotes = false
        }
      } else {
        field += ch
      }
    } else if (ch === '"') {
      inQuotes = true
    } else if (ch === ',') {
      fields.push(field)
      field = ''
    } else {
      field += ch
    }
  }
  fields.push(field)
  return fields
}

// parseCredsJSONL reads one {subject,token} object per non-blank line. token is
// required; subject is optional and omitted from the entry when blank (so a token-only
// JSONL line produces the same subject-less entry as a plain-tokens line).
function parseCredsJSONL(body: string): CredentialEntry[] {
  const out: CredentialEntry[] = []
  for (const raw of body.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line) continue
    let parsed: unknown
    try {
      parsed = JSON.parse(line)
    } catch {
      throw new Error('a JSONL credential line is not valid JSON')
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error('each JSONL credential line must be a {subject, token} object')
    }
    const { subject, token } = parsed as { subject?: unknown; token?: unknown }
    if (typeof token !== 'string' || !token) throw new Error('a JSONL credential is missing its token')
    out.push(subject ? { subject: String(subject), token } : { token })
  }
  if (out.length === 0) throw new Error('JSONL credentials have no rows (need at least one credential)')
  return out
}

// parseCredsTokens reads one secret per non-blank line, leaving the subject empty —
// the plain-token (no-subject) case. Each session then authenticates with a bare
// token and no principal id, which is fine for opaque bearer tokens where the subject
// is encoded in the token itself.
function parseCredsTokens(body: string): CredentialEntry[] {
  const out: CredentialEntry[] = []
  for (const raw of body.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line) continue
    out.push({ token: line })
  }
  if (out.length === 0) {
    throw new Error('token credentials have no non-blank line (need at least one credential)')
  }
  return out
}

// parseLoginCredentials resolves the "log in multiple users" list (a pasted/uploaded
// username,password body) into the credentialPool.entries the login strategy carries.
// Each row becomes { subject: username, token: password } — login INPUTS, mapped onto
// the same wire entry shape the backend reads, so for login `subject` is the username
// and `token` is the password (NOT a pre-issued token). The two formats name those
// columns explicitly so the operator is never confused about which is which:
//
//   - 'csv'   — a header row that must include BOTH a "username" and a "password"
//               column (in any order), then one credential per data row.
//   - 'jsonl' — one {"username":..,"password":..} object per non-blank line.
//
// Blank lines (and a trailing newline) are ignored. It throws a clear, format-named
// error on malformed input or when zero rows are parsed, so the console can surface the
// reason inline rather than POSTing an empty list. Parsing in the browser mirrors the
// token pool (the server never sees a file/env source, D1).
export function parseLoginCredentials(format: LoginCredFormat, body: string): CredentialEntry[] {
  switch (format) {
    case 'csv':
      return parseLoginCredsCSV(body)
    case 'jsonl':
      return parseLoginCredsJSONL(body)
    default:
      throw new Error(`unknown login credential format "${format}"`)
  }
}

// parseLoginCredsCSV reads a header row that must carry both a "username" and a
// "password" column (in any order), then one login credential per data row. It reuses
// the dependency-free RFC-4180-lite reader (splitCSVRow) and ignores blank lines.
function parseLoginCredsCSV(body: string): CredentialEntry[] {
  const rows = body
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map(splitCSVRow)
  if (rows.length === 0) throw new Error('login credential text is empty')
  const header = rows[0].map((h) => h.trim())
  const userIdx = header.indexOf('username')
  const passIdx = header.indexOf('password')
  if (userIdx < 0 || passIdx < 0) {
    throw new Error('login credentials need a header with "username" and "password" columns')
  }
  const out: CredentialEntry[] = []
  for (const rec of rows.slice(1)) {
    const username = userIdx < rec.length ? rec[userIdx].trim() : ''
    const password = passIdx < rec.length ? rec[passIdx] : ''
    if (!username) throw new Error('a login credential row is missing its username')
    if (!password) throw new Error('a login credential row is missing its password')
    out.push({ subject: username, token: password })
  }
  if (out.length === 0) {
    throw new Error('login credentials have no data rows (need at least one username,password)')
  }
  return out
}

// parseLoginCredsJSONL reads one {"username":..,"password":..} object per non-blank
// line, mapping username -> subject and password -> token. Both are required.
function parseLoginCredsJSONL(body: string): CredentialEntry[] {
  const out: CredentialEntry[] = []
  for (const raw of body.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line) continue
    let parsed: unknown
    try {
      parsed = JSON.parse(line)
    } catch {
      throw new Error('a login credential line is not valid JSON')
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error('each login credential line must be a {username, password} object')
    }
    const { username, password } = parsed as { username?: unknown; password?: unknown }
    if (typeof username !== 'string' || !username) {
      throw new Error('a login credential is missing its username')
    }
    if (typeof password !== 'string' || !password) {
      throw new Error('a login credential is missing its password')
    }
    out.push({ subject: username, token: password })
  }
  if (out.length === 0) {
    throw new Error('login credentials have no rows (need at least one username/password)')
  }
  return out
}

// loginBodyReferencesRow reports whether a login body pulls a credential-list row in —
// i.e. it mentions the {{.username}} or {{.password}} Go-template markers the backend
// exposes for each entry. The scenario doctor uses it to warn when a multi-user list is
// supplied but the body never templates a row in (so every virtual user would log in
// with the same literal body — almost certainly a mistake). Whitespace inside the braces
// (e.g. {{ .username }}) still counts.
export function loginBodyReferencesRow(body: string): boolean {
  return /\{\{\s*\.(username|password)\s*\}\}/.test(body)
}

// parseAllowlist mirrors the backend contract: comma-separated host patterns,
// trimmed, with empty entries ignored.
export function parseAllowlist(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

// hostFromBaseUrl extracts the hostname the safety guard will see. It accepts a
// bare host during editing by temporarily adding http://, but returns null for
// incomplete or malformed input so the UI can avoid noisy warnings mid-typing.
export function hostFromBaseUrl(baseUrl: string): string | null {
  const raw = baseUrl.trim()
  if (!raw) return null
  const candidate = /^[a-zA-Z][a-zA-Z\d+.-]*:\/\//.test(raw) ? raw : `http://${raw}`
  try {
    const host = new URL(candidate).hostname
    return host || null
  } catch {
    return null
  }
}

// allowlistMatchesHost implements the same exact / leading-wildcard semantics
// as the backend guard, so the console warns only when a run would really be
// blocked by the configured allowlist.
export function allowlistMatchesHost(allowlist: string[], host: string): boolean {
  const normalized = host.trim().toLowerCase()
  if (!normalized) return false
  return allowlist.some((pattern) => {
    const p = pattern.trim().toLowerCase()
    return p === normalized || (p.startsWith('*.') && normalized.endsWith(p.slice(1)))
  })
}

// addBaseUrlHostToAllowlist appends the Base URL host if the current allowlist
// does not already cover it. It preserves the existing comma-separated style and
// is safe to call on every Base URL change.
export function addBaseUrlHostToAllowlist(baseUrl: string, allowlistValue: string): string {
  const host = hostFromBaseUrl(baseUrl)
  if (!host) return allowlistValue
  const allowlist = parseAllowlist(allowlistValue)
  if (allowlistMatchesHost(allowlist, host)) return allowlistValue
  return [...allowlist, host].join(', ')
}

// runDisabled reports whether the Run button should be disabled for a given run
// status — i.e. while a run is in flight. 'pending' is included alongside
// 'starting' and 'running' because the SSE stream can emit it before 'running';
// omitting it briefly re-enables the button mid-run.
export function runDisabled(status: string): boolean {
  return status === 'starting' || status === 'pending' || status === 'running'
}

export interface Stats {
  total: number
  errors: number
  timeouts: number
  errorRate: number
  statusCounts: Record<string, number>
  p50: number
  p95: number
  p99: number
  max: number
}

// EvidenceSession is one representative session behind a finding, exactly as the
// server marshals domain.EvidenceSession. The wire names are part of the masking
// contract: the shared-report PII masker redacts any field whose NAME contains a
// sensitive substring (including "session"), so the synthetic session id rides
// under "vu" (and the list under "vus") to survive masking intact.
export interface EvidenceSession {
  vu: string // the X-Tmula-Session-ID header value the session sent on every request
  seed: number // the session's walk seed (run seed + userIndex)
  userIndex: number // the offset that derives the seed — the reproduce coordinate
  persona?: string // segment label; absent when the run had no persona mix
  path?: string[] // node chain up to and including the failing request; absent when the producing path carries no journeys
  statusCode?: number // absent/0 for transport-level failures (see errorClass)
  latencyMs: number
  errorClass?: string
  ts: string // RFC 3339 completion time of the failing request
}

// EvidenceBucket is one fixed quarter of the run window and how many of the
// finding's occurrences fell into it.
export interface EvidenceBucket {
  label: string
  count: number
}

// FindingEvidence is the optional diagnostic bundle behind a finding (mirrors
// domain.FindingEvidence): representative sessions with reproduce coordinates,
// the status-code distribution and the failure timing across the run window.
export interface FindingEvidence {
  vus?: EvidenceSession[]
  timeBuckets?: EvidenceBucket[]
  // Go marshals map[int]int with string keys, so "503": 12 — not numeric keys.
  statusCounts?: Record<string, number>
  // Recorded by the reproduce flow once it has replayed the failure and
  // classified its root cause; absent until then.
  rootCauseClass?: string
}

export interface Finding {
  runId: string
  category: string
  severity: string
  evidenceRef?: string
  description: string
  // Occurrences behind the finding (errors surfaced, violation count, streak
  // length); absent when the category carries rates instead (omitempty).
  count?: number
  // Diagnostic bundle; absent on legacy persisted findings and on the coarse
  // summary-derived ones, which retain no per-request data.
  evidence?: FindingEvidence
}

// MetricSeries is one server-side Prometheus series fetched over the run's
// window (RunSpec.metrics opt-in); points are [unix-ms, value] samples.
export interface MetricSeries {
  name: string
  points: { ts: number; v: number }[]
}

export interface Report {
  // experimentId links a run back to its stored spec (GET /experiments/{id});
  // the ?run attach flow uses it to re-hydrate the form with the run's actual
  // scenario. Optional: legacy/store-rebuilt reports may omit it.
  run: {
    id: string
    status: string
    experimentId?: string
    killReason?: string
    mode?: string
    workers?: number
  }
  stats: Stats
  findings: Finding[]
  workers?: number
  // Present only when the run opted into server-metric correlation.
  serverMetrics?: MetricSeries[]
  metricsError?: string
}

// MAX_TRACE_USERS is the run size at or below which the backend additionally emits
// per-request trace events. Above it, tracing is still honored but only as per-edge
// aggregates, so the UI uses this cap to pick the render mode (events vs heatmap),
// not whether to enable visualization.
export const MAX_TRACE_USERS = 200

// MAX_WEB_PATTERN_ROWS caps how many rows the browser generates from a pattern.
// The browser resolves a pool into inline entries (D1) — it never ships a pattern
// spec — so a huge count would materialize huge text and a huge payload. For a
// truly large pool (hundreds of thousands) the CLI scenario file's usersPattern
// generates server-side at expand time; the web generator is a convenience for
// modest pools.
export const MAX_WEB_PATTERN_ROWS = 10000

// generateCredentialRows materializes `count` credential rows from a subject/token
// template pair, substituting {{.userIndex}} for i=0..count-1, into the pasteable
// text the pool ('tokens'/'csv') or login ('csv') textarea already parses — so a
// pattern flows through the SAME browser-side parse into inline entries, with no
// new wire shape. An empty subject template yields a bare-token list ('tokens');
// otherwise a username,password CSV. It throws on a non-positive/over-cap count or
// an empty token template so the caller surfaces the reason.
export function generateCredentialRows(
  subjectTemplate: string,
  tokenTemplate: string,
  count: number,
  format: 'csv' | 'tokens',
): string {
  if (!(count > 0)) throw new Error('pattern needs a positive count')
  if (count > MAX_WEB_PATTERN_ROWS) {
    throw new Error(
      `pattern count ${count} exceeds the browser limit of ${MAX_WEB_PATTERN_ROWS} — use the CLI scenario file's usersPattern for a larger pool`,
    )
  }
  if (!tokenTemplate.trim()) throw new Error('pattern needs a token (or password) template')
  const render = (tmpl: string, i: number) => tmpl.replace(/\{\{\.userIndex\}\}/g, String(i))
  const lines: string[] = []
  if (format === 'tokens') {
    for (let i = 0; i < count; i++) lines.push(render(tokenTemplate, i))
    return lines.join('\n')
  }
  lines.push('username,password')
  for (let i = 0; i < count; i++) {
    lines.push(`${csvCell(render(subjectTemplate, i))},${csvCell(render(tokenTemplate, i))}`)
  }
  return lines.join('\n')
}

// traceable reports whether a run is small enough that the backend will additionally
// stream per-request trace events — i.e. whether the live view should animate
// individual requests ('events') or fall back to the aggregate heatmap. It mirrors
// the server's traceSmallEnough: closed runs are capped on the user count, open runs
// on the back-pressure max-concurrency (the open model ignores the user count).
export function traceable(form: ExperimentForm): boolean {
  if (form.workloadKind === 'open') {
    return form.maxConcurrency > 0 && form.maxConcurrency <= MAX_TRACE_USERS
  }
  return form.users > 0 && form.users <= MAX_TRACE_USERS
}

// LOGIN_NODE_ID / LOGIN_TEMPLATE_ID are the fixed ids the simple-login mini-form
// stamps on the single node + template it compiles. They are inert labels (the
// simulated traffic never observes the login graph), so a constant keeps the compiled
// flow stable and round-trippable.
const LOGIN_NODE_ID = 'login'
const LOGIN_TEMPLATE_ID = 't_login'

// simpleLoginFlow compiles the simple-login mini-form (a method+path and a request-body
// template) into the single-step LoginFlowSpec graph/templates the backend mints tokens
// from — so a normal developer never authors raw graph JSON. The body is sent verbatim
// (its {username}/{password} or REPLACE_ME_* markers are the operator's to fill); the
// flow carries NO explicit token capture, so tmula auto-detects the token from the
// response (E1). tokenVar/subjectVar are attached only when the operator named them.
// It throws on a missing path so the caller surfaces a clear reason instead of POSTing
// an empty login.
export function simpleLoginFlow(form: ExperimentForm): LoginFlowSpec {
  const method = form.loginUrlMethod.trim() || 'POST'
  const path = form.loginUrlPath.trim()
  if (!path) throw new Error('login needs a request path (e.g. /login)')
  const body = applyReplaceMe(form.loginBodyTemplate, form.replaceMeValues)
  const template: Record<string, unknown> = { method, path }
  if (body.trim()) template.payloadTemplate = body
  const flow: LoginFlowSpec = {
    graph: { id: LOGIN_NODE_ID, nodes: [{ id: LOGIN_NODE_ID, apiTemplateId: LOGIN_TEMPLATE_ID }], edges: [] },
    templates: { [LOGIN_TEMPLATE_ID]: template },
    start: LOGIN_NODE_ID,
  }
  const tokenVar = form.loginTokenVar.trim()
  if (tokenVar) flow.tokenVar = tokenVar
  const subjectVar = form.loginSubjectVar.trim()
  if (subjectVar) flow.subjectVar = subjectVar
  return flow
}

// simpleSignupFlow compiles the simple-signup mini-form (a signup method+path with a
// body template, plus an optional teardown method+path) into the SignupFlowSpec the
// bootstrap strategy provisions accounts from — so the operator never authors the raw
// steps array. The signup body's {{.userIndex}} renders per account (distinct signups);
// the optional teardown step's {{.subject}} is the captured account id. It mirrors
// buildAuth's capture/keepAccounts rules: an empty token capture means auto-detect, and
// teardown is attached only when keepAccounts is off and a teardown path is given. It
// throws on a missing signup path so the caller surfaces a clear reason.
export function simpleSignupFlow(form: ExperimentForm): SignupFlowSpec {
  const method = form.signupUrlMethod.trim() || 'POST'
  const path = form.signupUrlPath.trim()
  if (!path) throw new Error('signup needs a request path (e.g. /register)')
  const body = applyReplaceMe(form.signupBodyTemplate, form.replaceMeValues)
  const signupStep: SignupStepSpec = { id: 'signup', method, path }
  if (body.trim()) signupStep.body = body
  const flow: SignupFlowSpec = { steps: [signupStep], capture: {} }
  const token = form.signupCaptureToken.trim()
  if (token) flow.capture.token = token
  const subject = form.signupCaptureSubject.trim()
  if (subject) flow.capture.subject = subject
  if (!form.keepAccounts) {
    const tdPath = form.signupTeardownUrlPath.trim()
    if (tdPath) {
      const tdMethod = form.signupTeardownUrlMethod.trim() || 'DELETE'
      flow.teardown = [{ id: 'teardown', method: tdMethod, path: tdPath }]
    }
  }
  return flow
}

// REPLACE_ME_RE matches the REPLACE_ME_* placeholder literals a derived login/signup
// body carries in place of a real secret (the backend never crosses a secret — it
// substitutes a placeholder the UI surfaces for the operator to fill). The suffix is
// the field hint, e.g. REPLACE_ME_PASSWORD. We match a run of A-Z0-9_ so adjacent JSON
// punctuation (quotes, commas) is never swallowed.
const REPLACE_ME_RE = /REPLACE_ME(?:_[A-Z0-9]+)*/g

// findReplaceMePlaceholders scans one or more bodies for distinct REPLACE_ME_*
// placeholder literals, in first-seen order, de-duplicated. The console renders one
// highlighted input per placeholder so the ONLY thing left after an auto-detected
// import is the secret. A body with no placeholder yields an empty list.
export function findReplaceMePlaceholders(...bodies: (string | undefined)[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const body of bodies) {
    if (!body) continue
    for (const match of body.match(REPLACE_ME_RE) ?? []) {
      if (!seen.has(match)) {
        seen.add(match)
        out.push(match)
      }
    }
  }
  return out
}

// applyReplaceMe substitutes every REPLACE_ME_* placeholder in a body with the
// operator-supplied value for it, leaving an unfilled placeholder untouched (so the
// server still sees a literal it can reject rather than a silently blank secret). A
// body with no placeholders, or an empty value map, is returned unchanged.
export function applyReplaceMe(body: string, values: Record<string, string>): string {
  if (!body) return body
  return body.replace(REPLACE_ME_RE, (match) => {
    const v = values[match]
    return v !== undefined && v !== '' ? v : match
  })
}

// placeholderLabel turns a REPLACE_ME_* literal into a short human label for its input:
// REPLACE_ME_PASSWORD -> "Password", REPLACE_ME -> "Value". It title-cases the suffix
// words so the highlighted field reads naturally without a translation per placeholder.
export function placeholderLabel(placeholder: string): string {
  const suffix = placeholder.replace(/^REPLACE_ME_?/, '')
  if (!suffix) return 'Value'
  return suffix
    .split('_')
    .filter(Boolean)
    .map((w) => w.charAt(0) + w.slice(1).toLowerCase())
    .join(' ')
}

// authFormFromImport maps an import response's derived auth (credentialPool / loginFlow
// / suggestedSignup) onto the form's auth fields, so a swagger/HAR import POPULATES the
// Auth section automatically and leaves only the secret to fill. It returns an empty
// patch when the import carries no auth (the Auth section stays as-is, defaulting to
// None), so it is safe to spread unconditionally. Precedence, easiest-first: a derived
// loginFlow wins (login mode), else a credentialPool (its strategy picks the mode),
// else only a suggestedSignup (create-accounts mode). It NEVER auto-confirms the
// bootstrap non-prod gate. Derived flows that are too rich for the simple mini-form
// fall back to advanced (raw JSON) so nothing is lost.
export function authFormFromImport(result: ImportResult): Partial<ExperimentForm> {
  const { credentialPool, loginFlow, suggestedSignup } = result
  // A derived login flow is the headline easy path: drop it into login mode.
  if (loginFlow) {
    const patch: Partial<ExperimentForm> = { authMode: 'login' }
    const simple = loginFlowToSimpleForm(loginFlow)
    if (simple) {
      Object.assign(patch, simple, { loginMode: 'simple' })
    } else {
      patch.loginMode = 'advanced'
      patch.loginGraphJSON = JSON.stringify(loginFlow.graph, null, 2)
      patch.loginTemplatesJSON = JSON.stringify(loginFlow.templates, null, 2)
      patch.loginStart = loginFlow.start
    }
    if (loginFlow.tokenVar) patch.loginTokenVar = loginFlow.tokenVar
    if (loginFlow.subjectVar) patch.loginSubjectVar = loginFlow.subjectVar
    if (credentialPool?.loginScope === 'shared') patch.loginScope = 'shared'
    return patch
  }
  if (credentialPool) {
    if (credentialPool.strategy === 'pool' && credentialPool.entries && credentialPool.entries.length > 0) {
      // A HAR import can carry captured (secret-omitted) entries; surface them as
      // pre-filled JSONL the operator completes with the bearer token.
      return {
        authMode: 'pool',
        authPoolFormat: 'jsonl',
        authPoolText: credentialPool.entries.map((e) => JSON.stringify(e)).join('\n'),
      }
    }
    if (credentialPool.strategy === 'bootstrap-signup' && credentialPool.signupFlow) {
      return signupFormPatch(credentialPool.signupFlow, credentialPool.keepAccounts === true)
    }
  }
  // Only a suggested signup: offer "create test accounts" pre-filled, gate unconfirmed.
  if (suggestedSignup) {
    return signupFormPatch(suggestedSignup, false)
  }
  return {}
}

// --- OAuth2 guide mode (a frontend ASSEMBLY layer, not a new server strategy) --

// tokenPathFromUrl reduces a token URL (absolute or bare path) to the request
// path the login flow POSTs — the run targets the scenario's base URL, so an
// absolute URL keeps only its path (mirroring the importer's requestPathOf).
export function tokenPathFromUrl(tokenUrl: string): string {
  const raw = tokenUrl.trim()
  if (!raw) return ''
  try {
    const u = new URL(raw)
    return u.pathname && u.pathname !== '/' ? u.pathname : ''
  } catch {
    /* not absolute: treat as a bare path */
  }
  return raw.startsWith('/') ? raw : '/' + raw
}

// csvCell quotes one CSV value per the RFC-4180-lite reader the cred list is
// parsed with, so a password carrying a comma or quote round-trips exactly.
function csvCell(v: string): string {
  if (/[",\n\r]/.test(v)) return '"' + v.replace(/"/g, '""') + '"'
  return v
}

// authFormFromOAuth2Guide compiles the guide's answers onto the EXISTING form
// fields — the same pattern authFormFromImport uses. The three token grants
// become an advanced-mode login flow (a single POST to the token path, form
// Content-Type so the backend's refresh auto-derivation and urlquery rewrite
// both engage); an access-token paste becomes a plain token pool. The password
// grant's identity rides the credential list (one CSV row for a single user), so
// the body carries only {{.username}}/{{.password}} placeholders — never a
// literal secret — and the server url-encodes the row at render time.
export function authFormFromOAuth2Guide(g: OAuth2GuideForm): Partial<ExperimentForm> {
  if (g.grant === 'accessToken') {
    return { authMode: 'pool', authPoolFormat: 'tokens', authPoolText: g.accessToken.trim() }
  }

  const parts: string[] = []
  let scope: LoginScope = 'shared'
  let credText = ''
  switch (g.grant) {
    case 'password':
      // Each virtual user logs in as its own row — per-user scope.
      scope = 'per-user'
      parts.push('grant_type=password', 'username={{.username}}', 'password={{.password}}')
      credText = g.users.trim()
        ? g.users.trim()
        : `username,password\n${csvCell(g.username)},${csvCell(g.password)}`
      break
    case 'clientCredentials':
      // One machine identity shared by every user.
      parts.push('grant_type=client_credentials')
      break
    case 'refreshToken':
      // The pasted refresh token belongs to ONE session — shared across users. The
      // backend's refresh auto-derivation replaces the pasted literal with the
      // freshly captured {{.refreshToken}} on every subsequent refresh.
      parts.push('grant_type=refresh_token', `refresh_token=${encodeURIComponent(g.refreshToken.trim())}`)
      break
  }
  if (g.clientId.trim()) parts.push(`client_id=${encodeURIComponent(g.clientId.trim())}`)
  if (g.clientSecret.trim()) parts.push(`client_secret=${encodeURIComponent(g.clientSecret.trim())}`)
  if (g.scope.trim()) parts.push(`scope=${encodeURIComponent(g.scope.trim())}`)

  const template = {
    method: 'POST',
    path: tokenPathFromUrl(g.tokenUrl),
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    payloadTemplate: parts.join('&'),
  }
  return {
    authMode: 'login',
    // Advanced mode is the storage: the guide IS the friendly form, and the raw
    // JSON stays reviewable/editable under the existing Advanced panel.
    loginMode: 'advanced',
    loginGraphJSON: JSON.stringify(
      { id: LOGIN_NODE_ID, nodes: [{ id: LOGIN_NODE_ID, apiTemplateId: LOGIN_TEMPLATE_ID }], edges: [] },
      null,
      2,
    ),
    loginTemplatesJSON: JSON.stringify({ [LOGIN_TEMPLATE_ID]: template }, null, 2),
    loginStart: LOGIN_NODE_ID,
    loginScope: scope,
    loginCredText: credText,
    loginCredFormat: 'csv',
  }
}

// isOAuth2GuideGeneratedFlow reports whether a login flow's graph/templates JSON is
// (structurally) something the OAuth2 guide generated: the single "login" node bound
// to t_login whose payload is a grant_type form body. The guide uses it to decide
// whether re-compiling may overwrite the fields — a hand-authored flow never
// matches, so it is never silently clobbered. Any parse/shape surprise is a "no".
export function isOAuth2GuideGeneratedFlow(graphJSON: string, templatesJSON: string): boolean {
  try {
    const g = JSON.parse(graphJSON) as { nodes?: { id?: unknown; apiTemplateId?: unknown }[] }
    const t = JSON.parse(templatesJSON) as Record<string, { payloadTemplate?: unknown }>
    if (!g || !Array.isArray(g.nodes) || g.nodes.length !== 1) return false
    if (g.nodes[0]?.id !== LOGIN_NODE_ID || g.nodes[0]?.apiTemplateId !== LOGIN_TEMPLATE_ID) return false
    const keys = Object.keys(t ?? {})
    if (keys.length !== 1 || keys[0] !== LOGIN_TEMPLATE_ID) return false
    const payload = t[LOGIN_TEMPLATE_ID]?.payloadTemplate
    return typeof payload === 'string' && payload.includes('grant_type=')
  } catch {
    return false
  }
}

// oauth2GuideCanCompileOver reports whether the OAuth2 guide may write its compiled
// login flow (graph/templates/cred list) into the form without asking: only when
// the flow fields are still the shipped default (empty) or were themselves
// generated by the guide. A hand-authored flow returns false — the guide then
// requires an explicit Regenerate instead of clobbering on every keystroke.
export function oauth2GuideCanCompileOver(form: ExperimentForm): boolean {
  if (!form.loginGraphJSON.trim() && !form.loginTemplatesJSON.trim()) return true
  return isOAuth2GuideGeneratedFlow(form.loginGraphJSON, form.loginTemplatesJSON)
}

// loginFlowToSimpleForm tries to express a derived login flow as the simple mini-form
// (a single login node hitting one template) so the import lands on the friendly fields
// rather than raw JSON. It returns null when the flow is multi-step or otherwise too
// rich for the mini-form, so the caller falls back to advanced mode. Best-effort and
// forgiving: any shape surprise yields null rather than a half-filled form.
function loginFlowToSimpleForm(flow: LoginFlowSpec): Partial<ExperimentForm> | null {
  const graph = flow.graph as { nodes?: unknown } | null | undefined
  const nodes = graph && Array.isArray(graph.nodes) ? (graph.nodes as { id?: unknown; apiTemplateId?: unknown }[]) : null
  if (!nodes || nodes.length !== 1) return null
  const templateId = nodes[0].apiTemplateId
  if (typeof templateId !== 'string') return null
  const templates = flow.templates as Record<string, unknown> | null | undefined
  const tmpl = templates && typeof templates === 'object' ? (templates[templateId] as Record<string, unknown>) : null
  if (!tmpl || typeof tmpl !== 'object') return null
  const method = typeof tmpl.method === 'string' ? tmpl.method : ''
  const path = typeof tmpl.path === 'string' ? tmpl.path : ''
  if (!path) return null
  const body = typeof tmpl.payloadTemplate === 'string' ? tmpl.payloadTemplate : ''
  return {
    loginUrlMethod: method || 'POST',
    loginUrlPath: path,
    loginBodyTemplate: body,
  }
}

// signupFormPatch maps a SignupFlowSpec (an imported suggestedSignup or a round-tripped
// bootstrap pool) onto the create-accounts form fields. It prefers the simple mini-form
// when the flow is a single signup step (the common derived shape), and falls back to
// advanced raw-steps JSON for a richer multi-step flow. It NEVER pre-confirms the
// non-prod gate (authBootstrapConfirmed stays false) — the operator must confirm before
// the run. keepAccounts is carried through.
function signupFormPatch(flow: SignupFlowSpec, keepAccounts: boolean): Partial<ExperimentForm> {
  const patch: Partial<ExperimentForm> = {
    authMode: 'bootstrap',
    authBootstrapConfirmed: false,
    keepAccounts,
  }
  if (flow.capture?.token) patch.signupCaptureToken = flow.capture.token
  if (flow.capture?.subject) patch.signupCaptureSubject = flow.capture.subject
  const single = flow.steps.length === 1 ? flow.steps[0] : null
  const teardownStep = flow.teardown && flow.teardown.length === 1 ? flow.teardown[0] : null
  // A single signup step (with at most a single teardown step) fits the mini-form.
  if (single && (!flow.teardown || teardownStep)) {
    patch.signupMode = 'simple'
    patch.signupUrlMethod = single.method || 'POST'
    patch.signupUrlPath = single.path
    if (single.body) patch.signupBodyTemplate = single.body
    if (teardownStep) {
      patch.signupTeardownUrlMethod = teardownStep.method || 'DELETE'
      patch.signupTeardownUrlPath = teardownStep.path
    }
  } else {
    patch.signupMode = 'advanced'
    patch.signupStepsJSON = JSON.stringify(flow.steps, null, 2)
    if (flow.start) patch.signupStart = flow.start
    if (flow.teardown && flow.teardown.length > 0) {
      patch.signupTeardownJSON = JSON.stringify(flow.teardown, null, 2)
    }
    if (flow.teardownStart) patch.signupTeardownStart = flow.teardownStart
  }
  return patch
}

// AuthBuild is the credential material buildAuth derives from the form: the wire
// authStrategy the experiment params advertise, the credentialPool the server reads,
// and (for the login strategy) the standalone loginFlow it mints tokens from. It is
// null when the form configures no auth (authMode 'none'), so the run stays anonymous
// and byte-identical to before.
export interface AuthBuild {
  authStrategy: 'pool' | 'login' | 'bootstrap-signup' | 'mint' | 'exec'
  credentialPool: CredentialPool
  loginFlow?: LoginFlowSpec
}

// secondsToGoDuration formats a whole-second count as the Go duration string the
// backend parses (time.ParseDuration), e.g. 3600 -> "1h0m0s", 90 -> "1m30s". The
// mint TTL is authored as a friendly seconds number in the form but crosses the wire
// as a duration so the backend reads it with the same parser the CLI uses.
export function secondsToGoDuration(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  let out = ''
  if (h > 0) out += `${h}h`
  if (h > 0 || m > 0) out += `${m}m`
  out += `${sec}s`
  return out
}

// goDurationToSeconds parses a Go duration string (the form the backend marshals, e.g.
// "1h0m0s", "30m", "5s", "1h30m") back to whole seconds, for the attach round-trip. It
// is forgiving: an unparseable value yields 0 so the caller keeps the form's default.
export function goDurationToSeconds(dur: string): number {
  const trimmed = dur.trim()
  if (!trimmed) return 0
  let total = 0
  let matched = false
  const re = /(\d+(?:\.\d+)?)(h|m|s|ms|us|µs|ns)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(trimmed)) !== null) {
    matched = true
    const n = Number(m[1])
    switch (m[2]) {
      case 'h':
        total += n * 3600
        break
      case 'm':
        total += n * 60
        break
      case 's':
        total += n
        break
      // Sub-second units round into the seconds total; mint TTLs are whole seconds.
      case 'ms':
        total += n / 1000
        break
      default:
        break
    }
  }
  return matched ? Math.round(total) : 0
}

// buildMintSpec turns the form's mint fields into the wire MintSpec, throwing a clear
// error on invalid input (no key reference, a non-positive ttl, or malformed claims
// JSON) so buildAuth fails fast instead of POSTing a spec the backend will 400. The
// signing key is sent as a REFERENCE only — mintKeyEnv or mintKeyFile — never a key
// body, so the secret never leaves the operator's environment. An asymmetric alg omits
// the HS-only secretEncoding; an empty subject / empty claims are omitted so a minimal
// mint stays minimal.
export function buildMintSpec(form: ExperimentForm): MintSpec {
  const env = form.mintKeyEnv.trim()
  const file = form.mintKeyFile.trim()
  if (!env && !file) {
    throw new Error('mint needs a signing-key reference — set an environment variable or a file path')
  }
  if (env && file) {
    throw new Error('mint takes either a key environment variable or a key file, not both')
  }
  if (!(form.mintTtlSeconds > 0)) {
    throw new Error('mint needs a token lifetime (ttl) greater than zero seconds')
  }
  const mint: MintSpec = {
    alg: form.mintAlg,
    key: env ? { env } : { file },
    ttl: secondsToGoDuration(form.mintTtlSeconds),
  }
  // secretEncoding is HS-only; an asymmetric alg reads a PEM and ignores it.
  if (form.mintAlg === 'HS256') mint.secretEncoding = form.mintSecretEncoding
  const subject = form.mintSubject.trim()
  if (subject) mint.subject = subject
  const claimsText = form.mintClaimsJSON.trim()
  if (claimsText) {
    let parsed: unknown
    try {
      parsed = JSON.parse(claimsText)
    } catch {
      throw new Error('mint claims must be a JSON object of string values')
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error('mint claims must be a JSON object (e.g. {"role":"tester"})')
    }
    const claims: Record<string, string> = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      claims[k] = typeof v === 'string' ? v : String(v)
    }
    if (Object.keys(claims).length > 0) mint.claims = claims
  }
  return mint
}

// CRED_POOL_ID is the id the console stamps on every credential pool it builds. It
// mirrors the CLI's "cli-pool" so a console-built pool reads the same in logs; it is
// non-secret and otherwise inert.
const CRED_POOL_ID = 'web-pool'

// buildAuth turns the form's auth fields into the wire credential material, or returns
// null when auth is off (authMode 'none') so the None path attaches nothing. It throws
// a clear error on invalid input (malformed credential text, login/signup JSON that is
// not an array/object, a missing token capture, an unconfirmed bootstrap) so the
// caller surfaces the reason instead of POSTing a spec the server will 400.
//
// It NEVER produces a file/env source — a 'pool' run resolves its pasted text and
// uploaded file into inline entries in the browser (parseCredentials), because the
// server rejects an unresolved source arriving over the wire (D1).
// buildExecSpec turns the form's exec fields into the wire ExecSpec, throwing a clear
// error on invalid input (the opt-in not confirmed, an empty command, a non-positive
// timeout, or a malformed env line) so buildAuth fails fast instead of POSTing a spec the
// backend will reject. exec runs an arbitrary LOCAL command, so the operator MUST confirm
// the opt-in (execConfirmed); the run-start server gate enforces it again. The command is
// argv (one element per line, argv[0] is the program — never a shell string); operator
// secrets belong in env (KEY=VALUE lines), not argv. An empty env is omitted so a minimal
// exec stays minimal. maxOutputBytes is left to the backend default.
export function buildExecSpec(form: ExperimentForm): ExecSpec {
  if (!form.execConfirmed) {
    throw new Error('exec runs a local command per virtual user — confirm you allow it before running')
  }
  const command = form.execCommandText
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.length > 0)
  if (command.length === 0) {
    throw new Error('exec needs a command (argv) — one element per line, the first is the program')
  }
  if (!(form.execTimeoutSeconds > 0)) {
    throw new Error('exec needs a per-invocation timeout greater than zero seconds')
  }
  const exec: ExecSpec = {
    command,
    timeout: secondsToGoDuration(form.execTimeoutSeconds),
  }
  const env: Record<string, string> = {}
  for (const raw of form.execEnvText.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const eq = line.indexOf('=')
    if (eq <= 0) {
      throw new Error(`exec env must be KEY=VALUE lines (got ${JSON.stringify(line)})`)
    }
    env[line.slice(0, eq).trim()] = line.slice(eq + 1)
  }
  if (Object.keys(env).length > 0) exec.env = env
  return exec
}

export function buildAuth(form: ExperimentForm): AuthBuild | null {
  switch (form.authMode) {
    case 'none':
      return null
    case 'pool': {
      const entries = parseCredentials(form.authPoolFormat, form.authPoolText)
      return {
        authStrategy: 'pool',
        credentialPool: { id: CRED_POOL_ID, strategy: 'pool', entries },
      }
    }
    case 'login': {
      // The simple mini-form is the common path: compile its method+path+body into the
      // single-step login flow (auto-detect token). Advanced authors the raw JSON.
      let loginFlow: LoginFlowSpec
      if (form.loginMode === 'simple') {
        loginFlow = simpleLoginFlow(form)
      } else {
        const graph = JSON.parse(form.loginGraphJSON)
        const templates = JSON.parse(form.loginTemplatesJSON)
        // An empty token capture is allowed: tmula auto-detects the token from the
        // login response, so the field is omitted rather than rejected.
        loginFlow = { graph, templates, start: form.loginStart }
        const tokenVar = form.loginTokenVar.trim()
        if (tokenVar) loginFlow.tokenVar = tokenVar
        const subjectVar = form.loginSubjectVar.trim()
        if (subjectVar) loginFlow.subjectVar = subjectVar
        // Explicit refresh-grant OVERRIDE (advanced only): when a body is given it WINS
        // over the backend's auto-derivation, so a JSON-body login still refreshes via a
        // real grant_type=refresh_token exchange. The request line is optional (defaults
        // to the login token endpoint). Both blank leaves the override off — the backend
        // auto-derives (form login) or re-logins, unchanged. The simple mini-form never
        // sets these, so it stays one-field.
        const refreshBody = form.loginRefreshBody.trim()
        if (refreshBody) loginFlow.refreshBody = refreshBody
        const refreshRequest = form.loginRefreshRequest.trim()
        if (refreshRequest) loginFlow.refreshRequest = refreshRequest
      }
      // The pool references the flow by id ("login") and carries the scope; the flow
      // itself rides at the top level. Send the scope only when it differs from the
      // per-user default so a default-scope pool stays minimal.
      const credentialPool: CredentialPool = {
        id: CRED_POOL_ID,
        strategy: 'login',
        loginFlowId: 'login',
      }
      if (form.loginScope === 'shared') credentialPool.loginScope = 'shared'
      // "Log in multiple users": when the operator supplied a credential list, resolve it
      // into entries of { subject: username, token: password } so each virtual user logs
      // in as a different account (the backend uses entries[i % N], exposing the row as
      // {{.username}}/{{.password}}). A blank list leaves entries off — the single-identity
      // login, byte-identical to before this feature.
      if (form.loginCredText.trim()) {
        credentialPool.entries = parseLoginCredentials(form.loginCredFormat, form.loginCredText)
      }
      return { authStrategy: 'login', credentialPool, loginFlow }
    }
    case 'bootstrap': {
      if (!form.authBootstrapConfirmed) {
        throw new Error(
          'confirm this targets a non-production system before running bootstrap (it creates/deletes real accounts)',
        )
      }
      // The simple mini-form compiles a method+path+body into the single-step signup
      // flow (with optional teardown). Advanced authors the raw steps array.
      let signupFlow: SignupFlowSpec
      if (form.signupMode === 'simple') {
        signupFlow = simpleSignupFlow(form)
      } else {
        const steps = parseSignupSteps(form.signupStepsJSON, 'signup')
        // An empty token capture is allowed: tmula auto-detects the token from the
        // signup response, so the field is omitted rather than rejected.
        signupFlow = { steps, capture: {} }
        const token = form.signupCaptureToken.trim()
        if (token) signupFlow.capture.token = token
        const start = form.signupStart.trim()
        if (start) signupFlow.start = start
        const subject = form.signupCaptureSubject.trim()
        if (subject) signupFlow.capture.subject = subject
        if (!form.keepAccounts && form.signupTeardownJSON.trim()) {
          const teardown = parseSignupSteps(form.signupTeardownJSON, 'teardown')
          if (teardown.length > 0) {
            signupFlow.teardown = teardown
            const tdStart = form.signupTeardownStart.trim()
            if (tdStart) signupFlow.teardownStart = tdStart
          }
        }
      }
      const credentialPool: CredentialPool = {
        id: CRED_POOL_ID,
        strategy: 'bootstrap-signup',
        signupFlow,
        keepAccounts: form.keepAccounts,
      }
      return { authStrategy: 'bootstrap-signup', credentialPool }
    }
    case 'mint': {
      // Self-issue a JWT per virtual user by signing locally with a key the operator
      // holds. buildMintSpec throws on a missing key reference, a non-positive ttl, or
      // malformed claims, so an invalid mint fails fast. No loginFlow rides along —
      // mint needs no token acquisition.
      const mint = buildMintSpec(form)
      return {
        authStrategy: 'mint',
        credentialPool: { id: CRED_POOL_ID, strategy: 'mint', mint },
      }
    }
    case 'exec': {
      // Run an operator-supplied command per virtual user; its stdout is the token. exec
      // runs an arbitrary LOCAL command, so the operator must confirm the opt-in here
      // (the run-start server gate enforces it again). buildExecSpec throws on an
      // unconfirmed opt-in, an empty command, a malformed env line, or a non-positive
      // timeout, so an invalid exec fails fast. No loginFlow rides along.
      const exec = buildExecSpec(form)
      return {
        authStrategy: 'exec',
        credentialPool: { id: CRED_POOL_ID, strategy: 'exec', exec },
      }
    }
    default:
      return null
  }
}

// parseSignupSteps parses a JSON array of SignupStep objects (the bootstrap signup /
// teardown journey), validating the minimum shape each step needs (id, method, path).
// kind labels the error ("signup" / "teardown"). It throws on non-array input or a
// malformed step so the caller can surface a clear message rather than 400-ing.
export function parseSignupSteps(json: string, kind: 'signup' | 'teardown'): SignupStepSpec[] {
  const parsed = JSON.parse(json)
  if (!Array.isArray(parsed)) throw new Error(`${kind} steps must be a JSON array`)
  return parsed.map((step, i) => {
    if (typeof step !== 'object' || step === null) {
      throw new Error(`${kind} step ${i} must be an object with id, method and path`)
    }
    const { id, method, path } = step as { id?: unknown; method?: unknown; path?: unknown }
    if (typeof id !== 'string' || !id.trim()) throw new Error(`${kind} step ${i} needs an id`)
    if (typeof method !== 'string' || !method.trim()) throw new Error(`${kind} step "${id}" needs a method`)
    if (typeof path !== 'string' || !path.trim()) throw new Error(`${kind} step "${id}" needs a path`)
    return step as SignupStepSpec
  })
}

// buildRunSpec turns the form into the RunSpec the API expects. It throws on
// invalid JSON so the caller can surface a clear error.
export function buildRunSpec(form: ExperimentForm): RunSpec {
  const graph = JSON.parse(form.graphJSON)
  const templates = JSON.parse(form.templatesJSON)
  const allowlist = parseAllowlist(form.allowlist)
  const workers = form.workers
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  // Neither model ships one object per virtual user. The open model generates its
  // own sessions from the arrival rate and reads only a single template user; the
  // closed model now sends an empty pool plus `userCount` and lets the server
  // synthesize u0..uN-1 at run time. Materializing one object per user would be
  // megabytes — over the server's request size limit ("request body too large") —
  // at large counts (~270k+), which was the closed-run bug this fixes.
  const users = form.workloadKind === 'open' ? [{ id: 'u0' }] : []

  // Size the safety cap to the configured load so the guard protects the target
  // (host allowlist + a ceiling) without silently throttling what the operator
  // asked for — a hardcoded 1000 rps would cap a 12k arrival rate. Both fields
  // must be > 0 (the guard rejects 0); an "uncapped" (0) max-concurrency maps to a
  // generous ceiling derived from the arrival rate.
  // Math.ceil every term: the form fields can be fractional, and the server decodes
  // these into ints (a non-integer would be rejected with a 400).
  const rateCap =
    form.workloadKind === 'open'
      ? {
          maxRps: Math.max(1000, Math.ceil(form.arrivalRate * 1.5)),
          maxConcurrency:
            form.maxConcurrency > 0
              ? Math.max(Math.ceil(form.maxConcurrency), 200)
              : Math.max(2000, Math.ceil(form.arrivalRate * 2)),
        }
      : { maxRps: Math.max(1000, Math.ceil(form.users)), maxConcurrency: Math.max(200, Math.ceil(form.users)) }

  // The form takes the deviation rate as a friendly percent (0–100); the server
  // contract is a 0..1 fraction it hard-rejects outside [0,1], so clamp here so a
  // hand-typed out-of-range value degrades gracefully instead of 400-ing the run.
  const deviationRate = clamp01(form.deviationPct / 100)

  // Resolve auth first so a malformed credential pool / login / signup throws before
  // we build the spec (same fail-fast contract as the graph/segments JSON above). A
  // null build means the None path: authStrategy stays 'pool' and no credentialPool is
  // attached, keeping an anonymous run byte-identical to before this feature.
  const auth = buildAuth(form)

  const spec: RunSpec = {
    experiment: {
      name: 'ui-run',
      targetEnvId: 'env',
      scenarioGraphId: 'graph',
      params: {
        virtualUserCount: form.users,
        deviationRate,
        authStrategy: auth ? auth.authStrategy : 'pool',
      },
    },
    targetEnv: {
      baseUrl: form.baseUrl,
      allowlist,
      rateCap,
      envClass: 'dev',
    },
    graph,
    templates,
    start: form.start,
    maxSteps: form.maxSteps,
    users,
    seed: 1,
  }
  // Closed runs send the pool size as a count and let the server synthesize
  // u0..uN-1; the open model generates its own sessions, so the count is
  // meaningless there and is left off to keep the open spec clean.
  if (form.workloadKind !== 'open') spec.userCount = form.users
  // Only attach workers when the operator named at least one address; an empty
  // list would otherwise signal a distributed run with no workers. Worker-side
  // aggregation only makes sense for a distributed run, so gate it on workers.
  if (workers.length > 0) {
    spec.workers = workers
    if (form.aggregateWorkers) spec.aggregateWorkers = true
  }
  // Attach the open workload model when selected; otherwise the run uses the
  // default closed (fixed-user) model.
  if (form.workloadKind === 'open') {
    spec.workload = {
      kind: 'open',
      arrival: { shape: 'constant', startRate: form.arrivalRate, peakRate: form.arrivalRate },
      durationSeconds: form.durationSeconds,
      maxConcurrency: form.maxConcurrency,
      thinkTime: { minMs: form.thinkMinMs, maxMs: form.thinkMaxMs },
    }
    // Personas apply only to the open model; attach them only when provided.
    const segments = parseSegments(form.segmentsJSON)
    if (segments.length > 0) spec.segments = segments
  }
  // Opt into visualization whenever requested; the backend now honors it at any
  // scale (small runs additionally get per-request events, all opted-in runs get
  // per-edge aggregates). The render mode is chosen client-side via traceable().
  if (form.traceEnabled) spec.trace = true
  // Attach the credential pool (and, for a login pool, the standalone login flow) only
  // when auth is configured; the None path leaves both off so an anonymous run is
  // byte-identical to before.
  if (auth) {
    spec.credentialPool = auth.credentialPool
    if (auth.loginFlow) spec.loginFlow = auth.loginFlow
  }
  return spec
}

// formFromRunSpec is buildRunSpec's inverse for the ?run attach flow: it maps a
// server-stored RunSpec (GET /experiments/{id}) back onto the console form, so
// attaching to a server-started run (e.g. one `tmula demo` opened) converges on
// the same state the form-submit path produces — the live flow map draws the
// run's actual scenario, the target fields match the run's spec, and traceable()
// picks the same render mode the run streams. It is deliberately forgiving: it
// returns null when the spec carries no usable scenario graph (the caller keeps
// the form defaults) and omits any field that does not match the expected shape
// rather than clobbering the form with garbage.
export function formFromRunSpec(spec: unknown): Partial<ExperimentForm> | null {
  if (typeof spec !== 'object' || spec === null) return null
  const s = spec as Record<string, unknown>
  const graph = s.graph as { nodes?: unknown; edges?: unknown } | null | undefined
  if (!graph || !Array.isArray(graph.nodes) || !Array.isArray(graph.edges)) return null

  const patch: Partial<ExperimentForm> = {
    graphJSON: JSON.stringify(graph, null, 2),
    // Trace is an explicit opt-in on the spec; absent means the run streams no
    // visualization, so the attached view should not pretend otherwise.
    traceEnabled: s.trace === true,
  }
  if (typeof s.templates === 'object' && s.templates !== null) {
    patch.templatesJSON = JSON.stringify(s.templates, null, 2)
  }
  if (typeof s.start === 'string' && s.start) patch.start = s.start
  if (typeof s.maxSteps === 'number' && s.maxSteps > 0) patch.maxSteps = s.maxSteps

  const env = s.targetEnv as { baseUrl?: unknown; allowlist?: unknown } | null | undefined
  if (env && typeof env.baseUrl === 'string' && env.baseUrl) patch.baseUrl = env.baseUrl
  if (env && Array.isArray(env.allowlist) && env.allowlist.every((h) => typeof h === 'string')) {
    patch.allowlist = (env.allowlist as string[]).join(', ')
  }

  // The wire carries the deviation as a 0..1 fraction; the form speaks percent.
  const params = (s.experiment as { params?: { deviationRate?: unknown } } | null | undefined)
    ?.params
  if (params && typeof params.deviationRate === 'number') {
    patch.deviationPct = Math.min(100, Math.max(0, Math.round(params.deviationRate * 100)))
  }

  if (Array.isArray(s.workers) && s.workers.length > 0 && s.workers.every((w) => typeof w === 'string')) {
    patch.workers = (s.workers as string[]).join(', ')
  }
  if (typeof s.aggregateWorkers === 'boolean') patch.aggregateWorkers = s.aggregateWorkers

  const wl = s.workload as
    | {
        kind?: unknown
        arrival?: { startRate?: unknown; peakRate?: unknown }
        durationSeconds?: unknown
        maxConcurrency?: unknown
        thinkTime?: { minMs?: unknown; maxMs?: unknown }
      }
    | null
    | undefined
  if (wl && wl.kind === 'open') {
    patch.workloadKind = 'open'
    // The form models a constant arrival; the peak is the honest single number
    // for a ramp (startRate is the fallback for an open block without a peak).
    const rate = typeof wl.arrival?.peakRate === 'number' ? wl.arrival.peakRate : wl.arrival?.startRate
    if (typeof rate === 'number' && rate > 0) patch.arrivalRate = rate
    if (typeof wl.durationSeconds === 'number' && wl.durationSeconds > 0) {
      patch.durationSeconds = wl.durationSeconds
    }
    if (typeof wl.maxConcurrency === 'number' && wl.maxConcurrency >= 0) {
      patch.maxConcurrency = wl.maxConcurrency
    }
    if (typeof wl.thinkTime?.minMs === 'number') patch.thinkMinMs = wl.thinkTime.minMs
    if (typeof wl.thinkTime?.maxMs === 'number') patch.thinkMaxMs = wl.thinkTime.maxMs
    if (Array.isArray(s.segments) && s.segments.length > 0) {
      patch.segmentsJSON = JSON.stringify(s.segments, null, 2)
    }
  } else {
    patch.workloadKind = 'closed'
    // Closed pool size: the compact userCount wins; an explicit user list is the
    // legacy form. Neither present leaves the form's pool size untouched.
    const count =
      typeof s.userCount === 'number' && s.userCount > 0
        ? s.userCount
        : Array.isArray(s.users)
          ? s.users.length
          : 0
    if (count > 0) patch.users = count
  }

  // Auth (P5): map back what is NON-SECRET. The server masks the credential secret
  // (Credential.Secret is json:"-"), so a 'pool' run's entries can never be restored —
  // attach mode selects the pool mode but leaves the pasted text empty, and the
  // operator re-supplies the credentials. The login/bootstrap flow shapes carry no
  // secret (tokens are minted at run time), so their structure round-trips; only the
  // live-minted secrets are absent.
  Object.assign(patch, authFormFromSpec(s))

  return patch
}

// authFormFromSpec maps a stored spec's credentialPool (and top-level loginFlow) back
// onto the form's auth fields, restoring only non-secret structure. A spec with no
// credentialPool yields the None mode. It is deliberately forgiving (mirrors
// formFromRunSpec): an unrecognized strategy or a malformed flow is skipped rather
// than clobbering the form. Pool entries are never restored (the secret is masked), so
// 'pool' mode comes back with empty text the operator re-fills.
export function authFormFromSpec(s: Record<string, unknown>): Partial<ExperimentForm> {
  const pool = s.credentialPool as { strategy?: unknown } | null | undefined
  if (!pool || typeof pool !== 'object') return { authMode: 'none' }
  const strategy = pool.strategy
  if (strategy === 'pool') {
    // The secret never crosses the wire, so there is nothing to restore beyond the
    // mode; the operator re-pastes the credentials.
    return { authMode: 'pool' }
  }
  if (strategy === 'login') {
    // A round-tripped spec restores the raw flow JSON, so it lands on advanced mode
    // (the simple mini-form cannot always re-express an arbitrary minted flow).
    const patch: Partial<ExperimentForm> = { authMode: 'login', loginMode: 'advanced' }
    const scope = (pool as { loginScope?: unknown }).loginScope
    patch.loginScope = scope === 'shared' ? 'shared' : 'per-user'
    const flow = s.loginFlow as
      | { graph?: unknown; templates?: unknown; start?: unknown; tokenVar?: unknown; subjectVar?: unknown }
      | null
      | undefined
    if (flow && typeof flow === 'object') {
      if (flow.graph && typeof flow.graph === 'object') patch.loginGraphJSON = JSON.stringify(flow.graph, null, 2)
      if (flow.templates && typeof flow.templates === 'object') {
        patch.loginTemplatesJSON = JSON.stringify(flow.templates, null, 2)
      }
      if (typeof flow.start === 'string') patch.loginStart = flow.start
      if (typeof flow.tokenVar === 'string') patch.loginTokenVar = flow.tokenVar
      if (typeof flow.subjectVar === 'string') patch.loginSubjectVar = flow.subjectVar
    }
    return patch
  }
  if (strategy === 'bootstrap-signup') {
    // A bootstrap pool round-trips its flow shape, but it provisions real accounts —
    // so attach mode does NOT pre-confirm the non-prod safety gate. The operator must
    // re-confirm before submitting.
    const patch: Partial<ExperimentForm> = {
      authMode: 'bootstrap',
      authBootstrapConfirmed: false,
      signupMode: 'advanced',
    }
    const flow = s.credentialPool as { signupFlow?: unknown; keepAccounts?: unknown }
    const sf = flow.signupFlow as
      | { steps?: unknown; start?: unknown; capture?: { token?: unknown; subject?: unknown }; teardown?: unknown; teardownStart?: unknown }
      | null
      | undefined
    if (sf && typeof sf === 'object') {
      if (Array.isArray(sf.steps)) patch.signupStepsJSON = JSON.stringify(sf.steps, null, 2)
      if (typeof sf.start === 'string') patch.signupStart = sf.start
      if (sf.capture && typeof sf.capture.token === 'string') patch.signupCaptureToken = sf.capture.token
      if (sf.capture && typeof sf.capture.subject === 'string') patch.signupCaptureSubject = sf.capture.subject
      if (Array.isArray(sf.teardown) && sf.teardown.length > 0) {
        patch.signupTeardownJSON = JSON.stringify(sf.teardown, null, 2)
      }
      if (typeof sf.teardownStart === 'string') patch.signupTeardownStart = sf.teardownStart
    }
    if (typeof flow.keepAccounts === 'boolean') patch.keepAccounts = flow.keepAccounts
    return patch
  }
  if (strategy === 'mint') {
    // A mint pool carries NO secret (the key is a reference), so its whole config
    // round-trips. ttl crosses the wire as a Go duration string; parse it back to the
    // form's seconds. Unrecognized fields are skipped (forgiving, like the rest).
    const patch: Partial<ExperimentForm> = { authMode: 'mint' }
    const mint = (s.credentialPool as { mint?: unknown }).mint as
      | { alg?: unknown; secretEncoding?: unknown; key?: { env?: unknown; file?: unknown }; subject?: unknown; claims?: unknown; ttl?: unknown }
      | null
      | undefined
    if (mint && typeof mint === 'object') {
      if (mint.alg === 'HS256' || mint.alg === 'RS256' || mint.alg === 'ES256') patch.mintAlg = mint.alg
      if (mint.secretEncoding === 'raw' || mint.secretEncoding === 'base64' || mint.secretEncoding === 'base64url') {
        patch.mintSecretEncoding = mint.secretEncoding
      }
      if (mint.key && typeof mint.key === 'object') {
        if (typeof mint.key.env === 'string') patch.mintKeyEnv = mint.key.env
        if (typeof mint.key.file === 'string') patch.mintKeyFile = mint.key.file
      }
      if (typeof mint.subject === 'string') patch.mintSubject = mint.subject
      if (mint.claims && typeof mint.claims === 'object') patch.mintClaimsJSON = JSON.stringify(mint.claims, null, 2)
      if (typeof mint.ttl === 'string') {
        const secs = goDurationToSeconds(mint.ttl)
        if (secs > 0) patch.mintTtlSeconds = secs
      }
    }
    return patch
  }
  if (strategy === 'exec') {
    // An exec pool carries NO secret inline (operator secrets live in the command's env,
    // resolved on the server), so its config round-trips — EXCEPT the opt-in: execConfirmed
    // is never restored as true, so the operator must re-acknowledge that exec runs a local
    // command (like bootstrap's confirm). The command joins back to one-per-line; env to
    // KEY=VALUE lines; the Go duration string parses back to seconds.
    const patch: Partial<ExperimentForm> = { authMode: 'exec', execConfirmed: false }
    const exec = (s.credentialPool as { exec?: unknown }).exec as
      | { command?: unknown; env?: unknown; timeout?: unknown }
      | null
      | undefined
    if (exec && typeof exec === 'object') {
      if (Array.isArray(exec.command)) {
        patch.execCommandText = exec.command.filter((c): c is string => typeof c === 'string').join('\n')
      }
      if (exec.env && typeof exec.env === 'object' && !Array.isArray(exec.env)) {
        patch.execEnvText = Object.entries(exec.env as Record<string, unknown>)
          .filter(([, v]) => typeof v === 'string')
          .map(([k, v]) => `${k}=${v as string}`)
          .join('\n')
      }
      if (typeof exec.timeout === 'string') {
        const secs = goDurationToSeconds(exec.timeout)
        if (secs > 0) patch.execTimeoutSeconds = secs
      }
    }
    return patch
  }
  // Unknown strategy: leave the form's auth untouched rather than guessing.
  return {}
}

export interface CapacityPlan {
  arrivalPerSec: number
  peakConcurrency: number
  workersNeeded: number
}

// getCapacity asks the server to size a target population via Little's Law.
export async function getCapacity(
  totalUsers: number,
  windowSeconds: number,
  avgSessionSeconds: number,
  perWorkerCap = 2000,
): Promise<CapacityPlan> {
  const q = new URLSearchParams({
    totalUsers: String(totalUsers),
    windowSeconds: String(windowSeconds),
    avgSessionSeconds: String(avgSessionSeconds),
    perWorkerCap: String(perWorkerCap),
  })
  const res = await fetch(`${API}/capacity?${q}`)
  if (!res.ok) throw new Error(`capacity failed: ${res.status}`)
  return (await res.json()) as CapacityPlan
}

export interface StreamFrame {
  status?: string
  reason?: string
  stats?: Stats
}

// runFailureHintKey maps a run's kill/fail reason onto a friendly i18n key for the
// known prewarm failures — the cases where the run died BEFORE any traffic flowed
// because auth could not be established. The raw reason is still shown beneath the
// friendly line; anything unrecognized returns null and callers show only the raw
// reason.
export function runFailureHintKey(reason: string | undefined): string | null {
  if (!reason) return null
  if (reason.startsWith('api: prewarm login token')) return 'run.failLoginPrewarm'
  if (reason.startsWith('api: prewarm bootstrap accounts')) return 'run.failBootstrapPrewarm'
  return null
}

// parseSSEData parses a single SSE "data:" line, returning null for anything
// else (comments, blank lines, malformed payloads).
export function parseSSEData(line: string): StreamFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as StreamFrame
  } catch {
    return null
  }
}

// TraceEvent is one step a virtual user took: a request from `from` to `to`. The
// entry request has from === "". `status` is 0 on a transport error, and `ok` is
// true when the request completed with status < 400.
export interface TraceEvent {
  seq: number
  userId: string
  from: string
  to: string
  status: number
  latencyMs: number
  ok: boolean
}

// TraceFrame is one SSE frame of the live-trace stream: zero or more events in
// ascending seq order. The final frame sets done === true, then the server closes.
export interface TraceFrame {
  events: TraceEvent[]
  done?: boolean
}

// parseTraceFrame parses a single trace SSE "data:" line, mirroring parseSSEData:
// it returns null for comments, blank lines, and malformed payloads.
export function parseTraceFrame(line: string): TraceFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as TraceFrame
  } catch {
    return null
  }
}

// HeatEdge is one edge's cumulative traffic in the aggregate heatmap stream: total
// `requests` and `errors` (int64 counts) seen on the edge `from` -> `to`. `from` is
// "" for the entry into a node (a user starting there), matching the trace contract.
export interface HeatEdge {
  from: string
  to: string
  requests: number
  errors: number
}

// HeatFrame is one SSE frame of the per-edge heatmap stream: every edge that has
// seen traffic so far, with cumulative counts. The final frame sets done === true,
// then the server closes. Unlike the trace stream this scales to any run size
// because the payload is bounded by the edge count, not the request count.
export interface HeatFrame {
  edges: HeatEdge[]
  done?: boolean
}

// parseHeatFrame parses a single heatmap SSE "data:" line, mirroring parseTraceFrame:
// it returns null for comments, blank lines, and malformed payloads.
export function parseHeatFrame(line: string): HeatFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as HeatFrame
  } catch {
    return null
  }
}

// --- Latency heatmap stream (the canonical load-test heatmap) -------------------
// A LatencyFrame is a 2-D histogram: rows are latency bands (LOW -> HIGH), columns
// are time buckets since the run started, and each cell holds the request count in
// that band × bucket. It streams over SSE while the run is active and the final
// frame sets done === true, then the server closes — same lifecycle as the per-edge
// heatmap, but the payload encodes density over time rather than over the graph.

// LatencyRow describes one latency band on the Y axis. hiMs === 0 marks the
// unbounded top bucket (everything at or above loMs, e.g. "5s+").
export interface LatencyRow {
  loMs: number
  hiMs: number
  label: string
}

export interface LatencyFrame {
  binWidthMs: number // wall-clock width of one time column (ms)
  rows: LatencyRow[] // latency bands, ordered LOW -> HIGH
  cells: number[][] // cells[rowIndex][colIndex] = request count
  maxCount: number // the densest cell's count, for color scaling
  done?: boolean
}

// parseLatencyFrame parses a single latency-heatmap SSE "data:" line, mirroring
// parseHeatFrame exactly: it returns null for comments, blank lines, and malformed
// payloads, keeping the stream open on a bad frame.
export function parseLatencyFrame(line: string): LatencyFrame | null {
  if (!line.startsWith('data:')) return null
  const payload = line.slice('data:'.length).trim()
  if (!payload) return null
  try {
    return JSON.parse(payload) as LatencyFrame
  } catch {
    return null
  }
}

// LAT_CELL_EMPTY / LAT_CELL_HOT are the endpoints of the latency-heatmap density
// ramp: a near-blank tint of the accent for low/zero density, the strong saturated
// accent at the peak. Kept as "#rrggbb" so latencyCellColor can reuse lerpColor.
export const LAT_CELL_EMPTY = '#eef2ff' // indigo-50: almost blank
export const LAT_CELL_HOT = '#4338ca' // indigo-700: dense

// latencyCellColor maps a cell's request count onto a sequential color ramp from a
// very-light tint (low/zero density) to a strong, saturated accent (max density).
// A zero count is nearly blank so empty cells recede; the ramp is interpolated in
// sRGB via lerpColor, so it stays dependency-free. The fraction uses a sqrt so the
// low end of a wide count range still separates visibly (a few requests already
// read as more than nothing).
export function latencyCellColor(count: number, maxCount: number): string {
  if (count <= 0 || maxCount <= 0) return LAT_CELL_EMPTY
  const frac = Math.sqrt(clamp01(count / maxCount))
  return lerpColor(LAT_CELL_EMPTY, LAT_CELL_HOT, frac)
}

// --- Heatmap visual encoding (pure, unit-tested) -------------------------------
// These map an edge's aggregate counts onto the stroke width and color the SVG
// draws. They live here (next to layoutGraph) so they can be tested without the
// React component, matching the project's "pure helpers in api.ts" convention.

const clamp01 = (n: number) => (n < 0 ? 0 : n > 1 ? 1 : n)

// HEAT_MIN_W / HEAT_MAX_W bound the edge stroke width (SVG units); the busiest
// edge gets HEAT_MAX_W, an idle/zero edge HEAT_MIN_W.
export const HEAT_MIN_W = 1.5
export const HEAT_MAX_W = 14

// heatWidth maps a request count onto a stroke width using a logarithmic scale so
// the busiest edge is the thickest and a 12-request edge and a 12-million-request
// edge stay legible in the same frame: width = MIN + (MAX-MIN)·ln(n+1)/ln(max+1).
// It returns the floor when there is no traffic (n or max <= 0).
export function heatWidth(requests: number, maxRequests: number): number {
  if (requests <= 0 || maxRequests <= 0) return HEAT_MIN_W
  const frac = Math.log(requests + 1) / Math.log(maxRequests + 1)
  return HEAT_MIN_W + (HEAT_MAX_W - HEAT_MIN_W) * clamp01(frac)
}

// --- Terminal nodes & edge classification (pure, unit-tested) -------------------
// The flow map reads as a forward funnel: requests enter on the left and fan
// toward an outcome on the right. To keep that funnel legible at high volume the
// view sorts edges into classes and treats the graph's endpoints specially. These
// helpers encode that grammar without React so they can be tested in isolation.

// terminalNodeIds is the set of node ids that are journey endpoints: a node with
// no apiTemplateId fires no request, so reaching it means the user *finished*
// (done) or *left* (exit) rather than made another call. The backend now emits a
// "terminal" traversal into these, so the flow stream carries inflow edges to
// them; the view styles those as completion/drop-off, not as requests.
export function terminalNodeIds(nodes: { id: string; apiTemplateId?: string }[]): Set<string> {
  const term = new Set<string>()
  for (const n of nodes) {
    if (!n.apiTemplateId) term.add(n.id)
  }
  return term
}

// EdgeKind sorts an edge by its role in the funnel so the view can weight it:
//   'forward'  — advances the journey (drawn boldest; this is the main funnel).
//   'back'     — returns to an earlier, already-visited node (a loop, e.g.
//                category -> browse); de-emphasized so it doesn't fight forward.
//   'terminal' — flows into a template-less endpoint (done/exit); rendered as a
//                completion/drop-off, faded so endpoints read as outcomes.
export type EdgeKind = 'forward' | 'back' | 'terminal'

// classifyEdge labels one edge given the terminal set and each node's BFS depth
// (its funnel column, as produced by layoutGraph). Terminal wins first (an edge
// into done/exit is an outcome regardless of direction). Otherwise an edge whose
// destination sits at an equal-or-shallower depth than its source is a back/loop
// edge; everything else advances the funnel and is 'forward'. Missing depths
// (unreachable nodes) default to forward so they still draw at full strength.
export function classifyEdge(
  from: string,
  to: string,
  terminals: Set<string>,
  depth: Map<string, number>,
): EdgeKind {
  if (terminals.has(to)) return 'terminal'
  const df = depth.get(from)
  const dt = depth.get(to)
  if (df !== undefined && dt !== undefined && dt <= df) return 'back'
  return 'forward'
}

// requestTotal sums the request counts that represent real API calls — every edge
// *except* those flowing into a terminal node. Completions and drop-offs into
// done/exit are journey outcomes, not requests, so counting them would inflate the
// "N requests" headline; they still render as completion flow, just outside this
// total. Entry edges (from === "") into a non-terminal node are real first
// requests and are included.
export function requestTotal(
  edges: { from: string; to: string; requests: number }[],
  terminals: Set<string>,
): number {
  let total = 0
  for (const e of edges) {
    if (terminals.has(e.to)) continue
    total += e.requests
  }
  return total
}

// terminalRole classifies a terminal node as a 'completion' or a 'dropoff' for
// styling, copy, and the outcome headline. 'exit' is the drop-off (a user left);
// 'done' — and any other template-less endpoint — reads as a completion (a user
// finished), so an unnamed terminal defaults to the positive outcome rather than
// looking like a leak.
export type TerminalRole = 'completion' | 'dropoff'
export function terminalRole(id: string): TerminalRole {
  return id === 'exit' ? 'dropoff' : 'completion'
}

// OutcomeSummary is the journey-outcome headline: how many journeys began (entry
// inflow), how many reached a completion terminal (done) vs a drop-off terminal
// (exit), and those counts as fractions of the journeys started. Rates are 0..1;
// with nothing started they are 0, never NaN.
export interface OutcomeSummary {
  started: number
  completed: number
  dropped: number
  completionRate: number
  dropOffRate: number
}

// outcomeRates derives the headline rates from raw outcome counts. It is split
// from outcomeSummary so the events view — which counts per-request trace events
// rather than per-edge aggregates — shares the exact same rate math.
export function outcomeRates(started: number, completed: number, dropped: number): OutcomeSummary {
  const rate = (n: number) => (started > 0 ? n / started : 0)
  return { started, completed, dropped, completionRate: rate(completed), dropOffRate: rate(dropped) }
}

// outcomeSummary folds the cumulative per-edge flow into the completion/drop-off
// headline. Journeys started are the entry edges (from === ""); outcomes are the
// inflow into terminal nodes, split by terminalRole. Mid-journey request edges
// contribute to neither side: they are traffic, not outcomes.
export function outcomeSummary(
  edges: { from: string; to: string; requests: number }[],
  terminals: Set<string>,
): OutcomeSummary {
  let started = 0
  let completed = 0
  let dropped = 0
  for (const e of edges) {
    if (e.from === '') started += e.requests
    if (terminals.has(e.to)) {
      if (terminalRole(e.to) === 'dropoff') dropped += e.requests
      else completed += e.requests
    }
  }
  return outcomeRates(started, completed, dropped)
}

// HEAT_OK / HEAT_ERR are the endpoints of the error-ratio color ramp (the same
// GitHub-dark green/red used elsewhere in the live view).
export const HEAT_OK = '#3fb950'
export const HEAT_ERR = '#f85149'

// heatColor tints an edge from healthy-green to error-red by its error ratio
// (errors/requests). With no requests it is fully green (nothing has failed). The
// result is an "rgb(r, g, b)" string interpolated in sRGB — good enough for a
// status tint and dependency-free.
export function heatColor(errors: number, requests: number): string {
  const ratio = requests > 0 ? clamp01(errors / requests) : 0
  return lerpColor(HEAT_OK, HEAT_ERR, ratio)
}

// lerpColor linearly interpolates between two "#rrggbb" colors; t is clamped to
// [0,1]. Kept tiny to avoid pulling in a color dependency.
export function lerpColor(a: string, b: string, t: number): string {
  const ca = hexToRgb(a)
  const cb = hexToRgb(b)
  const k = clamp01(t)
  const r = Math.round(ca.r + (cb.r - ca.r) * k)
  const g = Math.round(ca.g + (cb.g - ca.g) * k)
  const bl = Math.round(ca.b + (cb.b - ca.b) * k)
  return `rgb(${r}, ${g}, ${bl})`
}

function hexToRgb(hex: string): { r: number; g: number; b: number } {
  const n = parseInt(hex.slice(1), 16)
  return { r: (n >> 16) & 0xff, g: (n >> 8) & 0xff, b: n & 0xff }
}

// formatCount renders large cumulative counts compactly (1234 -> "1.2k",
// 5_000_000 -> "5M") so per-edge labels stay short at any scale.
export function formatCount(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return trimZero(n / 1000) + 'k'
  if (n < 1_000_000_000) return trimZero(n / 1_000_000) + 'M'
  return trimZero(n / 1_000_000_000) + 'B'
}

// trimZero formats to one decimal but drops a trailing ".0" so "5.0k" reads "5k".
function trimZero(n: number): string {
  return n.toFixed(1).replace(/\.0$/, '')
}

// Layout spacing, in the SVG's own (unitless) coordinate space. The SVG scales to
// fit via its viewBox, so these are relative, not pixels.
const COL_GAP = 200 // horizontal distance between layers (columns)
const ROW_GAP = 110 // vertical distance between nodes in the same column

// graphDepths runs the BFS that underlies the layout: from `start`, each reachable
// node gets its shortest-path depth (its funnel column); unreachable nodes (and the
// case where `start` is missing) are simply absent from the map. Only edges between
// declared nodes are followed, in input order, so the result is deterministic. It
// is exported (and reused by layoutGraph) so the flow view can classify edges as
// forward vs back/loop from the same depths the layout draws.
export function graphDepths(
  nodes: { id: string }[],
  edges: { from: string; to: string }[],
  start: string,
): Map<string, number> {
  const ids = nodes.map((n) => n.id)
  const known = new Set(ids)

  // Adjacency: only edges between declared nodes, in input order (determinism).
  const adj = new Map<string, string[]>()
  for (const id of ids) adj.set(id, [])
  for (const e of edges) {
    if (known.has(e.from) && known.has(e.to)) adj.get(e.from)!.push(e.to)
  }

  // BFS from start assigns each reachable node its shortest depth (the column).
  const depth = new Map<string, number>()
  if (known.has(start)) {
    const queue = [start]
    depth.set(start, 0)
    for (let i = 0; i < queue.length; i++) {
      const cur = queue[i]
      const d = depth.get(cur)!
      for (const next of adj.get(cur)!) {
        if (!depth.has(next)) {
          depth.set(next, d + 1)
          queue.push(next)
        }
      }
    }
  }
  return depth
}

// layoutGraph computes a deterministic layered (DAG) layout: BFS depth from
// `start` is the column (x); nodes sharing a depth are spread vertically (y) and
// centered around a common midline so unbalanced columns still look tidy. Nodes
// unreachable from `start` are parked together in a single trailing column. The
// result is a plain id -> {x,y} map the SVG renders from; it is pure and stable
// for a given input, so it is unit-tested.
export function layoutGraph(
  nodes: { id: string }[],
  edges: { from: string; to: string }[],
  start: string,
): Record<string, { x: number; y: number }> {
  const ids = nodes.map((n) => n.id)

  // Shortest-path depth from start (the column); shared with the flow view.
  const depth = graphDepths(nodes, edges, start)

  // Bucket reachable nodes by depth (discovery order within a column); collect the
  // rest (unreachable, incl. when start is missing) for one trailing column.
  const columns: string[][] = []
  const unreachable: string[] = []
  for (const id of ids) {
    const d = depth.get(id)
    if (d === undefined) {
      unreachable.push(id)
      continue
    }
    while (columns.length <= d) columns.push([])
    columns[d].push(id)
  }
  if (unreachable.length > 0) columns.push(unreachable)

  // Centre each column vertically around y = 0 so columns of differing heights
  // stay visually balanced regardless of how many nodes they hold.
  const pos: Record<string, { x: number; y: number }> = {}
  columns.forEach((col, c) => {
    const offset = ((col.length - 1) * ROW_GAP) / 2
    col.forEach((id, r) => {
      pos[id] = { x: c * COL_GAP, y: r * ROW_GAP - offset }
    })
  })
  return pos
}

const API = '/api'

export async function createExperiment(spec: RunSpec): Promise<string> {
  const res = await fetch(`${API}/experiments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(spec),
  })
  if (!res.ok) throw new Error(`create failed: ${res.status} ${await res.text()}`)
  return (await res.json()).id as string
}

export async function startRun(experimentId: string): Promise<string> {
  const res = await fetch(`${API}/experiments/${experimentId}/run`, { method: 'POST' })
  if (!res.ok) throw new Error(`run failed: ${res.status} ${await res.text()}`)
  return (await res.json()).runId as string
}

// ImportSkippedSample is one example line the importer dropped, as the server
// reports it; every field is best-effort (an importer that tracks no
// diagnostics omits them all).
export interface ImportSkippedSample {
  line?: number
  text?: string
  reason?: string
}

// ImportStats is the optional coverage report POST /api/import attaches when
// the importer learned from real traffic (the access-log path): what was kept
// and what was dropped, so a capped or noisy import is visible instead of
// silently passing as full coverage. Old servers and spec conversions
// (OpenAPI/HAR) omit it entirely.
export interface ImportStats {
  // format is the resolved access-log format profile (e.g. "combined", "alb",
  // "cloudfront", "caddy", "traefik", "jsonl"). Omitted when the importer
  // reports none (old server, OpenAPI/HAR conversion).
  format?: string
  requests?: number
  skipped?: number
  sessions?: number
  clients?: number
  droppedEndpoints?: number
  skippedSamples?: ImportSkippedSample[]
}

// ImportResult is what POST /api/import returns on success: a ready-to-edit
// scenario the caller can drop straight into the Scenario card's fields, plus
// the optional coverage stats behind the import.
//
// P7 (Easy-Auth): the import additionally AUTO-derives auth material the console
// can populate the Auth section from, so a swagger/HAR import leaves only the
// secret to fill. All three are optional — an old server, or a spec with no auth,
// simply omits them:
//   - credentialPool — the same shape buildRunSpec emits (strategy + material); a
//     HAR import may carry a 'pool' with the captured (secret-omitted) entries.
//   - loginFlow      — a derived login flow (E2 OpenAPI->login / E3 HAR->token) the
//     UI drops straight into the login mode.
//   - suggestedSignup — a derived signup flow (P7 OpenAPI /register->signup) the UI
//     offers as "create test accounts". Its bodies carry REPLACE_ME_* placeholders
//     (never real secrets) the UI surfaces as highlighted inputs.
export interface ImportResult {
  graph: unknown
  templates: unknown
  start: string
  maxSteps: number
  stats?: ImportStats
  credentialPool?: CredentialPool
  loginFlow?: LoginFlowSpec
  suggestedSignup?: SignupFlowSpec
  authAdvisories?: AuthAdvisory[]
}

// AuthAdvisory is an import-time hint about auth the importer could not act on:
// code is a stable machine key ('mint-managed-idp', 'openidconnect-discovery')
// and detail its code-specific parameter (the IdP host, the discovery URL).
export interface AuthAdvisory {
  code: string
  detail?: string
}

// mintManagedIdPAdvisory picks the managed-IdP mint footgun advisory out of an
// import's advisories, or null. When present, the Auth card warns on the mint
// mode: the token issuer holds the signing key, so a self-issued (mint) token
// would be rejected — the OAuth2 route is the answer for that service.
export function mintManagedIdPAdvisory(advisories: AuthAdvisory[] | undefined): AuthAdvisory | null {
  for (const a of advisories ?? []) {
    if (a.code === 'mint-managed-idp') return a
  }
  return null
}

// openIdConnectDiscoveryUrl picks the openIdConnect discovery-document URL out of
// an import's advisories, or ''. The OAuth2 guide surfaces it next to the token
// URL: the document's token_endpoint is exactly what belongs in that field.
export function openIdConnectDiscoveryUrl(advisories: AuthAdvisory[] | undefined): string {
  for (const a of advisories ?? []) {
    if (a.code === 'openidconnect-discovery' && a.detail) return a.detail
  }
  return ''
}

// importScenario converts a raw OpenAPI / HAR / access-log document into a
// scenario via the backend. `format` is 'auto' (let the server sniff it),
// 'openapi', 'har', or 'accesslog'. The body is the raw text, posted as-is. On a
// non-2xx it throws the server's own error text so the UI can show a meaningful
// message (the backend returns 400 {error} on a bad spec and 501 when the
// importer is unavailable); otherwise it returns the parsed scenario. It
// deliberately throws rather than returning a sentinel so the caller's catch
// surfaces the message inline.
export async function importScenario(
  spec: string,
  format: 'auto' | 'openapi' | 'har' | 'accesslog',
): Promise<ImportResult> {
  const res = await fetch(`${API}/import?format=${format}`, { method: 'POST', body: spec })
  if (!res.ok) {
    const text = (await res.text()).trim()
    let message = text
    // The server reports failures as { "error": "..." }; unwrap it when present so
    // the user sees the reason, not the raw JSON envelope. Fall back to the body
    // text, then to the status code, so there is always something to show.
    try {
      const parsed = JSON.parse(text) as { error?: unknown }
      if (parsed && typeof parsed.error === 'string' && parsed.error.trim()) message = parsed.error
    } catch {
      /* not JSON: keep the raw text */
    }
    throw new Error(message || `import failed: ${res.status}`)
  }
  return (await res.json()) as ImportResult
}

export async function getReport(runId: string): Promise<Report> {
  const res = await fetch(`${API}/runs/${runId}/report`)
  if (!res.ok) throw new Error(`report failed: ${res.status}`)
  return (await res.json()) as Report
}

// probeRun looks a run up for the ?run attach flow. The report endpoint answers
// for live and finalized runs alike, so it doubles as the existence probe: the
// report when the run exists, null when the server does not know the id (404),
// and a throw on any other failure so the caller can fall back to the form. The
// id comes straight from the URL, so it is escaped into the path.
export async function probeRun(runId: string): Promise<Report | null> {
  const res = await fetch(`${API}/runs/${encodeURIComponent(runId)}/report`)
  if (res.status === 404) return null
  if (!res.ok) throw new Error(`report failed: ${res.status}`)
  return (await res.json()) as Report
}

// getExperimentSpec fetches the stored RunSpec behind an experiment id, or null
// when it cannot (evicted spec, restarted server, network failure). Attach-mode
// form hydration is best-effort: without the spec the console still follows the
// run's stream, it just keeps the default scenario fields.
export async function getExperimentSpec(experimentId: string): Promise<unknown | null> {
  try {
    const res = await fetch(`${API}/experiments/${encodeURIComponent(experimentId)}`)
    if (!res.ok) return null
    return (await res.json()) as unknown
  } catch {
    return null
  }
}

export async function killRun(runId: string): Promise<void> {
  const res = await fetch(`${API}/runs/${runId}/kill`, { method: 'POST' })
  if (!res.ok) throw new Error(`kill failed: ${res.status}`)
}

export function streamURL(runId: string): string {
  return `${API}/runs/${runId}/stream`
}

// traceURL is the per-step live-trace SSE stream for a run (opt-in via spec.trace).
export function traceURL(runId: string): string {
  return `${API}/runs/${runId}/trace`
}

// heatmapURL is the per-edge aggregate SSE stream for a run (opt-in via spec.trace),
// used by the heatmap view for runs too large to animate request-by-request.
export function heatmapURL(runId: string): string {
  return `${API}/runs/${runId}/heatmap`
}

// latencyHeatmapURL is the latency-over-time SSE stream for a run (opt-in via
// spec.trace): a 2-D histogram of request counts by latency band × time bucket,
// used by the canonical load-test latency heatmap.
export function latencyHeatmapURL(runId: string): string {
  return `${API}/runs/${runId}/latency-heatmap`
}

// reportHTMLURL is the server-rendered, standalone HTML report for a run.
export function reportHTMLURL(runId: string): string {
  return `${API}/runs/${runId}/report.html`
}

// compareURL is the server-rendered HTML diff of two runs (regression view).
export function compareURL(a: string, b: string): string {
  return `${API}/runs/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`
}

// SharedReportError carries a stable code (and the HTTP status) so the viewer can
// render a localized message instead of a hard-coded English string. The message
// is kept as a readable English fallback for non-UI callers / logs.
export class SharedReportError extends Error {
  code: 'expired' | 'notFound' | 'unavailable'
  status: number
  constructor(code: 'expired' | 'notFound' | 'unavailable', status: number, message: string) {
    super(message)
    this.name = 'SharedReportError'
    this.code = code
    this.status = status
  }
}

export async function getSharedReport(token: string): Promise<Report> {
  const res = await fetch(`${API}/reports/shared/${token}`)
  if (res.status === 410) throw new SharedReportError('expired', 410, 'This shared report has expired.')
  if (res.status === 404)
    throw new SharedReportError('notFound', 404, 'This shared report was not found.')
  if (!res.ok)
    throw new SharedReportError('unavailable', res.status, `Shared report unavailable (${res.status}).`)
  return (await res.json()) as Report
}

// shareTokenFromQuery extracts a read-only viewer token from a query string,
// e.g. "?share=abc" -> "abc". Returns null when absent or blank.
export function shareTokenFromQuery(search: string): string | null {
  const t = new URLSearchParams(search).get('share')
  return t && t.trim() ? t.trim() : null
}

// runIdFromQuery extracts a run id from a query string, e.g. "?run=run-1" ->
// "run-1". Returns null when absent or blank. This is the attach contract:
// `tmula demo` opens the console as /?run=<run-id> so the page attaches
// straight to the demo's live run instead of showing only the form.
export function runIdFromQuery(search: string): string | null {
  const id = new URLSearchParams(search).get('run')
  return id && id.trim() ? id.trim() : null
}
