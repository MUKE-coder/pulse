import { useEffect } from 'react'

export default function Modal({ open, onClose, title, children, width = 640 }) {
  useEffect(() => {
    if (!open) return
    const onKey = (e) => e.key === 'Escape' && onClose?.()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div onMouseDown={(e) => e.target === e.currentTarget && onClose?.()}
      role="dialog" aria-modal="true" aria-label={title}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(2, 6, 12, .6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000, padding: 20,
      }}>
      <div className="card" style={{
        width: '100%', maxWidth: width, maxHeight: '80vh', overflow: 'auto', padding: 0,
      }}>
        <div className="card-head" style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)', margin: 0,
        }}>
          <h3 style={{ fontSize: 15, fontWeight: 600 }}>{title}</h3>
          <button type="button" className="btn btn-ghost btn-icon" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div style={{ padding: 18 }}>{children}</div>
      </div>
    </div>
  )
}
