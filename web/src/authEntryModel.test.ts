import { describe, it, expect } from 'vitest'
import {
  ADVANCED_AUTH_ENTRIES,
  advancedFoldOpen,
  entryPatch,
  isAdvancedAuthMode,
  modeForEntry,
  PRIMARY_AUTH_ENTRIES,
  selectedEntry,
} from './authEntryModel'
import { dict } from './i18n'
import { AUTH_FORM_DEFAULTS, type AuthMode, type ExperimentForm } from './api'

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

  it('entryPatch sets exactly the wire mode and the guide flag', () => {
    expect(entryPatch('oauth2')).toEqual({ authEntryOAuth2: true, authMode: 'login' })
    expect(entryPatch('pool')).toEqual({ authEntryOAuth2: false, authMode: 'pool' })
    expect(entryPatch('login')).toEqual({ authEntryOAuth2: false, authMode: 'login' })
    expect(entryPatch('none')).toEqual({ authEntryOAuth2: false, authMode: 'none' })
  })

  it('preserves every OAuth2 guide answer when switching entry points and back', () => {
    // The guide answers live on the form; picking an entry only touches the two
    // fields entryPatch names, so a round trip through another entry keeps them.
    const answers = {
      ...AUTH_FORM_DEFAULTS.oauth2Guide,
      tokenUrl: 'https://idp.example.com/oauth/token',
      username: 'alice',
      password: 'pw-a',
      clientId: 'web',
      scope: 'read write',
    }
    let form = { ...AUTH_FORM_DEFAULTS, oauth2Guide: answers } as Pick<
      ExperimentForm,
      'authMode' | 'authEntryOAuth2' | 'oauth2Guide'
    >
    form = { ...form, ...entryPatch('oauth2') }
    expect(selectedEntry(form.authMode, form.authEntryOAuth2)).toBe('oauth2')
    // Wander off to the pool entry and back to the guide.
    form = { ...form, ...entryPatch('pool') }
    expect(selectedEntry(form.authMode, form.authEntryOAuth2)).toBe('pool')
    form = { ...form, ...entryPatch('oauth2') }
    expect(selectedEntry(form.authMode, form.authEntryOAuth2)).toBe('oauth2')
    expect(form.oauth2Guide).toEqual(answers)
  })

  it('keeps the expert fold open once the operator opened it this session', () => {
    // An advanced mode always forces the fold open.
    expect(advancedFoldOpen('mint', false)).toBe(true)
    expect(advancedFoldOpen('exec', false)).toBe(true)
    // Leaving mint/exec used to snap the fold shut; the userOpened bit keeps it
    // open for the rest of the session.
    expect(advancedFoldOpen('pool', true)).toBe(true)
    expect(advancedFoldOpen('none', true)).toBe(true)
    // Never opened by the user and no advanced mode selected: stays folded.
    expect(advancedFoldOpen('pool', false)).toBe(false)
    expect(advancedFoldOpen('login', false)).toBe(false)
  })

  it('selectedEntry self-heals when the wire mode no longer matches the guide', () => {
    // The guide compiles onto login; an import that moved the wire mode elsewhere
    // (e.g. a HAR-derived pool) must win over the remembered pseudo-entry.
    expect(selectedEntry('login', true)).toBe('oauth2')
    expect(selectedEntry('pool', true)).toBe('pool')
    expect(selectedEntry('login', false)).toBe('login')
    expect(selectedEntry('none', false)).toBe('none')
  })
})
