// Import coverage: the render model behind the "import honesty" panel. The
// access-log learner reports what it kept and dropped (importer.AccessLogStats
// on the server); this module turns that optional, possibly-partial wire object
// into a defensive, ready-to-render report so the UI can say "this import
// reflects only part of the captured traffic" instead of letting a capped or
// noisy import pass as full coverage. Pure functions only — the React panel in
// App.tsx stays a thin view over this model.

import type { ImportStats } from './api'

// CoverageSample is one skipped-line example, normalized for the diagnostic
// table: a missing line number renders as null, text/reason default to ''.
export interface CoverageSample {
  line: number | null
  text: string
  reason: string
}

// CoverageReport is the fully-defaulted shape the panel renders.
export interface CoverageReport {
  requests: number
  skipped: number
  sessions: number
  clients: number
  droppedEndpoints: number
  // totalLines is requests + skipped: every observed input line, used or not.
  totalLines: number
  // skippedPct is the share of input lines skipped, as a whole percent. A real
  // skip never rounds down to 0 so the warning always justifies itself.
  skippedPct: number
  // partial flags an import that did not use every observed line — the cue for
  // the warning tone ("this run replays only part of the captured traffic").
  partial: boolean
  samples: CoverageSample[]
}

// MAX_COVERAGE_SAMPLES bounds the diagnostic table; the server decides how many
// samples to send, the UI decides how many to show.
export const MAX_COVERAGE_SAMPLES = 8

// MAX_SAMPLE_TEXT_CHARS bounds one sample row so a pathological log line cannot
// flood the panel.
const MAX_SAMPLE_TEXT_CHARS = 160

// coverageFromStats builds the report from the optional `stats` field of a
// POST /api/import response. It accepts unknown and verifies every field, so an
// old server (no stats), a spec import (nil stats) or a newer server with extra
// fields all degrade safely: null means "nothing to report — render no panel".
export function coverageFromStats(stats: unknown): CoverageReport | null {
  if (typeof stats !== 'object' || stats === null || Array.isArray(stats)) return null
  const s = stats as ImportStats
  const requests = toCount(s.requests)
  const skipped = toCount(s.skipped)
  const totalLines = requests + skipped
  // A zero-line report has nothing honest to say; stay quiet rather than
  // rendering "0 requests used" noise for a malformed or empty stats object.
  if (totalLines === 0) return null
  return {
    requests,
    skipped,
    sessions: toCount(s.sessions),
    clients: toCount(s.clients),
    droppedEndpoints: toCount(s.droppedEndpoints),
    totalLines,
    skippedPct: skipped === 0 ? 0 : Math.max(1, Math.round((skipped / totalLines) * 100)),
    partial: skipped > 0,
    samples: toSamples(s.skippedSamples),
  }
}

// toCount coerces an untrusted wire value into a non-negative whole number,
// defaulting anything else (missing, junk type, NaN, Infinity, negative) to 0.
function toCount(v: unknown): number {
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0) return 0
  return Math.floor(v)
}

// toSamples keeps only sample rows that carry something renderable (a positive
// line number, text, or a reason), normalizes their fields, and caps the list.
function toSamples(v: unknown): CoverageSample[] {
  if (!Array.isArray(v)) return []
  const out: CoverageSample[] = []
  for (const item of v) {
    if (out.length >= MAX_COVERAGE_SAMPLES) break
    if (typeof item !== 'object' || item === null || Array.isArray(item)) continue
    const row = item as { line?: unknown; text?: unknown; reason?: unknown }
    const line = typeof row.line === 'number' && Number.isFinite(row.line) && row.line > 0 ? Math.floor(row.line) : null
    const text = typeof row.text === 'string' ? truncate(row.text) : ''
    const reason = typeof row.reason === 'string' ? row.reason : ''
    if (line === null && text === '' && reason === '') continue
    out.push({ line, text, reason })
  }
  return out
}

function truncate(text: string): string {
  if (text.length <= MAX_SAMPLE_TEXT_CHARS) return text
  return `${text.slice(0, MAX_SAMPLE_TEXT_CHARS)}…`
}
