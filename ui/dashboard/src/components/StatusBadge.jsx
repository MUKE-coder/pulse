const presets = {
  healthy:   { bg: '#22c55e18', text: '#22c55e', border: '#22c55e30' },
  degraded:  { bg: '#f59e0b18', text: '#f59e0b', border: '#f59e0b30' },
  unhealthy: { bg: '#ef444418', text: '#ef4444', border: '#ef444430' },
  firing:    { bg: '#ef444418', text: '#ef4444', border: '#ef444430' },
  resolved:  { bg: '#22c55e18', text: '#22c55e', border: '#22c55e30' },
  pending:   { bg: '#f59e0b18', text: '#f59e0b', border: '#f59e0b30' },
  ok:        { bg: '#22c55e18', text: '#22c55e', border: '#22c55e30' },
  critical:  { bg: '#ef444418', text: '#ef4444', border: '#ef444430' },
  warning:   { bg: '#f59e0b18', text: '#f59e0b', border: '#f59e0b30' },
  info:      { bg: '#6366f118', text: '#818cf8', border: '#6366f130' },
}

export default function StatusBadge({ status }) {
  const s = status?.toLowerCase() || 'info'
  const colors = presets[s] || presets.info

  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 5,
      padding: '3px 10px', borderRadius: 9999, fontSize: 12, fontWeight: 600,
      background: colors.bg, color: colors.text, border: `1px solid ${colors.border}`,
      textTransform: 'capitalize',
    }}>
      <span style={{
        width: 6, height: 6, borderRadius: '50%', background: colors.text,
      }} />
      {status}
    </span>
  )
}
