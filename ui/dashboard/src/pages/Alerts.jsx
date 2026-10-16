import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { PageHead } from '../components/ui'
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
      <span style={{ color: 'var(--accent)', fontSize: 12, fontFamily: "'SF Mono', monospace" }}>{v}</span>
    )},
    { key: 'value', label: 'Value', render: (v, row) => (
      <span>
        <span style={{ fontWeight: 600, color: 'var(--text)' }}>{v?.toFixed(1)}</span>
        <span style={{ color: 'var(--text-3)', fontSize: 12 }}> {row.operator} {row.threshold?.toFixed(1)}</span>
      </span>
    )},
    { key: 'message', label: 'Message', render: (v) => (
      <span style={{ maxWidth: 250, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{v}</span>
    )},
    { key: 'fired_at', label: 'Fired At', render: (v) => v ? new Date(v).toLocaleString() : '-' },
    { key: 'resolved_at', label: 'Resolved', render: (v) => v ? new Date(v).toLocaleString() : (
      <span style={{ color: 'var(--text-3)' }}>-</span>
    )},
  ]

  // Summary cards
  const firing = alerts?.filter((a) => a.state === 'firing').length || 0
  const resolved = alerts?.filter((a) => a.state === 'resolved').length || 0
  const critical = alerts?.filter((a) => a.severity === 'critical').length || 0

  return (
    <>
      <PageHead
        title="Alerts"
        subtitle={firing > 0
          ? `${firing} firing right now`
          : 'Nothing is firing. Rules evaluate continuously.'}
      />

      <section className="grid-3" aria-label="Alert summary">
        <div className={`card kpi${firing > 0 ? ' is-critical' : ''}`}>
          <div className="kpi-head"><span>Firing</span></div>
          <div className={`kpi-value${firing > 0 ? ' crit' : ''}`}>{firing}</div>
        </div>
        <div className="card kpi">
          <div className="kpi-head"><span>Critical severity</span></div>
          <div className="kpi-value">{critical}</div>
        </div>
        <div className="card kpi">
          <div className="kpi-head"><span>Resolved in window</span></div>
          <div className="kpi-value">{resolved}</div>
        </div>
      </section>

      <div style={{ display: 'flex', gap: 8 }}>
        <select className="input" style={{ width: 'auto' }} value={stateFilter}
          onChange={(e) => setStateFilter(e.target.value)}>
          <option value="">All states</option>
          <option value="firing">Firing</option>
          <option value="resolved">Resolved</option>
        </select>
        <select className="input" style={{ width: 'auto' }} value={sevFilter}
          onChange={(e) => setSevFilter(e.target.value)}>
          <option value="">All severities</option>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </select>
      </div>

      {loading ? <p className="muted">Loading…</p> :
        <DataTable columns={columns} data={alerts || []}
          emptyText="No alerts in the last 24 hours." />
      }
    </>
  )
}
