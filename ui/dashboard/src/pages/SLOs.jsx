import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

// SLOs page: live compliance + burn-rate windows per configured SLO.
// Backend contract: GET /pulse/api/slos returns []SLOStatus (pulse/slo.go).

function statusBadge(status) {
  // status is one of: "ok" | "fast-burn" | "slow-burn" | "exhausted"
  const palette = {
    'ok':         'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
    'fast-burn':  'bg-red-500/15 text-red-300 ring-red-500/30',
    'slow-burn':  'bg-amber-500/15 text-amber-300 ring-amber-500/30',
    'exhausted':  'bg-purple-500/15 text-purple-300 ring-purple-500/30',
  }
  return palette[status] || 'bg-slate-500/15 text-slate-300 ring-slate-500/30'
}

function ProgressBar({ value, target }) {
  // value is "consumed %" (0-100+). >=100 means budget exhausted; clamp the
  // visible bar at 100% but tint red.
  const pct = Math.min(100, value)
  const tint = value >= 100 ? 'bg-purple-500'
             : value >= 50  ? 'bg-red-500'
             : value >= 25  ? 'bg-amber-500'
             : 'bg-emerald-500'
  return (
    <div className="w-full h-1.5 bg-slate-800 rounded overflow-hidden">
      <div className={`h-full ${tint}`} style={{ width: `${pct}%` }} />
    </div>
  )
}

export default function SLOs() {
  const { get } = useAPI()
  const [slos, setSlos] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    const tick = async () => {
      try {
        const res = await get('/slos')
        if (res.ok && alive) setSlos(await res.json())
      } catch {}
      if (alive) setLoading(false)
    }
    tick()
    const id = setInterval(tick, 5000)
    return () => { alive = false; clearInterval(id) }
  }, [get])

  if (loading) return <div className="text-slate-500 py-10">Loading…</div>

  if (slos.length === 0) {
    return (
      <div className="max-w-3xl">
        <h1 className="text-xl font-bold text-slate-200 mb-2">SLOs</h1>
        <p className="text-slate-400 text-sm mb-6">No SLOs configured.</p>
        <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-5 text-sm text-slate-300 leading-relaxed">
          <p className="mb-3">Declare a Service-Level Objective in your <code className="text-indigo-300">pulse.Mount(…)</code> call:</p>
          <pre className="bg-slate-950 border border-slate-800 rounded p-3 text-xs overflow-x-auto"><code>{`pulse.WithSLO(pulse.SLO{
    Name:      "API availability",
    Target:    0.999,
    Window:    28 * 24 * time.Hour,
    Indicator: pulse.SLIErrorRate{Routes: []string{"/api/*"}},
})`}</code></pre>
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-xl font-bold text-slate-200">SLOs</h1>
          <p className="text-slate-500 text-xs mt-0.5">{slos.length} objective{slos.length !== 1 && 's'} tracked · auto-refresh 5 s</p>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4">
        {slos.map((s) => (
          <article key={s.name} className="bg-slate-900/60 border border-slate-800 rounded-lg p-5">
            <header className="flex items-start justify-between mb-3">
              <div>
                <h2 className="text-base font-semibold text-slate-100">{s.name}</h2>
                <p className="text-xs text-slate-500 mt-0.5 font-mono">{s.indicator}</p>
              </div>
              <span className={`px-2.5 py-1 rounded-full text-xs font-medium ring-1 ${statusBadge(s.status)}`}>
                {s.status}
              </span>
            </header>

            <div className="grid grid-cols-4 gap-4 mb-4 text-sm">
              <Stat label="Target"        value={`${(s.target * 100).toFixed(2)}%`} />
              <Stat label="Compliance"    value={`${(s.compliance * 100).toFixed(3)}%`} />
              <Stat label="Events"        value={`${s.good_events?.toLocaleString() ?? 0} / ${s.total_events?.toLocaleString() ?? 0}`} />
              <Stat label="Window"        value={s.window} />
            </div>

            <div className="mb-4">
              <div className="flex items-center justify-between mb-1 text-xs">
                <span className="text-slate-400">Error budget</span>
                <span className={`font-mono ${s.budget_consumed_pct >= 100 ? 'text-purple-300' : 'text-slate-300'}`}>
                  {s.budget_consumed_pct?.toFixed(1)}% consumed · {Math.max(0, s.budget_remaining_pct ?? 0).toFixed(1)}% left
                </span>
              </div>
              <ProgressBar value={s.budget_consumed_pct} target={100} />
            </div>

            <div className="border-t border-slate-800 pt-3">
              <p className="text-xs uppercase tracking-wider text-slate-500 mb-2">Burn-rate windows</p>
              <div className="space-y-1.5">
                {(s.burn_windows || []).map((w) => (
                  <div key={w.name} className="grid grid-cols-12 items-center gap-2 text-xs">
                    <div className="col-span-2 text-slate-300 font-medium">{w.name}</div>
                    <div className="col-span-2 text-slate-500">{w.window}</div>
                    <div className="col-span-3 text-slate-400">
                      compliance <span className="font-mono text-slate-200">{(w.compliance * 100).toFixed(2)}%</span>
                    </div>
                    <div className="col-span-3 text-slate-400">
                      burn <span className={`font-mono ${w.firing ? 'text-red-400' : 'text-slate-200'}`}>{w.burn_rate?.toFixed(2)}×</span>
                      <span className="text-slate-600"> / threshold {w.threshold}×</span>
                    </div>
                    <div className="col-span-2 text-right">
                      {w.firing
                        ? <span className="text-red-400 font-medium">FIRING</span>
                        : <span className="text-emerald-400">ok</span>}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </article>
        ))}
      </div>
    </div>
  )
}

function Stat({ label, value }) {
  return (
    <div>
      <p className="text-xs text-slate-500 uppercase tracking-wider">{label}</p>
      <p className="text-sm font-semibold text-slate-100 mt-0.5">{value}</p>
    </div>
  )
}
