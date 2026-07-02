import type { AuthMode } from './api'

// authEntryModel groups the Auth card's strategy radios into the entry points a
// normal operator reads first and the expert paths folded behind an Advanced
// disclosure. It is presentation-layer ONLY: the wire authMode values, the API
// payload, and AUTH_FORM_DEFAULTS are untouched — this module just decides which
// radio renders where, in user language.

// AuthEntryOption is one radio: the wire mode it selects and its i18n keys.
export interface AuthEntryOption {
  mode: AuthMode
  labelKey: string
  descKey: string
}

// PRIMARY_AUTH_MODES are the always-visible entry points, ordered by effort:
// anonymous first, then "I already have tokens" (paste a pool — the easiest real
// auth), "log in to get tokens" (the importer's best-path landing), and "create
// accounts to test with" (the gated non-prod path).
export const PRIMARY_AUTH_MODES: AuthEntryOption[] = [
  { mode: 'none', labelKey: 'auth.mode.none', descKey: 'auth.mode.none.desc' },
  { mode: 'pool', labelKey: 'auth.mode.pool', descKey: 'auth.mode.pool.desc' },
  { mode: 'login', labelKey: 'auth.mode.login', descKey: 'auth.mode.login.desc' },
  { mode: 'bootstrap', labelKey: 'auth.mode.bootstrap', descKey: 'auth.mode.bootstrap.desc' },
]

// ADVANCED_AUTH_MODES fold behind the Advanced disclosure: mint (self-issued JWT
// signing — only correct when the operator holds the signing key) and exec (the
// bring-your-own-token escape hatch, opt-in gated). A normal operator never needs
// either, and surfacing them beside the entry points invited the managed-IdP
// mint footgun.
export const ADVANCED_AUTH_MODES: AuthEntryOption[] = [
  { mode: 'mint', labelKey: 'auth.mode.mint', descKey: 'auth.mode.mint.desc' },
  { mode: 'exec', labelKey: 'auth.mode.exec', descKey: 'auth.mode.exec.desc' },
]

// isAdvancedAuthMode reports whether a mode's radio lives behind the Advanced
// disclosure — the card auto-opens the fold when such a mode is selected (e.g. a
// round-tripped spec restored an exec run).
export function isAdvancedAuthMode(mode: AuthMode): boolean {
  return ADVANCED_AUTH_MODES.some((o) => o.mode === mode)
}
