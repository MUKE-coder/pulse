import { useCallback, useEffect, useState } from 'react'

const KEY = 'pulse-theme'

function stored() {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : null
  } catch {
    return null
  }
}

const systemTheme = () =>
  window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'

// resolve mirrors the inline script in index.html: user choice, then the
// system, then dark.
const resolve = () => stored() || systemTheme()

// readPalette turns the CSS tokens into concrete colours. Charts draw with
// SVG attributes, which can't read custom properties, so they need the
// resolved values and a re-render whenever the theme flips.
function readPalette() {
  const css = getComputedStyle(document.documentElement)
  const token = (name) => css.getPropertyValue(`--${name}`).trim()
  return {
    text: token('text'), text2: token('text-2'), text3: token('text-3'),
    border: token('border'), borderStrong: token('border-strong'),
    surface: token('surface'), surface2: token('surface-2'), bg: token('bg'),
    accent: token('accent'), accentSoft: token('accent-soft'),
    ok: token('ok'), warn: token('warn'), crit: token('crit'), info: token('info'),
    grid: token('chart-grid'),
  }
}

// setTheme is the single writer: it flips the attribute, remembers the
// choice in localStorage and a cookie (so a server-rendered page could read
// it), and announces the change for anything drawing outside React.
export function setTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme)
  try {
    localStorage.setItem(KEY, theme)
  } catch {}
  try {
    document.cookie = `${KEY}=${theme}; path=/; max-age=31536000; samesite=lax`
  } catch {}
  window.dispatchEvent(new CustomEvent('pulse:theme', { detail: { theme } }))
}

export function useTheme() {
  const [theme, setThemeState] = useState(resolve)
  const [palette, setPalette] = useState(readPalette)

  // Keep every hook instance in step with whoever flipped the theme.
  useEffect(() => {
    const onChange = (e) => setThemeState(e.detail?.theme || resolve())
    window.addEventListener('pulse:theme', onChange)
    return () => window.removeEventListener('pulse:theme', onChange)
  }, [])

  // Follow the system only while no explicit choice is stored.
  useEffect(() => {
    if (!window.matchMedia) return
    const mq = window.matchMedia('(prefers-color-scheme: light)')
    const onChange = () => {
      if (stored()) return
      const next = mq.matches ? 'light' : 'dark'
      document.documentElement.setAttribute('data-theme', next)
      setThemeState(next)
      window.dispatchEvent(new CustomEvent('pulse:theme', { detail: { theme: next } }))
    }
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  // Tokens are read after the attribute has been applied, so the palette
  // always matches what is on screen.
  useEffect(() => {
    setPalette(readPalette())
  }, [theme])

  const toggle = useCallback(() => setTheme(theme === 'dark' ? 'light' : 'dark'), [theme])

  return { theme, dark: theme === 'dark', palette, toggle, setTheme }
}
