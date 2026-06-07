import { useEffect, useReducer, useRef } from 'react'
import {
  formatCount,
  latencyCellColor,
  latencyHeatmapURL,
  parseLatencyFrame,
  type LatencyFrame,
} from './api'

// LatencyHeatmap is the canonical load-test heatmap: a grid where X is time (run
// progress) and Y is a latency band, and each cell's COLOR encodes how many
// requests landed in that (latency band × time bucket). It streams over SSE while
// the run is active and freezes on the final frame, so the finished run leaves a
// readable picture of where latency drifted as load built up.
//
// The stream lifecycle mirrors LiveGraph's flow view exactly: open an EventSource
// on mount when active, re-add the "data:" prefix the browser strips, parse each
// line with the tested pure parseLatencyFrame, and close on the done frame / when
// the run id changes / on unmount. The latest frame is held in a ref so the SSE
// handler can replace it without a render per frame; a single tick() repaints.

interface LatencyHeatmapProps {
  runId: string
  active: boolean
}

// Light-panel palette so the heatmap reads as a clean data-viz card, consistent
// with the rest of the bright dashboard. Cell fill comes from latencyCellColor.
const GRID = '#eceef4' // hairline between cells
const AXIS = '#aab0bf' // tick text / axis labels
const LABEL = '#4b5364' // band labels

// Geometry, in the SVG's own coordinate space (the viewBox scales to fit). The
// plot area sits to the right of the row labels and above the time axis.
const LABEL_W = 96 // left gutter for latency-band labels
const AXIS_H = 22 // bottom gutter for time ticks
const TOP_PAD = 6
const RIGHT_PAD = 10
const CELL_H = 26 // height of one latency-band row
const MIN_CELL_W = 6 // a column never renders thinner than this
const ROW_GAP = 1 // hairline gap between cells (drawn via background grid)

export default function LatencyHeatmap({ runId, active }: LatencyHeatmapProps) {
  // Latest histogram frame, held in a ref; tick() forces a repaint on update.
  const frameRef = useRef<LatencyFrame | null>(null)
  const [, tick] = useReducer((n: number) => n + 1, 0)

  // Open the latency-heatmap stream while active; tear it down on unmount, when
  // active goes false, on the done frame, or when the run id changes.
  useEffect(() => {
    if (!active) return
    frameRef.current = null
    tick()

    const es = new EventSource(latencyHeatmapURL(runId))
    es.onmessage = (e: MessageEvent) => {
      // EventSource strips the "data:" prefix; re-add it so the tested pure parser
      // (which mirrors parseHeatFrame) handles the line uniformly.
      const line = typeof e.data === 'string' && e.data.startsWith('data:') ? e.data : `data: ${e.data}`
      const frame = parseLatencyFrame(line)
      if (!frame) return // malformed frame: ignore, keep the stream open
      frameRef.current = frame
      tick()
      if (frame.done) es.close()
    }
    es.onerror = () => {
      // The server also closes the stream on completion; nothing to recover, so
      // just close our side. The last frame stays painted (the run is frozen).
      es.close()
    }
    return () => {
      es.close()
    }
  }, [active, runId])

  const frame = frameRef.current
  const hasData =
    frame !== null && frame.rows.length > 0 && frame.cells.length > 0 && cellsAnyPositive(frame.cells)

  return (
    <figure className="latheat">
      <figcaption className="latheat__cap">
        <span className="latheat__cap-main">Requests per latency × time bucket</span>
        <span className="latheat__cap-sub">darker = more requests · high latency at top</span>
        {frame && hasData && (
          <span className="latheat__cap-peak">
            peak <strong>{formatCount(frame.maxCount)}</strong> / cell
          </span>
        )}
      </figcaption>

      {hasData && frame ? (
        <Grid frame={frame} />
      ) : (
        <div className="empty-viz" role="img" aria-label="Latency heatmap — waiting for the first requests">
          <span className="empty-viz__title">Waiting for traffic…</span>
          <span className="empty-viz__sub">
            cells fill in as requests complete, building a map of latency over the run
          </span>
        </div>
      )}
    </figure>
  )
}

// Grid draws the histogram as SVG rects: one column per time bucket, one row per
// latency band with HIGH latency at the TOP (rows are stored LOW->HIGH, so the row
// axis is reversed on render). The viewBox is derived from the data extents so the
// panel scales responsively at any column count.
function Grid({ frame }: { frame: LatencyFrame }) {
  const rows = frame.rows
  const nRows = rows.length
  const nCols = frame.cells[0]?.length ?? 0
  // The column width floats up from MIN_CELL_W so a short run fills the panel and a
  // long run stays at least minimally legible; the viewBox scales either way.
  const colW = Math.max(MIN_CELL_W, 720 / Math.max(nCols, 1))
  const plotW = colW * nCols
  const plotH = CELL_H * nRows
  const width = LABEL_W + plotW + RIGHT_PAD
  const height = TOP_PAD + plotH + AXIS_H

  // rowY maps a stored row index (0 = lowest latency) to its top Y, reversed so the
  // highest-latency band sits at the top of the plot.
  const rowY = (storedIndex: number) => TOP_PAD + (nRows - 1 - storedIndex) * CELL_H

  // A few evenly-spaced time ticks (in seconds) along the X axis.
  const ticks = timeTicks(nCols, frame.binWidthMs)

  return (
    <svg
      className="latheat__svg"
      viewBox={`0 0 ${width} ${height}`}
      width="100%"
      role="img"
      aria-label={`Latency heatmap: ${nRows} latency bands over ${nCols} time buckets, color shows request density`}
    >
      {/* Cells. */}
      <g>
        {frame.cells.map((row, ri) =>
          row.map((count, ci) => (
            <rect
              key={`c${ri}-${ci}`}
              x={LABEL_W + ci * colW + ROW_GAP / 2}
              y={rowY(ri) + ROW_GAP / 2}
              width={Math.max(1, colW - ROW_GAP)}
              height={Math.max(1, CELL_H - ROW_GAP)}
              rx={2}
              fill={latencyCellColor(count, frame.maxCount)}
            >
              <title>
                {rows[ri]?.label ?? `band ${ri}`} · {tickLabel(ci * frame.binWidthMs)} — {formatCount(count)}{' '}
                request{count === 1 ? '' : 's'}
              </title>
            </rect>
          )),
        )}
      </g>

      {/* Row (latency band) labels, high latency at top. */}
      <g>
        {rows.map((band, ri) => (
          <text
            key={`l${ri}`}
            x={LABEL_W - 10}
            y={rowY(ri) + CELL_H / 2 + 4}
            textAnchor="end"
            fontSize={11}
            fill={LABEL}
            className="latheat__band"
          >
            {band.label}
          </text>
        ))}
      </g>

      {/* Faint frame around the plot so empty (near-blank) cells still read as a grid. */}
      <rect
        x={LABEL_W}
        y={TOP_PAD}
        width={plotW}
        height={plotH}
        fill="none"
        stroke={GRID}
        strokeWidth={1}
      />

      {/* Time axis: a few ticks labeled in seconds. */}
      <g>
        {ticks.map((t) => {
          const x = LABEL_W + t.col * colW
          return (
            <g key={`t${t.col}`}>
              <line x1={x} y1={TOP_PAD + plotH} x2={x} y2={TOP_PAD + plotH + 4} stroke={GRID} strokeWidth={1} />
              <text
                x={x}
                y={TOP_PAD + plotH + AXIS_H - 6}
                textAnchor={t.col === 0 ? 'start' : t.col >= nCols - 1 ? 'end' : 'middle'}
                fontSize={10.5}
                fill={AXIS}
              >
                {t.label}
              </text>
            </g>
          )
        })}
      </g>
    </svg>
  )
}

// cellsAnyPositive reports whether any cell has a positive count, so the empty
// state shows until the first real requests land (an all-zero frame still reads as
// "waiting" rather than a blank grid).
function cellsAnyPositive(cells: number[][]): boolean {
  for (const row of cells) for (const c of row) if (c > 0) return true
  return false
}

interface TimeTick {
  col: number
  label: string
}

// timeTicks picks up to 5 evenly-spaced column indices and labels each by its time
// offset (col * binWidth) in seconds. Always includes the first and last column.
function timeTicks(nCols: number, binWidthMs: number): TimeTick[] {
  if (nCols <= 1) return [{ col: 0, label: tickLabel(0) }]
  const target = Math.min(5, nCols)
  const step = (nCols - 1) / (target - 1)
  const seen = new Set<number>()
  const ticks: TimeTick[] = []
  for (let i = 0; i < target; i++) {
    const col = Math.round(i * step)
    if (seen.has(col)) continue
    seen.add(col)
    ticks.push({ col, label: tickLabel(col * binWidthMs) })
  }
  return ticks
}

// tickLabel renders a millisecond offset compactly as seconds: 0 -> "0s",
// 1500 -> "1.5s", 30000 -> "30s".
function tickLabel(ms: number): string {
  const s = ms / 1000
  if (s === 0) return '0s'
  const r = s >= 10 ? Math.round(s) : Math.round(s * 10) / 10
  return `${r}s`
}

// Presentation lives in styles.css under the .latheat* classes so the panel
// matches the dashboard's design tokens (the SVG itself carries only data-driven
// fills via latencyCellColor).
