import { useEffect, useState } from 'react'
import { useAPI } from './useAPI'

// useInstances lists the instances recording into this Pulse's storage —
// several when they share a PostgreSQL database — refreshed every 15s.
export function useInstances() {
  const { get } = useAPI()
  const [data, setData] = useState({ instances: [], shared: false, self: '', leader: '' })

  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const res = await get('/instances')
        if (res.ok && alive) setData(await res.json())
      } catch {}
    }
    load()
    const timer = setInterval(load, 15000)
    return () => { alive = false; clearInterval(timer) }
  }, [get])

  return data
}
