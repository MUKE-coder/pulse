// InstancePicker chooses the instance a per-instance page shows. It renders
// nothing unless several instances record into the same storage.
export default function InstancePicker({ instances, value, onChange }) {
  if (!instances || instances.length < 2) return null
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} title="Instance">
      {instances.map((i) => (
        <option key={i.id} value={i.id}>
          {i.id}
          {i.self ? ' (this one)' : ''}
          {i.leader ? ' · leader' : ''}
          {i.status !== 'running' ? ` · ${i.status}` : ''}
        </option>
      ))}
    </select>
  )
}
