import { Area, AreaChart, ResponsiveContainer } from 'recharts'

// Building blocks of the ops dashboard: the section headers, panels, meters
// and stat tiles its layout is made of. Styling lives in ops.css.

export function Pill({ tone = 'ok', children }) {
  return <span className={`ops-pill ${tone}`}>{children}</span>
}

export function Section({ title, status, tone = 'ok', meta }) {
  return (
    <div className="ops-section">
      <h2>{title}</h2>
      {status && <Pill tone={tone}>{status}</Pill>}
      {meta && <span className="meta">{meta}</span>}
    </div>
  )
}

export function Panel({ title, now, width = 'w4', children }) {
  return (
    <div className={`ops-panel ${width}`}>
      {title && <h3>{title}{now != null && now !== '' && <span className="now">{now}</span>}</h3>}
      {children}
    </div>
  )
}

export function Empty({ children }) {
  return <p className="ops-empty">{children}</p>
}

// Meter is a labelled value over a proportional bar: how full something is.
export function Meter({ label, value, pct, tone }) {
  const width = Math.max(0, Math.min(100, pct || 0))
  return (
    <div className="ops-meter">
      <div className="row"><span>{label}</span><span>{value}</span></div>
      <div className="ops-bar"><b className={tone || ''} style={{ width: `${width}%` }} /></div>
    </div>
  )
}

export function KVs({ rows, one }) {
  return (
    <div className={`ops-kvs${one ? ' one' : ''}`}>
      {rows.map(([label, value]) => (
        <div key={label}><span>{label}</span><span>{value}</span></div>
      ))}
    </div>
  )
}

// StatTile is one golden signal: the current value, how it moved, and a
// sparkline of the window.
export function StatTile({ label, value, unit, delta, deltaTone, series, color }) {
  const points = (series || []).map((p, i) => ({ i, v: p }))
  return (
    <div className="ops-stat">
      <div>
        <div className="l">{label}</div>
        <div className="v">{value}{unit && <small>{unit}</small>}</div>
        {delta && <div className={`d ${deltaTone || ''}`}>{delta}</div>}
      </div>
      <div className="spark">
        {points.length > 1 && (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={points} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
              <Area type="monotone" dataKey="v" stroke={color} fill={color} fillOpacity={0.14}
                strokeWidth={1.5} dot={false} isAnimationActive={false} />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>
    </div>
  )
}
