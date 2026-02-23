export default function StatCard({ label, value, sub, color = '#e2e8f0' }) {
  return (
    <div style={{
      background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
      padding: '16px 20px',
    }}>
      <p style={{ color: '#64748b', fontSize: 12, textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: 4 }}>
        {label}
      </p>
      <p style={{ fontSize: 26, fontWeight: 700, color, lineHeight: 1.2 }}>
        {value}
      </p>
      {sub && <p style={{ color: '#64748b', fontSize: 12, marginTop: 4 }}>{sub}</p>}
    </div>
  )
}
