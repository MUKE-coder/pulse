import { useCallback, useEffect, useState } from 'react'

const KEY = 'pulse.theme'

const stored = () => {
  try {
    return localStorage.getItem(KEY) || 'system'
  } catch {
    return 'system'
  }
}

const systemDark = () => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false

// useTheme keeps the dashboard's light/dark preference — following the
// system until someone chooses — and resolves the design tokens charts need
// as concrete colours, since SVG attributes can't read CSS variables.
export function useTheme() {
  const [pref, setPref] = useState(stored)
  const [dark, setDark] = useState(() => (stored() === 'system' ? systemDark() : stored() === 'dark'))
  const [palette, setPalette] = useState({})

  useEffect(() => {
    const root = document.documentElement
    if (pref === 'system') root.removeAttribute('data-theme')
    else root.setAttribute('data-theme', pref)
    try {
      localStorage.setItem(KEY, pref)
    } catch {}
    setDark(pref === 'system' ? systemDark() : pref === 'dark')
  }, [pref])

  // Follow the system while no explicit choice is stored.
  useEffect(() => {
    if (pref !== 'system' || !window.matchMedia) return
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => setDark(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [pref])

  // Re-read the tokens whenever the resolved theme changes.
  useEffect(() => {
    const css = getComputedStyle(document.documentElement)
    const token = (name) => css.getPropertyValue(`--ops-${name}`).trim()
    setPalette({
      ink: token('ink'), ink2: token('ink-2'), ink3: token('ink-3'),
      line: token('line'), grid: token('grid'), surface: token('surface'),
      accent: token('accent'), ok: token('ok'), warn: token('warn'), bad: token('bad'),
    })
  }, [dark])

  const toggle = useCallback(() => setPref(dark ? 'light' : 'dark'), [dark])
  return { dark, palette, toggle }
}
