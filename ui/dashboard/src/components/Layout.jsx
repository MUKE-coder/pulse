import { NavLink, Outlet } from 'react-router-dom'
import { useAuth } from '../context/AuthContext'

const nav = [
  { to: '/pulse/ui', label: 'Overview', icon: '◈', end: true },
  { to: '/pulse/ui/routes', label: 'Routes', icon: '◇' },
  { to: '/pulse/ui/database', label: 'Database', icon: '⊞' },
  { to: '/pulse/ui/errors', label: 'Errors', icon: '⊘' },
  { to: '/pulse/ui/runtime', label: 'Runtime', icon: '◎' },
  { to: '/pulse/ui/health', label: 'Health', icon: '♡' },
  { to: '/pulse/ui/alerts', label: 'Alerts', icon: '⚠' },
  { to: '/pulse/ui/settings', label: 'Settings', icon: '⚙' },
]

export default function Layout() {
  const { logout, user } = useAuth()

  return (
    <div style={{ display: 'flex', height: '100vh', background: '#0a0a12' }}>
      {/* Sidebar */}
      <aside style={{
        width: 224, minWidth: 224, background: '#0d0d15', borderRight: '1px solid #1e1e2e',
        display: 'flex', flexDirection: 'column', padding: '0',
      }}>
        {/* Logo */}
        <div style={{
          padding: '20px 20px 16px', borderBottom: '1px solid #1e1e2e',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <div style={{
            width: 32, height: 32, borderRadius: 8,
            background: 'linear-gradient(135deg, #6366f1, #8b5cf6)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 16,
          }}>💜</div>
          <span style={{ fontWeight: 700, fontSize: 18, color: '#e2e8f0' }}>PULSE</span>
        </div>

        {/* Nav Items */}
        <nav style={{ flex: 1, padding: '12px 8px', display: 'flex', flexDirection: 'column', gap: 2 }}>
          {nav.map(({ to, label, icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              style={({ isActive }) => ({
                display: 'flex', alignItems: 'center', gap: 10,
                padding: '10px 12px', borderRadius: 6, textDecoration: 'none',
                fontSize: 14, fontWeight: 500, transition: 'all 0.15s',
                background: isActive ? 'rgba(99,102,241,0.12)' : 'transparent',
                color: isActive ? '#818cf8' : '#8892a4',
              })}
            >
              <span style={{ fontSize: 16, width: 20, textAlign: 'center' }}>{icon}</span>
              {label}
            </NavLink>
          ))}
        </nav>

        {/* User + Logout */}
        <div style={{
          padding: '12px 16px', borderTop: '1px solid #1e1e2e',
          display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        }}>
          <span style={{ fontSize: 13, color: '#64748b' }}>{user || 'admin'}</span>
          <button
            onClick={logout}
            style={{
              background: 'none', border: 'none', color: '#64748b',
              cursor: 'pointer', fontSize: 13, padding: '4px 8px',
              borderRadius: 4,
            }}
            onMouseOver={(e) => e.target.style.color = '#ef4444'}
            onMouseOut={(e) => e.target.style.color = '#64748b'}
          >
            Logout
          </button>
        </div>
      </aside>

      {/* Main Content */}
      <main style={{ flex: 1, overflow: 'auto', padding: 24 }}>
        <Outlet />
      </main>
    </div>
  )
}
