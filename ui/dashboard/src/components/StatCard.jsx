// StatCard is the simple tile: a label, a mono value and one line of
// context. Tone carries severity — a value is neutral unless the metric
// itself is in alarm.
export default function StatCard({ label, value, sub, tone, window }) {
  return (
    <div className={`card kpi${tone === 'crit' ? ' is-critical' : ''}`}>
      <div className="kpi-head">
        <span>{label}</span>
        {window ? <span className="window">{window}</span> : null}
      </div>
      <div className={`kpi-value${tone ? ` ${tone}` : ''}`}>{value}</div>
      {sub ? <div className="kpi-foot"><span>{sub}</span></div> : null}
    </div>
  )
}
