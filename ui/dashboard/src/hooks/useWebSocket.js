import { useEffect, useRef, useState } from 'react'
import { useAuth } from '../context/AuthContext'

// useWebSocket subscribes to live-update channels; with no channels it
// doesn't connect. The server packs queued messages into one frame,
// newline-separated: each is passed to onMessage, and the latest is also
// returned as lastMessage.
export function useWebSocket(channels = [], onMessage) {
  const { token } = useAuth()
  const [lastMessage, setLastMessage] = useState(null)
  const [connected, setConnected] = useState(false)
  const handler = useRef(onMessage)
  handler.current = onMessage
  // Pages pass a new array on every render; reconnect only when the
  // channels themselves change.
  const key = channels.join(',')

  useEffect(() => {
    if (!token || !key) return
    let ws
    let timer
    let stopped = false

    const connect = () => {
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      ws = new WebSocket(`${proto}//${window.location.host}/pulse/ws/live?token=${encodeURIComponent(token)}`)
      ws.onopen = () => {
        setConnected(true)
        ws.send(JSON.stringify({ subscribe: key.split(',') }))
      }
      ws.onmessage = (event) => {
        for (const line of String(event.data).split('\n')) {
          if (!line) continue
          let msg
          try { msg = JSON.parse(line) } catch { continue }
          setLastMessage(msg)
          handler.current?.(msg)
        }
      }
      ws.onclose = () => {
        setConnected(false)
        if (!stopped) timer = setTimeout(connect, 3000)
      }
      ws.onerror = () => ws.close()
    }

    connect()
    return () => {
      stopped = true
      clearTimeout(timer)
      ws?.close()
    }
  }, [key, token])

  return { lastMessage, connected }
}
