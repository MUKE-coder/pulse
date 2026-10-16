import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Download, Play, Database as DatabaseIcon, FileSearch } from 'lucide-react'
import { useAPI } from '../hooks/useAPI'
import { useUI, usePolling, rangeSeconds } from '../context/UIContext'
import { KpiTile, Delta, Tabs, EmptyState, PageHead, Severity } from '../components/ui'

const ms = (ns) => (ns === undefined || ns === null ? '—' : `${(ns / 1e6).toFixed(ns < 1e6 ? 2 : ns < 1e8 ? 1 : 0)}ms`)
const num = (v) => (v === undefined || v === null ? '—' : Number(v).toLocaleString())

// Note on sparklines: the slow-query list and the N+1 rankings are top-N
// samples rather than time series, so this page carries windows, deltas and
// context instead of drawing a shape that would not mean anything. The
// Overview, which does have per-minute series, draws them.

// The N+1 severity bands the design uses, measured on added latency.
function n1Severity(addedNs) {
  const added = addedNs / 1e6
  if (added > 300) return 'critical'
  if (added >= 100) return 'warning'
  return 'low'
}

// shortCaller keeps the tail of a path, which is the part that identifies
// the file: internal/services/catalog_report.go:148.
function shortCaller(file, line) {
  if (!file) return '—'
  const parts = file.split(/[\\/]/)
  const tail = parts.slice(-2).join('/')
  return line ? `${tail}:${line}` : tail
}

function sinceText(iso) {
  const then = new Date(iso).getTime()
  if (!isFinite(then)) return '—'
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 60) return `${secs} s ago`
  if (secs < 3600) return `${Math.round(secs / 60)} min ago`
  if (secs < 86400) return `${Math.round(secs / 3600)} h ago`
  return `${Math.round(secs / 86400)} d ago`
}

export default function DatabasePage() {
  const { get, post, request } = useAPI()
  const { range, windowLabel, rangeLabel } = useUI()
  const [overview, setOverview] = useState(null)
  const [previous, setPrevious] = useState(null)
  const [slowQueries, setSlowQueries] = useState([])
  const [patterns, setPatterns] = useState([])
  const [n1, setN1] = useState([])
  const [plans, setPlans] = useState(null)
  const [pool, setPool] = useState(null)
  const [poolSeen, setPoolSeen] = useState(null)
  const [tab, setTab] = useState('n1')
  const [loading, setLoading] = useState(true)
  const [explaining, setExplaining] = useState(false)
  const [notice, setNotice] = useState(null)

  const load = useCallback(async () => {
    const seconds = rangeSeconds(range)
    try {
      const [oRes, sRes, pRes, nRes, poolRes, planRes] = await Promise.all([
        get(`/database/overview?range=${range}`),
        get(`/database/slow-queries?limit=100`),
        get(`/database/patterns?range=${range}`),
        get(`/database/n1/ranked?range=${range}&limit=50`),
        get('/database/pool'),
        get('/database/plans?limit=50'),
      ])
      if (oRes.ok) setOverview(await oRes.json())
      if (sRes.ok) setSlowQueries(await sRes.json())
      if (pRes.ok) setPatterns(await pRes.json())
      if (nRes.ok) {
        const payload = await nRes.json()
        setN1(Array.isArray(payload) ? payload : payload?.findings || [])
      }
      if (poolRes.ok) {
        const stats = await poolRes.json()
        setPool(stats)
        // Remember the wait count at first sight, so "Rising" means it rose
        // while you were watching rather than "is not zero".
        setPoolSeen((seen) => (seen === null ? stats?.wait_count ?? 0 : seen))
      }
      if (planRes.ok) setPlans(await planRes.json())

      // The window before this one, for an honest delta.
      const end = new Date(Date.now() - seconds * 1000)
      const prevRes = await get(`/database/overview?range=${range}&end=${end.toISOString()}`)
      if (prevRes.ok) setPrevious(await prevRes.json())
    } catch {}
    setLoading(false)
  }, [get, range])

  usePolling(load, 15000, [load])

  const runExplain = async () => {
    setExplaining(true)
    setNotice(null)
    try {
      const res = await post('/database/explain')
      const data = await res.json()
      if (!res.ok) setNotice(data.hint || data.error || 'EXPLAIN is unavailable')
      else if (data.explained) {
        setNotice(data.generic
          ? `Captured a generic plan for ${data.query} — planned without the values it ran with.`
          : `Captured the plan for ${data.query}`)
        setTab('plans')
      } else {
        setNotice(data.message)
      }
      const planRes = await get('/database/plans?limit=50')
      if (planRes.ok) setPlans(await planRes.json())
    } catch (err) {
      setNotice(String(err.message || err))
    }
    setExplaining(false)
  }

  const exportCSV = async () => {
    try {
      const res = await request('/data/export', {
        method: 'POST',
        body: { format: 'csv', type: 'queries', range },
      })
      if (!res.ok) return setNotice('Export failed')
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `pulse-queries-${range}.csv`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      setNotice(String(err.message || err))
    }
  }

  const topPattern = useMemo(() => {
    if (!patterns?.length) return null
    return [...patterns].sort((a, b) => (b.count || 0) - (a.count || 0))[0]
  }, [patterns])
  const slowP95 = useMemo(() => {
    if (!slowQueries?.length) return null
    const sorted = slowQueries.map((q) => q.duration).sort((a, b) => a - b)
    return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * 0.95))]
  }, [slowQueries])

  const n1Routes = useMemo(() => new Set(n1.map((d) => d.route).filter(Boolean)).size, [n1])
  const n1Count = overview?.n1_count ?? n1.length
  const poolMax = pool?.max_open_connections || 0
  // Without a configured maximum the bar shows the split of what is open.
  const poolScale = poolMax || pool?.open_connections || 0
  const waitRising = pool && poolSeen !== null && (pool.wait_count || 0) > poolSeen

  const tabs = [
    { id: 'slow', label: 'Slow queries', count: slowQueries?.length || 0 },
    { id: 'patterns', label: 'Patterns', count: patterns?.length || 0 },
    { id: 'n1', label: 'N+1 detections', count: n1Count, tone: n1Count > 0 ? 'crit' : undefined },
    { id: 'plans', label: 'Query plans', count: plans?.plans?.length ?? null },
  ]

  if (loading) return <div className="muted" style={{ padding: 40 }}>Loading…</div>

  const subtitle = [
    overview?.engine ? overview.engine[0].toUpperCase() + overview.engine.slice(1) : null,
    overview?.database || null,
    `captured ${num(overview?.total_queries)} queries in the ${rangeLabel.toLowerCase()}`,
  ].filter(Boolean).join(' · ')

  return (
    <>
      <PageHead
        title="Database"
        subtitle={subtitle}
        actions={
          <>
            <button type="button" className="btn" onClick={exportCSV}>
              <Download size={15} strokeWidth={1.5} aria-hidden="true" /> Export CSV
            </button>
            <button type="button" className="btn btn-primary" onClick={runExplain} disabled={explaining}>
              <Play size={15} strokeWidth={2} aria-hidden="true" />
              {explaining ? 'Running…' : 'Run EXPLAIN'}
            </button>
          </>
        }
      />

      {notice ? (
        <div className="card" role="status" style={{ padding: '10px 14px', fontSize: 13 }}>
          <span className="muted">{notice}</span>
        </div>
      ) : null}

      <section className="kpi-grid" aria-label="Database summary">
        <KpiTile
          label="Total queries" window={windowLabel}
          value={num(overview?.total_queries)}
          foot={<Delta current={overview?.total_queries || 0} previous={previous?.total_queries} />}
        />
        <KpiTile
          label="Query patterns" window="distinct"
          value={num(overview?.pattern_count)}
          foot={topPattern
            ? <span>Top pattern: <b>{topPattern.operation} {topPattern.table || ''}</b></span>
            : <span className="muted">nothing captured yet</span>}
        />
        <KpiTile
          label="Slow queries"
          window={overview?.slow_threshold_ms ? `> ${overview.slow_threshold_ms} ms` : windowLabel}
          value={num(overview?.slow_query_count)}
          badge={overview?.slow_query_count === 0 ? { tone: 'ok', label: 'Healthy' } : undefined}
          foot={slowP95 ? <span>p95 <b>{ms(slowP95)}</b></span> : <span className="muted">none in this window</span>}
        />
        <KpiTile
          label="N+1 detected" window={windowLabel}
          value={num(n1Count)}
          tone={n1Count > 0 ? 'crit' : undefined}
          badge={n1Count > 0 ? { tone: 'crit', label: 'Needs attention' } : { tone: 'ok', label: 'Clear' }}
          foot={n1Count > 0
            ? <span>across <b>{n1Routes}</b> {n1Routes === 1 ? 'route' : 'routes'}</span>
            : <span className="muted">no repeated query patterns</span>}
        />
      </section>

      {pool && pool.open_connections !== undefined ? (
        <section className="card" aria-label="Connection pool"
          style={{ display: 'flex', flexWrap: 'wrap', gap: 24, alignItems: 'center' }}>
          <div style={{ flex: '1 1 320px', minWidth: 0 }}>
            <div className="card-head" style={{ marginBottom: 10 }}>
              <span className="card-title">Connection pool</span>
              <span className="muted mono" style={{ fontSize: 12 }}>
                {poolMax ? `${pool.open_connections} of ${poolMax} open` : `${pool.open_connections} open · no max set`}
              </span>
            </div>
            <div className="bar" role="img"
              aria-label={poolMax
                ? `${pool.in_use} in use, ${pool.idle} idle, ${Math.max(0, poolMax - pool.open_connections)} free of ${poolMax}`
                : `${pool.in_use} in use, ${pool.idle} idle, no maximum configured`}>
              <span className="seg-in-use" style={{ width: `${poolScale ? (pool.in_use / poolScale) * 100 : 0}%` }} />
              <span className="seg-idle" style={{ width: `${poolScale ? (pool.idle / poolScale) * 100 : 0}%` }} />
            </div>
            <div className="legend" style={{ marginTop: 10 }}>
              <span><i className="swatch" style={{ background: 'var(--info)' }} /> In use</span>
              <span><i className="swatch" style={{ background: 'var(--accent)' }} /> Idle</span>
              <span><i className="swatch" style={{ background: 'var(--border)' }} /> Free</span>
            </div>
          </div>

          <dl className="readouts" style={{ flex: '1 1 380px' }}>
            <div>
              <dt className="eyebrow">Open</dt>
              <dd className="value">{pool.open_connections}</dd>
            </div>
            <div>
              <dt className="eyebrow">In use</dt>
              <dd className="value">{pool.in_use}</dd>
            </div>
            <div>
              <dt className="eyebrow">Idle</dt>
              <dd className="value">{pool.idle}</dd>
            </div>
            <div>
              <dt className="eyebrow">Wait count</dt>
              <dd className={`value${waitRising ? ' warn' : ''}`}>
                {pool.wait_count}
                {waitRising ? <span className="badge badge-warn">Rising</span> : null}
              </dd>
            </div>
          </dl>
        </section>
      ) : null}

      <section className="section" aria-label="Query detail">
        <Tabs tabs={tabs} active={tab} onChange={setTab} label="Database views" />

        {tab === 'n1' ? (
          n1.length ? (
            <div className="table-wrap">
              <div className="table-bar">
                <span>
                  <b>{n1.length}</b> grouped {n1.length === 1 ? 'finding' : 'findings'}
                  {' · '}<b>{n1Count}</b> {n1Count === 1 ? 'detection' : 'detections'} in the window
                </span>
              </div>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Route</th>
                    <th scope="col">Repeated query</th>
                    <th scope="col">Issued from</th>
                    <th scope="col" className="num">Calls / req</th>
                    <th scope="col" className="num">Added latency</th>
                    <th scope="col">Last seen</th>
                    <th scope="col">Severity</th>
                  </tr>
                </thead>
                <tbody>
                  {n1.map((d, i) => {
                    const added = (d.avg_queries_per_hit || 0) * (d.avg_query_duration || 0)
                    const level = n1Severity(added)
                    return (
                      <tr key={`${d.route}-${d.pattern}-${i}`}>
                        <td className="mono">{d.route || '—'}</td>
                        <td className="code" title={d.suggested_fix ? `${d.pattern}

${d.suggested_fix}` : d.pattern}>
                          {d.pattern}
                          {d.suggested_fix ? (
                            <div className="muted" style={{
                              fontFamily: 'var(--font-sans)', fontSize: 11, marginTop: 2,
                              overflow: 'hidden', textOverflow: 'ellipsis',
                            }}>
                              {d.suggested_fix}
                            </div>
                          ) : null}
                        </td>
                        <td className="mono" style={{ fontSize: 12 }}>
                          {d.caller_file
                            ? <span title={`${d.caller_file}${d.caller_line ? `:${d.caller_line}` : ''}`}>
                              {shortCaller(d.caller_file, d.caller_line)}
                            </span>
                            : <span className="muted" title="Set Database.TrackCallers to capture this">—</span>}
                        </td>
                        <td className="num">{Math.round(d.avg_queries_per_hit || 0)}</td>
                        <td className={`num ${level === 'critical' ? 'crit' : level === 'warning' ? 'warn' : 'muted'}`}>
                          +{ms(added)}
                        </td>
                        <td className="muted">{sinceText(d.last_seen)}</td>
                        <td><Severity level={level} /></td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState
              icon={<DatabaseIcon size={22} strokeWidth={1.5} aria-hidden="true" />}
              title="No N+1 patterns in this window"
              body="Pulse flags a query pattern that repeats inside one request. Nothing has repeated often enough in the selected range."
            />
          )
        ) : null}

        {tab === 'slow' ? (
          slowQueries.length ? (
            <div className="table-wrap">
              <div className="table-bar">
                <span>Slowest first · <b>{slowQueries.length}</b> captured</span>
              </div>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Op</th>
                    <th scope="col">Table</th>
                    <th scope="col" className="num">Duration</th>
                    <th scope="col" className="num">Rows</th>
                    <th scope="col">Caller</th>
                    <th scope="col">SQL</th>
                    <th scope="col">Trace</th>
                  </tr>
                </thead>
                <tbody>
                  {slowQueries.map((q, i) => (
                    <tr key={`${q.request_trace_id}-${i}`}>
                      <td className="mono">{q.operation}</td>
                      <td>{q.table || '—'}</td>
                      <td className={`num ${q.duration > 5e8 ? 'crit' : q.duration > 2e8 ? 'warn' : ''}`}>{ms(q.duration)}</td>
                      <td className="num">{q.rows_affected ?? '—'}</td>
                      <td className="muted mono" style={{ fontSize: 12 }}>
                        {q.caller_file ? `${q.caller_file}${q.caller_line ? `:${q.caller_line}` : ''}` : '—'}
                      </td>
                      <td className="code" title={q.sql}>{q.sql}</td>
                      <td className="mono">
                        {q.request_trace_id
                          ? <Link to={`/pulse/ui/traces/${q.request_trace_id}`}>{q.request_trace_id.slice(0, 8)}</Link>
                          : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState
              icon={<DatabaseIcon size={22} strokeWidth={1.5} aria-hidden="true" />}
              title="No slow queries"
              body={`Nothing has crossed the ${overview?.slow_threshold_ms ?? 200} ms slow threshold in this window.`}
            />
          )
        ) : null}

        {tab === 'patterns' ? (
          patterns.length ? (
            <div className="table-wrap">
              <div className="table-bar"><span><b>{patterns.length}</b> distinct patterns</span></div>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Op</th>
                    <th scope="col">Table</th>
                    <th scope="col">Query</th>
                    <th scope="col" className="num">Count</th>
                    <th scope="col" className="num">Avg</th>
                    <th scope="col" className="num">Max</th>
                    <th scope="col" className="num">Total</th>
                  </tr>
                </thead>
                <tbody>
                  {patterns.map((p, i) => (
                    <tr key={`${p.normalized_sql}-${i}`}>
                      <td className="mono">{p.operation}</td>
                      <td>{p.table || '—'}</td>
                      <td className="code" title={p.normalized_sql}>{p.normalized_sql}</td>
                      <td className="num">{num(p.count)}</td>
                      <td className="num">{ms(p.avg_duration)}</td>
                      <td className="num">{ms(p.max_duration)}</td>
                      <td className="num">{ms(p.total_duration)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState
              icon={<DatabaseIcon size={22} strokeWidth={1.5} aria-hidden="true" />}
              title="No query patterns yet"
              body="Pulse groups queries by their normalised SQL. Nothing has run in this window."
            />
          )
        ) : null}

        {tab === 'plans' ? (
          plans?.plans?.length ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {plans.plans.map((plan) => (
                <article className="card" key={plan.normalized_sql}>
                  <div className="card-head" style={{ alignItems: 'flex-start' }}>
                    <code className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }}>
                      {plan.normalized_sql}
                    </code>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 8, whiteSpace: 'nowrap' }}>
                      {plan.generic ? (
                        <span className="badge badge-neutral"
                          title="Planned without the values the query ran with — Pulse does not keep those. The planner can choose differently with real values.">
                          generic plan
                        </span>
                      ) : null}
                      <span className="mono warn" style={{ fontSize: 12 }}>{ms(plan.duration)}</span>
                    </span>
                  </div>
                  {(plan.hints || []).map((hint) => (
                    <div key={hint} className="crit" style={{ fontSize: 12, marginBottom: 4 }}>{hint}</div>
                  ))}
                  <pre className="mono" style={{
                    margin: '8px 0 0', padding: 10, background: 'var(--bg-2)',
                    borderRadius: 'var(--radius)', border: '1px solid var(--border)',
                    fontSize: 11, color: 'var(--text-2)', overflowX: 'auto', whiteSpace: 'pre-wrap',
                  }}>{plan.plan}</pre>
                  <div className="meta" style={{ marginTop: 8 }}>
                    captured {new Date(plan.captured_at).toLocaleString()}
                  </div>
                </article>
              ))}
            </div>
          ) : (
            <EmptyState
              icon={<FileSearch size={22} strokeWidth={1.5} aria-hidden="true" />}
              title="No query plans yet"
              body={plans?.capturing
                ? `Pulse captures an EXPLAIN plan the first time a query crosses the ${overview?.slow_threshold_ms ?? 200} ms slow threshold. Nothing has crossed it in this window.`
                : 'Query plans are off. Mount Pulse with pulse.WithExplainSlowQueries(0) and it will ask the database why a slow SELECT is slow — EXPLAIN only, one plan per pattern per hour.'}
              action={plans?.capturing
                ? <button type="button" className="btn btn-sm" onClick={runExplain} disabled={explaining}>
                  Explain the slowest query now
                </button>
                : null}
            />
          )
        ) : null}
      </section>
    </>
  )
}
