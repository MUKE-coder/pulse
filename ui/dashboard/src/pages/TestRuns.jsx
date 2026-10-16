import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useAuth } from '../context/AuthContext'
import { isZeroTime } from '../components/TimelineOverlays'

// Test runs page. Backend: GET /pulse/api/test-runs?range=… returns []TestRun
// (pulse/metrics.go TestRun type); GET /pulse/api/test-runs/:id/compare
// returns the before/during/after comparison (pulse/testrun_compare.go).

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
  if (isZeroTime(iso)) return '—'
  return new Date(iso).toLocaleString()
}

export default function TestRuns() {
  const { get } = useAPI()
  const [runs, setRuns] = useState([])
  const [range, setRange] = useState('24h')
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(null) // run ID whose comparison is shown

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
        <h1 className="text-[22px] font-semibold tracking-[-0.01em] text-ink">Test Runs</h1>
        <div className="flex items-center gap-2 text-xs">
          {RANGES.map((r) => (
            <button
              key={r.value}
              onClick={() => setRange(r.value)}
              className={`px-2.5 py-1 rounded ring-1 transition ${
                range === r.value
                  ? 'bg-accent/15 text-accent ring-accent/30'
                  : 'bg-surface-2 text-ink-2 ring-line hover:text-ink'
              }`}
            >{r.label}</button>
          ))}
        </div>
      </div>

      <p className="text-ink-3 text-xs mb-5">
        Recorded by load-test harnesses via <code className="text-accent">POST /pulse/api/test-runs</code>.
        The bundled <code className="text-accent">examples/k6/pulse-k6-bridge.js</code> wires this up for k6 in three lines.
        Runs also appear as shaded bands on the Dashboard charts.
      </p>

      {loading && <div className="text-ink-3 py-10">Loading…</div>}

      {!loading && runs.length === 0 && (
        <div className="bg-surface border border-line rounded-lg p-5 text-sm text-ink-2">
          No test runs recorded in the selected window. Once a k6 (or other) harness POSTs
          a run, it will appear here and as a band on the Dashboard charts.
        </div>
      )}

      {!loading && runs.length > 0 && (
        <div className="bg-surface border border-line rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-surface">
              <tr className="text-xs uppercase tracking-wider text-ink-3">
                <th className="px-3 py-2 text-left font-medium">Name</th>
                <th className="px-3 py-2 text-left font-medium">Type</th>
                <th className="px-3 py-2 text-left font-medium">Started</th>
                <th className="px-3 py-2 text-left font-medium">Duration</th>
                <th className="px-3 py-2 text-left font-medium">Metadata</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {runs.map((r) => {
                const startedMs = new Date(r.started_at).getTime()
                const inFlight = isZeroTime(r.ended_at)
                const endedMs = inFlight ? Date.now() : new Date(r.ended_at).getTime()
                return [
                  <tr key={r.id} className="hover:bg-surface">
                    <td className="px-3 py-3">
                      <div className="font-semibold text-ink">{r.name}</div>
                      <div className="text-[11px] text-ink-3 font-mono">{r.id}</div>
                    </td>
                    <td className="px-3 py-3 text-ink-2 font-mono text-xs">{r.type || '—'}</td>
                    <td className="px-3 py-3 text-ink-2 text-xs">{fmtTime(r.started_at)}</td>
                    <td className="px-3 py-3 text-xs">
                      {inFlight
                        ? <span className="text-warn">in flight</span>
                        : <span className="text-ink">{fmtDuration(endedMs - startedMs)}</span>}
                    </td>
                    <td className="px-3 py-3">
                      <MetadataBadges meta={r.metadata} />
                    </td>
                    <td className="px-3 py-3 text-right">
                      <button
                        onClick={() => setOpen(open === r.id ? null : r.id)}
                        className="text-xs px-2.5 py-1 rounded ring-1 ring-line text-ink-2 hover:text-ink"
                      >{open === r.id ? 'Hide' : 'Compare'}</button>
                    </td>
                  </tr>,
                  open === r.id && (
                    <tr key={`${r.id}-compare`}>
                      <td colSpan={6} className="px-3 pb-4 bg-surface">
                        <Comparison id={r.id} />
                      </td>
                    </tr>
                  ),
                ]
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// Comparison shows how production traffic behaved before, during and after
// a run, and how quickly latency recovered.
function Comparison({ id }) {
  const { get } = useAPI()
  const [cmp, setCmp] = useState(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    get(`/test-runs/${encodeURIComponent(id)}/compare`)
      .then(async (res) => {
        if (!alive) return
        if (res.ok) setCmp(await res.json())
        else setError((await res.json()).error || `HTTP ${res.status}`)
      })
      .catch((e) => alive && setError(e.message))
    return () => { alive = false }
  }, [get, id])

  if (error) return <p className="text-crit text-xs pt-3">{error}</p>
  if (!cmp) return <p className="text-ink-3 text-xs pt-3">Loading comparison…</p>

  const windows = [['Before', cmp.before], ['During', cmp.during], ['After', cmp.after]]
  const rows = [
    ['Requests', (w) => w.requests.toLocaleString()],
    ['Requests / s', (w) => w.rps.toFixed(2)],
    ['5xx rate', (w) => `${w.error_rate.toFixed(2)}%`],
    ['p50', (w) => fmtDuration(w.p50_ms)],
    ['p95', (w) => fmtDuration(w.p95_ms)],
    ['p99', (w) => fmtDuration(w.p99_ms)],
  ]

  let verdict
  if (cmp.before.requests === 0) verdict = 'No traffic before the run, so there is no baseline to compare with.'
  else if (!cmp.degraded) verdict = 'p95 stayed within 10% of the baseline during the run.'
  else if (cmp.recovery_seconds != null) {
    // Resolution is one minute, so anything under 60s means "the first minute".
    const when = cmp.recovery_seconds < 60
      ? 'within a minute of the run ending'
      : `${fmtDuration(cmp.recovery_seconds * 1000)} after the run ended`
    verdict = `p95 went from ${fmtDuration(cmp.before.p95_ms)} to ${fmtDuration(cmp.during.p95_ms)}, then recovered ${when}.`
  } else if (isZeroTime(cmp.run.ended_at)) verdict = 'The run is still in flight.'
  else verdict = `p95 went from ${fmtDuration(cmp.before.p95_ms)} to ${fmtDuration(cmp.during.p95_ms)} and has not recovered yet.`

  return (
    <div className="pt-3">
      <p className="text-sm text-ink mb-2">{verdict}</p>
      <table className="text-xs">
        <thead>
          <tr className="text-ink-3">
            <th className="pr-6 py-1 text-left font-medium" />
            {windows.map(([label]) => <th key={label} className="pr-6 py-1 text-right font-medium">{label}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map(([label, value]) => (
            <tr key={label}>
              <td className="pr-6 py-0.5 text-ink-2">{label}</td>
              {windows.map(([w, data]) => (
                <td key={w} className="pr-6 py-0.5 text-right font-mono text-ink">{value(data)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="text-[11px] text-ink-3 mt-2">
        Counts every request (not just sampled ones) at one-minute resolution; percentiles are within ~6%.
      </p>

      {cmp.routes?.length ? <RouteBreakdown routes={cmp.routes} /> : null}
      {cmp.database ? <DatabaseDeltas db={cmp.database} /> : null}
      <RunProfile id={id} profile={cmp.profile} />
    </div>
  )
}

// RouteBreakdown answers the question the global numbers hide: a load test
// hammers two or three endpoints, and an average over everything buries the
// one that fell over.
function RouteBreakdown({ routes }) {
  return (
    <div className="mt-4">
      <h4 className="text-[11px] uppercase tracking-wider text-ink-3 mb-1">Per route</h4>
      <table className="text-xs">
        <thead>
          <tr className="text-ink-3">
            <th className="pr-6 py-1 text-left font-medium">Route</th>
            <th className="pr-6 py-1 text-right font-medium">Requests</th>
            <th className="pr-6 py-1 text-right font-medium">5xx</th>
            <th className="pr-6 py-1 text-right font-medium">Avg before</th>
            <th className="pr-6 py-1 text-right font-medium">Avg during</th>
            <th className="pr-6 py-1 text-right font-medium">Change</th>
          </tr>
        </thead>
        <tbody>
          {routes.map((r) => (
            <tr key={`${r.method} ${r.route}`}>
              <td className="pr-6 py-0.5 font-mono text-ink">{r.method} {r.route}</td>
              <td className="pr-6 py-0.5 text-right font-mono text-ink">{r.during.requests.toLocaleString()}</td>
              <td className={`pr-6 py-0.5 text-right font-mono ${r.during.error_rate > 1 ? 'text-crit' : 'text-ink'}`}>
                {r.during.error_rate.toFixed(1)}%
              </td>
              <td className="pr-6 py-0.5 text-right font-mono text-ink-2">
                {r.before.requests ? fmtDuration(r.before.avg_ms) : '—'}
              </td>
              <td className="pr-6 py-0.5 text-right font-mono text-ink">{fmtDuration(r.during.avg_ms)}</td>
              <td className={`pr-6 py-0.5 text-right font-mono ${
                r.slowdown_ratio >= 1.5 ? 'text-crit' : r.slowdown_ratio >= 1.1 ? 'text-warn' : 'text-ink-3'}`}>
                {r.slowdown_ratio ? `${r.slowdown_ratio.toFixed(1)}×` : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="text-[11px] text-ink-3 mt-1">
        Latency here is an average, not a percentile: per-route percentiles are not kept per minute.
      </p>
    </div>
  )
}

// DatabaseDeltas shows whether each request started doing more database
// work under load, which is how an N+1 announces itself.
function DatabaseDeltas({ db }) {
  return (
    <div className="mt-4">
      <h4 className="text-[11px] uppercase tracking-wider text-ink-3 mb-1">Database</h4>
      <table className="text-xs">
        <tbody>
          <tr>
            <td className="pr-6 py-0.5 text-ink-2">Queries per request</td>
            <td className="pr-6 py-0.5 text-right font-mono text-ink-2">
              {db.queries_per_request_before.toFixed(2)} before
            </td>
            <td className={`pr-6 py-0.5 text-right font-mono ${db.queries_per_request_increased ? 'text-warn' : 'text-ink'}`}>
              {db.queries_per_request_during.toFixed(2)} during
            </td>
          </tr>
          <tr>
            <td className="pr-6 py-0.5 text-ink-2">N+1 detections</td>
            <td className="pr-6 py-0.5 text-right font-mono text-ink-2">{db.n1_detections_before} before</td>
            <td className={`pr-6 py-0.5 text-right font-mono ${db.n1_detections_during > db.n1_detections_before ? 'text-crit' : 'text-ink'}`}>
              {db.n1_detections_during} during
            </td>
          </tr>
          <tr>
            <td className="pr-6 py-0.5 text-ink-2">Slow queries during</td>
            <td />
            <td className="pr-6 py-0.5 text-right font-mono text-ink">{db.slow_queries_during}</td>
          </tr>
        </tbody>
      </table>
    </div>
  )
}

function MetadataBadges({ meta }) {
  if (!meta || Object.keys(meta).length === 0) return <span className="text-ink-3 text-xs">—</span>
  return (
    <div className="flex flex-wrap gap-1">
      {Object.entries(meta).slice(0, 6).map(([k, v]) => (
        <span
          key={k}
          className="text-[11px] px-1.5 py-0.5 rounded bg-surface-2 text-ink-2 font-mono"
          title={JSON.stringify(v)}
        >
          {k}={typeof v === 'object' ? '…' : String(v).slice(0, 24)}
        </span>
      ))}
    </div>
  )
}

// RunProfile is the half no external tool can do: the flame graph of the
// run itself, captured in-process while the load was on.
function RunProfile({ id, profile }) {
  const { token } = useAuth()
  const [svg, setSvg] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  if (!profile) {
    return (
      <p className="text-[11px] text-ink-3 mt-4">
        No CPU profile for this run. Pulse captures one when a run&apos;s start is registered
        with profiling enabled — <code className="text-accent">pulse.WithProfiling()</code> plus{' '}
        <code className="text-accent">PULSE_PROFILE_ENABLED=true</code>.
      </p>
    )
  }
  if (profile.in_flight && !profile.available) {
    return <p className="text-[11px] text-warn mt-4">Profiling this run now. The profile is kept when its end is registered.</p>
  }

  const show = async () => {
    setLoading(true); setError('')
    try {
      const res = await fetch(`/pulse/api/test-runs/${encodeURIComponent(id)}/profile?format=svg`,
        { headers: { Authorization: `Bearer ${token}` } })
      const text = await res.text()
      if (!res.ok) {
        try {
          const j = JSON.parse(text)
          setError(j.hint || j.error || `HTTP ${res.status}`)
        } catch {
          setError(`HTTP ${res.status}`)
        }
        return
      }
      setSvg(text)
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="mt-4">
      <div className="flex items-center gap-3">
        <h4 className="text-[11px] uppercase tracking-wider text-ink-3">CPU profile of the run</h4>
        <button onClick={show} disabled={loading}
          className="text-xs px-2.5 py-1 rounded ring-1 ring-line text-ink-2 hover:text-ink disabled:opacity-50">
          {loading ? 'Rendering…' : svg ? 'Refresh' : 'Show flame graph'}
        </button>
        <a className="text-xs text-accent"
          href={`/pulse/api/test-runs/${encodeURIComponent(id)}/profile`}
          title="Opens the raw pprof profile; go tool pprof can read it">
          Download .pprof
        </a>
        <span className="text-[11px] text-ink-3">{Math.round((profile.bytes || 0) / 1024)} KB</span>
      </div>
      {error ? <p className="text-crit text-xs mt-2">{error}</p> : null}
      {svg ? (
        <div className="mt-2 rounded border border-line bg-white overflow-x-auto"
          dangerouslySetInnerHTML={{ __html: svg }} />
      ) : null}
    </div>
  )
}
