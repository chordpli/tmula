import { afterEach, describe, expect, it, vi } from 'vitest'
import { detectLang, dict, translate, type Lang } from './i18n'

// A tiny in-memory localStorage stand-in, since the test environment is 'node'
// (no DOM globals). Only the methods detectLang touches are implemented.
function fakeStorage(initial: Record<string, string> = {}) {
  const store = { ...initial }
  return {
    getItem: (k: string) => (k in store ? store[k] : null),
    setItem: (k: string, v: string) => {
      store[k] = v
    },
    removeItem: (k: string) => {
      delete store[k]
    },
  }
}

// stubNavigator sets navigator.language for the browser-preference branch.
function stubNavigator(language: string) {
  vi.stubGlobal('navigator', { language })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('detectLang', () => {
  it('prefers a valid stored choice over the browser language', () => {
    vi.stubGlobal('localStorage', fakeStorage({ 'tmula.lang': 'ko' }))
    stubNavigator('en-US') // browser says English…
    expect(detectLang()).toBe('ko') // …but the stored choice wins
  })

  it('honors a stored English choice even when the browser is Korean', () => {
    vi.stubGlobal('localStorage', fakeStorage({ 'tmula.lang': 'en' }))
    stubNavigator('ko-KR')
    expect(detectLang()).toBe('en')
  })

  it('ignores an invalid stored value and falls back to the browser language', () => {
    vi.stubGlobal('localStorage', fakeStorage({ 'tmula.lang': 'fr' }))
    stubNavigator('ko-KR')
    expect(detectLang()).toBe('ko')
  })

  it('uses Korean when the browser language starts with ko and nothing is stored', () => {
    vi.stubGlobal('localStorage', fakeStorage())
    stubNavigator('ko')
    expect(detectLang()).toBe('ko')
    // Region-tagged Korean locales also resolve to ko.
    stubNavigator('ko-KR')
    expect(detectLang()).toBe('ko')
  })

  it('defaults to English for any non-Korean browser language', () => {
    vi.stubGlobal('localStorage', fakeStorage())
    stubNavigator('en-GB')
    expect(detectLang()).toBe('en')
    stubNavigator('ja-JP')
    expect(detectLang()).toBe('en')
  })

  it('falls back to English when storage access throws (e.g. private mode)', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked')
      },
    })
    stubNavigator('en-US')
    expect(detectLang()).toBe('en')
  })
})

describe('translate', () => {
  it('returns the string for the active language', () => {
    expect(translate(dict, 'en', 'findings.title')).toBe('Findings')
    expect(translate(dict, 'ko', 'findings.title')).toBe('발견 항목')
  })

  it('falls back to English when a key is missing in the active language', () => {
    // A key present only in English; ko should fall back to the en string rather
    // than show the raw key.
    const probe: Record<Lang, Record<string, string>> = {
      en: { 'only.en': 'English only' },
      ko: {},
    }
    expect(translate(probe, 'ko', 'only.en')).toBe('English only')
  })

  it('falls back to the key itself when it is missing in every language', () => {
    expect(translate(dict, 'en', 'does.not.exist')).toBe('does.not.exist')
    expect(translate(dict, 'ko', 'does.not.exist')).toBe('does.not.exist')
  })

  it('interpolates a {var} placeholder', () => {
    expect(translate(dict, 'en', 'presets.loaded', { name: 'Health check' })).toBe(
      'Loaded template: Health check',
    )
    expect(translate(dict, 'ko', 'presets.loaded', { name: '헬스 체크' })).toBe('템플릿 적용됨: 헬스 체크')
  })

  it('interpolates multiple placeholders and accepts numeric vars', () => {
    // run.noteClosed carries **…** emphasis markers (rendered to <strong> in the
    // UI); translate only fills the {vars} and leaves the markers as plain text.
    expect(translate(dict, 'en', 'run.noteClosed', { users: 20, steps: 12 })).toBe(
      '**20** virtual users · up to **12** steps',
    )
    // Mode/unit interpolation used by the live-traffic copy.
    expect(translate(dict, 'en', 'live.events', { max: 200, unit: 'users' })).toBe(
      'animating each request (≤200 users)',
    )
  })

  it('leaves an unknown placeholder untouched so a missing var is visible', () => {
    const probe: Record<Lang, Record<string, string>> = {
      en: { greet: 'Hi {name}, {missing}' },
      ko: {},
    }
    expect(translate(probe, 'en', 'greet', { name: 'Sam' })).toBe('Hi Sam, {missing}')
  })
})

describe('dictionary completeness', () => {
  it('defines every English key in Korean too (no missing translations)', () => {
    const enKeys = Object.keys(dict.en).sort()
    const koKeys = Object.keys(dict.ko).sort()
    expect(koKeys).toEqual(enKeys)
  })
})
