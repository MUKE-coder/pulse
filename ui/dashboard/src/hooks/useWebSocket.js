import { useEffect, useRef, useCallback, useState } from 'react'
import { useAuth } from '../context/AuthContext'

export function useWebSocket(channels = []) {
  const { token } = useAuth()
  const wsRef = useRef(null)
  const [lastMessage, setLastMessage] = useState(null)
  const [connected, setConnected] = useState(false)
  const reconnectTimer = useRef(null)

  const connect = useCallback(() => {
    if (!token) return
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${window.location.host}/pulse/ws/live?token=${encodeURIComponent(token)}`
    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      if (channels.length > 0) {
        ws.send(JSON.stringify({ subscribe: channels }))
      }
    }

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data)
        setLastMessage(msg)
      } catch {}
    }

    ws.onclose = () => {
      setConnected(false)
      reconnectTimer.current = setTimeout(connect, 3000)
    }

    ws.onerror = () => ws.close()
  }, [channels, token])

  useEffect(() => {
    connect()
    return () => {
      clearTimeout(reconnectTimer.current)
      if (wsRef.current) wsRef.current.close()
    }
  }, [connect])

  return { lastMessage, connected }
}
