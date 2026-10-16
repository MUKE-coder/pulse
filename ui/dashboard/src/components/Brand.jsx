// The redesigned mark: one pulse-line stroke inside a rounded tile. The
// stroke is always the teal accent — it sits on a dark tile in both themes —
// while the tile itself comes from tokens.
export function PulseMark({ size = 28, strokeWidth = 2.2, title = 'Pulse' }) {
  return (
    <svg width={size} height={size} viewBox="0 0 28 28" role="img" aria-label={title} style={{ flex: 'none' }}>
      <rect x="0.5" y="0.5" width="27" height="27" rx="7"
        fill="var(--logo-tile)" stroke="var(--logo-tile-border)" />
      <path d="M5 14h4.2l2.6-6.5 3.6 13 2.6-6.5H23"
        fill="none" stroke="#2DD4BF" strokeWidth={strokeWidth}
        strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

// The same line at display scale, for the login brand panel. Decorative, so
// it is hidden from assistive tech.
export function PulseLine({ className = 'pulse-line' }) {
  return (
    <svg className={className} viewBox="0 0 520 120" fill="none" aria-hidden="true" focusable="false">
      <path
        d="M0 60h150l22-28 26 56 24-96 26 124 22-56h26l20-24 18 24h166"
        stroke="#2DD4BF" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"
      />
    </svg>
  )
}

export function Wordmark({ size = 17, className = 'wordmark' }) {
  return <span className={className} style={{ fontSize: size }}>Pulse</span>
}
