import { useState, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { useAPI } from '../hooks/useAPI'
import { useUI, usePolling } from '../context/UIContext'
import { PageHead } from '../components/ui'
import DataTable from '../components/DataTable'
import StatusBadge from '../components/StatusBadge'
import Modal from '../components/Modal'
import LogLines from '../components/LogLines'

// logsNote explains which lines an error's Logs section shows.
function logsNote(r) {
  if (!r) return 'Loading…'
  if (r.match === 'trace') return 'Lines logged by the request behind the latest occurrence.'
  if (r.match === 'time') return "No lines carry this request's trace; showing lines logged within 30s of the latest occurrence."
  return r.capturing ? 'No lines were logged around this error.' : 'Log capture is off. The Logs page shows how to turn it on.'
}

export default function ErrorsPage() {
  const { get, post, del } = useAPI()
  const { range, rangeLabel } = useUI()
  const [errors, setErrors] = useState([])
  const [filter, setFilter] = useState({ type: '', resolved: '', muted: '' })
  const [selected, setSelected] = useState(null)
  const [selectedLogs, setSelectedLogs] = useState(null)
  const [loading, setLoading] = useState(true)

  const fetchErrors = useCallback(async () => {
    const params = new URLSearchParams({ range, limit: '100' })
    if (filter.type) params.set('type', filter.type)
    if (filter.resolved) params.set('resolved', filter.resolved)
    if (filter.muted) params.set('muted', filter.muted)
    try {
      const res = await get(`/errors?${params}`)
      if (res.ok) setErrors(await res.json())
    } catch {}
    setLoading(false)
  }, [get, range, filter])

  usePolling(fetchErrors, 20000, [fetchErrors])

  const fetchDetail = async (row) => {
    setSelectedLogs(null)
    try {
      const [res, logsRes] = await Promise.all([get(`/errors/${row.id}`), get(`/errors/${row.id}/logs`)])
      if (res.ok) setSelected(await res.json())
      if (logsRes.ok) setSelectedLogs(await logsRes.json())
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
    { key: 'method', label: 'Method', mono: true },
    { key: 'route', label: 'Route', mono: true },
    { key: 'error_message', label: 'Message', code: true },
    { key: 'count', label: 'Count', num: true, render: (v) => (
      <span className={v > 10 ? 'crit' : ''}>{v}</span>
    )},
    { key: 'state', label: 'State', render: (_, row) => (
      row.resolved ? <span className="badge badge-ok">Resolved</span>
        : row.muted ? <span className="badge badge-neutral">Muted</span>
          : <span className="badge badge-crit">Open</span>
    )},
    { key: 'last_seen', label: 'Last seen', render: (v) => v ? new Date(v).toLocaleString() : '—' },
  ]

  const types = ['', 'panic', 'internal', 'database', 'validation', 'timeout', 'auth', 'not_found']

  const open = errors.filter((e) => !e.resolved && !e.muted).length

  return (
    <>
      <PageHead
        title="Errors"
        subtitle={`${errors.length} grouped in the ${rangeLabel.toLowerCase()}`
          + (open ? ` · ${open} still open` : '')}
        actions={
          <div style={{ display: 'flex', gap: 8 }}>
        <select className="input" style={{ width: 'auto' }} value={filter.type} onChange={(e) => setFilter({ ...filter, type: e.target.value })}>
          <option value="">All types</option>
          {types.filter(Boolean).map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <select className="input" style={{ width: 'auto' }} value={filter.resolved}
          onChange={(e) => setFilter({ ...filter, resolved: e.target.value })}>
          <option value="">All status</option>
          <option value="false">Unresolved</option>
          <option value="true">Resolved</option>
        </select>
        <select className="input" style={{ width: 'auto' }} value={filter.muted}
          onChange={(e) => setFilter({ ...filter, muted: e.target.value })}>
          <option value="">Muted and unmuted</option>
          <option value="false">Not muted</option>
          <option value="true">Muted</option>
        </select>
          </div>
        }
      />

      {loading ? <p className="muted">Loading…</p> :
        <DataTable columns={columns} data={errors || []} onRowClick={fetchDetail}
          emptyText="No errors recorded in this window." />
      }

      {/* Error Detail Modal */}
      <Modal open={!!selected} onClose={() => setSelected(null)} title="Error Detail" width={720}>
        {selected && (
          <div>
            <div className="grid-2" style={{ marginBottom: 16 }}>
              <div>
                <span className="eyebrow">Type</span>
                <div style={{ marginTop: 4 }}><StatusBadge status={selected.error_type} /></div>
              </div>
              <div>
                <span className="eyebrow">Route</span>
                <p className="mono" style={{ fontSize: 13, marginTop: 4 }}>{selected.method} {selected.route}</p>
              </div>
              <div>
                <span className="eyebrow">Count</span>
                <p className="kpi-value crit" style={{ fontSize: 20, marginTop: 4 }}>{selected.count}</p>
              </div>
              <div>
                <span className="eyebrow">First seen</span>
                <p className="muted" style={{ fontSize: 13, marginTop: 4 }}>{new Date(selected.first_seen).toLocaleString()}</p>
              </div>
            </div>

            <div style={{ marginBottom: 16 }}>
              <span className="eyebrow">Message</span>
              <p className="mono crit" style={{
                marginTop: 4, padding: '10px 14px', background: 'var(--bg-2)',
                border: '1px solid var(--border)', borderRadius: 'var(--radius)', fontSize: 13,
              }}>{selected.error_message}</p>
            </div>

            {selected.stack_trace && (
              <div style={{ marginBottom: 16 }}>
                <span className="eyebrow">Stack trace</span>
                <pre className="mono" style={{
                  marginTop: 4, padding: 14, background: 'var(--bg-2)',
                  border: '1px solid var(--border)', borderRadius: 'var(--radius)',
                  fontSize: 11, color: 'var(--text-2)', overflow: 'auto', maxHeight: 250,
                  lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                }}>{selected.stack_trace}</pre>
              </div>
            )}

            <div style={{ marginBottom: 16 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                <span className="eyebrow">Logs</span>
                {selected.trace_id && (
                  <span style={{ display: 'flex', gap: 12 }}>
                    <Link to={`/pulse/ui/traces/${selected.trace_id}`} style={{ fontSize: 12 }}>
                      Open trace →
                    </Link>
                    <Link to={`/pulse/ui/logs?trace_id=${selected.trace_id}`} style={{ fontSize: 12 }}>
                      Open in Logs →
                    </Link>
                  </span>
                )}
              </div>
              <p className="muted" style={{ fontSize: 12, margin: '4px 0 6px' }}>{logsNote(selectedLogs)}</p>
              <LogLines logs={selectedLogs?.logs} maxHeight={260} emptyText="" />
            </div>

            <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
              {!selected.muted && (
                <button type="button" className="btn" onClick={() => muteError(selected.id)}>Mute</button>
              )}
              {!selected.resolved && (
                <button type="button" className="btn btn-primary" onClick={() => resolveError(selected.id)}>Resolve</button>
              )}
              <button type="button" className="btn btn-danger" onClick={() => deleteError(selected.id)}>Delete</button>
            </div>
          </div>
        )}
      </Modal>
    </>
  )
}
