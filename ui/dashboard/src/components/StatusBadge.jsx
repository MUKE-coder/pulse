// StatusBadge maps the words the API uses to the four status tones. The
// word is always shown: status is never colour alone.
const tones = {
  healthy: 'ok', resolved: 'ok', ok: 'ok', passing: 'ok', up: 'ok', completed: 'ok',
  degraded: 'warn', pending: 'warn', warning: 'warn', running: 'warn',
  unhealthy: 'crit', firing: 'crit', critical: 'crit', failed: 'crit', down: 'crit',
  info: 'info',
}

export default function StatusBadge({ status }) {
  const tone = tones[String(status || '').toLowerCase()] || 'neutral'
  return (
    <span className={`badge badge-${tone}`} style={{ textTransform: 'capitalize' }}>
      <i className="dot" />
      {status}
    </span>
  )
}
