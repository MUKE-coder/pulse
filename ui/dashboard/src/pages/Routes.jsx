import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import DataTable from '../components/DataTable'
import Modal from '../components/Modal'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer } from 'recharts'

function fmtDuration(ns) {
  const ms = ns / 1e6
  if (ms < 1) return `${(ms * 1000).toFixed(0)}us`
  if (ms < 1000) return `${ms.toFixed(0)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

export default function RoutesPage() {
  const { get } = useAPI()
  const [routes, setRoutes] = useState([])
  const [search, setSearch] = useState('')
  const [range, setRange] = useState('1h')
  const [detail, setDetail] = useState(null)
  const [loading, setLoading] = useState(true)

  const fetchRoutes = async () => {
    try {
      const params = new URLSearchParams({ range })
      if (search) params.set('search', search)
      const res = await get(`/routes?${params}`)
      if (res.ok) setRoutes(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchRoutes() }, [range, search])

  const fetchDetail = async (row) => {
    try {
      const res = await get(`/routes/${row.method}${row.path}?range=${range}`)
      if (res.ok) setDetail(await res.json())
    } catch {}
  }

  const columns = [
    { key: 'method', label: 'Method', render: (v) => (
      <span style={{
        padding: '2px 8px', borderRadius: 4, fontSize: 11, fontWeight: 700,
        background: v === 'GET' ? '#22c55e18' : v === 'POST' ? '#6366f118' : v === 'PUT' ? '#f59e0b18' : v === 'DELETE' ? '#ef444418' : '#64748b18',
        color: v === 'GET' ? '#22c55e' : v === 'POST' ? '#818cf8' : v === 'PUT' ? '#f59e0b' : v === 'DELETE' ? '#ef4444' : '#94a3b8',
      }}>{v}</span>
    )},
    { key: 'path', label: 'Path' },
    { key: 'request_count', label: 'Requests', render: (v) => v?.toLocaleString() },
    { key: 'error_rate', label: 'Error Rate', render: (v) => (
      <span style={{ color: v > 5 ? '#ef4444' : v > 1 ? '#f59e0b' : '#22c55e' }}>{v?.toFixed(1)}%</span>
    )},
    { key: 'avg_latency', label: 'Avg', render: (v) => fmtDuration(v) },
    { key: 'p95_latency', label: 'P95', render: (v) => fmtDuration(v) },
    { key: 'p99_latency', label: 'P99', render: (v) => fmtDuration(v) },
    { key: 'rpm', label: 'RPM', render: (v) => v?.toFixed(1) },
    { key: 'trend', label: 'Trend', render: (v) => (
      <span style={{ color: v === 'up' ? '#22c55e' : v === 'down' ? '#ef4444' : '#64748b' }}>
        {v === 'up' ? '↑' : v === 'down' ? '↓' : '—'}
      </span>
    )},
  ]

  const latencyBars = detail ? [
    { name: 'Avg', value: detail.avg_latency / 1e6 },
    { name: 'P50', value: detail.p50_latency / 1e6 },
    { name: 'P75', value: detail.p75_latency / 1e6 },
    { name: 'P90', value: detail.p90_latency / 1e6 },
    { name: 'P95', value: detail.p95_latency / 1e6 },
    { name: 'P99', value: detail.p99_latency / 1e6 },
  ] : []

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Routes</h1>
        <div style={{ display: 'flex', gap: 8 }}>
          <input
            placeholder="Search routes..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            style={{ width: 200 }}
          />
          <select value={range} onChange={(e) => setRange(e.target.value)}>
            <option value="5m">5m</option>
            <option value="15m">15m</option>
            <option value="1h">1h</option>
            <option value="6h">6h</option>
            <option value="24h">24h</option>
            <option value="7d">7d</option>
          </select>
        </div>
      </div>

      {loading ? <p style={{ color: '#64748b' }}>Loading...</p> :
        <DataTable columns={columns} data={routes || []} onRowClick={fetchDetail} emptyText="No routes tracked yet" />
      }

      <Modal open={!!detail} onClose={() => setDetail(null)} title={`${detail?.method} ${detail?.path}`} width={700}>
        {detail && (
          <div>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, marginBottom: 20 }}>
              <div style={{ background: '#0a0a12', borderRadius: 6, padding: 12 }}>
                <p style={{ color: '#64748b', fontSize: 11 }}>REQUESTS</p>
                <p style={{ fontSize: 20, fontWeight: 700, color: '#818cf8' }}>{detail.request_count?.toLocaleString()}</p>
              </div>
              <div style={{ background: '#0a0a12', borderRadius: 6, padding: 12 }}>
                <p style={{ color: '#64748b', fontSize: 11 }}>ERROR RATE</p>
                <p style={{ fontSize: 20, fontWeight: 700, color: detail.error_rate > 5 ? '#ef4444' : '#22c55e' }}>{detail.error_rate?.toFixed(1)}%</p>
              </div>
              <div style={{ background: '#0a0a12', borderRadius: 6, padding: 12 }}>
                <p style={{ color: '#64748b', fontSize: 11 }}>RPM</p>
                <p style={{ fontSize: 20, fontWeight: 700, color: '#f59e0b' }}>{detail.rpm?.toFixed(1)}</p>
              </div>
            </div>

            <h4 style={{ fontSize: 13, color: '#64748b', marginBottom: 8, fontWeight: 600 }}>LATENCY DISTRIBUTION (ms)</h4>
            <ResponsiveContainer width="100%" height={180}>
              <BarChart data={latencyBars}>
                <XAxis dataKey="name" tick={{ fill: '#64748b', fontSize: 11 }} axisLine={false} tickLine={false} />
                <YAxis tick={{ fill: '#64748b', fontSize: 11 }} axisLine={false} tickLine={false} width={50} />
                <Tooltip contentStyle={{ background: '#16161e', border: '1px solid #2a2a3e', borderRadius: 6, fontSize: 12 }} />
                <Bar dataKey="value" fill="#6366f1" radius={[4, 4, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>

            {detail.status_codes && Object.keys(detail.status_codes).length > 0 && (
              <div style={{ marginTop: 16 }}>
                <h4 style={{ fontSize: 13, color: '#64748b', marginBottom: 8, fontWeight: 600 }}>STATUS CODES</h4>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  {Object.entries(detail.status_codes).map(([code, count]) => (
                    <span key={code} style={{
                      padding: '4px 10px', borderRadius: 6, fontSize: 12, fontWeight: 600,
                      background: code.startsWith('2') ? '#22c55e18' : code.startsWith('4') ? '#f59e0b18' : '#ef444418',
                      color: code.startsWith('2') ? '#22c55e' : code.startsWith('4') ? '#f59e0b' : '#ef4444',
                    }}>{code}: {count}</span>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </Modal>
    </div>
  )
}
