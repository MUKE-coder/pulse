import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import DataTable from '../components/DataTable'
import StatusBadge from '../components/StatusBadge'
import Modal from '../components/Modal'

export default function ErrorsPage() {
  const { get, post, del } = useAPI()
  const [errors, setErrors] = useState([])
  const [filter, setFilter] = useState({ type: '', resolved: '', muted: '' })
  const [selected, setSelected] = useState(null)
  const [loading, setLoading] = useState(true)

  const fetchErrors = async () => {
    const params = new URLSearchParams({ range: '24h', limit: '100' })
    if (filter.type) params.set('type', filter.type)
    if (filter.resolved) params.set('resolved', filter.resolved)
    if (filter.muted) params.set('muted', filter.muted)
    try {
      const res = await get(`/errors?${params}`)
      if (res.ok) setErrors(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchErrors() }, [filter])

  const fetchDetail = async (row) => {
    try {
      const res = await get(`/errors/${row.id}`)
      if (res.ok) setSelected(await res.json())
    } catch {}
  }

  const muteError = async (id) => {
    await post(`/errors/${id}/mute`)
    setSelected(null)
    fetchErrors()
  }

  const resolveError = async (id) => {
    await post(`/errors/${id}/resolve`)
    setSelected(null)
    fetchErrors()
  }

  const deleteError = async (id) => {
    await del(`/errors/${id}`)
    setSelected(null)
    fetchErrors()
  }

  const columns = [
    { key: 'error_type', label: 'Type', render: (v) => <StatusBadge status={v} /> },
    { key: 'method', label: 'Method', render: (v) => (
      <span style={{ fontWeight: 600, fontSize: 12, color: '#818cf8' }}>{v}</span>
    )},
    { key: 'route', label: 'Route' },
    { key: 'error_message', label: 'Message', render: (v) => (
      <span style={{ maxWidth: 280, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{v}</span>
    )},
    { key: 'count', label: 'Count', render: (v) => (
      <span style={{ fontWeight: 700, color: v > 10 ? '#ef4444' : '#e2e8f0' }}>{v}</span>
    )},
    { key: 'muted', label: 'Muted', render: (v) => v ? <span style={{ color: '#64748b' }}>Yes</span> : null },
    { key: 'resolved', label: 'Resolved', render: (v) => v ? <span style={{ color: '#22c55e' }}>Yes</span> : null },
    { key: 'last_seen', label: 'Last Seen', render: (v) => v ? new Date(v).toLocaleString() : '-' },
  ]

  const types = ['', 'panic', 'internal', 'database', 'validation', 'timeout', 'auth', 'not_found']

  return (
    <div>
      <h1 style={{ fontSize: 22, fontWeight: 700, marginBottom: 16 }}>Errors</h1>

      {/* Filters */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
        <select value={filter.type} onChange={(e) => setFilter({ ...filter, type: e.target.value })}>
          <option value="">All Types</option>
          {types.filter(Boolean).map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <select value={filter.resolved} onChange={(e) => setFilter({ ...filter, resolved: e.target.value })}>
          <option value="">All Status</option>
          <option value="false">Unresolved</option>
          <option value="true">Resolved</option>
        </select>
        <select value={filter.muted} onChange={(e) => setFilter({ ...filter, muted: e.target.value })}>
          <option value="">Muted/Unmuted</option>
          <option value="false">Not Muted</option>
          <option value="true">Muted</option>
        </select>
      </div>

      {loading ? <p style={{ color: '#64748b' }}>Loading...</p> :
        <DataTable columns={columns} data={errors || []} onRowClick={fetchDetail} emptyText="No errors recorded" />
      }

      {/* Error Detail Modal */}
      <Modal open={!!selected} onClose={() => setSelected(null)} title="Error Detail" width={720}>
        {selected && (
          <div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 16 }}>
              <div>
                <span style={{ color: '#64748b', fontSize: 12 }}>Type</span>
                <div style={{ marginTop: 4 }}><StatusBadge status={selected.error_type} /></div>
              </div>
              <div>
                <span style={{ color: '#64748b', fontSize: 12 }}>Route</span>
                <p style={{ fontSize: 14, color: '#e2e8f0', marginTop: 4 }}>{selected.method} {selected.route}</p>
              </div>
              <div>
                <span style={{ color: '#64748b', fontSize: 12 }}>Count</span>
                <p style={{ fontSize: 20, fontWeight: 700, color: '#ef4444', marginTop: 4 }}>{selected.count}</p>
              </div>
              <div>
                <span style={{ color: '#64748b', fontSize: 12 }}>First Seen</span>
                <p style={{ fontSize: 13, color: '#94a3b8', marginTop: 4 }}>{new Date(selected.first_seen).toLocaleString()}</p>
              </div>
            </div>

            <div style={{ marginBottom: 16 }}>
              <span style={{ color: '#64748b', fontSize: 12 }}>Message</span>
              <p style={{
                marginTop: 4, padding: '10px 14px', background: '#0a0a12', borderRadius: 6,
                fontSize: 13, color: '#ef4444', fontFamily: "'SF Mono', monospace",
              }}>{selected.error_message}</p>
            </div>

            {selected.stack_trace && (
              <div style={{ marginBottom: 16 }}>
                <span style={{ color: '#64748b', fontSize: 12 }}>Stack Trace</span>
                <pre style={{
                  marginTop: 4, padding: 14, background: '#0a0a12', borderRadius: 6,
                  fontSize: 11, color: '#94a3b8', overflow: 'auto', maxHeight: 250,
                  fontFamily: "'SF Mono', 'Fira Code', monospace", lineHeight: 1.5,
                  whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                }}>{selected.stack_trace}</pre>
              </div>
            )}

            <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
              {!selected.muted && (
                <button onClick={() => muteError(selected.id)} style={btnStyle('#f59e0b')}>Mute</button>
              )}
              {!selected.resolved && (
                <button onClick={() => resolveError(selected.id)} style={btnStyle('#22c55e')}>Resolve</button>
              )}
              <button onClick={() => deleteError(selected.id)} style={btnStyle('#ef4444')}>Delete</button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}

const btnStyle = (color) => ({
  padding: '6px 14px', borderRadius: 6, border: `1px solid ${color}40`,
  background: `${color}18`, color, fontSize: 13, fontWeight: 600, cursor: 'pointer',
})
