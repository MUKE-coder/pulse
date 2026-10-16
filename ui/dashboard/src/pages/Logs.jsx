import { useState, useEffect, useRef } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useAPI } from '../hooks/useAPI'
import { useUI } from '../context/UIContext'
import { PageHead } from '../components/ui'
import { useWebSocket } from '../hooks/useWebSocket'
import LogLines from '../components/LogLines'

const LEVELS = ['debug', 'info', 'warn', 'error']
const LIVE_MAX = 1000

// levelValue orders slog level names ("WARN+2" is 6) so live lines can be
// filtered the way the API filters stored ones.
const base = { DEBUG: -4, INFO: 0, WARN: 4, ERROR: 8 }
function levelValue(level) {
  const m = String(level || 'INFO').toUpperCase().match(/^(DEBUG|INFO|WARN|ERROR)([+-]\d+)?$/)
  return m ? base[m[1]] + Number(m[2] || 0) : 0
}

export default function LogsPage() {
  const { range } = useUI()
  const { get } = useAPI()
  const [params, setParams] = useSearchParams()
  const traceId = params.get('trace_id') || ''
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
    <>
      <PageHead
        title="Logs"
        subtitle={traceId ? 'One request, line by line' : undefined}
        actions={
          <label className="btn" style={{ gap: 8, cursor: 'pointer', color: live ? 'var(--accent)' : undefined }}>
            <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
            Live tail
          </label>
        }
      />

      {!data.capturing && <CaptureHint supported={data.supported} />}

      <form
        onSubmit={(e) => { e.preventDefault(); fetchLogs() }}
        style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}
      >
        <select className="input" style={{ width: 'auto' }} value={level}
          onChange={(e) => setLevel(e.target.value)}>
          <option value="">All levels</option>
          {LEVELS.map((l) => <option key={l} value={l}>{l} and above</option>)}
        </select>
        <input className="input" value={q} onChange={(e) => setQ(e.target.value)}
          placeholder="Search messages" style={{ flex: 1, minWidth: 180, width: 'auto' }} />
        <button type="submit" className="btn">Search</button>
      </form>

      {traceId && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, fontSize: 13, color: 'var(--text-2)' }}>
          Showing one request's lines: <code style={{ color: 'var(--text)' }}>{traceId}</code>
          <Link to={`/pulse/ui/traces/${traceId}`} className="btn btn-sm">Open trace</Link>
          <button type="button" className="btn btn-sm" onClick={() => setParams({})}>Show all</button>
        </div>
      )}

      {loading ? <p style={{ color: 'var(--text-3)' }}>Loading...</p> : (
        <div ref={scroller} style={{ maxHeight: 'calc(100vh - 240px)', overflow: 'auto' }}>
          <LogLines
            logs={data.logs}
            onTrace={(id) => setParams({ trace_id: id })}
            emptyText={data.capturing ? 'No log lines match.' : ''}
          />
        </div>
      )}
    </>
  )
}

function CaptureHint({ supported }) {
  return (
    <div className="card" style={{ fontSize: 13, color: 'var(--text-2)', lineHeight: 1.6 }}>
      <strong style={{ color: 'var(--text)' }}>Log capture is off.</strong> Send your application's logs through
      Pulse to see them here, next to the requests and errors they belong to:
      <pre className="mono" style={{
        marginTop: 10, padding: 12, background: 'var(--bg-2)', border: '1px solid var(--border)',
        borderRadius: 'var(--radius)', fontSize: 12, color: 'var(--text-2)', overflow: 'auto',
      }}>{`slog.SetDefault(slog.New(p.SlogHandler(slog.NewJSONHandler(os.Stdout, nil))))

// or, for the log package, zap or zerolog:
log.SetOutput(p.LogWriter(os.Stderr))`}</pre>
      {!supported && <p style={{ marginTop: 8, color: 'var(--warn)' }}>The configured storage backend doesn't keep logs.</p>}
    </div>
  )
}
