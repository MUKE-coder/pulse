// Topology draws the path a request takes — clients, this application's
// instances, its database and the services it calls out to — with each
// node's own traffic and health, so a glance says where trouble sits.

const W = 232
const H = 96

function Node({ x, y, title, rows, status = 'ok' }) {
  return (
    <g className={`node ${status}`} transform={`translate(${x},${y})`}>
      <rect width={W} height={H} rx="10" />
      <circle className={`dot ${status}`} cx={W - 18} cy="20" r="4.5" />
      <text className="name" x="14" y="25">{title}</text>
      {rows.map(([label, value], i) => (
        <text key={label} className="kv" x="14" y={48 + i * 18}>
          {label} <tspan className="v">{value}</tspan>
        </text>
      ))}
    </g>
  )
}

function Edge({ from, to, y, label, hot }) {
  const mid = (from + to) / 2
  return (
    <g>
      <path className={`edge${hot ? ' hot' : ''}`} d={`M ${from} ${y} H ${to}`} />
      <path className={`edge${hot ? ' hot' : ''}`} d={`M ${to - 8} ${y - 4} l 8 4 l -8 4`} />
      {label && <text className="edge-label" x={mid} y={y - 8} textAnchor="middle">{label}</text>}
    </g>
  )
}

export default function Topology({ clients, api, db, deps = [] }) {
  const height = 20 + H + deps.length * (H + 20) + 20
  return (
    <section className="ops-topo" aria-label="Request path">
      <svg viewBox={`0 0 1240 ${height}`} role="img" aria-label="Service map with live status">
        <Node x={20} y={20} title="Clients" rows={clients.rows} status={clients.status} />
        <Edge from={20 + W} to={420} y={68} label={clients.edge} hot />
        <Node x={420} y={20} title={api.title} rows={api.rows} status={api.status} />
        {db && (
          <>
            <Edge from={420 + W} to={820} y={68} label={db.edge} hot={db.hot} />
            <Node x={820} y={20} title={db.title} rows={db.rows} status={db.status} />
          </>
        )}
        {deps.map((d, i) => {
          const y = 20 + (i + 1) * (H + 20)
          return (
            <g key={d.title}>
              <path className={`edge${d.hot ? ' hot' : ''}`}
                d={`M ${420 + W / 2} ${20 + H} V ${y + H / 2} H 820`} />
              <text className="edge-label" x={660} y={y + H / 2 - 8} textAnchor="middle">{d.edge}</text>
              <Node x={820} y={y} title={d.title} rows={d.rows} status={d.status} />
            </g>
          )
        })}
      </svg>
    </section>
  )
}
