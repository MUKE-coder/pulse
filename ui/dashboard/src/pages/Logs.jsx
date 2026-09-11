import { useState, useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import LogLines from '../components/LogLines'

const RANGES = ['5m', '15m', '1h', '6h', '24h', '7d']
const LEVELS = ['debug', 'info', 'warn', 'error']
const LIVE_MAX = 1000

// levelValue orders slog level names ("WARN+2" is 6) so live lines can be
// filtered the way the API filters stored ones.
const base = { DEBUG: -4, INFO: 0, WARN: 4, ERROR: 8 }
function levelValue(level) {
  const m = String(level || 'INFO').toUpperCase().match(/^(DEBUG|INFO|WARN|ERROR)([+-]\d+)?$/)
  return m ? base[m[1]] + Number(m[2] || 0) : 0
}

const btn = {
  padding: '6px 14px', borderRadius: 6, border: '1px solid #6366f140', background: '#6366f118',
  color: '#818cf8', fontSize: 13, fontWeight: 600, cursor: 'pointer',
}

export default function LogsPage() {
  const { get } = useAPI()
  const [params, setParams] = useSearchParams()
  const traceId = params.get('trace_id') || ''
  const [range, setRange] = useState('1h')
  const [level, setLevel] = useState('')
  const [q, setQ] = useState('')
  const [live, setLive] = useState(false)
  const [data, setData] = useState({ logs: [], capturing: true, supported: true })
  const [loading, setLoading] = useState(true)
  const scroller = useRef(null)
  const filters = useRef({})
  filters.current = { traceId, level, q }

  const fetchLogs = async () => {
    const p = new URLSearchParams({ limit: '500' })
    if (traceId) p.set('trace_id', traceId)
    else p.set('range', range)
    if (level) p.set('level', level)
    if (q) p.set('q', q)
    try {
      const res = await get(`/logs?${p}`)
      if (res.ok) setData(await res.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchLogs() }, [traceId, range, level])

  useWebSocket(live ? ['logs'] : [], (msg) => {
    if (msg.type !== 'logs' || !Array.isArray(msg.payload)) return
    const f = filters.current
    const min = f.level ? levelValue(f.level) : -Infinity
    const text = f.q.toLowerCase()
    const fresh = msg.payload.filter((l) =>
      (!f.traceId || l.trace_id === f.traceId) &&
      levelValue(l.level) >= min &&
      (!text || l.message.toLowerCase().includes(text)))
    if (fresh.length) setData((d) => ({ ...d, logs: [...d.logs, ...fresh].slice(-LIVE_MAX) }))
  })

  // Follow the tail while live.
  useEffect(() => {
    if (live && scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight
  }, [data.logs, live])

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Logs</h1>
        <label style={{
          display: 'flex', gap: 6, alignItems: 'center', fontSize: 13, cursor: 'pointer',
          color: live ? '#22c55e' : '#8892a4',
        }}>
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
          Live tail
        </label>
      </div>

      {!data.capturing && <CaptureHint supported={data.supported} />}

      <form
        onSubmit={(e) => { e.preventDefault(); fetchLogs() }}
        style={{ display: 'flex', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}
      >
        <select value={range} onChange={(e) => setRange(e.target.value)} disabled={!!traceId}
          title={traceId ? 'A request trace is shown whenever it happened' : undefined}>
          {RANGES.map((r) => <option key={r} value={r}>Last {r}</option>)}
        </select>
        <select value={level} onChange={(e) => setLevel(e.target.value)}>
          <option value="">All levels</option>
          {LEVELS.map((l) => <option key={l} value={l}>{l} and above</option>)}
        </select>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search messages"
          style={{ flex: 1, minWidth: 180 }} />
        <button type="submit" style={btn}>Search</button>
      </form>

      {traceId && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, fontSize: 13, color: '#94a3b8' }}>
          Showing one request's lines: <code style={{ color: '#e2e8f0' }}>{traceId}</code>
          <button type="button" onClick={() => setParams({})} style={{ ...btn, padding: '2px 10px', fontSize: 12 }}>
            Show all
          </button>
        </div>
      )}

      {loading ? <p style={{ color: '#64748b' }}>Loading...</p> : (
        <div ref={scroller} style={{ maxHeight: 'calc(100vh - 240px)', overflow: 'auto' }}>
          <LogLines
            logs={data.logs}
            onTrace={(id) => setParams({ trace_id: id })}
            emptyText={data.capturing ? 'No log lines match.' : ''}
          />
        </div>
      )}
    </div>
  )
}

function CaptureHint({ supported }) {
  return (
    <div style={{
      padding: 16, border: '1px solid #6366f130', background: '#6366f10d', borderRadius: 8,
      marginBottom: 16, fontSize: 13, color: '#cbd5e1', lineHeight: 1.6,
    }}>
      <strong style={{ color: '#e2e8f0' }}>Log capture is off.</strong> Send your application's logs through
      Pulse to see them here, next to the requests and errors they belong to:
      <pre style={{
        marginTop: 10, padding: 12, background: '#0a0a12', borderRadius: 6, fontSize: 12, color: '#94a3b8',
        fontFamily: "'SF Mono', 'Fira Code', monospace", overflow: 'auto',
      }}>{`slog.SetDefault(slog.New(p.SlogHandler(slog.NewJSONHandler(os.Stdout, nil))))

// or, for the log package, zap or zerolog:
log.SetOutput(p.LogWriter(os.Stderr))`}</pre>
      {!supported && <p style={{ marginTop: 8, color: '#f59e0b' }}>The configured storage backend doesn't keep logs.</p>}
    </div>
  )
}
