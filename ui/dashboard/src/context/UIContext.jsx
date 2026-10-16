import { createContext, useContext, useState, useCallback, useMemo, useEffect } from 'react'

const UIContext = createContext(null)

// One time range for the whole dashboard, as the redesign's top bar implies:
// the control lives next to the breadcrumb and every page reads from here
// instead of carrying its own picker.
export const RANGES = [
  { value: '5m', label: 'Last 5 min' },
  { value: '15m', label: 'Last 15 min' },
  { value: '1h', label: 'Last hour' },
  { value: '6h', label: 'Last 6 hours' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
]

const RANGE_KEY = 'pulse-range'

const storedRange = () => {
  try {
    const v = localStorage.getItem(RANGE_KEY)
    return RANGES.some((r) => r.value === v) ? v : '1h'
  } catch {
    return '1h'
  }
}

// rangeSeconds is how long a range is, used for window labels and for
// asking an endpoint about the window before this one.
export function rangeSeconds(range) {
  const match = /^(\d+)([smhd])$/.exec(range)
  if (!match) return 3600
  const n = Number(match[1])
  return n * { s: 1, m: 60, h: 3600, d: 86400 }[match[2]]
}

export function rangeLabel(range) {
  return RANGES.find((r) => r.value === range)?.label || `Last ${range}`
}

// windowLabel is the short form a KPI tile carries ("15 min", "24 h").
export function windowLabel(range) {
  const match = /^(\d+)([smhd])$/.exec(range)
  if (!match) return range
  return `${match[1]} ${{ s: 'sec', m: 'min', h: 'h', d: 'd' }[match[2]]}`
}

export function UIProvider({ children }) {
  const [range, setRange] = useState(storedRange)
  const [live, setLive] = useState(true)

  useEffect(() => {
    try {
      localStorage.setItem(RANGE_KEY, range)
    } catch {}
  }, [range])

  const value = useMemo(() => ({
    range,
    setRange,
    rangeLabel: rangeLabel(range),
    windowLabel: windowLabel(range),
    seconds: rangeSeconds(range),
    live,
    setLive,
    toggleLive: () => setLive((v) => !v),
  }), [range, live])

  return <UIContext.Provider value={value}>{children}</UIContext.Provider>
}

export function useUI() {
  const ctx = useContext(UIContext)
  if (!ctx) throw new Error('useUI must be inside UIProvider')
  return ctx
}

// usePolling runs fn now and then every intervalMs while live is on.
export function usePolling(fn, intervalMs, deps = []) {
  const { live } = useUI()
  const stable = useCallback(fn, deps)
  useEffect(() => {
    let cancelled = false
    const run = () => {
      if (!cancelled) stable()
    }
    run()
    if (!live || !intervalMs) return () => { cancelled = true }
    const id = setInterval(run, intervalMs)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [stable, live, intervalMs])
}
