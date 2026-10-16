import { useState } from 'react'
import { useAuth } from '../context/AuthContext'

// Flame graph viewer. The backend returns a fully-rendered SVG, so this page
// is mostly a control panel that fetches and embeds it.
//
// Backend: GET /pulse/api/profile/flamegraph (pulse/profile.go).
// Returns 503 when profiling is not opted in — we surface that as a how-to.

const DURATIONS = ['1s', '5s', '10s', '30s']
const HZ_VALUES = [50, 100, 200]

export default function FlameGraph() {
  const { token } = useAuth()
  const [duration, setDuration] = useState('5s')
  const [hz, setHz] = useState(100)
  const [svg, setSvg] = useState('')
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(false)

  const sample = async () => {
    setLoading(true); setError(null); setSvg('')
    try {
      const res = await fetch(
        `/pulse/api/profile/flamegraph?duration=${duration}&hz=${hz}`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      const text = await res.text()
      if (!res.ok) {
        // 503 carries { error, hint } — surface the hint verbatim.
        try {
          const j = JSON.parse(text)
          setError(j.hint || j.error || `HTTP ${res.status}`)
        } catch {
          setError(`HTTP ${res.status}`)
        }
        return
      }
      setSvg(text)
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }

  const downloadFolded = async () => {
    const res = await fetch(
      `/pulse/api/profile/folded?duration=${duration}&hz=${hz}`,
      { headers: { Authorization: `Bearer ${token}` } },
    )
    if (!res.ok) return
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `pulse-profile-${Date.now()}.folded`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div>
      <h1 className="text-[22px] font-semibold tracking-[-0.01em] text-ink mb-2">Flame Graph</h1>
      <p className="text-ink-3 text-xs mb-5">
        Samples live goroutine stacks for the chosen window. Off by default in production —
        requires both <code className="text-accent">pulse.WithProfiling()</code> and
        the <code className="text-accent">PULSE_PROFILE_ENABLED=true</code> env var.
      </p>

      <div className="bg-surface border border-line rounded-lg p-4 mb-5">
        <div className="flex flex-wrap items-end gap-4">
          <div>
            <label className="block text-xs uppercase tracking-wider text-ink-3 mb-1">Duration</label>
            <div className="flex gap-1">
              {DURATIONS.map((d) => (
                <button
                  key={d}
                  onClick={() => setDuration(d)}
                  className={`px-2.5 py-1 rounded ring-1 text-xs transition ${
                    duration === d
                      ? 'bg-accent/15 text-accent ring-accent/30'
                      : 'bg-surface-2 text-ink-2 ring-line hover:text-ink'
                  }`}
                >{d}</button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs uppercase tracking-wider text-ink-3 mb-1">Sample rate</label>
            <div className="flex gap-1">
              {HZ_VALUES.map((v) => (
                <button
                  key={v}
                  onClick={() => setHz(v)}
                  className={`px-2.5 py-1 rounded ring-1 text-xs transition ${
                    hz === v
                      ? 'bg-accent/15 text-accent ring-accent/30'
                      : 'bg-surface-2 text-ink-2 ring-line hover:text-ink'
                  }`}
                >{v} Hz</button>
              ))}
            </div>
          </div>
          <div className="flex gap-2 ml-auto">
            <button
              onClick={sample}
              disabled={loading}
              className="px-3 py-1.5 rounded bg-accent hover:bg-accent disabled:opacity-50 text-sm text-white font-medium transition"
            >
              {loading ? `Sampling for ${duration}…` : 'Sample'}
            </button>
            <button
              onClick={downloadFolded}
              disabled={loading}
              className="px-3 py-1.5 rounded bg-surface-2 hover:bg-surface-2 disabled:opacity-50 text-sm text-ink font-medium transition"
              title="Download folded-stack text format (for flamegraph.pl / difffolded.pl)"
            >
              Download .folded
            </button>
          </div>
        </div>
      </div>

      {error && (
        <div className="bg-warn/10 border border-warn/30 text-warn rounded-lg p-4 mb-5 text-sm">
          <p className="font-medium mb-1">Profile request failed</p>
          <p className="font-mono text-xs text-warn/90">{error}</p>
        </div>
      )}

      {svg && (
        <div className="bg-surface border border-line rounded-lg p-3 overflow-x-auto">
          <div className="text-xs text-ink-3 mb-2">
            Hover a frame for sample counts. Width is proportional to samples; deterministic colours per frame name.
          </div>
          <div
            className="bg-white rounded"
            dangerouslySetInnerHTML={{ __html: svg }}
          />
        </div>
      )}

      {!svg && !error && !loading && (
        <div className="bg-surface border border-line rounded-lg p-5 text-sm text-ink-2">
          Choose a duration and sample rate, then hit <strong className="text-ink">Sample</strong>.
          The window blocks for the chosen duration; a flame graph renders in-place once samples have been collected.
        </div>
      )}
    </div>
  )
}
