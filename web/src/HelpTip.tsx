import { useCallback, useId, useRef } from 'react'
import { useI18n } from './i18n'

// HelpTip is a small, accessible "?" badge that reveals a one-line explanation on
// hover and on keyboard focus — no tooltip library. The bubble is shown via CSS
// (:hover / :focus-visible on the wrapper); a tiny JS nudge on reveal keeps a wide
// bubble inside the viewport on both edges (it is left-anchored by default, and
// shifted left by a CSS custom property when it would overflow the right edge).
//
// The badge is a real <button> so it is tab-reachable and screen-reader friendly:
// aria-label announces it as help for its field (localized, e.g. "Allowlist —
// Help" / "허용 목록 — 도움말"), and aria-describedby ties it to the bubble text so
// the explanation is read on focus.
//
// `label` is the field name woven into the accessible name; `text` is the
// explanation shown in the bubble.
export default function HelpTip({ label, text }: { label: string; text: string }) {
  const { t } = useI18n()
  const id = useId()
  const bubbleRef = useRef<HTMLSpanElement>(null)

  // reposition runs when the bubble is about to show (pointer enter / focus). It
  // resets any prior shift, measures the bubble against the viewport, and if it
  // would spill past the right edge, sets --shift to pull it left by exactly the
  // overflow (plus a small margin). Left-anchoring already prevents left spill.
  const reposition = useCallback(() => {
    const el = bubbleRef.current
    if (!el) return
    el.style.setProperty('--shift', '0px')
    const rect = el.getBoundingClientRect()
    const margin = 8
    const overflowRight = rect.right - (window.innerWidth - margin)
    if (overflowRight > 0) el.style.setProperty('--shift', `${-Math.ceil(overflowRight)}px`)
  }, [])

  return (
    <span className="helptip" onPointerEnter={reposition}>
      <button
        type="button"
        className="helptip__btn"
        aria-label={`${label} — ${t('helptip.show')}`}
        aria-describedby={id}
        onFocus={reposition}
        // The bubble is purely informational; clicking should not submit a form or
        // scroll, so swallow the activation. Hover/focus already reveal it.
        onClick={(e) => e.preventDefault()}
      >
        <span aria-hidden="true">?</span>
      </button>
      <span className="helptip__bubble" role="tooltip" id={id} ref={bubbleRef}>
        {text}
      </span>
    </span>
  )
}
