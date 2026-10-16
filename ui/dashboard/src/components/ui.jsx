// The component kit from the redesign. Everything here is token-only, so a
// page never reaches for a hex and the theme toggle stays a one-attribute
// flip.

// Sparkline draws a series as a polyline. It renders nothing when there is
// no real series to draw — the design would rather show a window and a
// delta than a shape that means nothing.
export function Sparkline({ values, stroke = 'var(--accent)', width = 88, height = 24, label }) {
  if (!values || values.length < 2) return null
  const max = Math.max(...values)
  const min = Math.min(...values)
  const span = max - min || 1
  const step = width / (values.length - 1)
  const points = values
    .map((v, i) => `${(i * step).toFixed(1)},${(height - 2 - ((v - min) / span) * (height - 4)).toFixed(1)}`)
    .join(' ')
  return (
    <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} role={label ? 'img' : undefined}
      aria-label={label} aria-hidden={label ? undefined : 'true'} focusable="false">
      <polyline points={points} fill="none" stroke={stroke} strokeWidth="1.5"
        strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

// KpiTile is label + window, a mono value, an optional badge, then a foot
// row of context and a sparkline. Every tile answers "compared to what?".
export function KpiTile({ label, window, value, unit, badge, tone, foot, series, seriesStroke, seriesLabel }) {
  return (
    <div className={`card kpi${tone === 'crit' ? ' is-critical' : ''}`}>
      <div className="kpi-head">
        <span>{label}</span>
        {window ? <span className="window">{window}</span> : null}
      </div>
      <div className="kpi-main">
        <span className={`kpi-value${tone === 'crit' ? ' crit' : ''}`}>
          {value}
          {unit ? <span className="unit">{unit}</span> : null}
        </span>
        {badge ? <span className={`badge badge-${badge.tone}`}>{badge.label}</span> : null}
      </div>
      <div className="kpi-foot">
        <span>{foot}</span>
        <Sparkline values={series} stroke={seriesStroke} label={seriesLabel} />
      </div>
    </div>
  )
}

// Delta renders a change against the previous window, with the arrow and
// colour that match the metric's direction (more errors is not good news).
export function Delta({ current, previous, higherIsBetter = true, suffix = 'vs prev window' }) {
  if (previous === null || previous === undefined || !isFinite(previous) || previous === 0) {
    return <span className="muted">no previous window</span>
  }
  const change = ((current - previous) / Math.abs(previous)) * 100
  if (!isFinite(change)) return <span className="muted">—</span>
  const rounded = Math.abs(change) < 0.1 ? 0 : change
  if (rounded === 0) return <span className="muted">flat {suffix}</span>
  const up = rounded > 0
  const good = up === higherIsBetter
  return (
    <span>
      <span className={good ? 'delta-up' : 'delta-down'}>
        {up ? '▲' : '▼'} {Math.abs(rounded).toFixed(1)}%
      </span>{' '}
      {suffix}
    </span>
  )
}

export function Badge({ tone = 'neutral', children, dot = false }) {
  return (
    <span className={`badge badge-${tone}`}>
      {dot ? <i className="dot" /> : null}
      {children}
    </span>
  )
}

// Severity is a word and a dot, never colour alone.
export function Severity({ level, label }) {
  const tone = level === 'critical' ? 'crit' : level === 'warning' ? 'warn' : level === 'info' ? 'info' : 'low'
  const text = label || (level ? level[0].toUpperCase() + level.slice(1) : 'Low')
  return (
    <span className={`severity sev-${tone}`}>
      <i className="dot" />
      {text}
    </span>
  )
}

export function Tabs({ tabs, active, onChange, label = 'Views' }) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {tabs.map((tab) => (
        <button key={tab.id} type="button" role="tab" className="tab"
          aria-selected={active === tab.id} onClick={() => onChange(tab.id)}>
          {tab.label}
          {tab.count !== undefined && tab.count !== null
            ? <span className={`count${tab.tone ? ` ${tab.tone}` : ''}`}>{tab.count}</span>
            : null}
        </button>
      ))}
    </div>
  )
}

// EmptyState replaces a bare grey sentence: what is missing, why, and the
// one thing that would change it.
export function EmptyState({ icon, title, body, action }) {
  return (
    <div className="empty">
      {icon}
      <div className="title">{title}</div>
      <div className="body">{body}</div>
      {action ? <div style={{ marginTop: 4 }}>{action}</div> : null}
    </div>
  )
}

export function PageHead({ title, subtitle, actions }) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {subtitle ? <div className="subtitle">{subtitle}</div> : null}
      </div>
      {actions ? <div className="actions">{actions}</div> : null}
    </div>
  )
}
