import { useEffect, useMemo, useRef, useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import { useUI } from '../context/UIContext'
import { NAV_GROUPS } from './Layout'

// The ⌘K palette the top bar's search opens. It jumps between pages and
// searches the two things worth searching from anywhere: routes by path and
// errors by message. Hits navigate to the page that can show them properly.
export default function CommandPalette({ onClose, onNavigate }) {
  const { get } = useAPI()
  const { range } = useUI()
  const [query, setQuery] = useState('')
  const [hits, setHits] = useState([])
  const [cursor, setCursor] = useState(0)
  const inputRef = useRef(null)

  const pages = useMemo(
    () => NAV_GROUPS.flatMap((g) => g.items.map((i) => ({ ...i, group: g.eyebrow })))
      .concat([{ to: '/pulse/ui/settings', label: 'Settings', group: 'Settings' }]),
    []
  )

  useEffect(() => { inputRef.current?.focus() }, [])

  // Pages filter instantly; routes and errors are fetched, debounced.
  useEffect(() => {
    const term = query.trim()
    const matchedPages = pages
      .filter((p) => !term || p.label.toLowerCase().includes(term.toLowerCase()))
      .map((p) => ({ kind: 'page', label: p.label, hint: p.group, to: p.to }))

    if (term.length < 2) {
      setHits(matchedPages)
      setCursor(0)
      return
    }

    let cancelled = false
    const id = setTimeout(async () => {
      const found = [...matchedPages]
      try {
        const res = await get(`/routes?range=${range}&search=${encodeURIComponent(term)}`)
        if (res.ok) {
          const routes = await res.json()
          for (const r of (routes || []).slice(0, 5)) {
            found.push({
              kind: 'route', label: `${r.method} ${r.path}`,
              hint: `${Math.round(r.request_count || 0)} req`, to: '/pulse/ui/routes',
            })
          }
        }
      } catch {}
      try {
        const res = await get(`/errors?range=${range}&limit=5&search=${encodeURIComponent(term)}`)
        if (res.ok) {
          const payload = await res.json()
          const errors = Array.isArray(payload) ? payload : payload?.errors || []
          for (const e of errors.slice(0, 5)) {
            found.push({
              kind: 'error', label: e.message || e.error_type || 'error',
              hint: e.route || 'error', to: '/pulse/ui/errors',
            })
          }
        }
      } catch {}
      if (!cancelled) {
        setHits(found)
        setCursor(0)
      }
    }, 180)

    return () => {
      cancelled = true
      clearTimeout(id)
    }
  }, [query, pages, get, range])

  const onKeyDown = (e) => {
    if (e.key === 'Escape') return onClose()
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setCursor((c) => Math.min(c + 1, hits.length - 1))
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setCursor((c) => Math.max(c - 1, 0))
    }
    if (e.key === 'Enter' && hits[cursor]) onNavigate(hits[cursor].to)
  }

  return (
    <div className="palette-backdrop" role="dialog" aria-modal="true" aria-label="Search"
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette">
        <input ref={inputRef} className="input palette-input" value={query} onKeyDown={onKeyDown}
          onChange={(e) => setQuery(e.target.value)} placeholder="Search routes, errors or pages…"
          aria-label="Search routes, errors or pages" />
        {hits.length ? (
          <ul>
            {hits.map((hit, i) => (
              <li key={`${hit.kind}-${hit.label}-${i}`} className={i === cursor ? 'is-active' : ''}
                onMouseEnter={() => setCursor(i)} onClick={() => onNavigate(hit.to)}>
                <span className={hit.kind === 'page' ? '' : 'mono'}
                  style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {hit.label}
                </span>
                <span className="hint">{hit.hint}</span>
              </li>
            ))}
          </ul>
        ) : (
          <div className="palette-empty">Nothing matches “{query}”.</div>
        )}
      </div>
    </div>
  )
}
