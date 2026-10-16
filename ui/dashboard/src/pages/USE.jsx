import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

// USE-method dashboard: U / S / E grid per resource.
// Backend: GET /pulse/api/use returns USESnapshot (pulse/use.go).

const levelStyles = {
  green:   'bg-ok/10 text-ok ring-ok/30',
  amber:   'bg-warn/10  text-warn  ring-warn/30',
  red:     'bg-crit/10    text-crit    ring-crit/30',
  unknown: 'bg-surface-2  text-ink-2  ring-line-strong',
}

function Cell({ metric }) {
  if (!metric) return <td className="px-3 py-3">—</td>
  const cls = levelStyles[metric.level] || levelStyles.unknown
  return (
    <td className="px-3 py-3 align-top">
      <div className={`inline-block px-2 py-1 rounded ring-1 ${cls} text-xs font-medium`}>
        {metric.display}
      </div>
      <p className="text-[11px] text-ink-3 mt-1 leading-snug">{metric.description}</p>
    </td>
  )
}

export default function USE() {
  const { get } = useAPI()
  const [snap, setSnap] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    const tick = async () => {
      try {
        const res = await get('/use')
        if (res.ok && alive) setSnap(await res.json())
      } catch {}
      if (alive) setLoading(false)
    }
    tick()
    const id = setInterval(tick, 5000)
    return () => { alive = false; clearInterval(id) }
  }, [get])

  if (loading) return <div className="text-ink-3 py-10">Loading…</div>

  const resources = snap?.resources || []

  return (
    <div>
      <div className="flex items-center justify-between mb-2">
        <h1 className="text-[22px] font-semibold tracking-[-0.01em] text-ink">USE Method</h1>
        <span className="text-xs text-ink-3">
          {snap?.host && <>host <span className="text-ink-2">{snap.host}</span> · </>}
          {snap?.os && <>os <span className="text-ink-2">{snap.os}</span> · </>}
          auto-refresh 5 s
        </span>
      </div>
      <p className="text-ink-3 text-xs mb-5">
        Brendan Gregg's <a className="text-accent hover:text-accent" href="https://brendangregg.com/usemethod.html" target="_blank" rel="noreferrer">USE method</a>:
        for every resource, walk Utilization / Saturation / Errors.
        Green / amber / red bands let you scan for the bottleneck in one glance.
      </p>

      {resources.length === 0 && (
        <div className="bg-surface border border-line rounded-lg p-5 text-sm text-ink-2">
          USE sampler is disabled, or no data has arrived yet.
          Enable it with the default <code className="text-accent">pulse.Mount(…)</code>
          (it's on by default), or re-enable via <code className="text-accent">pulse.WithUSEDisabled()=false</code>.
        </div>
      )}

      {resources.length > 0 && (
        <div className="bg-surface border border-line rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-surface">
              <tr className="text-xs uppercase tracking-wider text-ink-3">
                <th className="px-3 py-2 text-left font-medium w-28">Resource</th>
                <th className="px-3 py-2 text-left font-medium">Utilization</th>
                <th className="px-3 py-2 text-left font-medium">Saturation</th>
                <th className="px-3 py-2 text-left font-medium">Errors</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {resources.map((r) => (
                <tr key={r.name} className="hover:bg-surface">
                  <td className="px-3 py-3 font-semibold text-ink align-top">{r.name}</td>
                  <Cell metric={r.utilization} />
                  <Cell metric={r.saturation} />
                  <Cell metric={r.errors} />
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
