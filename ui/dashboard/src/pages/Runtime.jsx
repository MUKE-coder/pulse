import { useState, useEffect } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useWebSocket } from '../hooks/useWebSocket'
import StatCard from '../components/StatCard'
import { LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend } from 'recharts'

function fmtBytes(b) {
  if (!b) return '0 B'
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1073741824) return `${(b / 1048576).toFixed(1)} MB`
  return `${(b / 1073741824).toFixed(2)} GB`
}

export default function RuntimePage() {
  const { get } = useAPI()
  const [current, setCurrent] = useState(null)
  const [history, setHistory] = useState([])
  const [info, setInfo] = useState(null)
  const [range, setRange] = useState('1h')
  const [loading, setLoading] = useState(true)
  const { lastMessage } = useWebSocket(['runtime'])

  const fetchData = async () => {
    try {
      const [cRes, hRes, iRes] = await Promise.all([
        get('/runtime/current'),
        get(`/runtime/history?range=${range}`),
        get('/runtime/info'),
      ])
      if (cRes.ok) setCurrent(await cRes.json())
      if (hRes.ok) setHistory(await hRes.json())
      if (iRes.ok) setInfo(await iRes.json())
    } catch {}
    setLoading(false)
  }

  useEffect(() => { fetchData() }, [range])

  useEffect(() => {
    if (lastMessage?.type === 'runtime' && lastMessage?.data) {
      setCurrent(lastMessage.data)
    }
  }, [lastMessage])

  if (loading) return <div style={{ color: '#64748b', padding: 40 }}>Loading...</div>

  const r = current || {}

  const chartData = (history || []).map((h) => ({
    time: new Date(h.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    heap: (h.heap_alloc || 0) / 1048576,
    heapInUse: (h.heap_in_use || 0) / 1048576,
    goroutines: h.num_goroutine || 0,
    gcPause: (h.gc_pause_ns || 0) / 1e6,
  }))

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Runtime</h1>
        <select value={range} onChange={(e) => setRange(e.target.value)}>
          <option value="5m">5m</option>
          <option value="15m">15m</option>
          <option value="1h">1h</option>
          <option value="6h">6h</option>
          <option value="24h">24h</option>
        </select>
      </div>

      {/* KPI Cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 14, marginBottom: 20 }}>
        <StatCard label="Heap Alloc" value={fmtBytes(r.heap_alloc)} color="#818cf8" />
        <StatCard label="Goroutines" value={r.num_goroutine || '0'} color="#22c55e" />
        <StatCard label="GC Cycles" value={r.num_gc || '0'} color="#f59e0b" />
        <StatCard label="Sys Memory" value={fmtBytes(r.sys)} color="#94a3b8" />
      </div>

      {/* Memory Chart */}
      <div style={{ background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: 16, marginBottom: 14 }}>
        <h3 style={{ fontSize: 13, color: '#64748b', marginBottom: 12, fontWeight: 600 }}>MEMORY (MB)</h3>
        <ResponsiveContainer width="100%" height={220}>
          <LineChart data={chartData}>
            <XAxis dataKey="time" tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} />
            <YAxis tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} width={50} />
            <Tooltip contentStyle={{ background: '#16161e', border: '1px solid #2a2a3e', borderRadius: 6, fontSize: 12 }} />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            <Line type="monotone" dataKey="heap" name="Heap Alloc" stroke="#6366f1" strokeWidth={2} dot={false} />
            <Line type="monotone" dataKey="heapInUse" name="Heap In Use" stroke="#8b5cf6" strokeWidth={2} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* Goroutines Chart */}
      <div style={{ background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: 16, marginBottom: 14 }}>
        <h3 style={{ fontSize: 13, color: '#64748b', marginBottom: 12, fontWeight: 600 }}>GOROUTINES</h3>
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={chartData}>
            <XAxis dataKey="time" tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} />
            <YAxis tick={{ fill: '#64748b', fontSize: 10 }} axisLine={false} tickLine={false} width={50} />
            <Tooltip contentStyle={{ background: '#16161e', border: '1px solid #2a2a3e', borderRadius: 6, fontSize: 12 }} />
            <Line type="monotone" dataKey="goroutines" stroke="#22c55e" strokeWidth={2} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* System Info */}
      {info && (
        <div style={{ background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8, padding: 16 }}>
          <h3 style={{ fontSize: 13, color: '#64748b', marginBottom: 12, fontWeight: 600 }}>SYSTEM INFO</h3>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, fontSize: 13 }}>
            {info.system && Object.entries(info.system).map(([k, v]) => (
              <div key={k}>
                <span style={{ color: '#64748b' }}>{k.replace(/_/g, ' ')}: </span>
                <span style={{ color: '#e2e8f0' }}>{String(v)}</span>
              </div>
            ))}
            {info.uptime && (
              <div>
                <span style={{ color: '#64748b' }}>uptime: </span>
                <span style={{ color: '#22c55e' }}>{info.uptime}</span>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
