import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useAPI } from '../hooks/useAPI'
import { Empty, Panel, Pill, Section } from '../components/ops'

// One request, end to end: the request's own span with every query it ran
// and every call it made, laid out against the time it took, and the lines
// it logged beside them.

const ms = (ns) => {
  const v = (ns || 0) / 1e6
  if (v >= 1000) return `${(v / 1000).toFixed(2)}s`
  if (v < 1) return `${((ns || 0) / 1e3).toFixed(0)}µs` // sub-millisecond queries are common
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`
}
const clock = (t) => {
  const d = new Date(t)
  return `${d.toLocaleTimeString([], { hour12: false })}.${String(d.getMilliseconds()).padStart(3, '0')}`
}
const kindColor = { request: 'var(--ops-accent)', query: 'var(--ops-warn)', dependency: 'var(--ops-ok)' }

export default function TracePage() {
  const { id } = useParams()
  const { get } = useAPI()
  const [data, setData] = useState(null)
  const [missing, setMissing] = useState(false)

  useEffect(() => {
    let alive = true
    ;(async () => {
      try {
        const res = await get(`/traces/${id}`)
        if (!alive) return
        if (res.status === 404) setMissing(true)
        else if (res.ok) setData(await res.json())
      } catch {}
    })()
    return () => { alive = false }
  }, [get, id])

  if (missing) {
    return (
      <div className="ops">
        <header className="ops-top"><h1>Trace</h1><span className="ops-env">{id}</span></header>
        <Empty>
          Nothing was recorded for this trace. Requests that sampling skipped still count towards
          totals and SLOs, but their records aren't kept.
        </Empty>
      </div>
    )
  }
  if (!data) return <div className="ops"><p className="ops-status">Loading…</p></div>

  const spans = data.spans || []
  const start = Math.min(...spans.map((s) => new Date(s.start).getTime()))
  const end = Math.max(...spans.map((s) => new Date(s.start).getTime() + s.duration / 1e6))
  const total = Math.max(1, end - start)
  const root = spans.find((s) => s.kind === 'request')
  const failed = spans.some((s) => s.error || (s.status_code >= 500))
  const offset = (t) => ((new Date(t).getTime() - start) / total) * 100
  const width = (d) => Math.max(0.6, (d / 1e6 / total) * 100)

  return (
    <div className="ops">
      <header className="ops-top">
        <h1>{root ? root.name : 'Trace'}</h1>
        <span className="ops-env">{id}</span>
        <Pill tone={failed ? 'bad' : 'ok'}>{failed ? 'failed' : 'ok'}</Pill>
        <span className="ops-spacer" />
        <Link className="ops-btn" to={`/pulse/ui/logs?trace_id=${id}`}>Logs for this trace</Link>
      </header>

      <p className="ops-status">
        <b>{ms(total * 1e6)}</b> end to end · <b>{spans.length}</b> spans ·
        started <b>{clock(start)}</b>
        {root?.instance_id && <> · served by <b>{root.instance_id}</b></>}
        {root?.status_code ? <> · status <b>{root.status_code}</b></> : null}
      </p>

      <Section title="Waterfall" meta={`${spans.filter((s) => s.kind === 'query').length} queries · ${spans.filter((s) => s.kind === 'dependency').length} outbound calls`} />
      <div className="ops-grid">
        <Panel width="w12">
          {spans.length === 0 ? <Empty>No spans</Empty> : (
            <div className="ops-wf">
              {spans.map((s, i) => (
                <div className="ops-wf-row" key={i}>
                  <div className="ops-wf-name">
                    <span className="ops-method">{s.kind}</span>
                    {s.name}
                    {s.detail && <span className="ops-wf-detail" title={s.detail}>{s.detail}</span>}
                  </div>
                  <div className="ops-wf-track">
                    <span className="ops-wf-bar"
                      style={{
                        left: `${offset(s.start)}%`, width: `${width(s.duration)}%`,
                        background: s.error || s.status_code >= 500 ? 'var(--ops-bad)' : kindColor[s.kind],
                      }} />
                  </div>
                  <div className="ops-wf-dur">{ms(s.duration)}</div>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>

      <Section title="Logs" meta={`${(data.logs || []).length} lines`} />
      <div className="ops-grid">
        <Panel width="w12">
          {(data.logs || []).length === 0 ? <Empty>No lines were logged with this trace</Empty> : (
            <div className="ops-logs" style={{ height: 'auto', maxHeight: 360 }}>
              {(data.logs || []).map((l, i) => (
                <div key={i}>
                  <span className="ts">+{ms((new Date(l.time).getTime() - start) * 1e6)}</span>
                  <span className={`lvl ${String(l.level).toLowerCase().startsWith('error') ? 'error'
                    : String(l.level).toLowerCase().startsWith('warn') ? 'warn' : 'info'}`}>{l.level}</span>
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
