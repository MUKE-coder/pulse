import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { PageHead } from '../components/ui'
import { useWebSocket } from '../hooks/useWebSocket'
import { useInstances } from '../hooks/useInstances'
import StatusBadge from '../components/StatusBadge'
import DataTable from '../components/DataTable'
import InstancePicker from '../components/InstancePicker'

export default function HealthPage() {
  const { get, post } = useAPI()
  const [health, setHealth] = useState(null)
  const [selectedCheck, setSelectedCheck] = useState(null)
  const [history, setHistory] = useState([])
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['health'])
  const { instances, self } = useInstances()
  const [instance, setInstance] = useState('')
  const shown = instance || self
  // Checks run where they are registered: only this instance's can be run
  // from here.
  const local = !shown || shown === self
  const which = shown ? `instance=${encodeURIComponent(shown)}` : ''

  const fetchHealth = async () => {
    try {
      const res = await get(`/health/checks?${which}`)
      if (res.ok) setHealth(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchHealth() }, [shown])

  useEffect(() => {
    if (lastMessage?.type === 'health') fetchHealth()
  }, [lastMessage])

  const fetchHistory = async (name) => {
    setSelectedCheck(name)
    try {
      const res = await get(`/health/checks/${name}/history?limit=20&${which}`)
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

  if (loading) return <div style={{ color: 'var(--text-3)', padding: 40 }}>Loading...</div>

  const checks = health?.checks || {}
  const status = health?.status || 'unknown'

  const historyCols = [
    { key: 'status', label: 'Status', render: (v) => <StatusBadge status={v} /> },
    { key: 'latency_ms', label: 'Latency', render: (v) => `${v?.toFixed(1)}ms` },
    { key: 'error', label: 'Error', render: (v) => v ? <span style={{ color: 'var(--crit)', fontSize: 12 }}>{v}</span> : <span style={{ color: 'var(--text-3)' }}>-</span> },
    { key: 'timestamp', label: 'Time', render: (v) => v ? new Date(v).toLocaleString() : '-' },
  ]

  return (
    <>
      <PageHead
        title="Health"
        subtitle={health?.uptime
          ? `${Object.keys(checks).length} ${Object.keys(checks).length === 1 ? 'check' : 'checks'} · up ${health.uptime}`
          : undefined}
        actions={
          <>
            <InstancePicker instances={instances} value={shown} onChange={setInstance} />
            <StatusBadge status={status} />
          </>
        }
      />

      <div className="grid-3">
        {Object.entries(checks).map(([name, check]) => (
          <div key={name} className="card" style={{
            cursor: 'pointer',
            borderLeft: `3px solid ${check.status === 'healthy' ? 'var(--ok)' : check.status === 'degraded' ? 'var(--warn)' : 'var(--crit)'}`,
          }}
            onClick={() => fetchHistory(name)}
          >
            <div className="card-head" style={{ marginBottom: 8 }}>
              <h3 className="card-title" style={{ fontSize: 14 }}>{name}</h3>
              <StatusBadge status={check.status} />
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
              <span className="muted" style={{ fontSize: 12 }}>
                Latency <span className="mono" style={{ color: 'var(--text)' }}>{check.latency_ms?.toFixed(1)}ms</span>
              </span>
              {local && <button type="button" className="btn btn-sm"
                onClick={(e) => { e.stopPropagation(); runCheck(name) }}>
                Run now
              </button>}
            </div>
            {check.error && (
              <p style={{ color: 'var(--crit)', fontSize: 12, marginTop: 8 }}>{check.error}</p>
            )}
          </div>
        ))}
      </div>

      {Object.keys(checks).length === 0 && (
        <div className="empty">
          <div className="title">No health checks registered</div>
          <div className="body">
            Register one with pulse.AddHealthCheck, or mount Pulse with a database and the
            built-in database check appears here.
          </div>
        </div>
      )}

      {/* History */}
      {selectedCheck && (
        <section className="section">
          <div className="section-head">
            <h2>History — <span className="mono">{selectedCheck}</span></h2>
          </div>
          <DataTable columns={historyCols} data={history || []} emptyText="No history recorded yet." />
        </section>
      )}
    </>
  )
}
