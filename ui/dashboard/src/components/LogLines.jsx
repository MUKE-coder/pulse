import { Link } from 'react-router-dom'

const levelColors = { DEBUG: '#64748b', INFO: '#818cf8', WARN: '#f59e0b', ERROR: '#ef4444' }

// slog levels between the named ones read like "WARN+2".
const levelColor = (level) => levelColors[String(level || 'INFO').replace(/[+-]\d+$/, '')] || '#94a3b8'

const clock = (t) => {
  const d = new Date(t)
  return `${d.toLocaleTimeString([], { hour12: false })}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

const traceStyle = {
  color: '#818cf8', fontSize: 11, whiteSpace: 'nowrap', background: 'none', border: 'none',
  padding: 0, cursor: 'pointer', fontFamily: 'inherit', textDecoration: 'none',
}

// LogLines renders captured log lines, oldest first. A line's trace ID
// calls onTrace when given, or links to that request's lines on the Logs
// page.
export default function LogLines({ logs, onTrace, emptyText = 'No log lines', maxHeight }) {
  if (!logs?.length) {
    return emptyText ? <p style={{ color: '#64748b', fontSize: 13 }}>{emptyText}</p> : null
  }
  return (
    <div style={{
      background: '#0a0a12', borderRadius: 6, border: '1px solid #1e1e2e', overflow: 'auto', maxHeight,
      fontFamily: "'SF Mono', 'Fira Code', monospace", fontSize: 12, lineHeight: 1.6,
    }}>
      {logs.map((l, i) => (
        <div key={i} style={{
          display: 'flex', gap: 10, padding: '3px 12px', borderBottom: '1px solid #13131d', alignItems: 'baseline',
        }}>
          <span style={{ color: '#475569', whiteSpace: 'nowrap' }} title={new Date(l.time).toLocaleString()}>{clock(l.time)}</span>
          <span style={{ color: levelColor(l.level), fontWeight: 700, width: 64, flexShrink: 0 }}>{l.level}</span>
          <span style={{ color: '#e2e8f0', whiteSpace: 'pre-wrap', wordBreak: 'break-word', flex: 1 }}>
            {l.message}
            {l.attrs && Object.entries(l.attrs).map(([k, v]) => (
              <span key={k} style={{ color: '#64748b', marginLeft: 10 }}>
                {k}=<span style={{ color: '#94a3b8' }}>{v}</span>
              </span>
            ))}
          </span>
          {l.trace_id && (onTrace
            ? <button type="button" onClick={() => onTrace(l.trace_id)} style={traceStyle}
                title="Show only this request's lines">{l.trace_id.slice(0, 8)}</button>
            : <Link to={`/pulse/ui/logs?trace_id=${l.trace_id}`} style={traceStyle}
                title="Show this request's lines">{l.trace_id.slice(0, 8)}</Link>
          )}
        </div>
      ))}
    </div>
  )
}
