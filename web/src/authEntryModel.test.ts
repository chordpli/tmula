import { describe, it, expect } from 'vitest'
import {
  ADVANCED_AUTH_ENTRIES,
  isAdvancedAuthMode,
  modeForEntry,
  PRIMARY_AUTH_ENTRIES,
} from './authEntryModel'
import { dict } from './i18n'
import type { AuthMode } from './api'

// The Auth card's regrouping contract: the entry points render by default,
// mint and exec fold behind Advanced, and no wire mode is silently dropped from
// the UI.
describe('authEntryModel', () => {
  it('shows exactly the entry points by default, in effort order', () => {
    expect(PRIMARY_AUTH_ENTRIES.map((o) => o.entry)).toEqual([
      'none',
      'pool',
      'login',
      'oauth2',
      'bootstrap',
    ])
  })

  it('folds exactly mint and exec behind Advanced', () => {
    expect(ADVANCED_AUTH_ENTRIES.map((o) => o.entry)).toEqual(['mint', 'exec'])
    expect(isAdvancedAuthMode('mint')).toBe(true)
    expect(isAdvancedAuthMode('exec')).toBe(true)
    expect(isAdvancedAuthMode('pool')).toBe(false)
    expect(isAdvancedAuthMode('none')).toBe(false)
  })

  it('maps the oauth2 pseudo-entry onto the login wire mode, identity otherwise', () => {
    expect(modeForEntry('oauth2')).toBe('login')
    expect(modeForEntry('pool')).toBe('pool')
    expect(modeForEntry('mint')).toBe('mint')
    expect(modeForEntry('none')).toBe('none')
  })

  it('covers every wire auth mode exactly once across both groups', () => {
    const all = [...PRIMARY_AUTH_ENTRIES, ...ADVANCED_AUTH_ENTRIES]
      .map((o) => o.entry)
      .filter((e) => e !== 'oauth2') // presentation-only pseudo-entry
    const wireModes: AuthMode[] = ['none', 'pool', 'login', 'bootstrap', 'mint', 'exec']
    expect([...all].sort()).toEqual([...wireModes].sort())
    expect(new Set(all).size).toBe(all.length)
  })

  it('references only i18n keys that exist in both dictionaries', () => {
    for (const o of [...PRIMARY_AUTH_ENTRIES, ...ADVANCED_AUTH_ENTRIES]) {
      expect(dict.en[o.labelKey], `en ${o.labelKey}`).toBeTruthy()
      expect(dict.en[o.descKey], `en ${o.descKey}`).toBeTruthy()
      expect(dict.ko[o.labelKey], `ko ${o.labelKey}`).toBeTruthy()
      expect(dict.ko[o.descKey], `ko ${o.descKey}`).toBeTruthy()
    }
  })
})
