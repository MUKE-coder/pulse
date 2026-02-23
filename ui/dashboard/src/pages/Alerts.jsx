import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import StatusBadge from '../components/StatusBadge'
import DataTable from '../components/DataTable'

export default function AlertsPage() {
  const { get } = useAPI()
  const [alerts, setAlerts] = useState([])
  const [stateFilter, setStateFilter] = useState('')
  const [sevFilter, setSevFilter] = useState('')
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['alert'])

  const fetchAlerts = async () => {
    const params = new URLSearchParams({ range: '24h', limit: '100' })
    if (stateFilter) params.set('state', stateFilter)
    if (sevFilter) params.set('severity', sevFilter)
    try {
      const res = await get(`/alerts?${params}`)
      if (res.ok) setAlerts(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchAlerts() }, [stateFilter, sevFilter])

  useEffect(() => {
    if (lastMessage?.type === 'alert') fetchAlerts()
  }, [lastMessage])

  const columns = [
    { key: 'state', label: 'State', render: (v) => <StatusBadge status={v} /> },
    { key: 'severity', label: 'Severity', render: (v) => <StatusBadge status={v} /> },
    { key: 'rule_name', label: 'Rule' },
    { key: 'metric', label: 'Metric', render: (v) => (
      <span style={{ color: '#818cf8', fontSize: 12, fontFamily: "'SF Mono', monospace" }}>{v}</span>
    )},
    { key: 'value', label: 'Value', render: (v, row) => (
      <span>
        <span style={{ fontWeight: 600, color: '#e2e8f0' }}>{v?.toFixed(1)}</span>
        <span style={{ color: '#64748b', fontSize: 12 }}> {row.operator} {row.threshold?.toFixed(1)}</span>
      </span>
    )},
    { key: 'message', label: 'Message', render: (v) => (
      <span style={{ maxWidth: 250, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{v}</span>
    )},
    { key: 'fired_at', label: 'Fired At', render: (v) => v ? new Date(v).toLocaleString() : '-' },
    { key: 'resolved_at', label: 'Resolved', render: (v) => v ? new Date(v).toLocaleString() : (
      <span style={{ color: '#64748b' }}>-</span>
    )},
  ]

  // Summary cards
  const firing = alerts?.filter((a) => a.state === 'firing').length || 0
  const resolved = alerts?.filter((a) => a.state === 'resolved').length || 0
  const critical = alerts?.filter((a) => a.severity === 'critical').length || 0

  return (
    <div>
      <h1 style={{ fontSize: 22, fontWeight: 700, marginBottom: 16 }}>Alerts</h1>

      {/* Summary */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 14, marginBottom: 20 }}>
        <div style={{
          background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: '14px 18px',
          borderLeft: '3px solid #ef4444',
        }}>
          <p style={{ color: '#64748b', fontSize: 12 }}>FIRING</p>
          <p style={{ fontSize: 28, fontWeight: 700, color: firing > 0 ? '#ef4444' : '#22c55e' }}>{firing}</p>
        </div>
        <div style={{
          background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: '14px 18px',
          borderLeft: '3px solid #f59e0b',
        }}>
          <p style={{ color: '#64748b', fontSize: 12 }}>CRITICAL</p>
          <p style={{ fontSize: 28, fontWeight: 700, color: critical > 0 ? '#f59e0b' : '#94a3b8' }}>{critical}</p>
        </div>
        <div style={{
          background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: '14px 18px',
          borderLeft: '3px solid #22c55e',
        }}>
          <p style={{ color: '#64748b', fontSize: 12 }}>RESOLVED</p>
          <p style={{ fontSize: 28, fontWeight: 700, color: '#22c55e' }}>{resolved}</p>
        </div>
      </div>

      {/* Filters */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
        <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value)}>
          <option value="">All States</option>
          <option value="firing">Firing</option>
          <option value="resolved">Resolved</option>
        </select>
        <select value={sevFilter} onChange={(e) => setSevFilter(e.target.value)}>
          <option value="">All Severities</option>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </select>
      </div>

      {loading ? <p style={{ color: '#64748b' }}>Loading...</p> :
        <DataTable columns={columns} data={alerts || []} emptyText="No alerts in the last 24 hours" />
      }
    </div>
  )
}
