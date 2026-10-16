import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { PageHead } from '../components/ui'

function fmtSeconds(s) {
  if (s < 3600) return `${Math.round(s / 60)}m`
  if (s < 86400) return `${(s / 3600).toFixed(1)}h`
  return `${(s / 86400).toFixed(1)}d`
}

function fmtBytes(b) {
  if (!b) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(b) / Math.log(1024)))
  return `${(b / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`
}

// StorageSummary says whether buffer capacity, rather than the configured
// retention, is limiting how much history Pulse keeps.
function StorageSummary({ storage }) {
  const limited = storage.retention_coverage < 1
  const color = storage.retention_coverage < 0.5 ? 'var(--warn)' : 'var(--text-2)'
  return (
    <p style={{ fontSize: 13, color: limited ? color : 'var(--text-2)' }}>
      {storage.driver} backend, {storage.retention_hours}h retention.{' '}
      {limited
        ? `The ${storage.limited_by} buffer is full and holds only ${(storage.retention_coverage * 100).toFixed(0)}% of that — raise Storage.MemoryCapacity or switch to SQLite.`
        : 'Every kind of data covers the full retention window.'}
    </p>
  )
}

export default function SettingsPage() {
  const { get, post } = useAPI()
  const [settings, setSettings] = useState(null)
  const [storage, setStorage] = useState(null)
  const [exportType, setExportType] = useState('requests')
  const [exportFormat, setExportFormat] = useState('json')
  const [exportRange, setExportRange] = useState('1h')
  const [exporting, setExporting] = useState(false)
  const [resetConfirm, setResetConfirm] = useState(false)
  const [message, setMessage] = useState('')

  useEffect(() => {
    const load = async () => {
      try {
        const res = await get('/settings')
        if (res.ok) setSettings(await res.json())
        const st = await get('/storage')
        if (st.ok) setStorage(await st.json())
      } catch {}
    }
    load()
  }, [])

  const handleExport = async () => {
    setExporting(true)
    setMessage('')
    try {
      const res = await post('/data/export', {
        format: exportFormat, type: exportType, range: exportRange,
      })
      if (!res.ok) {
        const err = await res.json()
        setMessage(`Export failed: ${err.error}`)
        return
      }
      const blob = await res.blob()
      const disposition = res.headers.get('Content-Disposition') || ''
      const match = disposition.match(/filename=(.+)/)
      const filename = match ? match[1] : `pulse_${exportType}.${exportFormat}`

      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      a.click()
      URL.revokeObjectURL(url)
      setMessage('Export downloaded successfully')
    } catch (err) {
      setMessage(`Export error: ${err.message}`)
    } finally {
      setExporting(false)
    }
  }

  const handleReset = async () => {
    try {
      const res = await post('/data/reset', { confirm: true })
      if (res.ok) {
        setMessage('All data has been reset')
        setResetConfirm(false)
      } else {
        const err = await res.json()
        setMessage(`Reset failed: ${err.error}`)
      }
    } catch (err) {
      setMessage(`Reset error: ${err.message}`)
    }
  }

  const Section = ({ title, children }) => (
    <section className="card">
      <h3 className="card-title" style={{ marginBottom: 14 }}>{title}</h3>
      {children}
    </section>
  )

  const ConfigRow = ({ label, value }) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '6px 0', borderBottom: '1px solid var(--surface-2)' }}>
      <span style={{ color: 'var(--text-3)', fontSize: 13 }}>{label}</span>
      <span className="mono" style={{ fontSize: 13 }}>
        {typeof value === 'boolean' ? (value ? 'true' : 'false') : String(value ?? '-')}
      </span>
    </div>
  )

  const failed = message && (message.includes('fail') || message.includes('error'))

  return (
    <>
      <PageHead title="Settings" subtitle="What this process is configured to do, and what it has stored." />

      {message && (
        <div role="status" style={{
          padding: '10px 14px', borderRadius: 'var(--radius)', fontSize: 13,
          background: failed ? 'var(--crit-soft)' : 'var(--ok-soft)',
          color: failed ? 'var(--crit)' : 'var(--ok)',
          border: `1px solid ${failed ? 'var(--crit-border)' : 'var(--ok-border)'}`,
        }}>
          {message}
        </div>
      )}

      {/* Export */}
      <Section title="Data Export">
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <select className="input" style={{ width: 'auto' }} value={exportType}
            onChange={(e) => setExportType(e.target.value)}>
            <option value="requests">Requests</option>
            <option value="queries">Queries</option>
            <option value="errors">Errors</option>
            <option value="runtime">Runtime</option>
            <option value="alerts">Alerts</option>
          </select>
          <select className="input" style={{ width: 'auto' }} value={exportFormat}
            onChange={(e) => setExportFormat(e.target.value)}>
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
          <select className="input" style={{ width: 'auto' }} value={exportRange}
            onChange={(e) => setExportRange(e.target.value)}>
            <option value="5m">Last 5m</option>
            <option value="15m">Last 15m</option>
            <option value="1h">Last 1h</option>
            <option value="6h">Last 6h</option>
            <option value="24h">Last 24h</option>
            <option value="7d">Last 7d</option>
          </select>
          <button type="button" className="btn btn-primary" onClick={handleExport} disabled={exporting}>
            {exporting ? 'Exporting…' : 'Export'}
          </button>
        </div>
      </Section>

      {/* Storage capacity */}
      {storage && (
        <Section title="Storage">
          <StorageSummary storage={storage} />
          <table style={{ width: '100%', fontSize: 12.5, marginTop: 10, borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ color: 'var(--text-3)', textAlign: 'left' }}>
                <th style={{ padding: '4px 0', fontWeight: 600 }}>Kind</th>
                <th style={{ fontWeight: 600 }}>Stored</th>
                <th style={{ fontWeight: 600 }}>Holds</th>
                <th style={{ fontWeight: 600 }}>Notes</th>
              </tr>
            </thead>
            <tbody>
              {storage.kinds.map((k) => (
                <tr key={k.kind} style={{ borderTop: '1px solid var(--surface-2)', color: 'var(--text)' }}>
                  <td style={{ padding: '5px 0', fontFamily: "'SF Mono', monospace" }}>{k.kind}</td>
                  <td>
                    {k.kind === 'database' ? fmtBytes(k.bytes)
                      : k.kind === 'write_queue' ? `${k.stored.toLocaleString()} / ${k.capacity.toLocaleString()} queued`
                      : `${k.stored.toLocaleString()}${k.capacity ? ` / ${k.capacity.toLocaleString()}` : ''}`}
                  </td>
                  <td style={{ color: k.full ? 'var(--warn)' : 'var(--text-2)' }}>
                    {k.kind === 'database' || k.kind === 'write_queue' ? '—'
                      : k.full && k.effective_retention_seconds ? fmtSeconds(k.effective_retention_seconds)
                      : 'full retention'}
                  </td>
                  <td style={{ color: 'var(--text-2)' }}>
                    {[
                      k.dropped ? `${k.dropped.toLocaleString()} dropped` : '',
                      k.failed ? `${k.failed.toLocaleString()} failed` : '',
                      k.free_bytes ? `${fmtBytes(k.free_bytes)} free on disk` : '',
                    ].filter(Boolean).join(' · ')}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* Configuration Display */}
      {settings && (
        <Section title="Current Configuration">
          <ConfigRow label="App Name" value={settings.AppName} />
          <ConfigRow label="Prefix" value={settings.Prefix} />
          <ConfigRow label="Dev Mode" value={settings.DevMode} />
          <ConfigRow label="Dashboard Username" value={settings.Dashboard?.Username} />
          <ConfigRow label="Storage Driver" value={settings.Storage?.Driver === 0 ? 'Memory' : 'SQLite'} />
          <ConfigRow label="Retention Hours" value={settings.Storage?.RetentionHours} />
          <ConfigRow label="Tracing Enabled" value={settings.Tracing?.Enabled} />
          <ConfigRow label="Slow Request Threshold" value={`${(settings.Tracing?.SlowRequestThreshold / 1e6)?.toFixed(0)}ms`} />
          <ConfigRow label="Database Enabled" value={settings.Database?.Enabled} />
          <ConfigRow label="Slow Query Threshold" value={`${(settings.Database?.SlowQueryThreshold / 1e6)?.toFixed(0)}ms`} />
          <ConfigRow label="N+1 Detection" value={settings.Database?.DetectN1} />
          <ConfigRow label="Runtime Enabled" value={settings.Runtime?.Enabled} />
          <ConfigRow label="Error Tracking" value={settings.Errors?.Enabled} />
          <ConfigRow label="Health Checks" value={settings.Health?.Enabled} />
          <ConfigRow label="Alerts Enabled" value={settings.Alerts?.Enabled} />
          <ConfigRow label="Prometheus" value={settings.Prometheus?.Enabled} />
        </Section>
      )}

      {/* Danger Zone */}
      <Section title="Danger zone">
        <p className="muted" style={{ fontSize: 13, marginBottom: 12 }}>
          Reset every stored metric, error, health result and alert. This cannot be undone.
        </p>
        {!resetConfirm ? (
          <button type="button" className="btn btn-danger" onClick={() => setResetConfirm(true)}>
            Reset all data
          </button>
        ) : (
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <span className="crit" style={{ fontSize: 13, fontWeight: 600 }}>Are you sure?</span>
            <button type="button" className="btn btn-danger" onClick={handleReset}>
              Yes, reset everything
            </button>
            <button type="button" className="btn" onClick={() => setResetConfirm(false)}>Cancel</button>
          </div>
        )}
      </Section>
    </>
  )
}
