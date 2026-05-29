import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

// Test runs page. Backend: GET /pulse/api/test-runs?range=… returns []TestRun
// (pulse/metrics.go TestRun type).

const RANGES = [
  { label: '1h', value: '1h' },
  { label: '6h', value: '6h' },
  { label: '24h', value: '24h' },
  { label: '7d', value: '7d' },
]

function fmtDuration(ms) {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

function fmtTime(iso) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

export default function TestRuns() {
  const { get } = useAPI()
  const [runs, setRuns] = useState([])
  const [range, setRange] = useState('24h')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    const tick = async () => {
      try {
        const res = await get(`/test-runs?range=${range}`)
        if (res.ok && alive) setRuns(await res.json())
      } catch {}
      if (alive) setLoading(false)
    }
    tick()
    const id = setInterval(tick, 10_000)
    return () => { alive = false; clearInterval(id) }
  }, [get, range])

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-xl font-bold text-slate-200">Test Runs</h1>
        <div className="flex items-center gap-2 text-xs">
          {RANGES.map((r) => (
            <button
              key={r.value}
              onClick={() => setRange(r.value)}
              className={`px-2.5 py-1 rounded ring-1 transition ${
                range === r.value
                  ? 'bg-indigo-500/15 text-indigo-300 ring-indigo-500/30'
                  : 'bg-slate-800/50 text-slate-400 ring-slate-700 hover:text-slate-200'
              }`}
            >{r.label}</button>
          ))}
        </div>
      </div>

      <p className="text-slate-500 text-xs mb-5">
        Recorded by load-test harnesses via <code className="text-indigo-300">POST /pulse/api/test-runs</code>.
        The bundled <code className="text-indigo-300">examples/k6/pulse-k6-bridge.js</code> wires this up for k6 in three lines.
      </p>

      {loading && <div className="text-slate-500 py-10">Loading…</div>}

      {!loading && runs.length === 0 && (
        <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-5 text-sm text-slate-400">
          No test runs recorded in the selected window. Once a k6 (or other) harness POSTs
          a run, it will appear here and on the timeline charts as a labelled vertical band.
        </div>
      )}

      {!loading && runs.length > 0 && (
        <div className="bg-slate-900/60 border border-slate-800 rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-slate-950/50">
              <tr className="text-xs uppercase tracking-wider text-slate-500">
                <th className="px-3 py-2 text-left font-medium">Name</th>
                <th className="px-3 py-2 text-left font-medium">Type</th>
                <th className="px-3 py-2 text-left font-medium">Started</th>
                <th className="px-3 py-2 text-left font-medium">Duration</th>
                <th className="px-3 py-2 text-left font-medium">Metadata</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800">
              {runs.map((r) => {
                const startedMs = new Date(r.started_at).getTime()
                const endedMs = r.ended_at ? new Date(r.ended_at).getTime() : Date.now()
                const inFlight = !r.ended_at
                return (
                  <tr key={r.id} className="hover:bg-slate-900">
                    <td className="px-3 py-3">
                      <div className="font-semibold text-slate-200">{r.name}</div>
                      <div className="text-[11px] text-slate-500 font-mono">{r.id}</div>
                    </td>
                    <td className="px-3 py-3 text-slate-300 font-mono text-xs">{r.type || '—'}</td>
                    <td className="px-3 py-3 text-slate-400 text-xs">{fmtTime(r.started_at)}</td>
                    <td className="px-3 py-3 text-xs">
                      {inFlight
                        ? <span className="text-amber-400">in flight</span>
                        : <span className="text-slate-200">{fmtDuration(endedMs - startedMs)}</span>}
                    </td>
                    <td className="px-3 py-3">
                      <MetadataBadges meta={r.metadata} />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function MetadataBadges({ meta }) {
  if (!meta || Object.keys(meta).length === 0) return <span className="text-slate-600 text-xs">—</span>
  return (
    <div className="flex flex-wrap gap-1">
      {Object.entries(meta).slice(0, 6).map(([k, v]) => (
        <span
          key={k}
          className="text-[11px] px-1.5 py-0.5 rounded bg-slate-800/80 text-slate-300 font-mono"
          title={JSON.stringify(v)}
        >
          {k}={typeof v === 'object' ? '…' : String(v).slice(0, 24)}
        </span>
      ))}
    </div>
  )
}
