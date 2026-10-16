import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowRight, Eye, EyeOff, Lock, Sun, Moon, TriangleAlert } from 'lucide-react'
import { useAuth } from '../context/AuthContext'
import { useTheme } from '../hooks/useTheme'
import { PulseMark, PulseLine } from '../components/Brand'

const VERSION = import.meta.env.VITE_PULSE_VERSION || ''

// The brand panel's three readouts are real or they are blank. They come
// from the public status endpoint, which is opt-in (Dashboard.PublicStatus):
// with it off this page shows em-dashes rather than invented numbers.
function usePublicStatus() {
  const [status, setStatus] = useState({ loading: true, available: false })

  useEffect(() => {
    let cancelled = false
    fetch('/pulse/api/public/status')
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (cancelled) return
        setStatus(data
          ? { loading: false, available: true, ...data }
          : { loading: false, available: false })
      })
      .catch(() => !cancelled && setStatus({ loading: false, available: false }))
    return () => { cancelled = true }
  }, [])

  return status
}

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(false)
  const [reveal, setReveal] = useState(false)
  const [error, setError] = useState(null)
  const [locked, setLocked] = useState(false)
  const [loading, setLoading] = useState(false)
  const { login } = useAuth()
  const { theme, toggle } = useTheme()
  const navigate = useNavigate()
  const status = usePublicStatus()

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      await login(username, password, remember)
      navigate('/pulse/ui')
    } catch (err) {
      // Keep the username, clear the password, put the cursor back in it.
      setPassword('')
      setLocked(err.status === 429)
      setError(err.status === 429
        ? 'Too many attempts. Try again in a few minutes.'
        : 'Username or password is incorrect.')
      document.getElementById('password')?.focus()
    } finally {
      setLoading(false)
    }
  }

  const readout = (value) => (status.loading ? '…' : value ?? '—')

  return (
    <div className="login">
      <section className="login-brand" aria-label="Pulse">
        <div className="brand-row">
          <PulseMark size={32} strokeWidth={2.2} />
          <span className="wordmark">Pulse</span>
        </div>

        <div>
          <PulseLine />
          <h1>Every request, every query, in one place.</h1>
          <p>Routes, database, errors, SLOs and runtime health for your service — captured live and kept readable.</p>
        </div>

        <div className="login-readouts">
          <div className="card">
            <div className="eyebrow">Uptime</div>
            <div className="value">{readout(status.uptime)}</div>
          </div>
          <div className="card">
            <div className="eyebrow">p95 latency</div>
            <div className="value">
              {status.p95_latency_ms !== undefined
                ? <>{status.p95_latency_ms < 10
                  ? status.p95_latency_ms.toFixed(1)
                  : Math.round(status.p95_latency_ms)}<span className="unit">ms</span></>
                : readout(undefined)}
            </div>
          </div>
          <div className="card">
            <div className="eyebrow">Status</div>
            <div className="value status" style={{
              color: !status.available ? 'var(--sidebar-text-3)'
                : status.status === 'healthy' ? 'var(--ok)'
                  : status.status === 'degraded' ? 'var(--warn)' : 'var(--crit)',
            }}>
              {status.available ? <i className="dot" /> : null}
              {status.available
                ? (status.status === 'healthy' ? 'All systems go'
                  : status.status === 'degraded' ? 'Degraded' : 'Unhealthy')
                : readout(undefined)}
            </div>
          </div>
        </div>
      </section>

      <section className="login-form-panel" aria-label="Sign in">
        <div className="panel-top">
          <button type="button" className="btn btn-pill btn-icon" onClick={toggle}
            aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}>
            {theme === 'dark'
              ? <Sun size={15} strokeWidth={1.5} aria-hidden="true" />
              : <Moon size={15} strokeWidth={1.5} aria-hidden="true" />}
          </button>
        </div>

        <div className="centre">
          <div className="login-form">
            <h2>Sign in</h2>
            <div className="sub">Use your operator account to open the dashboard.</div>

            <form onSubmit={handleSubmit} noValidate>
              {error ? (
                <div className="error" role="alert" id="login-error">
                  <TriangleAlert size={15} strokeWidth={1.5} aria-hidden="true" />
                  {error}
                </div>
              ) : null}

              <div className={error ? 'has-error' : undefined}>
                <label className="label" htmlFor="username">Username</label>
                <input id="username" className="input input-lg" type="text" value={username}
                  onChange={(e) => setUsername(e.target.value)} required autoFocus
                  autoComplete="username" placeholder="admin"
                  aria-describedby={error ? 'login-error' : undefined} />
              </div>

              <div className={error ? 'has-error' : undefined}>
                <div className="label-row">
                  <label className="label" htmlFor="password" style={{ marginBottom: 0 }}>Password</label>
                  <a href="https://github.com/MUKE-coder/pulse#dashboard-authentication"
                    target="_blank" rel="noreferrer">Forgot password?</a>
                </div>
                <label className="field" style={{ marginTop: 6 }}>
                  <input id="password" className="input input-lg" value={password}
                    type={reveal ? 'text' : 'password'} required autoComplete="current-password"
                    onChange={(e) => setPassword(e.target.value)} placeholder="••••••••"
                    style={{ paddingRight: 44 }}
                    aria-describedby={error ? 'login-error' : undefined} />
                  <button type="button" className="field-action" onClick={() => setReveal((v) => !v)}
                    aria-label={reveal ? 'Hide password' : 'Show password'}>
                    {reveal
                      ? <EyeOff size={16} strokeWidth={1.5} aria-hidden="true" />
                      : <Eye size={16} strokeWidth={1.5} aria-hidden="true" />}
                  </button>
                </label>
              </div>

              <label className="check">
                <input type="checkbox" checked={remember}
                  onChange={(e) => setRemember(e.target.checked)} />
                Keep me signed in on this device for 30 days
              </label>

              <button type="submit" className="btn btn-primary btn-lg" disabled={loading}
                style={{ width: '100%' }}>
                {loading ? 'Signing in…' : <>Sign in <ArrowRight size={16} strokeWidth={2} aria-hidden="true" /></>}
              </button>

              <div className={`notice${locked ? ' is-locked' : ''}`}>
                {locked
                  ? <TriangleAlert size={15} strokeWidth={1.5} aria-hidden="true" />
                  : <Lock size={15} strokeWidth={1.5} aria-hidden="true" />}
                <span>
                  {locked
                    ? 'Too many attempts from this address. The limiter clears within a minute.'
                    : 'This is an internal tool. Keep the session to this device unless you tick the box above.'}
                </span>
              </div>
            </form>

            <div className="login-footer meta">
              Pulse{VERSION ? ` v${VERSION}` : ''}
              {typeof window !== 'undefined' ? ` · ${window.location.host}` : ''}
              {status.available && status.uptime ? ` · up ${status.uptime}` : ''}
            </div>
          </div>
        </div>
      </section>
    </div>
  )
}
