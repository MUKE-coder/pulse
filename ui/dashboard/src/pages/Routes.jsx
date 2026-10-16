import { useState, useEffect, useCallback } from 'react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer } from 'recharts'
import { useAPI } from '../hooks/useAPI'
import { useUI, usePolling } from '../context/UIContext'
import { useTheme } from '../hooks/useTheme'
import DataTable from '../components/DataTable'
import Modal from '../components/Modal'
import { PageHead } from '../components/ui'

function fmtDuration(ns) {
  if (ns === undefined || ns === null) return '—'
  const msv = ns / 1e6
  if (msv < 1) return `${(msv * 1000).toFixed(0)}µs`
  if (msv < 1000) return `${msv.toFixed(msv < 10 ? 1 : 0)}ms`
  return `${(msv / 1000).toFixed(2)}s`
}

// Methods read as a word, not a colour: the tint only reinforces it.
const methodTone = {
  GET: 'ok', POST: 'info', PUT: 'warn', PATCH: 'warn', DELETE: 'crit',
}

export default function RoutesPage() {
  const { get } = useAPI()
  const { range, rangeLabel } = useUI()
  const { palette } = useTheme()
  const [routes, setRoutes] = useState([])
  const [search, setSearch] = useState('')
  const [baselines, setBaselines] = useState({})
  const [detail, setDetail] = useState(null)
  const [loading, setLoading] = useState(true)

  const fetchRoutes = useCallback(async () => {
    try {
      const params = new URLSearchParams({ range })
      if (search) params.set('search', search)
      const res = await get(`/routes?${params}`)
      if (res.ok) setRoutes(await res.json())
    } catch {}
    setLoading(false)
  }, [get, range, search])

  usePolling(fetchRoutes, 15000, [fetchRoutes])

  // What each route usually does, so the table can say a route got slower
  // rather than only that it is slow. Independent of the time range.
  useEffect(() => {
    get('/routes/baselines')
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (!data) return
        const byRoute = {}
        for (const b of data.baselines || []) byRoute[`${b.method} ${b.route}`] = b
        setBaselines(byRoute)
      })
      .catch(() => {})
  }, [get])

  const fetchDetail = async (row) => {
    try {
      const res = await get(`/routes/${row.method}${row.path}?range=${range}`)
      if (res.ok) setDetail(await res.json())
    } catch {}
  }

  const columns = [
    { key: 'method', label: 'Method', render: (v) => (
      <span className={`badge badge-${methodTone[v] || 'neutral'}`}>{v}</span>
    )},
    { key: 'path', label: 'Path', mono: true },
    { key: 'request_count', label: 'Requests', num: true, render: (v) => v?.toLocaleString() },
    { key: 'error_rate', label: 'Error rate', num: true, render: (v) => (
      <span className={v > 5 ? 'crit' : v > 1 ? 'warn' : ''}>{v?.toFixed(1)}%</span>
    )},
    { key: 'avg_latency', label: 'Avg', num: true, render: fmtDuration },
    { key: 'p95_latency', label: 'p95', num: true, render: fmtDuration },
    { key: 'p99_latency', label: 'p99', num: true, render: fmtDuration },
    { key: 'rpm', label: 'RPM', num: true, render: (v) => v?.toFixed(1) },
    { key: 'baseline', label: 'vs usual', num: true, render: (_, row) => {
      const b = baselines[`${row.method} ${row.path}`]
      if (!b || !b.ratio) {
        return <span className="muted" title="Not enough hours of history yet">—</span>
      }
      const pct = Math.round((b.ratio - 1) * 100)
      return (
        <span className={b.regressed ? 'crit' : pct <= -20 ? 'ok' : 'muted'}
          title={`p95 ${fmtDuration(b.current_p95)} against a ${fmtDuration(b.baseline_p95)} baseline over ${b.hours}h`}>
          {pct > 0 ? '+' : ''}{pct}%
        </span>
      )
    }},
    { key: 'trend', label: 'Trend', render: (v) => (
      <span className={v === 'up' ? 'ok' : v === 'down' ? 'crit' : 'muted'}>
        {v === 'up' ? '↑' : v === 'down' ? '↓' : '—'}
      </span>
    )},
  ]

  const latencyBars = detail ? [
    { name: 'Avg', value: detail.avg_latency / 1e6 },
    { name: 'p50', value: detail.p50_latency / 1e6 },
    { name: 'p75', value: detail.p75_latency / 1e6 },
    { name: 'p90', value: detail.p90_latency / 1e6 },
    { name: 'p95', value: detail.p95_latency / 1e6 },
    { name: 'p99', value: detail.p99_latency / 1e6 },
  ] : []

  const regressed = Object.values(baselines).filter((b) => b.regressed).length

  return (
    <>
      <PageHead
        title="Routes"
        subtitle={`${routes.length} ${routes.length === 1 ? 'route' : 'routes'} in the ${rangeLabel.toLowerCase()}`
          + (regressed ? ` · ${regressed} slower than usual` : '')}
        actions={
          <label className="field" style={{ width: 220 }}>
            <input className="input" placeholder="Filter routes…" value={search}
              onChange={(e) => setSearch(e.target.value)} aria-label="Filter routes" />
          </label>
        }
      />

      {loading
        ? <p className="muted">Loading…</p>
        : <DataTable columns={columns} data={routes || []} onRowClick={fetchDetail}
          emptyText="No routes tracked yet in this window." />}

      <Modal open={!!detail} onClose={() => setDetail(null)}
        title={`${detail?.method || ''} ${detail?.path || ''}`} width={700}>
        {detail && (
          <div>
            <div className="grid-3" style={{ marginBottom: 20 }}>
              <div className="card">
                <div className="eyebrow">Requests</div>
                <div className="kpi-value" style={{ fontSize: 22, marginTop: 6 }}>
                  {detail.request_count?.toLocaleString()}
                </div>
              </div>
              <div className="card">
                <div className="eyebrow">Error rate</div>
                <div className={`kpi-value${detail.error_rate > 5 ? ' crit' : ''}`}
                  style={{ fontSize: 22, marginTop: 6 }}>
                  {detail.error_rate?.toFixed(1)}%
                </div>
              </div>
              <div className="card">
                <div className="eyebrow">Requests / min</div>
                <div className="kpi-value" style={{ fontSize: 22, marginTop: 6 }}>{detail.rpm?.toFixed(1)}</div>
              </div>
            </div>

            <h4 className="eyebrow" style={{ marginBottom: 8 }}>Latency distribution (ms)</h4>
            <ResponsiveContainer width="100%" height={180}>
              <BarChart data={latencyBars}>
                <XAxis dataKey="name" tick={{ fill: palette.text3, fontSize: 11 }} axisLine={false} tickLine={false} />
                <YAxis tick={{ fill: palette.text3, fontSize: 11 }} axisLine={false} tickLine={false} width={50} />
                <Tooltip contentStyle={{
                  background: palette.surface, border: `1px solid ${palette.border}`,
                  borderRadius: 6, fontSize: 12, color: palette.text,
                }} />
                <Bar dataKey="value" fill={palette.accent} radius={[4, 4, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>

            {detail.status_codes && Object.keys(detail.status_codes).length > 0 && (
              <div style={{ marginTop: 16 }}>
                <h4 className="eyebrow" style={{ marginBottom: 8 }}>Status codes</h4>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  {Object.entries(detail.status_codes).map(([code, count]) => (
                    <span key={code}
                      className={`badge badge-${code.startsWith('2') ? 'ok' : code.startsWith('4') ? 'warn' : 'crit'}`}>
                      {code}: {count}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </Modal>
    </>
  )
}
