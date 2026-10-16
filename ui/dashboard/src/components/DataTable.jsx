// DataTable renders the shared table chrome from the redesign: hairline
// rows, uppercase eyebrow headers, mono numerics. Columns opt into
// alignment with `num: true` and ellipsis with `code: true`.
export default function DataTable({ columns, data, onRowClick, emptyText = 'No data available' }) {
  if (!data || data.length === 0) {
    return (
      <div className="empty">
        <div className="body">{emptyText}</div>
      </div>
    )
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.map((col) => (
              <th key={col.key} scope="col" className={col.num ? 'num' : undefined}>{col.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.map((row, i) => (
            <tr key={i} onClick={() => onRowClick?.(row)}
              className={onRowClick ? 'is-clickable' : undefined}>
              {columns.map((col) => (
                <td key={col.key}
                  className={[col.num ? 'num' : '', col.code ? 'code' : '', col.mono ? 'mono' : '']
                    .filter(Boolean).join(' ') || undefined}
                  title={col.code ? String(row[col.key] ?? '') : undefined}>
                  {col.render ? col.render(row[col.key], row) : row[col.key]}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
