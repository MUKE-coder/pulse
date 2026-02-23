import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import StatusBadge from '../components/StatusBadge'
import DataTable from '../components/DataTable'

export default function HealthPage() {
  const { get, post } = useAPI()
  const [health, setHealth] = useState(null)
  const [selectedCheck, setSelectedCheck] = useState(null)
  const [history, setHistory] = useState([])
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['health'])

  const fetchHealth = async () => {
    try {
      const res = await get('/health/checks')
      if (res.ok) setHealth(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchHealth() }, [])

  useEffect(() => {
    if (lastMessage?.type === 'health') fetchHealth()
  }, [lastMessage])

  const fetchHistory = async (name) => {
    setSelectedCheck(name)
    try {
      const res = await get(`/health/checks/${name}/history?limit=20`)
      if (res.ok) setHistory(await res.json())
    } catch {}
  }

  const runCheck = async (name) => {
    try {
      await post(`/health/checks/${name}/run`)
      fetchHealth()
      if (selectedCheck === name) fetchHistory(name)
    } catch {}
  }

  if (loading) return <div style={{ color: '#64748b', padding: 40 }}>Loading...</div>

  const checks = health?.checks || {}
  const status = health?.status || 'unknown'

  const historyCols = [
    { key: 'status', label: 'Status', render: (v) => <StatusBadge status={v} /> },
    { key: 'latency_ms', label: 'Latency', render: (v) => `${v?.toFixed(1)}ms` },
    { key: 'error', label: 'Error', render: (v) => v ? <span style={{ color: '#ef4444', fontSize: 12 }}>{v}</span> : <span style={{ color: '#64748b' }}>-</span> },
    { key: 'timestamp', label: 'Time', render: (v) => v ? new Date(v).toLocaleString() : '-' },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Health</h1>
        <StatusBadge status={status} />
      </div>

      {health?.uptime && (
        <p style={{ color: '#64748b', fontSize: 13, marginBottom: 16 }}>Uptime: {health.uptime}</p>
      )}

      {/* Health Check Cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280, 1fr))', gap: 14, marginBottom: 20 }}>
        {Object.entries(checks).map(([name, check]) => (
          <div key={name} style={{
            background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
            padding: 16, cursor: 'pointer',
            borderLeft: `3px solid ${check.status === 'healthy' ? '#22c55e' : check.status === 'degraded' ? '#f59e0b' : '#ef4444'}`,
          }}
            onClick={() => fetchHistory(name)}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
              <h3 style={{ fontSize: 15, fontWeight: 600, color: '#e2e8f0' }}>{name}</h3>
              <StatusBadge status={check.status} />
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ color: '#64748b', fontSize: 12 }}>
                Latency: <span style={{ color: '#e2e8f0' }}>{check.latency_ms?.toFixed(1)}ms</span>
              </span>
              <button
                onClick={(e) => { e.stopPropagation(); runCheck(name) }}
                style={{
                  padding: '3px 10px', borderRadius: 4, fontSize: 11, fontWeight: 600,
                  background: '#6366f118', color: '#818cf8', border: '1px solid #6366f130',
                  cursor: 'pointer',
                }}
              >
                Run
              </button>
            </div>
            {check.error && (
              <p style={{ color: '#ef4444', fontSize: 12, marginTop: 8 }}>{check.error}</p>
            )}
          </div>
        ))}
      </div>

      {Object.keys(checks).length === 0 && (
        <div style={{
          background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
          padding: 40, textAlign: 'center', color: '#64748b', fontSize: 14,
        }}>
          No health checks registered
        </div>
      )}

      {/* History */}
      {selectedCheck && (
        <div>
          <h2 style={{ fontSize: 16, fontWeight: 600, marginBottom: 10 }}>
            History: <span style={{ color: '#818cf8' }}>{selectedCheck}</span>
          </h2>
          <DataTable columns={historyCols} data={history || []} emptyText="No history available" />
        </div>
      )}
    </div>
  )
}
