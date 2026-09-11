import { useEffect, useState } from 'react'
import { ReferenceArea, ReferenceLine } from 'recharts'
import { useAPI } from '../hooks/useAPI'

// Shared timeline decorations: load-test runs drawn as labelled bands and
// Pulse restarts drawn as vertical markers, plus a banner explaining gaps in
// the data. Charts using these must use `timeAxisProps` (a numeric time
// axis) so band edges land on real timestamps.

const RANGE_MS = { '5m': 5 * 60e3, '15m': 15 * 60e3, '1h': 60 * 60e3, '6h': 6 * 3600e3, '24h': 24 * 3600e3, '7d': 7 * 86400e3 }

// Go marshals a zero time.Time as "0001-01-01T00:00:00Z" rather than omitting it.
export function isZeroTime(s) {
  return !s || s.startsWith('0001-')
}

export function fmtClock(ts) {
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export const timeAxisProps = {
  dataKey: 'ts',
  type: 'number',
  scale: 'time',
  domain: ['dataMin', 'dataMax'],
  tickFormatter: fmtClock,
  tick: { fill: '#64748b', fontSize: 10 },
  axisLine: false,
  tickLine: false,
}

// useTimelineOverlays loads test runs and lifecycle events for a range.
export function useTimelineOverlays(range) {
  const { get } = useAPI()
  const [state, setState] = useState({ runs: [], events: [], lifecycle: null })

  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const [runsRes, lifeRes] = await Promise.all([
          get(`/test-runs?range=${range}`),
          get(`/lifecycle?range=${range}`),
        ])
        const runs = runsRes.ok ? await runsRes.json() : []
        const lifecycle = lifeRes.ok ? await lifeRes.json() : null
        if (alive) setState({ runs: runs || [], events: lifecycle?.events || [], lifecycle })
      } catch {}
    }
    load()
    const id = setInterval(load, 30_000)
    return () => { alive = false; clearInterval(id) }
  }, [get, range])

  return state
}

// overlayElements returns recharts children for the given overlays, clipped
// to the chart's [lo, hi] time domain. Recharts only renders its own
// components as direct chart children, so spread the result inside the chart.
export function overlayElements({ runs = [], events = [] }, [lo, hi]) {
  if (lo == null || hi == null) return []
  const out = []
  for (const r of runs) {
    const x1 = Math.max(lo, new Date(r.started_at).getTime())
    const x2 = Math.min(hi, isZeroTime(r.ended_at) ? Date.now() : new Date(r.ended_at).getTime())
    if (x2 <= x1) continue
    out.push(
      <ReferenceArea
        key={`run-${r.id}`} x1={x1} x2={x2} ifOverflow="hidden"
        fill="#6366f1" fillOpacity={0.12} stroke="#6366f1" strokeOpacity={0.35}
        label={{ value: r.name, position: 'insideTop', fill: '#a5b4fc', fontSize: 10 }}
      />,
    )
  }
  for (const e of events) {
    if (e.type !== 'start') continue
    const x = new Date(e.at).getTime()
    if (x < lo || x > hi) continue
    const color = e.previous_unclean ? '#ef4444' : '#f59e0b'
    out.push(
      <ReferenceLine
        key={`start-${e.instance_id}-${e.at}`} x={x} ifOverflow="hidden"
        stroke={color} strokeDasharray="4 3"
        label={{ value: e.previous_unclean ? 'restart after crash' : 'Pulse started', position: 'insideTopRight', fill: color, fontSize: 10 }}
      />,
    )
  }
  return out
}

// timeDomain returns [min, max] of the points' `ts`, or [null, null].
export function timeDomain(points) {
  if (!points.length) return [null, null]
  return [points[0].ts, points[points.length - 1].ts]
}

// DataBanner explains missing data in the selected range: in-memory storage
// that lost history at the last restart, or an unclean shutdown.
export function DataBanner({ lifecycle, range }) {
  if (!lifecycle) return null
  const windowStart = Date.now() - (RANGE_MS[range] || RANGE_MS['1h'])
  const messages = []

  if (!lifecycle.persistent && new Date(lifecycle.data_since).getTime() > windowStart) {
    messages.push({
      color: '#f59e0b',
      text: `In-memory storage: nothing from before ${fmtClock(lifecycle.data_since)} survived the last restart. ` +
        'Use pulse.WithSQLite(path) to keep history across restarts.',
    })
  }
  const unclean = (lifecycle.events || []).filter((e) => e.type === 'start' && e.previous_unclean).at(-1)
  if (unclean && new Date(unclean.at).getTime() > windowStart) {
    messages.push({
      color: '#ef4444',
      text: `Pulse restarted at ${fmtClock(unclean.at)} after an unclean shutdown (crash or kill)` +
        (unclean.gap_from ? `; data between ${fmtClock(unclean.gap_from)} and the restart is missing.` : '.'),
    })
  }

  return messages.map((m) => (
    <div key={m.text} style={{
      padding: '9px 14px', borderRadius: 6, marginBottom: 12, fontSize: 12.5,
      background: `${m.color}14`, color: m.color, border: `1px solid ${m.color}33`,
    }}>
      {m.text}
    </div>
  ))
}
