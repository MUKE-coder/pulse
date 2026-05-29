import { useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

// USE-method dashboard: U / S / E grid per resource.
// Backend: GET /pulse/api/use returns USESnapshot (pulse/use.go).

const levelStyles = {
  green:   'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30',
  amber:   'bg-amber-500/10  text-amber-300  ring-amber-500/30',
  red:     'bg-red-500/10    text-red-300    ring-red-500/30',
  unknown: 'bg-slate-700/30  text-slate-400  ring-slate-600/30',
}

function Cell({ metric }) {
  if (!metric) return <td className="px-3 py-3">—</td>
  const cls = levelStyles[metric.level] || levelStyles.unknown
  return (
    <td className="px-3 py-3 align-top">
      <div className={`inline-block px-2 py-1 rounded ring-1 ${cls} text-xs font-medium`}>
        {metric.display}
      </div>
      <p className="text-[11px] text-slate-500 mt-1 leading-snug">{metric.description}</p>
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

  if (loading) return <div className="text-slate-500 py-10">Loading…</div>

  const resources = snap?.resources || []

  return (
    <div>
      <div className="flex items-center justify-between mb-2">
        <h1 className="text-xl font-bold text-slate-200">USE Method</h1>
        <span className="text-xs text-slate-500">
          {snap?.host && <>host <span className="text-slate-300">{snap.host}</span> · </>}
          {snap?.os && <>os <span className="text-slate-300">{snap.os}</span> · </>}
          auto-refresh 5 s
        </span>
      </div>
      <p className="text-slate-500 text-xs mb-5">
        Brendan Gregg's <a className="text-indigo-400 hover:text-indigo-300" href="https://brendangregg.com/usemethod.html" target="_blank" rel="noreferrer">USE method</a>:
        for every resource, walk Utilization / Saturation / Errors.
        Green / amber / red bands let you scan for the bottleneck in one glance.
      </p>

      {resources.length === 0 && (
        <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-5 text-sm text-slate-400">
          USE sampler is disabled, or no data has arrived yet.
          Enable it with the default <code className="text-indigo-300">pulse.Mount(…)</code>
          (it's on by default), or re-enable via <code className="text-indigo-300">pulse.WithUSEDisabled()=false</code>.
        </div>
      )}

      {resources.length > 0 && (
        <div className="bg-slate-900/60 border border-slate-800 rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-slate-950/50">
              <tr className="text-xs uppercase tracking-wider text-slate-500">
                <th className="px-3 py-2 text-left font-medium w-28">Resource</th>
                <th className="px-3 py-2 text-left font-medium">Utilization</th>
                <th className="px-3 py-2 text-left font-medium">Saturation</th>
                <th className="px-3 py-2 text-left font-medium">Errors</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800">
              {resources.map((r) => (
                <tr key={r.name} className="hover:bg-slate-900">
                  <td className="px-3 py-3 font-semibold text-slate-200 align-top">{r.name}</td>
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
