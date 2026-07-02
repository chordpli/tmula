import type { AuthMode } from './api'

// authEntryModel groups the Auth card's strategy radios into the entry points a
// normal operator reads first and the expert paths folded behind an Advanced
// disclosure. It is presentation-layer ONLY: the wire authMode values, the API
// payload, and AUTH_FORM_DEFAULTS are untouched — this module just decides which
// radio renders where, in user language.

// AuthEntry is a UI-layer choice in the Auth card: every wire mode, plus the
// 'oauth2' guided assembler — a pseudo-entry that compiles onto the login (or
// pool) wire mode and never crosses the wire itself.
export type AuthEntry = AuthMode | 'oauth2'

// AuthEntryOption is one radio: the UI entry it selects and its i18n keys.
export interface AuthEntryOption {
  entry: AuthEntry
  labelKey: string
  descKey: string
}

// PRIMARY_AUTH_ENTRIES are the always-visible entry points, ordered by effort:
// anonymous first, then "I already have tokens" (paste a pool — the easiest real
// auth), "log in to get tokens" (the importer's best-path landing), "it's an
// OAuth2 service" (the guided assembler), and "create accounts to test with"
// (the gated non-prod path).
export const PRIMARY_AUTH_ENTRIES: AuthEntryOption[] = [
  { entry: 'none', labelKey: 'auth.mode.none', descKey: 'auth.mode.none.desc' },
  { entry: 'pool', labelKey: 'auth.mode.pool', descKey: 'auth.mode.pool.desc' },
  { entry: 'login', labelKey: 'auth.mode.login', descKey: 'auth.mode.login.desc' },
  { entry: 'oauth2', labelKey: 'auth.mode.oauth2', descKey: 'auth.mode.oauth2.desc' },
  { entry: 'bootstrap', labelKey: 'auth.mode.bootstrap', descKey: 'auth.mode.bootstrap.desc' },
]

// ADVANCED_AUTH_ENTRIES fold behind the Advanced disclosure: mint (self-issued
// JWT signing — only correct when the operator holds the signing key) and exec
// (the bring-your-own-token escape hatch, opt-in gated). A normal operator never
// needs either, and surfacing them beside the entry points invited the
// managed-IdP mint footgun.
export const ADVANCED_AUTH_ENTRIES: AuthEntryOption[] = [
  { entry: 'mint', labelKey: 'auth.mode.mint', descKey: 'auth.mode.mint.desc' },
  { entry: 'exec', labelKey: 'auth.mode.exec', descKey: 'auth.mode.exec.desc' },
]

// modeForEntry maps a UI entry onto the wire mode its panel authors: the OAuth2
// guide assembles a login flow (its access-token path applies a pool patch
// explicitly), every other entry IS its wire mode.
export function modeForEntry(entry: AuthEntry): AuthMode {
  return entry === 'oauth2' ? 'login' : entry
}

// isAdvancedAuthMode reports whether a mode's radio lives behind the Advanced
// disclosure — the card auto-opens the fold when such a mode is selected (e.g. a
// round-tripped spec restored an exec run).
export function isAdvancedAuthMode(mode: AuthMode): boolean {
  return ADVANCED_AUTH_ENTRIES.some((o) => o.entry === mode)
}
