import { useCallback } from 'react'
import { useAuth } from '../context/AuthContext'

export function useAPI() {
  const { token, logout } = useAuth()

  const request = useCallback(async (path, options = {}) => {
    const headers = { ...options.headers }
    if (token) headers['Authorization'] = `Bearer ${token}`
    if (options.body && typeof options.body === 'object' && !(options.body instanceof FormData)) {
      headers['Content-Type'] = 'application/json'
      options.body = JSON.stringify(options.body)
    }

    const res = await fetch(`/pulse/api${path}`, { ...options, headers })
    if (res.status === 401) {
      logout()
      throw new Error('Session expired')
    }
    return res
  }, [token, logout])

  const get = useCallback((path) => request(path), [request])

  const post = useCallback((path, body) =>
    request(path, { method: 'POST', body }), [request])

  const del = useCallback((path) =>
    request(path, { method: 'DELETE' }), [request])

  return { get, post, del, request }
}
