import { useState, useEffect, useCallback } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useUI, usePolling } from '../context/UIContext'
import { useTheme } from '../hooks/useTheme'
import { PageHead } from '../components/ui'
import { useWebSocket } from '../hooks/useWebSocket'
import { useInstances } from '../hooks/useInstances'
import StatCard from '../components/StatCard'
import InstancePicker from '../components/InstancePicker'
import { LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend } from 'recharts'
import {
  DataBanner, fmtClock, overlayElements, timeAxisProps, timeDomain, useTimelineOverlays,
} from '../components/TimelineOverlays'

function fmtBytes(b) {
  if (!b) return '0 B'
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1073741824) return `${(b / 1048576).toFixed(1)} MB`
  return `${(b / 1073741824).toFixed(2)} GB`
}

export default function RuntimePage() {
  const { get } = useAPI()
  const { range, rangeLabel } = useUI()
  const { palette } = useTheme()
  const [current, setCurrent] = useState(null)
  const [history, setHistory] = useState([])
  const [info, setInfo] = useState(null)
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['runtime'])
  const overlays = useTimelineOverlays(range)
  const { instances, self } = useInstances()
  const [instance, setInstance] = useState('')
  const shown = instance || self

  const fetchData = useCallback(async () => {
    const which = shown ? `instance=${encodeURIComponent(shown)}` : ''
    try {
      const [cRes, hRes, iRes] = await Promise.all([
        get(`/runtime/current?${which}`),
        get(`/runtime/history?range=${range}&${which}`),
        get('/runtime/info'),
      ])
      if (cRes.ok) setCurrent(await cRes.json())
      if (hRes.ok) setHistory(await hRes.json())
      if (iRes.ok) setInfo(await iRes.json())
    } catch {}
    setLoading(false)
  }, [get, range, shown])

  usePolling(fetchData, 15000, [fetchData])

  useEffect(() => {
    if (lastMessage?.type === 'runtime' && lastMessage?.data) {
      setCurrent(lastMessage.data)
    }
  }, [lastMessage])

  // Charts take concrete colours, so they read the resolved tokens and
  // re-render whenever the theme flips.
  const tooltipStyle = {
    background: palette.surface, border: `1px solid ${palette.border}`,
    borderRadius: 6, fontSize: 12, color: palette.text,
  }
  const axisTick = { fill: palette.text3, fontSize: 10 }

  if (loading) return <div className="muted" style={{ padding: 40 }}>Loading…</div>

  const r = current || {}

  const chartData = (history || []).map((h) => ({
    ts: new Date(h.timestamp).getTime(),
    heap: (h.heap_alloc || 0) / 1048576,
    heapInUse: (h.heap_in_use || 0) / 1048576,
    goroutines: h.num_goroutine || 0,
    gcPause: (h.gc_pause_ns || 0) / 1e6,
  }))
  const domain = timeDomain(chartData)

  return (
    <>
      <PageHead
        title="Runtime"
        subtitle={`${shown || 'this instance'} · ${rangeLabel.toLowerCase()}`}
        actions={<InstancePicker instances={instances} value={shown} onChange={setInstance} />}
      />

      <DataBanner lifecycle={overlays.lifecycle} range={range} />

      {/* KPI Cards */}
      <div className="kpi-grid">
        <StatCard label="Heap allocated" value={fmtBytes(r.heap_alloc)} />
        <StatCard label="Goroutines" value={r.num_goroutine || '0'} />
        <StatCard label="GC cycles" value={r.num_gc || '0'} />
        <StatCard label="System memory" value={fmtBytes(r.sys)} />
      </div>

      {/* Memory Chart */}
      <div className="card">
        <h3 className="eyebrow" style={{ marginBottom: 12 }}>Memory (MB)</h3>
        <ResponsiveContainer width="100%" height={220}>
          <LineChart data={chartData}>
            <XAxis {...timeAxisProps} tick={axisTick} />
            <YAxis tick={axisTick} axisLine={false} tickLine={false} width={50} />
            <Tooltip labelFormatter={fmtClock} contentStyle={tooltipStyle} />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {overlayElements(overlays, domain, palette)}
            <Line type="monotone" dataKey="heap" name="Heap Alloc" stroke={palette.accent} strokeWidth={2} dot={false} />
            <Line type="monotone" dataKey="heapInUse" name="Heap In Use" stroke={palette.info} strokeWidth={2} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* Goroutines Chart */}
      <div className="card">
        <h3 className="eyebrow" style={{ marginBottom: 12 }}>Goroutines</h3>
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={chartData}>
            <XAxis {...timeAxisProps} tick={axisTick} />
            <YAxis tick={axisTick} axisLine={false} tickLine={false} width={50} />
            <Tooltip labelFormatter={fmtClock} contentStyle={tooltipStyle} />
            {overlayElements(overlays, domain, palette)}
            <Line type="monotone" dataKey="goroutines" stroke={palette.ok} strokeWidth={2} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* System Info — of the process serving the dashboard, so only shown for it */}
      {info && (!shown || shown === self) && (
        <div className="card">
          <h3 className="eyebrow" style={{ marginBottom: 12 }}>System info</h3>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, fontSize: 13 }}>
            {info.system && Object.entries(info.system).map(([k, v]) => (
              <div key={k}>
                <span className="muted">{k.replace(/_/g, ' ')}: </span>
                <span className="mono">{String(v)}</span>
              </div>
            ))}
            {info.uptime && (
              <div>
                <span className="muted">uptime: </span>
                <span className="mono ok">{info.uptime}</span>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  )
}
