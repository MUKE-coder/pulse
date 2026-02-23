import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'

export default function SettingsPage() {
  const { get, post } = useAPI()
  const [settings, setSettings] = useState(null)
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
    <div style={{
      background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
      padding: 20, marginBottom: 16,
    }}>
      <h3 style={{ fontSize: 14, fontWeight: 600, color: '#e2e8f0', marginBottom: 14 }}>{title}</h3>
      {children}
    </div>
  )

  const ConfigRow = ({ label, value }) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '6px 0', borderBottom: '1px solid #16161e' }}>
      <span style={{ color: '#64748b', fontSize: 13 }}>{label}</span>
      <span style={{ color: '#e2e8f0', fontSize: 13, fontFamily: "'SF Mono', monospace" }}>
        {typeof value === 'boolean' ? (value ? 'true' : 'false') : String(value ?? '-')}
      </span>
    </div>
  )

  return (
    <div>
      <h1 style={{ fontSize: 22, fontWeight: 700, marginBottom: 16 }}>Settings</h1>

      {message && (
        <div style={{
          padding: '10px 14px', borderRadius: 6, marginBottom: 14, fontSize: 13,
          background: message.includes('fail') || message.includes('error') ? '#ef444418' : '#22c55e18',
          color: message.includes('fail') || message.includes('error') ? '#ef4444' : '#22c55e',
          border: `1px solid ${message.includes('fail') || message.includes('error') ? '#ef444430' : '#22c55e30'}`,
        }}>
          {message}
        </div>
      )}

      {/* Export */}
      <Section title="Data Export">
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <select value={exportType} onChange={(e) => setExportType(e.target.value)}>
            <option value="requests">Requests</option>
            <option value="queries">Queries</option>
            <option value="errors">Errors</option>
            <option value="runtime">Runtime</option>
            <option value="alerts">Alerts</option>
          </select>
          <select value={exportFormat} onChange={(e) => setExportFormat(e.target.value)}>
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
          <select value={exportRange} onChange={(e) => setExportRange(e.target.value)}>
            <option value="5m">Last 5m</option>
            <option value="15m">Last 15m</option>
            <option value="1h">Last 1h</option>
            <option value="6h">Last 6h</option>
            <option value="24h">Last 24h</option>
            <option value="7d">Last 7d</option>
          </select>
          <button
            onClick={handleExport}
            disabled={exporting}
            style={{
              padding: '8px 16px', borderRadius: 6, border: 'none', fontSize: 13,
              fontWeight: 600, cursor: 'pointer',
              background: 'linear-gradient(135deg, #6366f1, #7c3aed)', color: '#fff',
              opacity: exporting ? 0.6 : 1,
            }}
          >
            {exporting ? 'Exporting...' : 'Export'}
          </button>
        </div>
      </Section>

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
      <Section title="Danger Zone">
        <p style={{ color: '#94a3b8', fontSize: 13, marginBottom: 12 }}>
          Reset all stored metrics, errors, health history, and alerts. This action cannot be undone.
        </p>
        {!resetConfirm ? (
          <button
            onClick={() => setResetConfirm(true)}
            style={{
              padding: '8px 16px', borderRadius: 6, fontSize: 13, fontWeight: 600,
              cursor: 'pointer', background: '#ef444418', color: '#ef4444',
              border: '1px solid #ef444430',
            }}
          >
            Reset All Data
          </button>
        ) : (
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <span style={{ color: '#ef4444', fontSize: 13, fontWeight: 600 }}>Are you sure?</span>
            <button onClick={handleReset} style={{
              padding: '8px 16px', borderRadius: 6, fontSize: 13, fontWeight: 600,
              cursor: 'pointer', background: '#ef4444', color: '#fff', border: 'none',
            }}>Yes, Reset Everything</button>
            <button onClick={() => setResetConfirm(false)} style={{
              padding: '8px 16px', borderRadius: 6, fontSize: 13, fontWeight: 600,
              cursor: 'pointer', background: '#2a2a3e', color: '#94a3b8', border: 'none',
            }}>Cancel</button>
          </div>
        )}
      </Section>
    </div>
  )
}
