export default function DataTable({ columns, data, onRowClick, emptyText = 'No data available' }) {
  if (!data || data.length === 0) {
    return (
      <div style={{
        background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
        padding: 40, textAlign: 'center', color: '#64748b', fontSize: 14,
      }}>
        {emptyText}
      </div>
    )
  }

  return (
    <div style={{
      background: '#111118', border: '1px solid #1e1e2e', borderRadius: 8,
      overflow: 'hidden',
    }}>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #1e1e2e' }}>
              {columns.map((col) => (
                <th key={col.key} style={{
                  padding: '10px 14px', textAlign: 'left', color: '#64748b',
                  fontWeight: 600, fontSize: 11, textTransform: 'uppercase',
                  letterSpacing: '0.05em', whiteSpace: 'nowrap',
                }}>
                  {col.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data.map((row, i) => (
              <tr
                key={i}
                onClick={() => onRowClick?.(row)}
                style={{
                  borderBottom: i < data.length - 1 ? '1px solid #16161e' : 'none',
                  cursor: onRowClick ? 'pointer' : 'default',
                  transition: 'background 0.1s',
                }}
                onMouseOver={(e) => e.currentTarget.style.background = '#16161e'}
                onMouseOut={(e) => e.currentTarget.style.background = 'transparent'}
              >
                {columns.map((col) => (
                  <td key={col.key} style={{ padding: '10px 14px', whiteSpace: 'nowrap', color: '#e2e8f0' }}>
                    {col.render ? col.render(row[col.key], row) : row[col.key]}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
