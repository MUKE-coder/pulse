import { useEffect, useMemo, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  LayoutGrid, Waypoints, Database, CircleSlash, ScrollText, Gauge,
  Activity, TriangleAlert, Target, LayoutPanelLeft, Play, Flame,
  Settings, LogOut, Search, Clock, ChevronDown, Sun, Moon,
} from 'lucide-react'
import { useAuth } from '../context/AuthContext'
import { useAPI } from '../hooks/useAPI'
import { useUI, RANGES, usePolling } from '../context/UIContext'
import { useTheme } from '../hooks/useTheme'
import { PulseMark } from './Brand'
import CommandPalette from './CommandPalette'

const VERSION = import.meta.env.VITE_PULSE_VERSION || ''

// Nav is grouped the way the redesign groups it: what you watch, what you
// promise, and what you reach for.
export const NAV_GROUPS = [
  {
    eyebrow: 'Observe',
    items: [
      { to: '/pulse/ui', label: 'Overview', icon: LayoutGrid, end: true },
      { to: '/pulse/ui/routes', label: 'Routes', icon: Waypoints },
      { to: '/pulse/ui/database', label: 'Database', icon: Database },
      { to: '/pulse/ui/errors', label: 'Errors', icon: CircleSlash, count: 'errors' },
      { to: '/pulse/ui/logs', label: 'Logs', icon: ScrollText },
      { to: '/pulse/ui/runtime', label: 'Runtime', icon: Gauge },
    ],
  },
  {
    eyebrow: 'Reliability',
    items: [
      { to: '/pulse/ui/health', label: 'Health', icon: Activity, status: true },
      { to: '/pulse/ui/alerts', label: 'Alerts', icon: TriangleAlert, count: 'alerts' },
      { to: '/pulse/ui/slos', label: 'SLOs', icon: Target },
      { to: '/pulse/ui/use', label: 'USE', icon: LayoutPanelLeft },
    ],
  },
  {
    eyebrow: 'Tools',
    items: [
      { to: '/pulse/ui/test-runs', label: 'Test Runs', icon: Play },
      { to: '/pulse/ui/flame', label: 'Flame Graph', icon: Flame },
    ],
  },
]

const ICON = { size: 16, strokeWidth: 1.5 }

// breadcrumbFor names where you are: the nav group, then the page.
function breadcrumbFor(pathname) {
  const path = pathname.replace(/\/$/, '') || '/pulse/ui'
  if (path.startsWith('/pulse/ui/traces/')) return ['Observe', 'Trace']
  if (path === '/pulse/ui/settings') return ['Settings', 'Configuration']
  for (const group of NAV_GROUPS) {
    const hit = group.items.find((i) => (i.end ? path === i.to : path.startsWith(i.to)))
    if (hit) return [group.eyebrow, hit.label]
  }
  return ['Observe', 'Overview']
}

export default function Layout() {
  const { logout, user } = useAuth()
  const { get } = useAPI()
  const { range, setRange, live, toggleLive } = useUI()
  const { theme, toggle } = useTheme()
  const location = useLocation()
  const navigate = useNavigate()
  const [summary, setSummary] = useState(null)
  const [env, setEnv] = useState(null)
  const [paletteOpen, setPaletteOpen] = useState(false)

  // One poll feeds every badge in the shell: errors, firing alerts and the
  // health dot all come off the overview.
  usePolling(() => {
    get(`/overview?range=${range}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => data && setSummary(data))
      .catch(() => {})
  }, 20000, [range])

  useEffect(() => {
    get('/settings')
      .then((res) => (res.ok ? res.json() : null))
      .then((cfg) => cfg && setEnv({
        name: cfg.DevMode ? 'development' : 'production',
        dev: !!cfg.DevMode,
        app: cfg.AppName,
        instance: cfg.InstanceID,
      }))
      .catch(() => {})
  }, [get])

  useEffect(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const counts = useMemo(() => ({
    errors: summary?.total_errors || 0,
    alerts: summary?.active_alerts || 0,
    health: summary?.health_status || '',
  }), [summary])

  const [group, page] = breadcrumbFor(location.pathname)
  // The Overview and the trace waterfall paint their own surface edge to edge.
  const bleed = location.pathname.replace(/\/$/, '') === '/pulse/ui'
    || location.pathname.startsWith('/pulse/ui/traces/')

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <PulseMark size={28} />
          <span className="wordmark">Pulse</span>
          {VERSION ? <span className="version-chip">v{VERSION}</span> : null}
        </div>

        <nav aria-label="Primary">
          {NAV_GROUPS.map((navGroup) => (
            <div className="nav-group" key={navGroup.eyebrow}>
              <div className="nav-eyebrow">{navGroup.eyebrow}</div>
              {navGroup.items.map(({ to, label, icon: Icon, end, count, status }) => (
                <NavLink key={to} to={to} end={end}
                  className={({ isActive }) => `nav-item${isActive ? ' is-active' : ''}`}>
                  <Icon {...ICON} aria-hidden="true" />
                  {label}
                  {count && counts[count] > 0 ? (
                    <span className={`count ${count === 'errors' ? 'crit' : 'warn'}`}>
                      {counts[count]}
                    </span>
                  ) : null}
                  {status && counts.health ? (
                    <span className={`count ${counts.health === 'healthy' ? 'ok'
                      : counts.health === 'degraded' ? 'warn' : 'crit'}`}
                      title={`Health: ${counts.health}`}>
                      <i className="dot dot-lg" style={{ display: 'inline-block' }} />
                    </span>
                  ) : null}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="sidebar-foot">
          <NavLink to="/pulse/ui/settings"
            className={({ isActive }) => `nav-item${isActive ? ' is-active' : ''}`}>
            <Settings {...ICON} aria-hidden="true" />
            Settings
          </NavLink>
          <div className="sidebar-user">
            <span className="avatar">{(user || 'admin').slice(0, 2).toUpperCase()}</span>
            <span className="who">
              <span className="name">{user || 'admin'}</span>
              <span className="role" style={{ display: 'block' }}>Operator</span>
            </span>
            <button type="button" className="btn btn-ghost btn-icon signout" onClick={logout}
              aria-label="Sign out" title="Sign out">
              <LogOut {...ICON} aria-hidden="true" />
            </button>
          </div>
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <span className="breadcrumb">{group} / <b>{page}</b></span>

          <label className="field topbar-search">
            <span className="field-icon"><Search size={15} strokeWidth={1.5} aria-hidden="true" /></span>
            <input className="input input-search" type="search" readOnly
              onFocus={() => setPaletteOpen(true)}
              onClick={() => setPaletteOpen(true)}
              placeholder="Search routes, queries, errors…"
              aria-label="Search" />
            <span className="kbd">⌘K</span>
          </label>

          <div className="topbar-controls">
            {env ? (
              <span className="btn btn-pill" style={{ cursor: 'default' }}
                title={`${env.app} · instance ${env.instance}`}>
                <i className="dot" style={{ color: env.dev ? 'var(--warn)' : 'var(--ok)' }} />
                {env.name}
              </span>
            ) : null}

            <label className="btn btn-pill" style={{ gap: 6, paddingRight: 8 }}>
              <Clock size={14} strokeWidth={1.5} aria-hidden="true" />
              <select value={range} onChange={(e) => setRange(e.target.value)}
                aria-label="Time range"
                style={{
                  appearance: 'none', background: 'transparent', border: 0, padding: 0,
                  font: 'inherit', color: 'inherit', cursor: 'pointer', outline: 'none',
                  height: 'auto',
                }}>
                {RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
              </select>
              <ChevronDown size={14} strokeWidth={1.5} aria-hidden="true" />
            </label>

            <button type="button" className={`btn btn-pill btn-live${live ? '' : ' is-paused'}`}
              onClick={toggleLive} aria-pressed={live}
              title={live ? 'Pause live updates' : 'Resume live updates'}>
              <i className={`dot${live ? ' live-dot' : ''}`} />
              {live ? 'Live' : 'Paused'}
            </button>

            <button type="button" className="btn btn-pill btn-icon" onClick={toggle}
              aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}>
              {theme === 'dark'
                ? <Sun size={15} strokeWidth={1.5} aria-hidden="true" />
                : <Moon size={15} strokeWidth={1.5} aria-hidden="true" />}
            </button>
          </div>
        </header>

        <main className={bleed ? 'page bleed' : 'page'}>
          <Outlet />
        </main>
      </div>

      {paletteOpen ? (
        <CommandPalette onClose={() => setPaletteOpen(false)} onNavigate={(to) => {
          setPaletteOpen(false)
          navigate(to)
        }} />
      ) : null}
    </div>
  )
}
