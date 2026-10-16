import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

// SLOs page: live compliance + burn-rate windows per configured SLO.
// Backend contract: GET /pulse/api/slos returns []SLOStatus (pulse/slo.go).

function statusBadge(status) {
  // status is one of: "ok" | "fast-burn" | "slow-burn" | "exhausted"
  const palette = {
    'ok':         'bg-ok/15 text-ok ring-ok/30',
    'fast-burn':  'bg-crit/15 text-crit ring-crit/30',
    'slow-burn':  'bg-warn/15 text-warn ring-warn/30',
    'exhausted':  'bg-accent/15 text-accent ring-accent/30',
  }
  return palette[status] || 'bg-surface-2 text-ink-2 ring-line-strong'
}

function ProgressBar({ value, target }) {
  // value is "consumed %" (0-100+). >=100 means budget exhausted; clamp the
  // visible bar at 100% but tint red.
  const pct = Math.min(100, value)
  const tint = value >= 100 ? 'bg-accent'
             : value >= 50  ? 'bg-crit'
             : value >= 25  ? 'bg-warn'
             : 'bg-ok'
  return (
    <div className="w-full h-1.5 bg-surface-2 rounded overflow-hidden">
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

  if (loading) return <div className="text-ink-3 py-10">Loading…</div>

  if (slos.length === 0) {
    return (
      <div className="max-w-3xl">
        <h1 className="text-[22px] font-semibold tracking-[-0.01em] text-ink mb-2">SLOs</h1>
        <p className="text-ink-2 text-sm mb-6">No SLOs configured.</p>
        <div className="bg-surface border border-line rounded-lg p-5 text-sm text-ink-2 leading-relaxed">
          <p className="mb-3">Declare a Service-Level Objective in your <code className="text-accent">pulse.Mount(…)</code> call:</p>
          <pre className="bg-surface border border-line rounded p-3 text-xs overflow-x-auto"><code>{`pulse.WithSLO(pulse.SLO{
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
          <h1 className="text-[22px] font-semibold tracking-[-0.01em] text-ink">SLOs</h1>
          <p className="text-ink-3 text-xs mt-0.5">{slos.length} objective{slos.length !== 1 && 's'} tracked · auto-refresh 5 s</p>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4">
        {slos.map((s) => (
          <article key={s.name} className="bg-surface border border-line rounded-lg p-5">
            <header className="flex items-start justify-between mb-3">
              <div>
                <h2 className="text-base font-semibold text-ink">{s.name}</h2>
                <p className="text-xs text-ink-3 mt-0.5 font-mono">{s.indicator}</p>
              </div>
              <span className={`px-2.5 py-1 rounded-full text-xs font-medium ring-1 ${statusBadge(s.status)}`}>
                {s.status}
              </span>
            </header>

            {s.data_coverage < 1 && (
              <p className="text-xs text-warn/90 mb-3">
                Pulse holds data for {(s.data_coverage * 100).toFixed(0)}% of this {s.window} window so far —
                compliance covers only that part.
              </p>
            )}

            <div className="grid grid-cols-4 gap-4 mb-4 text-sm">
              <Stat label="Target"        value={`${(s.target * 100).toFixed(2)}%`} />
              <Stat label="Compliance"    value={`${(s.compliance * 100).toFixed(3)}%`} />
              <Stat label="Events"        value={`${s.good_events?.toLocaleString() ?? 0} / ${s.total_events?.toLocaleString() ?? 0}`} />
              <Stat label="Window"        value={s.window} />
            </div>

            <div className="mb-4">
              <div className="flex items-center justify-between mb-1 text-xs">
                <span className="text-ink-2">Error budget</span>
                <span className={`font-mono ${s.budget_consumed_pct >= 100 ? 'text-accent' : 'text-ink-2'}`}>
                  {s.budget_consumed_pct?.toFixed(1)}% consumed · {Math.max(0, s.budget_remaining_pct ?? 0).toFixed(1)}% left
                </span>
              </div>
              <ProgressBar value={s.budget_consumed_pct} target={100} />
            </div>

            <div className="border-t border-line pt-3">
              <p className="text-xs uppercase tracking-wider text-ink-3 mb-2">Burn-rate windows</p>
              <div className="space-y-1.5">
                {(s.burn_windows || []).map((w) => (
                  <div key={w.name} className="grid grid-cols-12 items-center gap-2 text-xs">
                    <div className="col-span-2 text-ink-2 font-medium">{w.name}</div>
                    <div className="col-span-2 text-ink-3">{w.window} <span className="text-ink-3">/ {w.short_window}</span></div>
                    <div className="col-span-2 text-ink-2">
                      <span className="font-mono text-ink">{(w.compliance * 100).toFixed(2)}%</span>
                    </div>
                    <div className="col-span-4 text-ink-2">
                      burn <span className={`font-mono ${w.firing ? 'text-crit' : 'text-ink'}`}>{w.burn_rate?.toFixed(2)}×</span>
                      <span className="text-ink-3"> · short {w.short_burn_rate?.toFixed(2)}×</span>
                      <span className="text-ink-3"> / threshold {w.threshold}×</span>
                    </div>
                    <div className="col-span-2 text-right">
                      {w.firing
                        ? <span className="text-crit font-medium">FIRING</span>
                        : w.events < w.min_events
                          ? <span className="text-ink-3" title="Not enough traffic in the window to alert yet">{w.events}/{w.min_events} events</span>
                          : <span className="text-ok">ok</span>}
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
      <p className="text-xs text-ink-3 uppercase tracking-wider">{label}</p>
      <p className="text-sm font-semibold text-ink mt-0.5">{value}</p>
    </div>
  )
}
