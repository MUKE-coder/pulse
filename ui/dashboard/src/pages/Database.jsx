import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import StatCard from '../components/StatCard'
import DataTable from '../components/DataTable'

export default function DatabasePage() {
  const { get } = useAPI()
  const [overview, setOverview] = useState(null)
  const [slowQueries, setSlowQueries] = useState([])
  const [patterns, setPatterns] = useState([])
  const [n1, setN1] = useState([])
  const [pool, setPool] = useState(null)
  const [tab, setTab] = useState('slow')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const load = async () => {
      try {
        const [oRes, sRes, pRes, nRes, poolRes] = await Promise.all([
          get('/database/overview?range=1h'),
          get('/database/slow-queries?limit=50'),
          get('/database/patterns?range=1h'),
          get('/database/n1?range=1h'),
          get('/database/pool'),
        ])
        if (oRes.ok) setOverview(await oRes.json())
        if (sRes.ok) setSlowQueries(await sRes.json())
        if (pRes.ok) setPatterns(await pRes.json())
        if (nRes.ok) setN1(await nRes.json())
        if (poolRes.ok) setPool(await poolRes.json())
      } catch {}
      setLoading(false)
    }
    load()
  }, [])

  const slowCols = [
    { key: 'operation', label: 'Op', render: (v) => (
      <span style={{ color: '#818cf8', fontWeight: 600, textTransform: 'uppercase', fontSize: 11 }}>{v}</span>
    )},
    { key: 'table', label: 'Table' },
    { key: 'duration', label: 'Duration', render: (v) => (
      <span style={{ color: v > 500e6 ? '#ef4444' : v > 200e6 ? '#f59e0b' : '#e2e8f0' }}>
        {(v / 1e6).toFixed(1)}ms
      </span>
    )},
    { key: 'rows_affected', label: 'Rows' },
    { key: 'caller_file', label: 'Caller', render: (v, row) => (
      <span style={{ color: '#64748b', fontSize: 12 }}>{v}{row.caller_line > 0 ? `:${row.caller_line}` : ''}</span>
    )},
    { key: 'sql', label: 'SQL', render: (v) => (
      <span style={{
        maxWidth: 300, display: 'inline-block', overflow: 'hidden',
        textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: '#94a3b8', fontSize: 12,
        fontFamily: "'SF Mono', 'Fira Code', monospace",
      }}>{v}</span>
    )},
  ]

  const patternCols = [
    { key: 'operation', label: 'Operation', render: (v) => (
      <span style={{ color: '#818cf8', fontWeight: 600, textTransform: 'uppercase', fontSize: 11 }}>{v}</span>
    )},
    { key: 'table', label: 'Table' },
    { key: 'count', label: 'Count', render: (v) => v?.toLocaleString() },
    { key: 'avg_duration', label: 'Avg Duration', render: (v) => `${(v / 1e6).toFixed(1)}ms` },
    { key: 'max_duration', label: 'Max', render: (v) => `${(v / 1e6).toFixed(1)}ms` },
    { key: 'total_duration', label: 'Total Time', render: (v) => `${(v / 1e6).toFixed(0)}ms` },
  ]

  const n1Cols = [
    { key: 'pattern', label: 'SQL Pattern', render: (v) => (
      <span style={{ fontFamily: "'SF Mono', 'Fira Code', monospace", fontSize: 12, color: '#f59e0b' }}>{v}</span>
    )},
    { key: 'count', label: 'Repetitions', render: (v) => (
      <span style={{ color: '#ef4444', fontWeight: 700 }}>{v}x</span>
    )},
    { key: 'request_trace_id', label: 'Trace ID', render: (v) => (
      <span style={{ color: '#64748b', fontSize: 11 }}>{v?.substring(0, 12)}...</span>
    )},
  ]

  const tabs = [
    { id: 'slow', label: 'Slow Queries', count: slowQueries?.length },
    { id: 'patterns', label: 'Patterns', count: patterns?.length },
    { id: 'n1', label: 'N+1 Detections', count: n1?.length },
  ]

  if (loading) return <div style={{ color: '#64748b', padding: 40 }}>Loading...</div>

  return (
    <div>
      <h1 style={{ fontSize: 22, fontWeight: 700, marginBottom: 16 }}>Database</h1>

      {/* KPI Cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 14, marginBottom: 20 }}>
        <StatCard label="Total Queries" value={overview?.total_queries?.toLocaleString() || '0'} color="#818cf8" />
        <StatCard label="Query Patterns" value={overview?.pattern_count || '0'} color="#22c55e" />
        <StatCard label="Slow Queries" value={overview?.slow_query_count || '0'} color="#f59e0b" />
        <StatCard label="N+1 Detected" value={overview?.n1_count || '0'} color={overview?.n1_count > 0 ? '#ef4444' : '#22c55e'} />
      </div>

      {/* Connection Pool */}
      {pool && pool.open_connections !== undefined && (
        <div style={{
          background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
          padding: 16, marginBottom: 20,
        }}>
          <h3 style={{ fontSize: 13, color: '#64748b', fontWeight: 600, marginBottom: 10 }}>CONNECTION POOL</h3>
          <div style={{ display: 'flex', gap: 24 }}>
            <span style={{ fontSize: 13 }}><span style={{ color: '#64748b' }}>Open:</span> <span style={{ color: '#818cf8', fontWeight: 600 }}>{pool.open_connections}</span></span>
            <span style={{ fontSize: 13 }}><span style={{ color: '#64748b' }}>In Use:</span> <span style={{ color: '#f59e0b', fontWeight: 600 }}>{pool.in_use}</span></span>
            <span style={{ fontSize: 13 }}><span style={{ color: '#64748b' }}>Idle:</span> <span style={{ color: '#22c55e', fontWeight: 600 }}>{pool.idle}</span></span>
            <span style={{ fontSize: 13 }}><span style={{ color: '#64748b' }}>Wait Count:</span> <span style={{ color: '#94a3b8' }}>{pool.wait_count}</span></span>
          </div>
        </div>
      )}

      {/* Tabs */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 14 }}>
        {tabs.map(({ id, label, count }) => (
          <button key={id} onClick={() => setTab(id)} style={{
            padding: '8px 16px', borderRadius: 6, border: 'none', fontSize: 13,
            fontWeight: 600, cursor: 'pointer',
            background: tab === id ? '#6366f120' : 'transparent',
            color: tab === id ? '#818cf8' : '#64748b',
          }}>
            {label} {count > 0 && <span style={{ fontSize: 11, opacity: 0.7 }}>({count})</span>}
          </button>
        ))}
      </div>

      {tab === 'slow' && <DataTable columns={slowCols} data={slowQueries || []} emptyText="No slow queries detected" />}
      {tab === 'patterns' && <DataTable columns={patternCols} data={patterns || []} emptyText="No query patterns yet" />}
      {tab === 'n1' && <DataTable columns={n1Cols} data={n1 || []} emptyText="No N+1 queries detected" />}
    </div>
  )
}
