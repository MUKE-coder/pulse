import { createContext, useContext, useState, useCallback, useEffect } from 'react'

const AuthContext = createContext(null)

const KEY = 'pulse-session'

// The session lives in sessionStorage by default — it dies with the tab —
// and moves to localStorage only when "keep me signed in" is ticked, which
// is what that checkbox actually means. Either way a reload no longer
// bounces you back to the login screen.
function readSession() {
  for (const store of [sessionStorage, localStorage]) {
    try {
      const raw = store.getItem(KEY)
      if (!raw) continue
      const session = JSON.parse(raw)
      if (session?.token) return session
    } catch {}
  }
  return null
}

function writeSession(session, remember) {
  const target = remember ? localStorage : sessionStorage
  const other = remember ? sessionStorage : localStorage
  try {
    other.removeItem(KEY)
    target.setItem(KEY, JSON.stringify(session))
  } catch {}
}

function clearSession() {
  for (const store of [sessionStorage, localStorage]) {
    try {
      store.removeItem(KEY)
    } catch {}
  }
}

export function AuthProvider({ children }) {
  const [session, setSession] = useState(readSession)

  const login = useCallback(async (username, password, remember = false) => {
    const res = await fetch('/pulse/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
    if (!res.ok) {
      let err = {}
      try {
        err = await res.json()
      } catch {}
      const message = err.error || (res.status === 429
        ? 'Too many attempts. Try again in a few minutes.'
        : 'Login failed')
      const failure = new Error(message)
      failure.status = res.status
      failure.retryAfter = Number(res.headers.get('Retry-After')) || 0
      throw failure
    }
    const data = await res.json()
    const next = { token: data.token, user: username }
    writeSession(next, remember)
    setSession(next)
    return data
  }, [])

  const logout = useCallback(() => {
    clearSession()
    setSession(null)
  }, [])

  // A session stored by another tab (or cleared there) applies here too.
  useEffect(() => {
    const onStorage = (e) => {
      if (e.key === KEY || e.key === null) setSession(readSession())
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  return (
    <AuthContext.Provider value={{
      token: session?.token || null,
      user: session?.user || null,
      isAuthenticated: !!session?.token,
      login,
      logout,
    }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be inside AuthProvider')
  return ctx
}
