import { useCallback, useEffect, useRef, useState } from 'react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import { useTheme } from '../hooks/useTheme'
import { Empty, KVs, Meter, Panel, Section, StatTile } from '../components/ops'
import Topology from '../components/Topology'

// The ops view: what the application is doing right now, by subsystem —
// requests, the database, the services it calls, and what is firing or being
// logged — each panel reading from the endpoints Pulse already serves.

const RANGES = ['15m', '1h', '6h', '24h']

const paths = (range) => ({
  overview: `/overview?range=${range}`,
  routes: `/routes?range=${range}`,
  db: `/database/overview?range=${range}`,
  slow: '/database/slow-queries?limit=6',
  n1: `/database/n1?range=${range}`,
  deps: `/dependencies?range=${range}`,
  alerts: '/alerts?range=24h&limit=6',
  logs: `/logs?range=${range}&level=warn&limit=40`,
  instances: '/instances',
  storage: '/storage',
  health: '/health/checks',
})

const num = (n) => (n || 0).toLocaleString()
const ms = (ns) => {
  const v = (ns || 0) / 1e6
  if (v >= 1000) return `${(v / 1000).toFixed(2)}s`
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`
}
const pct = (v) => `${(v || 0).toFixed(v >= 10 ? 0 : 2)}%`
const clock = (t) => new Date(t).toLocaleTimeString([], { hour12: false })
const bytes = (mb) => (mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${(mb || 0).toFixed(1)} MB`)

// Thresholds that decide a section's pill. Deliberately plain: a rate over
// 5% is bad, anything measurable is worth a warning.
const errorTone = (rate) => (rate >= 5 ? 'bad' : rate >= 1 ? 'warn' : 'ok')
const latencyTone = (ns) => (ns >= 1e9 ? 'bad' : ns >= 5e8 ? 'warn' : 'ok')
const worstTone = (...tones) => (tones.includes('bad') ? 'bad' : tones.includes('warn') ? 'warn' : 'ok')
const toSeries = (points) => (points || []).map((p) => ({ ts: new Date(p.timestamp).getTime(), v: p.value }))
const lastValues = (points, n = 24) => toSeries(points).slice(-n).map((p) => p.v)

// delta compares the last point of a series with the one before it.
function delta(points, { lowerIsBetter = false, unit = '' } = {}) {
  const values = toSeries(points).map((p) => p.v)
  if (values.length < 4) return null
  const half = Math.floor(values.length / 2)
  const mean = (list) => list.reduce((a, b) => a + b, 0) / Math.max(1, list.length)
  const before = mean(values.slice(0, half))
  const now = mean(values.slice(half))
  if (!before) return null
  const change = ((now - before) / before) * 100
  if (Math.abs(change) < 5) return { text: 'steady', tone: '' }
  const up = change > 0
  return {
    text: `${up ? '+' : ''}${change.toFixed(0)}%${unit} vs earlier`,
    tone: up === lowerIsBetter ? 'up' : 'down',
  }
}

// TimeChart is the shape every timeline on this page takes: one filled
// series, muted grid, time along the bottom.
function TimeChart({ data, color, palette, format = (v) => v, short }) {
  if (!data || data.length < 2) return <Empty>Not enough data yet</Empty>
  const id = `g${color.replace(/[^a-z0-9]/gi, '')}`
  return (
    <div className={`ops-chart${short ? ' short' : ''}`}>
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 6, right: 4, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.28} />
              <stop offset="100%" stopColor={color} stopOpacity={0} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke={palette.grid} vertical={false} />
          <XAxis dataKey="ts" type="number" domain={['dataMin', 'dataMax']} scale="time"
            tickFormatter={clock} tick={{ fill: palette.ink3, fontSize: 10 }}
            axisLine={{ stroke: palette.line }} tickLine={false} minTickGap={40} />
          <YAxis tick={{ fill: palette.ink3, fontSize: 10 }} axisLine={false} tickLine={false}
            width={44} tickFormatter={format} />
          <Tooltip labelFormatter={clock} formatter={(v) => [format(v), '']} separator=""
            contentStyle={{ background: palette.surface, border: `1px solid ${palette.line}`,
              borderRadius: 8, fontSize: 12, color: palette.ink }} />
          <Area type="monotone" dataKey="v" stroke={color} strokeWidth={1.8} fill={`url(#${id})`}
            dot={false} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

export default function Dashboard() {
  const { get } = useAPI()
  const { dark, palette, toggle } = useTheme()
  const [range, setRange] = useState('1h')
  const [paused, setPaused] = useState(false)
  const [data, setData] = useState({})
  const [loading, setLoading] = useState(true)
  const [tail, setTail] = useState([])
  const liveTail = useRef([])

  const load = useCallback(async () => {
    const entries = Object.entries(paths(range))
    const results = await Promise.all(entries.map(async ([key, path]) => {
      try {
        const res = await get(path)
        return [key, res.ok ? await res.json() : null]
      } catch {
        return [key, null]
      }
    }))
    setData(Object.fromEntries(results))
    setLoading(false)
  }, [get, range])

  useEffect(() => {
    load()
    if (paused) return
    const timer = setInterval(load, 10000)
    return () => clearInterval(timer)
  }, [load, paused])

  // Warnings and errors arrive live; the fetched page gives the tail its
  // starting point.
  useEffect(() => {
    if (data.logs?.logs) {
      liveTail.current = data.logs.logs.slice(-60)
      setTail(liveTail.current)
    }
  }, [data.logs])

  useWebSocket(paused ? [] : ['logs'], (msg) => {
    if (msg.type !== 'logs' || !Array.isArray(msg.payload)) return
    const fresh = msg.payload.filter((l) => String(l.level).startsWith('WARN') || String(l.level).startsWith('ERROR'))
    if (!fresh.length) return
    liveTail.current = [...liveTail.current, ...fresh].slice(-60)
    setTail(liveTail.current)
  })

  if (loading) return <div className="ops"><p className="ops-status">Loading…</p></div>

  const o = data.overview || {}
  const routes = data.routes || []
  const deps = data.deps || []
  const db = data.db || {}
  const pool = db.pool
  const instances = data.instances?.instances || []
  const alerts = data.alerts || []
  const firing = alerts.filter((a) => a.state === 'firing')
  const health = data.health?.status || o.health_status || 'healthy'

  const throughput = toSeries(o.throughput_series)
  const errorSeries = toSeries(o.error_series)
  const apiTone = worstTone(errorTone(o.error_rate), latencyTone(o.p95_latency))
  const depTone = deps.length ? errorTone(Math.max(...deps.map((d) => d.error_rate || 0))) : 'ok'
  const poolUse = pool && pool.max_open_connections > 0 ? (pool.in_use / pool.max_open_connections) * 100 : 0
  const dbTone = worstTone(poolUse >= 80 ? 'warn' : 'ok', (db.n1_count || 0) > 0 ? 'warn' : 'ok')
  const queue = (data.storage?.kinds || []).find((k) => k.kind === 'write_queue')
  const fiveXX = (r) => Object.entries(r.status_codes || {})
    .reduce((n, [code, count]) => n + (Number(code) >= 500 ? count : 0), 0)
  const slowest = [...routes].sort((a, b) => (b.p95_latency || 0) - (a.p95_latency || 0)).slice(0, 6)
  const trafficDelta = delta(o.throughput_series)
  const errorDelta = delta(o.error_series, { lowerIsBetter: true })

  return (
    <div className="ops">
      <header className="ops-top">
        <h1>{o.app_name || 'Pulse'}</h1>
        <span className="ops-env">
          {data.instances?.shared && instances.length > 1
            ? `${instances.length} instances`
            : data.instances?.self || 'single instance'}
        </span>
        <span className={`ops-live${paused ? ' paused' : ''}`}>
          <i />{paused ? 'Paused' : 'Live, 10s'}
        </span>
        <span className="ops-spacer" />
        <div className="ops-seg" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button key={r} aria-pressed={r === range} onClick={() => setRange(r)}>{r}</button>
          ))}
        </div>
        <button className="ops-btn" onClick={() => setPaused((p) => !p)}>{paused ? 'Resume' : 'Pause'}</button>
        <button className="ops-btn" onClick={toggle} aria-label="Toggle theme">{dark ? 'Light' : 'Dark'}</button>
      </header>

      <p className="ops-status">
        <b>{health}</b> · uptime <b>{o.uptime || '—'}</b> · p95 <b>{ms(o.p95_latency)}</b> ·
        error rate <b>{pct(o.error_rate)}</b> · <b>{num(o.total_requests)}</b> requests in the last {range}
        {firing.length > 0 && <> · <b>{firing.length}</b> alert{firing.length === 1 ? '' : 's'} firing</>}
        {data.instances?.leader && <> · leader <b>{data.instances.leader}</b></>}
      </p>

      <Topology
        clients={{
          rows: [['Requests', num(o.total_requests)], ['Error rate', pct(o.error_rate)]],
          status: errorTone(o.error_rate),
          edge: `${(o.rpm || 0).toFixed(1)}/min`,
        }}
        api={{
          title: o.app_name || 'API',
          status: apiTone,
          rows: [['p95', ms(o.p95_latency)], ['Instances', String(instances.length || 1)],
            ['Goroutines', num(o.active_goroutines)]],
        }}
        db={data.db ? {
          title: 'Database',
          status: dbTone,
          hot: (db.total_queries || 0) > 0,
          edge: `${num(db.total_queries)} queries`,
          rows: [['Patterns', num(db.pattern_count)], ['Slow', num(db.slow_query_count)],
            ['N+1', num(db.n1_count)]],
        } : null}
        deps={deps.slice(0, 2).map((d) => ({
          title: d.name,
          status: errorTone(d.error_rate),
          hot: (d.request_count || 0) > 0,
          edge: `${num(d.request_count)} calls`,
          rows: [['p95', ms(d.p95_latency)], ['Errors', pct(d.error_rate)]],
        }))}
      />

      <div className="ops-stats">
        <StatTile label="Traffic" value={(o.rpm || 0).toFixed(1)} unit="/min" color={palette.accent}
          series={lastValues(o.throughput_series)} delta={trafficDelta?.text} deltaTone={trafficDelta?.tone} />
        <StatTile label="Latency, p95" value={ms(o.p95_latency)} color={palette.accent}
          delta={`avg ${ms(o.avg_latency)}`} />
        <StatTile label="Error rate" value={pct(o.error_rate)} color={palette.bad}
          series={lastValues(o.error_series)} delta={errorDelta?.text} deltaTone={errorDelta?.tone} />
        <StatTile label="Saturation" color={palette.warn} delta={`heap ${bytes(o.heap_alloc_mb)}`}
          value={pool && pool.max_open_connections > 0 ? `${poolUse.toFixed(0)}%` : num(o.active_goroutines)}
          unit={pool && pool.max_open_connections > 0 ? ' of pool' : ' goroutines'} />
      </div>

      <Section title="API" status={apiTone === 'ok' ? 'healthy' : apiTone === 'warn' ? 'degraded' : 'unhealthy'}
        tone={apiTone} meta={`${routes.length} routes · ${num(o.total_requests)} requests`} />
      <div className="ops-grid">
        <Panel title="Request rate" now={`${(o.rpm || 0).toFixed(1)}/min`} width="w6">
          <TimeChart data={throughput} color={palette.accent} palette={palette} format={(v) => num(v)} />
        </Panel>
        <Panel title="Errors" now={pct(o.error_rate)} width="w6">
          <TimeChart data={errorSeries} color={palette.bad} palette={palette} format={(v) => num(v)} />
        </Panel>
        <Panel title="Saturation" width="w4">
          <Meter label="Goroutines, of 1,000" value={num(o.active_goroutines)}
            pct={Math.min(100, (o.active_goroutines || 0) / 10)} />
          <Meter label="Heap, of 512 MB" value={bytes(o.heap_alloc_mb)}
            pct={Math.min(100, ((o.heap_alloc_mb || 0) / 512) * 100)} />
          {pool && pool.max_open_connections > 0 ? (
            <Meter label="Database pool in use" value={`${pool.in_use} / ${pool.max_open_connections}`}
              pct={poolUse} tone={poolUse >= 90 ? 'bad' : poolUse >= 70 ? 'warn' : ''} />
          ) : pool && (
            <Meter label="Database connections open" value={num(pool.open_connections)}
              pct={Math.min(100, (pool.open_connections || 0) * 10)} />
          )}
          {queue && (
            <Meter label="Write queue" value={`${num(queue.stored)} / ${num(queue.capacity)}`}
              pct={queue.capacity ? (queue.stored / queue.capacity) * 100 : 0}
              tone={queue.dropped > 0 ? 'bad' : ''} />
          )}
        </Panel>
        <Panel title="Slowest endpoints, p95" width="w8">
          {slowest.length === 0 ? <Empty>No requests recorded yet</Empty> : (
            <div className="ops-table-wrap">
              <table>
                <thead><tr><th>Route</th><th className="num">p95</th><th className="num">req/min</th><th className="num">5xx</th></tr></thead>
                <tbody>
                  {slowest.map((r) => (
                    <tr key={`${r.method} ${r.path}`}>
                      <td><span className="ops-method">{r.method}</span>{r.path}</td>
                      <td className="num">{ms(r.p95_latency)}</td>
                      <td className="num">{(r.rpm || 0).toFixed(1)}</td>
                      <td className="num">{num(fiveXX(r))}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>

      <Section title="Database" status={dbTone === 'ok' ? 'healthy' : 'pressure'} tone={dbTone}
        meta={pool && pool.max_open_connections > 0
          ? `${pool.in_use} of ${pool.max_open_connections} connections in use · ${num(pool.wait_count)} waits`
          : `${num(db.total_queries)} queries in the last ${range}`} />
      <div className="ops-grid">
        <Panel title="Query load" width="w3">
          <KVs one rows={[
            ['Queries', num(db.total_queries)],
            ['Distinct patterns', num(db.pattern_count)],
            ['Slow queries', num(db.slow_query_count)],
            ['N+1 detections', num(db.n1_count)],
            ['Pool waits', pool ? num(pool.wait_count) : '—'],
            ['Idle connections', pool ? num(pool.idle) : '—'],
          ]} />
        </Panel>
        <Panel title="Slowest queries" width="w5">
          {(data.slow || []).length === 0 ? <Empty>No slow queries</Empty> : (
            <div className="ops-table-wrap">
              <table>
                <thead><tr><th>Query</th><th className="num">duration</th><th className="num">rows</th></tr></thead>
                <tbody>
                  {(data.slow || []).slice(0, 6).map((q, i) => (
                    <tr key={i}>
                      <td><span className="ops-trunc">{q.normalized_sql || q.sql}</span></td>
                      <td className="num">{ms(q.duration)}</td>
                      <td className="num">{num(q.rows_affected)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
        <Panel title="N+1 detections" width="w4">
          {(data.n1 || []).length === 0 ? <Empty>None detected</Empty> : (
            <div className="ops-table-wrap">
              <table>
                <thead><tr><th>Pattern</th><th className="num">repeats</th><th className="num">total</th></tr></thead>
                <tbody>
                  {(data.n1 || []).slice(0, 6).map((d, i) => (
                    <tr key={i}>
                      <td><span className="ops-trunc">{d.pattern}</span></td>
                      <td className="num">{num(d.count)}</td>
                      <td className="num">{ms(d.total_duration)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>

      <Section title="Outbound dependencies" status={depTone === 'ok' ? 'healthy' : depTone === 'warn' ? 'degraded' : 'failing'}
        tone={depTone} meta={deps.length ? `${deps.length} services called` : 'wrap clients with pulse.WrapHTTPClient'} />
      <div className="ops-grid">
        <Panel title="Calls by service" width="w8">
          {deps.length === 0 ? (
            <Empty>No outbound calls recorded. Wrap an http.Client with pulse.WrapHTTPClient to see them here.</Empty>
          ) : (
            <div className="ops-table-wrap">
              <table>
                <thead>
                  <tr><th>Service</th><th className="num">calls</th><th className="num">p95</th>
                    <th className="num">errors</th><th className="num">availability</th></tr>
                </thead>
                <tbody>
                  {deps.slice(0, 8).map((d) => (
                    <tr key={d.name}>
                      <td>{d.name}</td>
                      <td className="num">{num(d.request_count)}</td>
                      <td className="num">{ms(d.p95_latency)}</td>
                      <td className="num">{pct(d.error_rate)}</td>
                      <td className="num">{pct(d.availability)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
        <Panel title="Storage" width="w4">
          <KVs one rows={[
            ['Backend', data.storage?.driver || '—'],
            ['Retention', `${data.storage?.retention_hours ?? '—'}h`],
            ['Covered', pct((data.storage?.retention_coverage ?? 1) * 100)],
            ['Limited by', data.storage?.limited_by || 'nothing'],
            ['Dropped writes', num(queue?.dropped)],
            ['Failed writes', num(queue?.failed)],
          ]} />
        </Panel>
      </div>

      <Section title="Alerts and logs" meta={`${firing.length} firing · ${tail.length} recent warnings`} />
      <div className="ops-grid">
        <Panel title="Alerts" width="w5">
          {alerts.length === 0 ? <Empty>Nothing has fired</Empty> : (
            <div className="ops-alerts">
              {alerts.slice(0, 6).map((a) => (
                <div className="ops-alert" key={a.id}>
                  <i className={a.state === 'resolved' ? 'ok' : a.severity === 'critical' ? 'bad' : 'warn'} />
                  <div>
                    <div className="t">{a.rule_name}{a.instance_id && ` · ${a.instance_id}`}</div>
                    <div className="s">{a.message}</div>
                  </div>
                  <time dateTime={a.fired_at}>{clock(a.fired_at)}</time>
                </div>
              ))}
            </div>
          )}
        </Panel>
        <Panel title="Log tail, warnings and errors" now={data.logs?.capturing ? '' : 'capture off'} width="w7">
          {tail.length === 0 ? (
            <Empty>
              {data.logs?.capturing
                ? 'No warnings or errors logged in this window.'
                : 'Log capture is off. Route your logs through p.SlogHandler or p.LogWriter to see them here.'}
            </Empty>
          ) : (
            <div className="ops-logs">
              {tail.slice().reverse().map((l, i) => (
                <div key={i}>
                  <span className="ts">{clock(l.time)}</span>
                  <span className={`lvl ${String(l.level).toLowerCase().startsWith('error') ? 'error' : 'warn'}`}>{l.level}</span>
                  <span className="msg" title={l.message}>{l.message}</span>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>
    </div>
  )
}
