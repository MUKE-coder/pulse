import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import StatCard from '../components/StatCard'
import DataTable from '../components/DataTable'
import StatusBadge from '../components/StatusBadge'
import { AreaChart, Area, XAxis, YAxis, Tooltip, ResponsiveContainer } from 'recharts'

function fmt(ms) {
  if (ms < 1) return `${(ms * 1000).toFixed(0)}us`
  if (ms < 1000) return `${ms.toFixed(0)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function fmtDuration(ns) {
  const ms = ns / 1e6
  return fmt(ms)
}

export default function Dashboard() {
  const { get } = useAPI()
  const [overview, setOverview] = useState(null)
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['overview'])

  const fetchData = async () => {
    try {
      const res = await get('/overview?range=1h')
      if (res.ok) setOverview(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchData() }, [])

  useEffect(() => {
    if (lastMessage?.type === 'overview' && lastMessage?.data) {
      setOverview(lastMessage.data)
    }
  }, [lastMessage])

  if (loading) return <div style={{ color: '#64748b', padding: 40 }}>Loading...</div>

  const o = overview || {}

  const throughputData = (o.throughput_series || []).map((p) => ({
    time: new Date(p.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    value: p.value,
  }))

  const errorData = (o.error_series || []).map((p) => ({
    time: new Date(p.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    value: p.value,
  }))

  const routeCols = [
    { key: 'method', label: 'Method', render: (v) => (
      <span style={{
        padding: '2px 8px', borderRadius: 4, fontSize: 11, fontWeight: 700,
        background: v === 'GET' ? '#22c55e18' : v === 'POST' ? '#6366f118' : v === 'PUT' ? '#f59e0b18' : '#ef444418',
        color: v === 'GET' ? '#22c55e' : v === 'POST' ? '#818cf8' : v === 'PUT' ? '#f59e0b' : '#ef4444',
      }}>{v}</span>
    )},
    { key: 'path', label: 'Path' },
    { key: 'request_count', label: 'Requests' },
    { key: 'error_rate', label: 'Error Rate', render: (v) => (
      <span style={{ color: v > 5 ? '#ef4444' : v > 1 ? '#f59e0b' : '#22c55e' }}>{v?.toFixed(1)}%</span>
    )},
    { key: 'avg_latency', label: 'Avg Latency', render: (v) => fmtDuration(v) },
    { key: 'rpm', label: 'RPM', render: (v) => v?.toFixed(1) },
  ]

  const errorCols = [
    { key: 'error_type', label: 'Type', render: (v) => <StatusBadge status={v} /> },
    { key: 'method', label: 'Method' },
    { key: 'route', label: 'Route' },
    { key: 'error_message', label: 'Message', render: (v) => (
      <span style={{ maxWidth: 300, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{v}</span>
    )},
    { key: 'count', label: 'Count' },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700, color: '#e2e8f0' }}>{o.app_name || 'Pulse'}</h1>
          <p style={{ color: '#64748b', fontSize: 13, marginTop: 2 }}>Uptime: {o.uptime || '-'}</p>
        </div>
        <StatusBadge status={o.health_status || 'healthy'} />
      </div>

      {/* KPI Cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 14, marginBottom: 20 }}>
        <StatCard label="Total Requests" value={o.total_requests?.toLocaleString() || '0'} color="#818cf8" />
        <StatCard label="Error Rate" value={`${o.error_rate?.toFixed(2) || '0.00'}%`} color={o.error_rate > 5 ? '#ef4444' : '#22c55e'} />
        <StatCard label="Avg Latency" value={o.avg_latency ? fmtDuration(o.avg_latency) : '-'} color="#f59e0b" />
        <StatCard label="Goroutines" value={o.active_goroutines || '0'} sub={`Heap: ${o.heap_alloc_mb?.toFixed(1) || '0'} MB`} color="#22c55e" />
      </div>

      {/* Charts */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 20 }}>
        <div style={{ background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: 16 }}>
          <h3 style={{ fontSize: 13, color: '#64748b', marginBottom: 12, fontWeight: 600 }}>THROUGHPUT (RPM)</h3>
          <ResponsiveContainer width="100%" height={180}>
            <AreaChart data={throughputData}>
              <defs><linearGradient id="tg" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#6366f1" stopOpacity={0.3}/>
                <stop offset="95%" stopColor="#6366f1" stopOpacity={0}/>
              </linearGradient></defs>
              <XAxis dataKey="time" tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} />
              <YAxis tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} width={40} />
              <Tooltip contentStyle={{ background: '#16161e', border: '1px solid #2a2a3e', borderRadius: 6, fontSize: 12 }} />
              <Area type="monotone" dataKey="value" stroke="#6366f1" fill="url(#tg)" strokeWidth={2} />
            </AreaChart>
          </ResponsiveContainer>
        </div>
        <div style={{ background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: 16 }}>
          <h3 style={{ fontSize: 13, color: '#64748b', marginBottom: 12, fontWeight: 600 }}>ERRORS</h3>
          <ResponsiveContainer width="100%" height={180}>
            <AreaChart data={errorData}>
              <defs><linearGradient id="eg" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#ef4444" stopOpacity={0.3}/>
                <stop offset="95%" stopColor="#ef4444" stopOpacity={0}/>
              </linearGradient></defs>
              <XAxis dataKey="time" tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} />
              <YAxis tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} width={40} />
              <Tooltip contentStyle={{ background: '#16161e', border: '1px solid #2a2a3e', borderRadius: 6, fontSize: 12 }} />
              <Area type="monotone" dataKey="value" stroke="#ef4444" fill="url(#eg)" strokeWidth={2} />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Top Routes */}
      <div style={{ marginBottom: 20 }}>
        <h3 style={{ fontSize: 14, fontWeight: 600, color: '#e2e8f0', marginBottom: 10 }}>Top Routes</h3>
        <DataTable columns={routeCols} data={(o.top_routes || []).slice(0, 8)} emptyText="No request data yet" />
      </div>

      {/* Recent Errors */}
      <div>
        <h3 style={{ fontSize: 14, fontWeight: 600, color: '#e2e8f0', marginBottom: 10 }}>Recent Errors</h3>
        <DataTable columns={errorCols} data={(o.recent_errors || []).slice(0, 5)} emptyText="No errors recorded" />
      </div>
    </div>
  )
}
